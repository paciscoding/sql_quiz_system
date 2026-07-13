// Command gradeseed provisions the fixture the submission-grading load test
// drives: a pool of enrolled students, a lecturer, and one ACTIVE exam holding
// one SELECT question and one DML question (both against the default `public`
// company schema). It writes a manifest JSON that gradedriver consumes.
//
// Students and the lecturer are inserted straight into the DB (fast, and the
// login password is therefore known). The exam/question/activate/enroll steps go
// through the real HTTP API on purpose: those paths run Phase-0 capture and
// sandbox creation, which is exactly what a real setup exercises.
//
// Usage (run from the repo host against the loadtest compose override, which
// exposes db:5432 and backend:8080):
//
//	DATABASE_URL='postgres://sqlquiz:PASS@localhost:5432/sqlquiz?sslmode=disable' \
//	  go run ./loadtest/gradeseed -base http://localhost:8080 -n 100 -out loadtest/grade_manifest.json
//	go run ./loadtest/gradeseed -teardown   # remove gradeload accounts + exams
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// Shared with gradedriver — keep the prefix and password in sync.
const (
	StudentPrefix   = "gradeload"
	LecturerName    = "gradeload_lecturer"
	KnownPassword   = "gradeload-pw-1234"
	EmailDomain     = "example.com"
	defaultSelectQ  = "SELECT ssn, fname, lname FROM employee ORDER BY ssn"
	defaultDMLQ     = "UPDATE works_on SET hours = 40"
	defaultDMLTable = "works_on"
)

// Manifest is what gradedriver reads to know what to hit.
type Manifest struct {
	Base         string `json:"base"`
	ExamID       int    `json:"exam_id"`
	SelectQID    int    `json:"select_qid"`
	DMLQID       int    `json:"dml_qid"`
	StudentCount int    `json:"student_count"`
	StudentPre   string `json:"student_prefix"`
	Password     string `json:"password"`
	SelectSQL    string `json:"select_sql"`
	DMLSQL       string `json:"dml_sql"`
}

func main() {
	base := flag.String("base", "http://localhost:8080", "backend base URL")
	n := flag.Int("n", 100, "number of students to seed and enroll")
	out := flag.String("out", "loadtest/grade_manifest.json", "manifest output path")
	selectSQL := flag.String("select-sql", defaultSelectQ, "correct SQL for the SELECT question")
	dmlSQL := flag.String("dml-sql", defaultDMLQ, "correct SQL for the DML question")
	dmlTable := flag.String("dml-table", defaultDMLTable, "target table for the DML question")
	teardown := flag.Bool("teardown", false, "delete gradeload accounts + their exams instead of seeding")
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "postgres DSN (default $DATABASE_URL)")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("no DSN: set DATABASE_URL or pass -dsn")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if *teardown {
		doTeardown(ctx, pool)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(KnownPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}

	// 1. Lecturer (drives the API) + students (driven by gradedriver).
	if _, err := pool.Exec(ctx,
		`INSERT INTO app.system_users (username, password_hash, role, email)
		 VALUES ($1, $2, 'LECTURER', $3) ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash`,
		LecturerName, string(hash), LecturerName+"@"+EmailDomain); err != nil {
		log.Fatalf("seed lecturer: %v", err)
	}
	studentIDs := make([]int, 0, *n)
	for i := 1; i <= *n; i++ {
		username := fmt.Sprintf("%s%04d", StudentPrefix, i)
		email := fmt.Sprintf("%s%04d@%s", StudentPrefix, i, EmailDomain)
		var id int
		if err := pool.QueryRow(ctx,
			`INSERT INTO app.system_users (username, password_hash, role, email)
			 VALUES ($1, $2, 'STUDENT', $3)
			 ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash
			 RETURNING id`,
			username, string(hash), email).Scan(&id); err != nil {
			log.Fatalf("seed student %s: %v", username, err)
		}
		studentIDs = append(studentIDs, id)
	}
	log.Printf("seeded lecturer + %d students", len(studentIDs))

	// 2. Log in as the lecturer to drive the setup API.
	api := &apiClient{base: *base, hc: &http.Client{Timeout: 30 * time.Second}}
	token, err := api.login(LecturerName, KnownPassword)
	if err != nil {
		log.Fatalf("lecturer login (is the backend up at %s?): %v", *base, err)
	}
	api.token = token

	// 3. Create exam.
	examID, err := api.createExam(fmt.Sprintf("gradeload exam %d", time.Now().Unix()))
	if err != nil {
		log.Fatalf("create exam: %v", err)
	}

	// 4. Create the two questions (Phase-0 capture runs here).
	if err := api.createQuestion(map[string]any{
		"exam_id": examID, "type": "QUERY", "correct_sql": *selectSQL,
		"points": 10, "requirement": "loadtest select", "strict_mode": map[string]bool{},
	}); err != nil {
		log.Fatalf("create SELECT question: %v", err)
	}
	if err := api.createQuestion(map[string]any{
		"exam_id": examID, "type": "DML", "target_table": *dmlTable, "correct_sql": *dmlSQL,
		"points": 10, "requirement": "loadtest dml", "strict_mode": map[string]bool{},
	}); err != nil {
		log.Fatalf("create DML question: %v", err)
	}

	// 5. Resolve the assigned question ids by type.
	selectQID, dmlQID, err := api.questionIDsByType(examID)
	if err != nil {
		log.Fatalf("resolve question ids: %v", err)
	}

	// 6. Enroll every student, then activate the exam (instant).
	if err := api.assignStudents(examID, studentIDs); err != nil {
		log.Fatalf("assign students: %v", err)
	}
	if err := api.activate(examID); err != nil {
		log.Fatalf("activate exam: %v", err)
	}

	m := Manifest{
		Base: *base, ExamID: examID, SelectQID: selectQID, DMLQID: dmlQID,
		StudentCount: *n, StudentPre: StudentPrefix, Password: KnownPassword,
		SelectSQL: *selectSQL, DMLSQL: *dmlSQL,
	}
	buf, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		log.Fatalf("write manifest: %v", err)
	}
	log.Printf("ACTIVE exam %d ready: select_qid=%d dml_qid=%d students=%d", examID, selectQID, dmlQID, *n)
	log.Printf("manifest -> %s", *out)
	fmt.Println(string(buf))
}

