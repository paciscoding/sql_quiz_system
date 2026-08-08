package controllers

// Regression-tester (step [7]) coverage for the dml-autoseed feature, area
// #2: auto_seed must never reach a student. This IS the shipped-in-this-
// branch defect the validator caught: auto_seed contains the model answer's
// WHERE clause (table, column, exact literal), so leaking it hands students
// the answer. It is scrubbed unconditionally in practice_controller.go
// (ListForStudent, GetForStudent) and quiz_controller.go (GetExamQuestions).
//
// dual_leak_payload_test.go's TestListForStudent_DualOmitsCaptureErrorAndRowsAffected
// / TestGetForStudent_... / TestGetExamQuestions_... already say plainly (in
// their own header comment) that they are WHITE-BOX MIRROR handlers
// duplicating the controllers' redaction logic rather than calling the real
// controllers — so they would keep passing even if the real scrub were
// deleted. THIS FILE calls the REAL controllers
// (PracticeController.ListForStudent/GetForStudent,
// QuizController.GetExamQuestions) through a real gin router, wired to a
// real *repository.PostgresRepository (openCtrlTestRepo -> testsupport.SharedRepo),
// with a genuinely auto-seeded question (auto_seed non-null in the database)
// created via the real capture path — so deleting `questions[i].AutoSeed =
// nil` / `question.AutoSeed = nil` from the production controllers would make
// these tests FAIL. This is real coverage of the actual code path, not a
// mirror.
//
// Requires TEST_DATABASE_URL; skips cleanly via openCtrlTestRepo ->
// testsupport.SharedRepo when absent (repo convention).
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/... -run AutoSeedLeak -v -p 1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// buildAutoSeedLeakRouter wires exactly /login plus the three real
// student-facing question-listing routes this file exercises, mirroring
// cmd/main.go's real registration (practiceStudent.GET("/questions", ...),
// practiceStudent.GET("/questions/:id", ...), quiz.GET("/exams/:id/questions", ...)),
// using the REAL controller methods — never a mirror handler.
func buildAutoSeedLeakRouter(auth *services.AuthService, userCtrl *UserController, practiceCtrl *PracticeController, quizCtrl *QuizController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)

	practiceStudent := v1.Group("/practice")
	practiceStudent.Use(AuthMiddleware(auth), RoleMiddleware(models.RoleStudent))
	{
		practiceStudent.GET("/questions", practiceCtrl.ListForStudent)
		practiceStudent.GET("/questions/:id", practiceCtrl.GetForStudent)
	}

	quiz := v1.Group("/quiz")
	quiz.Use(AuthMiddleware(auth))
	{
		quiz.GET("/exams/:id/questions", RoleMiddleware(models.RoleStudent), quizCtrl.GetExamQuestions)
	}
	return r
}

// mkAutoSeedRescuedPracticeQuestion creates a fresh practice category with a
// table + one row, and a DML question whose model answer is a genuine 0-row
// DELETE that the real capture path rescues, returning the SAVED question
// (post-capture, so AutoSeed is populated from the database, not merely set
// on the in-memory struct before capture ran).
func mkAutoSeedRescuedPracticeQuestion(t *testing.T, repo *repository.PostgresRepository, lecturerID int, prefix string) *models.PracticeQuestion {
	t.Helper()
	ctx := context.Background()
	cat, err := repo.CreatePracticeCategory(ctx, fmt.Sprintf("%s_cat_%d", prefix, time.Now().UnixNano()), lecturerID)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeletePracticeCategory(context.Background(), int(cat.ID), true) })

	tbl := fmt.Sprintf("employee_leak_%d", cat.ID)
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);
		INSERT INTO %s (ssn, salary) VALUES ('111111111', 5000);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat.ID), importSQL, lecturerID, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(lecturerID),
		CategoryID:  cat.ID,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Requirement: "delete a specific employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" || len(saved.AutoSeed) == 0 {
		t.Fatalf("fixture precondition: expected a captured, auto-seeded question, got status=%q auto_seed=%s capture_error=%q",
			saved.ExpectedResultStatus, saved.AutoSeed, saved.CaptureError)
	}
	return saved
}

