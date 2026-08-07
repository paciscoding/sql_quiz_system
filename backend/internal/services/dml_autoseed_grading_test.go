package services

// Regression-tester (step [7]) coverage for the dml-autoseed feature.
//
// Area #3 ("grading discrimination restored — the whole point of ARCH-008"):
// TestAutoSeedGradingDiscrimination_Practice_CorrectVsNoOp drives the REAL
// decision layer (ValidationService.Validate -> decideStatus ->
// decideDMLStatus) against a real Postgres-backed auto-seeded question, the
// same way dml_grading_integration_test.go's D2 test does — not just
// repository.Evaluate's raw Status="success"/"error" (which cannot tell
// "correct" apart from "incorrect" on its own; that discrimination is
// ARCH-005's multiset diff, one layer up in this package).
//
// Area #8 ("seed-failure handling"), second half: TestAutoSeedFailureFeedback_
// SurvivesDualSchemaARCH018Scrub is a pure unit test (MockExecutor, no DB)
// proving the real ARCH-018 scrub in validation_service.go's Validate defer
// treats repository.AutoSeedFailureFeedback as an allowed exact-match
// constant (like MultiStatementFeedback) and does NOT overwrite it with the
// student-blaming dualExecErrorFeedback wording.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/services/... -run AutoSeed -v -p 1

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/testsupport"
)

// autoSeedPoolsOnce guards a single InitStudentPools call for this test
// binary's shared repo. testsupport.SharedRepo's bootstrap() does NOT call
// InitStudentPools (only package repository's own openTestRepo does — see
// postgres_password_events_test.go) — that gap is exactly why
// TestDMLGrading_D2_CorrectUpdateGradesCorrectDespiteReordering is a KNOWN
// pre-existing failure in this package (validator-confirmed on a HEAD
// worktree without this feature). Rather than characterize that gap as an
// auto-seed defect, this file closes it locally by calling the real,
// exported InitStudentPools once before any test that needs repo.Evaluate to
// run under a real restricted role — a legitimate production initialization
// call, not a workaround baked into implementation code.
var autoSeedPoolsOnce sync.Once

func ensureAutoSeedStudentPools(t *testing.T, repo *repository.PostgresRepository) {
	t.Helper()
	var err error
	autoSeedPoolsOnce.Do(func() {
		err = repo.InitStudentPools(context.Background())
	})
	if err != nil {
		t.Fatalf("InitStudentPools: %v", err)
	}
}

// TestAutoSeedGradingDiscrimination_Practice_CorrectVsNoOp is the headline
// test for area #3: the SAME DELETE the lecturer used as the model answer
// (rescued at capture time by a derived seed row) grades CORRECT when a
// student submits it, and a no-op DELETE (matching nothing) grades
// INCORRECT — the exact discrimination ARCH-008 exists to protect, restored
// by ARCH-037's rescue.
func TestAutoSeedGradingDiscrimination_Practice_CorrectVsNoOp(t *testing.T) {
	repo := testsupport.SharedRepo(t)
	ensureAutoSeedStudentPools(t, repo)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsernameSvc("autoseed_disc_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cat, err := repo.CreatePracticeCategory(ctx, uniqueUsernameSvc("autoseed_disc_cat"), uid)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	defer func() { _ = repo.DeletePracticeCategory(ctx, int(cat.ID), true) }()

	const tbl = "employee_svc_disc"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);
		INSERT INTO %s (ssn, salary) VALUES ('111111111', 5000), ('222222222', 6000);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat.ID), importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  cat.ID,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Requirement: "delete the employee with ssn 999999999",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if len(saved.AutoSeed) == 0 {
		t.Fatalf("expected a non-nil auto_seed for a rescued 0-row DELETE")
	}
	question := saved.AsQuestion()

	schema, err := repo.GetPracticeCategorySchema(ctx, int(cat.ID))
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	svc := NewValidationService(repo)

	correctResult, err := svc.Validate(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl), schema)
	if err != nil {
		t.Fatalf("Validate (correct): %v", err)
	}
	if correctResult.Status != "correct" || !correctResult.IsCorrect || correctResult.Score != question.Points {
		t.Fatalf("expected the model DELETE to grade correct with full score, got status=%q is_correct=%v score=%v feedback=%q",
			correctResult.Status, correctResult.IsCorrect, correctResult.Score, correctResult.Feedback)
	}

	noopResult, err := svc.Validate(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = 'does-not-exist'", tbl), schema)
	if err != nil {
		t.Fatalf("Validate (noop): %v", err)
	}
	if noopResult.Status == "correct" || noopResult.IsCorrect {
		t.Fatalf("expected a no-op DELETE (matching nothing) to grade incorrect, got status=%q is_correct=%v",
			noopResult.Status, noopResult.IsCorrect)
	}
}

// TestAutoSeedFailureFeedback_SurvivesDualSchemaARCH018Scrub is a pure
// MockExecutor unit test (no DB) proving the real ARCH-018 scrub keeps
// repository.AutoSeedFailureFeedback intact on a dual-schema question,
// instead of overwriting it with the student-blaming dualExecErrorFeedback —
// exactly like the existing MultiStatementFeedback allowance right above it
// in validation_service.go.
func TestAutoSeedFailureFeedback_SurvivesDualSchemaARCH018Scrub(t *testing.T) {
	ctx := context.Background()

	question := models.Question{
		ID:         7,
		Type:       "DML",
		Points:     5.0,
		DualSchema: true,
	}

	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			// Mirrors exactly what evaluateDML returns on a seed-replay
			// failure: Status "error", the fixed AutoSeedFailureFeedback
			// constant, and a NIL error.
			return models.ValidationResult{
				Status:   "error",
				Feedback: repository.AutoSeedFailureFeedback,
			}, nil
		},
	}
	svc := NewValidationService(mock)

	result, err := svc.Validate(ctx, 123, question, "DELETE FROM t WHERE id = 1", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "syntax_error" {
		t.Fatalf("expected the executor 'error' status to map to 'syntax_error' (existing decideStatus contract), got %q", result.Status)
	}
	if result.Feedback != repository.AutoSeedFailureFeedback {
		t.Fatalf("SECURITY/UX REGRESSION: expected the ARCH-018 scrub to preserve AutoSeedFailureFeedback verbatim, got %q", result.Feedback)
	}
	if result.ValidationDetails != nil || result.Columns != nil {
		t.Fatalf("expected ValidationDetails/Columns to still be scrubbed to nil on a dual-schema question, got details=%s columns=%v",
			result.ValidationDetails, result.Columns)
	}
}

// TestAutoSeedFailureFeedback_NonDualSchema_PassesThroughUnchanged is the
// non-dual control: AutoSeedFailureFeedback is not otherwise altered when
// the question isn't dual-schema (the ARCH-018 defer no-ops entirely).
func TestAutoSeedFailureFeedback_NonDualSchema_PassesThroughUnchanged(t *testing.T) {
	ctx := context.Background()

	question := models.Question{
		ID:         8,
		Type:       "DML",
		Points:     5.0,
		DualSchema: false,
	}

	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:   "error",
				Feedback: repository.AutoSeedFailureFeedback,
			}, nil
		},
	}
	svc := NewValidationService(mock)

	result, err := svc.Validate(ctx, 123, question, "DELETE FROM t WHERE id = 1", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Feedback != repository.AutoSeedFailureFeedback {
		t.Fatalf("expected AutoSeedFailureFeedback unchanged on a non-dual question, got %q", result.Feedback)
	}
}
