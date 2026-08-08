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
	cases := []struct {
		name    string
		qType   string
		kind    string
		seed    string
		wantErr bool
	}{
		{"query with no kind or seed", "QUERY", "", "", false},
		{"query with a kind", "QUERY", "DELETE", "", true},
		{"query with a seed", "QUERY", "", "INSERT INTO t (a) VALUES (1)", true},
		{"dml with no kind", "DML", "", "", true},
		{"dml delete with seed", "DML", "DELETE", "INSERT INTO t (a) VALUES (1)", false},
		{"dml update with seed", "DML", "UPDATE", "INSERT INTO t (a) VALUES (1)", false},
		{"dml insert with seed", "DML", "INSERT", "INSERT INTO t (a) VALUES (1)", true},
		{"dml insert without seed", "DML", "INSERT", "", false},
		{"dml with bogus kind", "DML", "MERGE", "", true},
		{"dml delete with non-insert seed", "DML", "DELETE", "DELETE FROM t", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSeedFields(tc.qType, tc.kind, tc.seed)
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected with %v, want accept", err)
			}
		})
	}
}