// mkSeedSQLPracticeQuestion creates a fresh practice category with a table +
// one row, and a DELETE question whose lecturer-authored seed_sql (rather
// than derivation) is what makes the model answer affect a row, returning
// the SAVED question with a genuinely non-empty seed_sql read back from the
// database (not merely set on the in-memory struct before capture ran).
func mkSeedSQLPracticeQuestion(t *testing.T, repo *repository.PostgresRepository, lecturerID int, prefix string) *models.PracticeQuestion {
	t.Helper()
	ctx := context.Background()
	cat, err := repo.CreatePracticeCategory(ctx, fmt.Sprintf("%s_cat_%d", prefix, time.Now().UnixNano()), lecturerID)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeletePracticeCategory(context.Background(), int(cat.ID), true) })

	tbl := fmt.Sprintf("employee_seedleak_%d", cat.ID)
	importSQL := fmt.Sprintf(`CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);`, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat.ID), importSQL, lecturerID, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(lecturerID),
		CategoryID:  cat.ID,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '888888888'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, salary) VALUES ('888888888', 4000)", tbl),
		Requirement: "delete a specific employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" || saved.SeedSQL == "" {
		t.Fatalf("fixture precondition: expected a captured question with a non-empty seed_sql, got status=%q seed_sql=%q capture_error=%q",
			saved.ExpectedResultStatus, saved.SeedSQL, saved.CaptureError)
	}
	return saved
}

// TestListForStudent_AutoSeedNeverLeaks_RealController drives the REAL
// PracticeController.ListForStudent handler (not a mirror) with a genuinely
// auto-seeded question in the database, and proves the JSON response
// contains no "auto_seed" field at all.
func TestListForStudent_AutoSeedNeverLeaks_RealController(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	practiceCtrl := NewPracticeController(repo, services.NewValidationService(repo), auth)
	quizCtrl := NewQuizController(services.NewValidationService(repo), services.NewSubmissionQueue(16, 30*time.Second), repo, nil, auth)
	r := buildAutoSeedLeakRouter(auth, userCtrl, practiceCtrl, quizCtrl)

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	saved := mkAutoSeedRescuedPracticeQuestion(t, repo, int(lecturer.ID), "listleak")
	seedSaved := mkSeedSQLPracticeQuestion(t, repo, int(lecturer.ID), "listseedleak")

	student := mkCtrlUser(t, repo, models.RoleStudent, "studpw")
	token := login(t, r, student.Username, "studpw")

	w := do(r, http.MethodGet, "/api/v1/practice/questions", token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	var got []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	wantID := publicid.Encode(publicid.TypePractice, int(saved.ID))
	wantSeedID := publicid.Encode(publicid.TypePractice, int(seedSaved.ID))
	found, foundSeed := false, false
	for _, q := range got {
		idVal, _ := q["id"].(string)
		if idVal == wantID {
			found = true
		}
		if idVal == wantSeedID {
			foundSeed = true
		}
		if v, ok := q["auto_seed"]; ok && v != nil {
			t.Fatalf("SECURITY REGRESSION: real ListForStudent response leaked auto_seed (question id=%v): %v", q["id"], v)
		}
		if v, ok := q["seed_sql"]; ok && v != nil {
			t.Fatalf("SECURITY REGRESSION: real ListForStudent response leaked seed_sql (question id=%v): %v", q["id"], v)
		}
	}
	if !found {
		t.Fatalf("expected the auto-seeded question (id=%s) to appear in the student list response", wantID)
	}
	if !foundSeed {
		t.Fatalf("expected the seed_sql question (id=%s) to appear in the student list response", wantSeedID)
	}
}

// TestGetForStudent_AutoSeedNeverLeaks_RealController is the single-question
// sibling, driving the REAL PracticeController.GetForStudent handler.
func TestGetForStudent_AutoSeedNeverLeaks_RealController(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	practiceCtrl := NewPracticeController(repo, services.NewValidationService(repo), auth)
	quizCtrl := NewQuizController(services.NewValidationService(repo), services.NewSubmissionQueue(16, 30*time.Second), repo, nil, auth)
	r := buildAutoSeedLeakRouter(auth, userCtrl, practiceCtrl, quizCtrl)

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw2")
	saved := mkAutoSeedRescuedPracticeQuestion(t, repo, int(lecturer.ID), "getleak")
	seedSaved := mkSeedSQLPracticeQuestion(t, repo, int(lecturer.ID), "getseedleak")

	student := mkCtrlUser(t, repo, models.RoleStudent, "studpw2")
	token := login(t, r, student.Username, "studpw2")

	path := fmt.Sprintf("/api/v1/practice/questions/%s", publicid.Encode(publicid.TypePractice, int(saved.ID)))
	w := do(r, http.MethodGet, path, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if v, ok := got["auto_seed"]; ok && v != nil {
		t.Fatalf("SECURITY REGRESSION: real GetForStudent response leaked auto_seed: %v", v)
	}
	if v, ok := got["seed_sql"]; ok && v != nil {
		t.Fatalf("SECURITY REGRESSION: real GetForStudent response leaked seed_sql: %v", v)
	}
	// Sanity: this is genuinely the fixture question (id matches), so the
	// assertion above was exercised against real data, not a miss.
	if idVal, _ := got["id"].(string); idVal != publicid.Encode(publicid.TypePractice, int(saved.ID)) {
		t.Fatalf("expected the response to be for the fixture question, got id=%v", got["id"])
	}

	// Repeat the request against the seed_sql fixture: the previous assertion
	// only proves the auto-seed fixture's (empty) seed_sql field was absent,
	// which proves nothing about the scrub. This proves it against a question
	// whose seed_sql is genuinely non-empty in the database.
	seedPath := fmt.Sprintf("/api/v1/practice/questions/%s", publicid.Encode(publicid.TypePractice, int(seedSaved.ID)))
	w2 := do(r, http.MethodGet, seedPath, token)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w2.Code, w2.Body.String())
	}
	var got2 map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &got2); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if v, ok := got2["seed_sql"]; ok && v != nil {
		t.Fatalf("SECURITY REGRESSION: real GetForStudent response leaked seed_sql (question id=%v): %v", got2["id"], v)
	}
	if idVal, _ := got2["id"].(string); idVal != publicid.Encode(publicid.TypePractice, int(seedSaved.ID)) {
		t.Fatalf("expected the response to be for the seed_sql fixture question, got id=%v", got2["id"])
	}
}

