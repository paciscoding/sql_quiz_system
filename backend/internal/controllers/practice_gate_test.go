package controllers

// ARCH-061 coverage: a submission shed because its schema was busy must be
// answered 503 and must NOT be recorded.
//
// The gate itself lives in the repository (ARCH-059) and is asserted there.
// What these tests pin is the controller's handling of the resulting sentinel,
// using a fake validator that simply returns it — deterministic, with no
// contention to arrange.
//
// They use a REAL repository (the handlers read the question, resolve the
// schema and persist the attempt) with a FAKE validator, and skip cleanly
// without TEST_DATABASE_URL via openCtrlTestRepo.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/ -run TestPracticeGate -p 1 -v

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// doJSON is the request-body counterpart to the package's existing `do` helper
// (password_http_integration_test.go:82), which sends no body.
func doJSON(r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// busyValidator always reports the schema as busy, exactly as the engine does
// when its wait budget expires.
type busyValidator struct{}

func (busyValidator) Validate(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error) {
	return models.ValidationResult{}, fmt.Errorf("%w: %s", repository.ErrSchemaBusy, schema)
}

func (busyValidator) ValidatePreview(ctx context.Context, studentID int, q models.Question, sql, schema, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error) {
	return models.ValidationResult{}, nil, nil, 0, false, fmt.Errorf("%w: %s", repository.ErrSchemaBusy, schema)
}

// buildPracticeGateRouter wires only the two practice student routes under the
// real auth stack, mirroring main.go's practice student group (minus the freeze
// middleware, which is not under test here).
func buildPracticeGateRouter(auth *services.AuthService, repo *repository.PostgresRepository, practiceCtrl *PracticeController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", NewUserController(auth, services.NewStudentImportService(repo)).Login)
	practice := v1.Group("/practice")
	practice.Use(AuthMiddleware(auth), RoleMiddleware(models.RoleStudent))
	{
		practice.POST("/submit", practiceCtrl.Submit)
		practice.POST("/preview", practiceCtrl.Preview)
	}
	return r
}

// mkGateFixture creates a lecturer, a practice category with one table, and one
// practice DML question. The seed_sql row is what makes capture succeed so
// ExpectedResultStatus reaches "captured", which the readiness gate at
// practice_controller.go:191 requires before Submit will call the validator.
func mkGateFixture(t *testing.T, repo *repository.PostgresRepository, ctx context.Context, tbl string) (catID int, questionID models.PracticeQuestionID) {
	t.Helper()

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	cat, err := repo.CreatePracticeCategory(ctx, fmt.Sprintf("gate_cat_%s", tbl), int(lecturer.ID))
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);
		INSERT INTO %s (ssn, name) VALUES ('111111111', 'Alice');`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat.ID), importSQL, int(lecturer.ID), false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   lecturer.ID,
		CategoryID:  cat.ID,
		TargetTable: tbl,
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "gate fixture",
		Points:      1,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl),
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("fixture question not captured: status=%q err=%q", saved.ExpectedResultStatus, saved.CaptureError)
	}
	return int(cat.ID), q.ID
}

func submitBody(t *testing.T, qid models.PracticeQuestionID, sql string) string {
	t.Helper()
	enc, err := qid.MarshalJSON()
	if err != nil {
		t.Fatalf("encode practice question id: %v", err)
	}
	return fmt.Sprintf(`{"practice_question_id":%s,"sql_text":%q}`, string(enc), sql)
}

// TestPracticeGate_BusySubmitShedsWithoutRecordingAttempt is the assertion most
// likely to regress silently. Practice persists every attempt regardless of
// outcome (practice_controller.go:205-224), so without an explicit branch a
// shed submission is written as is_correct=false for SQL that never ran,
// corrupting the student's history and their score.
func TestPracticeGate_BusySubmitShedsWithoutRecordingAttempt(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_shed")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, busyValidator{}, auth))

	student := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	tok := login(t, r, student.Username, "pw1")

	before, err := repo.ListPracticeAttempts(ctx, int(student.ID))
	if err != nil {
		t.Fatalf("ListPracticeAttempts (before): %v", err)
	}

	body := submitBody(t, qid, "DELETE FROM gate_shed WHERE ssn = '999999999'")
	w := doJSON(r, http.MethodPost, "/api/v1/practice/submit", tok, body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("submit into a busy schema: want 503, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "practice_busy") {
		t.Fatalf("503 body must carry error=practice_busy, got %s", w.Body.String())
	}

	after, err := repo.ListPracticeAttempts(ctx, int(student.ID))
	if err != nil {
		t.Fatalf("ListPracticeAttempts (after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a shed submission was recorded: attempts went %d -> %d; "+
			"evaluation never ran, so there is nothing to persist",
			len(before), len(after))
	}
}

// TestPracticeGate_BusyPreviewSheds: preview persists nothing, but it must
// still answer 503 rather than 500 so the client can retry.
func TestPracticeGate_BusyPreviewSheds(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_shed_preview")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, busyValidator{}, auth))

	student := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	tok := login(t, r, student.Username, "pw1")

	body := submitBody(t, qid, "DELETE FROM gate_shed_preview WHERE ssn = '999999999'")
	w := doJSON(r, http.MethodPost, "/api/v1/practice/preview", tok, body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("preview into a busy schema: want 503, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "practice_busy") {
		t.Fatalf("503 body must carry error=practice_busy, got %s", w.Body.String())
	}
}