func doTeardown(ctx context.Context, pool *pgxpool.Pool) {
	// Drop any sandbox schemas for gradeload exams, then the exams, then accounts.
	rows, err := pool.Query(ctx,
		`SELECT id FROM app.examinations WHERE title LIKE 'gradeload exam %'`)
	if err != nil {
		log.Fatalf("list exams: %v", err)
	}
	var examIDs []int
	for rows.Next() {
		var id int
		_ = rows.Scan(&id)
		examIDs = append(examIDs, id)
	}
	rows.Close()
	for _, id := range examIDs {
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS sandbox_exam_%d CASCADE", id))
		_, _ = pool.Exec(ctx, `DELETE FROM app.submissions WHERE exam_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM app.exam_students WHERE exam_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM app.questions WHERE exam_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM app.exam_sandboxes WHERE exam_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM app.examinations WHERE id = $1`, id)
	}
	tag, err := pool.Exec(ctx,
		`DELETE FROM app.system_users WHERE username LIKE $1 OR username = $2`,
		StudentPrefix+"%", LecturerName)
	if err != nil {
		log.Fatalf("delete accounts: %v", err)
	}
	log.Printf("torn down %d exams and %d accounts", len(examIDs), tag.RowsAffected())
}

// --- tiny API client ---

type apiClient struct {
	base  string
	token string
	hc    *http.Client
}

func (a *apiClient) login(user, pass string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := a.do(http.MethodPost, "/api/v1/login", map[string]string{"username": user, "password": pass}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("login returned no token")
	}
	return out.Token, nil
}

func (a *apiClient) createExam(title string) (int, error) {
	var out struct {
		ID int `json:"id"`
	}
	err := a.do(http.MethodPost, "/api/v1/lecturer/exams", map[string]string{"title": title}, &out)
	return out.ID, err
}

func (a *apiClient) createQuestion(body map[string]any) error {
	return a.do(http.MethodPost, "/api/v1/lecturer/question", body, nil)
}

func (a *apiClient) questionIDsByType(examID int) (selectQID, dmlQID int, err error) {
	var qs []struct {
		ID     int    `json:"id"`
		Type   string `json:"type"`
		Status string `json:"expected_result_status"`
	}
	if err = a.do(http.MethodGet, fmt.Sprintf("/api/v1/lecturer/exams/%d/questions", examID), nil, &qs); err != nil {
		return 0, 0, err
	}
	for _, q := range qs {
		if q.Status != "" && q.Status != "captured" {
			return 0, 0, fmt.Errorf("question %d did not capture (status=%q); exam cannot activate", q.ID, q.Status)
		}
		switch q.Type {
		case "QUERY":
			selectQID = q.ID
		case "DML":
			dmlQID = q.ID
		}
	}
	if selectQID == 0 || dmlQID == 0 {
		return 0, 0, fmt.Errorf("missing question ids (select=%d dml=%d)", selectQID, dmlQID)
	}
	return selectQID, dmlQID, nil
}

func (a *apiClient) assignStudents(examID int, ids []int) error {
	return a.do(http.MethodPost, fmt.Sprintf("/api/v1/lecturer/exams/%d/students", examID),
		map[string]any{"student_ids": ids}, nil)
}

func (a *apiClient) activate(examID int) error {
	return a.do(http.MethodPut, fmt.Sprintf("/api/v1/lecturer/exams/%d/activate", examID),
		map[string]any{"active": true, "mode": "instant"}, nil)
}

func (a *apiClient) do(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, a.base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