// TestGetExamQuestions_AutoSeedNeverLeaks_RealController drives the REAL
// QuizController.GetExamQuestions handler against an ACTIVE exam holding a
// genuinely auto-seeded DML question, with a real enrolled student.
//
// Question.ID (unlike PracticeQuestion.ID) is a plain, unencoded int
// (ARCH-015 — exam-question ids are not opaque), so it round-trips through
// this response's JSON as a bare number, not a publicid-encoded string; the
// comparison below reads it back as float64 accordingly.
func TestGetExamQuestions_AutoSeedNeverLeaks_RealController(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	practiceCtrl := NewPracticeController(repo, services.NewValidationService(repo), auth)
	quizCtrl := NewQuizController(services.NewValidationService(repo), services.NewSubmissionQueue(16, 30*time.Second), repo, nil, auth)
	r := buildAutoSeedLeakRouter(auth, userCtrl, practiceCtrl, quizCtrl)

	ctx := context.Background()
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw3")

	examID, err := repo.CreateExamination(ctx, models.Examination{
		Title:     fmt.Sprintf("autoseed_leak_exam_%d", time.Now().UnixNano()),
		CreatedBy: models.UserID(lecturer.ID),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	tbl := fmt.Sprintf("employee_examleak_%d", examID)
	if _, err := repo.Pool().Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (ssn TEXT PRIMARY KEY, salary INTEGER); INSERT INTO public.%s (ssn, salary) VALUES ('111111111', 5000)", tbl, tbl)); err != nil {
		t.Fatalf("create capture table: %v", err)
	}
	t.Cleanup(func() { repo.Pool().Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl) })

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Points:      5,
		Requirement: "delete a specific employee",
		CreatedBy:   models.UserID(lecturer.ID),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	savedQ, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if savedQ.ExpectedResultStatus != "captured" || len(savedQ.AutoSeed) == 0 {
		t.Fatalf("fixture precondition: expected a captured, auto-seeded exam question, got status=%q auto_seed=%s capture_error=%q",
			savedQ.ExpectedResultStatus, savedQ.AutoSeed, savedQ.CaptureError)
	}

	// A second question whose model answer is only satisfiable via a
	// lecturer-authored seed_sql (rather than derivation), so the leak
	// assertion below is exercised against a genuinely non-empty seed_sql —
	// a fixture whose seed_sql was accidentally empty would prove nothing.
	seedTbl := fmt.Sprintf("employee_examseedleak_%d", examID)
	if _, err := repo.Pool().Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (ssn TEXT PRIMARY KEY, salary INTEGER)", seedTbl)); err != nil {
		t.Fatalf("create seed capture table: %v", err)
	}
	t.Cleanup(func() { repo.Pool().Exec(context.Background(), "DROP TABLE IF EXISTS public."+seedTbl) })

	seedID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: seedTbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '777777777'", seedTbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, salary) VALUES ('777777777', 4000)", seedTbl),
		Points:      5,
		Requirement: "delete a specific employee",
		CreatedBy:   models.UserID(lecturer.ID),
	})
	if err != nil {
		t.Fatalf("CreateQuestion (seed_sql fixture): %v", err)
	}
	savedSeedQ, err := repo.GetQuestion(ctx, seedID)
	if err != nil {
		t.Fatalf("GetQuestion (seed_sql fixture): %v", err)
	}
	if savedSeedQ.ExpectedResultStatus != "captured" || savedSeedQ.SeedSQL == "" {
		t.Fatalf("fixture precondition: expected a captured question with a non-empty seed_sql, got status=%q seed_sql=%q capture_error=%q",
			savedSeedQ.ExpectedResultStatus, savedSeedQ.SeedSQL, savedSeedQ.CaptureError)
	}

	// Enroll BEFORE activating: AddStudentsToExam refuses to assign students
	// to an already-active examination.
	student := mkCtrlUser(t, repo, models.RoleStudent, "studpw3")
	if err := repo.AddStudentsToExam(ctx, examID, []int{int(student.ID)}); err != nil {
		t.Fatalf("AddStudentsToExam: %v", err)
	}

	if err := repo.SetExaminationActive(ctx, examID, true, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive: %v", err)
	}

	token := login(t, r, student.Username, "studpw3")
	path := fmt.Sprintf("/api/v1/quiz/exams/%s/questions", publicid.Encode(publicid.TypeExam, examID))
	w := do(r, http.MethodGet, path, token)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	var got []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	found, foundSeed := false, false
	for _, q := range got {
		if idNum, ok := q["id"].(float64); ok && int(idNum) == newID {
			found = true
		}
		if idNum, ok := q["id"].(float64); ok && int(idNum) == seedID {
			foundSeed = true
		}
		if v, ok := q["auto_seed"]; ok && v != nil {
			t.Fatalf("SECURITY REGRESSION: real GetExamQuestions response leaked auto_seed (question id=%v): %v", q["id"], v)
		}
		if v, ok := q["seed_sql"]; ok && v != nil {
			t.Fatalf("SECURITY REGRESSION: real GetExamQuestions response leaked seed_sql (question id=%v): %v", q["id"], v)
		}
	}
	if !found {
		t.Fatalf("expected the auto-seeded exam question (id=%d) to appear in the student exam-questions response: %+v", newID, got)
	}
	if !foundSeed {
		t.Fatalf("expected the seed_sql exam question (id=%d) to appear in the student exam-questions response: %+v", seedID, got)
	}
}

// TestValidateSeedFields exercises validateSeedFields (ARCH-044/050) without a
// database: every QUERY/DML × dml_kind × seed_sql combination that must be
// accepted or rejected at the API boundary.
func TestValidateSeedFields(t *testing.T) {
	const del = "DELETE FROM employee WHERE ssn = '1'"
	const upd = "UPDATE employee SET fname = 'x' WHERE ssn = '1'"
	const ins = "INSERT INTO employee (ssn) VALUES ('1')"
	const cte = "WITH recent AS (SELECT ssn FROM employee) DELETE FROM employee WHERE ssn IN (SELECT ssn FROM recent)"
	const sel = "SELECT * FROM employee"
	const seed = "INSERT INTO employee (ssn) VALUES ('1')"

	cases := []struct {
		name       string
		qType      string
		kind       string
		seedSQL    string
		correctSQL string
		wantErr    bool
		errSubstr  string
	}{
		// Unchanged QUERY rules.
		{name: "query clean", qType: "QUERY", correctSQL: sel},
		{name: "query with a kind", qType: "QUERY", kind: "DELETE", correctSQL: sel, wantErr: true},
		{name: "query with a seed", qType: "QUERY", seedSQL: seed, correctSQL: sel, wantErr: true},

		// Agreement (ARCH-057 rule 1).
		{name: "delete matches", qType: "DML", kind: "DELETE", correctSQL: del},
		{name: "update matches", qType: "DML", kind: "UPDATE", correctSQL: upd},
		{name: "insert matches", qType: "DML", kind: "INSERT", correctSQL: ins},
		{name: "declared DELETE but SQL is UPDATE", qType: "DML", kind: "DELETE", correctSQL: upd, wantErr: true, errSubstr: "UPDATE"},
		{name: "declared UPDATE but SQL is DELETE", qType: "DML", kind: "UPDATE", correctSQL: del, wantErr: true, errSubstr: "DELETE"},
		{name: "declared INSERT but SQL is DELETE", qType: "DML", kind: "INSERT", correctSQL: del, wantErr: true, errSubstr: "DELETE"},
		{name: "declared DELETE but SQL is INSERT", qType: "DML", kind: "DELETE", correctSQL: ins, wantErr: true, errSubstr: "INSERT"},

		// Derivable-only requirement (ARCH-057 rule 2) — the ARCH-050 regression.
		{name: "CTE-led DML with no kind is allowed", qType: "DML", kind: "", correctSQL: cte},
		{name: "CTE-led DML with a hand-set kind is allowed", qType: "DML", kind: "DELETE", correctSQL: cte},
		{name: "plain DELETE with no kind is still rejected", qType: "DML", kind: "", correctSQL: del, wantErr: true},
		{name: "bogus kind", qType: "DML", kind: "MERGE", correctSQL: del, wantErr: true},

		// Seed rules, unchanged.
		{name: "delete with seed", qType: "DML", kind: "DELETE", seedSQL: seed, correctSQL: del},
		{name: "update with seed", qType: "DML", kind: "UPDATE", seedSQL: seed, correctSQL: upd},
		{name: "insert with seed rejected", qType: "DML", kind: "INSERT", seedSQL: seed, correctSQL: ins, wantErr: true},
		{name: "non-insert seed rejected", qType: "DML", kind: "DELETE", seedSQL: "DELETE FROM t", correctSQL: del, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSeedFields(tc.qType, tc.kind, tc.seedSQL, tc.correctSQL)
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected with %v, want accept", err)
			}
			if tc.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tc.errSubstr)) {
				t.Errorf("error %v does not name %q", err, tc.errSubstr)
			}
		})
	}
}

// buildSeedRoundTripRouter wires exactly /login plus the two real
// lecturer-facing practice routes this test exercises (POST
// /lecturer/practice/question, GET /lecturer/practice/questions), mirroring
// cmd/main.go's real registration (practiceLecturer.Use(AuthMiddleware(...),
// RoleMiddleware(models.RoleAdmin, models.RoleLecturer))), using the REAL
// controller methods — never a mirror handler. postJSON (defined in
// create_account_role_http_test.go) supplies the POST-with-body helper.
func buildSeedRoundTripRouter(auth *services.AuthService, userCtrl *UserController, practiceCtrl *PracticeController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)

	practiceLecturer := v1.Group("/lecturer/practice")
	practiceLecturer.Use(AuthMiddleware(auth), RoleMiddleware(models.RoleAdmin, models.RoleLecturer))
	{
		practiceLecturer.POST("/question", practiceCtrl.Upsert)
		practiceLecturer.GET("/questions", practiceCtrl.ListForLecturer)
	}
	return r
}

// TestPracticeUpsertSeedFieldsRoundTrip_RealController is the ARCH-055 guard:
// PracticeController.Upsert binds the request body TWICE — once into
// models.PracticeQuestion (which can no longer see dml_kind/seed_sql, since
// ARCH-053 gives them `json:"-"`), and once into a narrow struct that
// recovers exactly those two. If that second bind were wrong, a lecturer's
// seed would be silently discarded with no error anywhere. This drives the
// REAL Upsert and ListForLecturer handlers through a real gin router and a
// real *repository.PostgresRepository — never a mirror handler.
func TestPracticeUpsertSeedFieldsRoundTrip_RealController(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	practiceCtrl := NewPracticeController(repo, services.NewValidationService(repo), auth)
	r := buildSeedRoundTripRouter(auth, userCtrl, practiceCtrl)

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpwrt")
	token := login(t, r, lecturer.Username, "lecpwrt")

	ctx := context.Background()
	cat, err := repo.CreatePracticeCategory(ctx, fmt.Sprintf("roundtrip_cat_%d", time.Now().UnixNano()), int(lecturer.ID))
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	t.Cleanup(func() { _ = repo.DeletePracticeCategory(context.Background(), int(cat.ID), true) })

	tbl := fmt.Sprintf("employee_roundtrip_%d", cat.ID)
	importSQL := fmt.Sprintf(`CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);`, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat.ID), importSQL, int(lecturer.ID), false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	// --- 1 & 3: POST a practice question with a non-empty dml_kind and
	// seed_sql, PLUS a client-supplied (bogus) auto_seed key, through the real
	// Upsert handler. -------------------------------------------------------
	seedSQL := fmt.Sprintf("INSERT INTO %s (ssn, salary) VALUES ('555555555', 4000)", tbl)
	body := map[string]interface{}{
		"category_id":   publicid.Encode(publicid.TypeCategory, int(cat.ID)),
		"target_table":  tbl,
		"correct_sql":   fmt.Sprintf("DELETE FROM %s WHERE ssn = '555555555'", tbl),
		"question_type": "DML",
		"dml_kind":      "DELETE",
		"seed_sql":      seedSQL,
		"requirement":   "delete a specific employee",
		"points":        5,
		// ARCH-053 inbound half: a client cannot supply auto_seed. If the
		// second bind (or anything else) let this through, the saved row's
		// auto_seed would echo "evil_table" below.
		"auto_seed": map[string]interface{}{"table": "evil_table", "columns": []interface{}{}},
	}
	w := postJSON(r, "/api/v1/lecturer/practice/question", token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("Upsert: expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var upsertResp struct {
		ID                   string `json:"id"`
		ExpectedResultStatus string `json:"expected_result_status"`
		CaptureError         string `json:"capture_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &upsertResp); err != nil {
		t.Fatalf("unmarshal Upsert response: %v", err)
	}
	qid, err := publicid.Decode(publicid.TypePractice, upsertResp.ID)
	if err != nil {
		t.Fatalf("decode saved id %q: %v", upsertResp.ID, err)
	}

	// 1: read the row back DIRECTLY FROM THE DATABASE (not via the API, which
	// would not distinguish "saved correctly" from "echoed back from the
	// request") and assert both dml_kind and seed_sql survived the double bind.
	saved, err := repo.GetPracticeQuestion(ctx, qid)
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("fixture precondition: expected a captured question, got status=%q capture_error=%q",
			saved.ExpectedResultStatus, saved.CaptureError)
	}
	if saved.DMLKind != "DELETE" {
		t.Errorf("ARCH-055 regression: dml_kind was dropped by the double bind, got %q, want DELETE", saved.DMLKind)
	}
	if saved.SeedSQL == "" {
		t.Errorf("ARCH-055 regression: seed_sql was dropped by the double bind")
	} else if !strings.Contains(saved.SeedSQL, "555555555") {
		t.Errorf("ARCH-055 regression: seed_sql in the database does not match what was posted, got %q", saved.SeedSQL)
	}

	// 3: the client-supplied auto_seed must never reach the saved row. The
	// backend's own rule ("auto_seed stays nil: ARCH-045 forbids both
	// mechanisms at once", postgres_practice.go) means the correct value here
	// is empty because seed_sql is non-empty — but the assertion that matters
	// is that it is NOT the client's "evil_table" value, whatever the backend
	// derived (or didn't).
	if strings.Contains(string(saved.AutoSeed), "evil_table") {
		t.Fatalf("SECURITY REGRESSION: client-supplied auto_seed reached the saved row: %s", saved.AutoSeed)
	}

	// --- 2: ListForLecturer must carry seed_sql for this question. auto_seed
	// and seed_sql are mutually exclusive on any single row by construction
	// (see the SeedSQL doc comment on models.PracticeQuestion: "When
	// non-empty it takes precedence over AutoSeed, which is then NULL"), so
	// proving the ARCH-054 view carries auto_seed too needs a second,
	// genuinely auto-seeded fixture rather than this same row.
	autoSeeded := mkAutoSeedRescuedPracticeQuestion(t, repo, int(lecturer.ID), "roundtriplist")

	w2 := do(r, http.MethodGet, "/api/v1/lecturer/practice/questions", token)
	if w2.Code != http.StatusOK {
		t.Fatalf("ListForLecturer: expected 200, got %d (%s)", w2.Code, w2.Body.String())
	}
	var listResp []map[string]interface{}
	if err := json.Unmarshal(w2.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal ListForLecturer response: %v", err)
	}
	wantSeedID := publicid.Encode(publicid.TypePractice, qid)
	wantAutoSeedID := publicid.Encode(publicid.TypePractice, int(autoSeeded.ID))
	foundSeed, foundAutoSeed := false, false
	for _, q := range listResp {
		idVal, _ := q["id"].(string)
		if idVal == wantSeedID {
			foundSeed = true
			if got, _ := q["seed_sql"].(string); got == "" {
				t.Errorf("ARCH-054 regression: ListForLecturer dropped seed_sql for the round-trip question: %+v", q)
			} else if !strings.Contains(got, "555555555") {
				t.Errorf("ARCH-054 regression: ListForLecturer seed_sql does not match what was saved, got %q", got)
			}
		}
		if idVal == wantAutoSeedID {
			foundAutoSeed = true
			if v, ok := q["auto_seed"]; !ok || v == nil {
				t.Errorf("ARCH-054 regression: ListForLecturer dropped auto_seed for the auto-seeded question: %+v", q)
			}
		}
	}
	if !foundSeed {
		t.Fatalf("expected the round-trip question (id=%s) in the ListForLecturer response: %+v", wantSeedID, listResp)
	}
	if !foundAutoSeed {
		t.Fatalf("expected the auto-seeded question (id=%s) in the ListForLecturer response: %+v", wantAutoSeedID, listResp)
	}

	// --- 4: a dml_kind that disagrees with correct_sql must be rejected with
	// a 400 naming both kinds (ARCH-057), through the same real Upsert
	// handler. A fresh category keeps this independent of the fixtures above.
	cat2, err := repo.CreatePracticeCategory(ctx, fmt.Sprintf("roundtrip_mismatch_cat_%d", time.Now().UnixNano()), int(lecturer.ID))
	if err != nil {
		t.Fatalf("CreatePracticeCategory (mismatch fixture): %v", err)
	}
	t.Cleanup(func() { _ = repo.DeletePracticeCategory(context.Background(), int(cat2.ID), true) })

	tbl2 := fmt.Sprintf("employee_roundtrip_mismatch_%d", cat2.ID)
	importSQL2 := fmt.Sprintf(`CREATE TABLE %s (ssn TEXT PRIMARY KEY, fname TEXT); INSERT INTO %s (ssn, fname) VALUES ('1', 'a');`, tbl2, tbl2)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat2.ID), importSQL2, int(lecturer.ID), false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory (mismatch fixture): %v", err)
	}

	mismatchBody := map[string]interface{}{
		"category_id":   publicid.Encode(publicid.TypeCategory, int(cat2.ID)),
		"target_table":  tbl2,
		"correct_sql":   fmt.Sprintf("UPDATE %s SET fname = 'b' WHERE ssn = '1'", tbl2),
		"question_type": "DML",
		"dml_kind":      "DELETE",
		"requirement":   "update a specific employee",
		"points":        5,
	}
	w4 := postJSON(r, "/api/v1/lecturer/practice/question", token, mismatchBody)
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a dml_kind/correct_sql mismatch, got %d (%s)", w4.Code, w4.Body.String())
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w4.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if !strings.Contains(errResp.Error, "DELETE") || !strings.Contains(errResp.Error, "UPDATE") {
		t.Errorf("expected the 400 message to name both DELETE and UPDATE, got %q", errResp.Error)
	}
}
