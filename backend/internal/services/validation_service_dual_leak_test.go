package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// This file covers ARCH-018 GAP 1: ValidationService.Validate must scrub every
// B-derived field on dual-schema questions (ValidationDetails, Columns, and the
// syntax_error Feedback text) while leaving Status/IsCorrect/Score - the verdict
// - and every non-dual behaviour completely untouched. The defer-based scrub in
// Validate runs strictly after decideStatus/decideDMLStatus have already
// consumed the rows for grading, so these tests also serve as the proof that
// grading is unchanged: they assert the SAME verdict for dual and non-dual runs
// fed identical executor output.

// TestValidate_DualSchema_ScrubsValidationDetailsAndColumns is acceptance A1:
// a correct dual QUERY submission returns the correct verdict AND nil
// ValidationDetails/Columns.
func TestValidate_DualSchema_ScrubsValidationDetailsAndColumns(t *testing.T) {
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1), "name": "Alice"}}
	teacherJSON, _ := json.Marshal(teacherData)
	studentJSON, _ := json.Marshal(teacherData) // matches -> correct

	question := models.Question{
		ID:             1,
		Type:           "QUERY",
		Points:         10.0,
		ExpectedResult: teacherJSON,
		DualSchema:     true,
	}

	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:            "success",
				ValidationDetails: studentJSON,
				Columns:           []string{"id", "name"},
			}, nil
		},
	}
	svc := NewValidationService(mock)

	result, err := svc.Validate(ctx, 123, question, "SELECT * FROM t", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != "correct" || !result.IsCorrect || result.Score != question.Points {
		t.Errorf("expected correct verdict with full score, got %+v", result)
	}
	if result.ValidationDetails != nil {
		t.Errorf("expected ValidationDetails nil for dual question, got %s", result.ValidationDetails)
	}
	if result.Columns != nil {
		t.Errorf("expected Columns nil for dual question, got %v", result.Columns)
	}
}

// TestValidate_NonDualSchema_Unaffected is acceptance A2: for identical
// executor output, DualSchema=false leaves ValidationDetails and Columns
// exactly as the executor produced them - non-dual is byte-identical to today.
func TestValidate_NonDualSchema_Unaffected(t *testing.T) {
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1), "name": "Alice"}}
	teacherJSON, _ := json.Marshal(teacherData)
	studentJSON, _ := json.Marshal(teacherData)
	studentCols := []string{"id", "name"}

	question := models.Question{
		ID:             1,
		Type:           "QUERY",
		Points:         10.0,
		ExpectedResult: teacherJSON,
		DualSchema:     false,
	}

	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:            "success",
				ValidationDetails: studentJSON,
				Columns:           studentCols,
			}, nil
		},
	}
	svc := NewValidationService(mock)

	result, err := svc.Validate(ctx, 123, question, "SELECT * FROM t", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(result.ValidationDetails) != string(studentJSON) {
		t.Errorf("expected ValidationDetails byte-equal to executor output, got %s want %s", result.ValidationDetails, studentJSON)
	}
	if len(result.Columns) != len(studentCols) || result.Columns[0] != studentCols[0] || result.Columns[1] != studentCols[1] {
		t.Errorf("expected Columns unchanged, got %v want %v", result.Columns, studentCols)
	}
}

// TestValidate_DualVsNonDual_IdenticalVerdict is acceptance A3: for identical
// executor output, Status/IsCorrect/Score are EQUAL whether DualSchema is true
// or false, proving the scrub (in the defer) runs strictly after decideStatus
// and never influences the grading decision itself - only the row-shaped
// fields it clears afterwards differ.
func TestValidate_DualVsNonDual_IdenticalVerdict(t *testing.T) {
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1)}, {"id": float64(2)}}
	teacherJSON, _ := json.Marshal(teacherData)
	// Deliberately wrong student data so the verdict under test is "incorrect",
	// not just the trivial "correct" case.
	studentData := []map[string]interface{}{{"id": float64(1)}}
	studentJSON, _ := json.Marshal(studentData)

	baseQuestion := models.Question{
		ID:             1,
		Type:           "QUERY",
		Points:         10.0,
		ExpectedResult: teacherJSON,
	}

	newMock := func() *MockExecutor {
		return &MockExecutor{
			EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
				return models.ValidationResult{
					Status:            "success",
					ValidationDetails: studentJSON,
					Columns:           []string{"id"},
				}, nil
			},
		}
	}

	dualQuestion := baseQuestion
	dualQuestion.DualSchema = true
	dualResult, err := NewValidationService(newMock()).Validate(ctx, 123, dualQuestion, "SELECT id FROM t", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error (dual): %v", err)
	}

	nonDualQuestion := baseQuestion
	nonDualQuestion.DualSchema = false
	nonDualResult, err := NewValidationService(newMock()).Validate(ctx, 123, nonDualQuestion, "SELECT id FROM t", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error (non-dual): %v", err)
	}

	if dualResult.Status != nonDualResult.Status {
		t.Errorf("Status differs between dual and non-dual: dual=%q non-dual=%q", dualResult.Status, nonDualResult.Status)
	}
	if dualResult.IsCorrect != nonDualResult.IsCorrect {
		t.Errorf("IsCorrect differs between dual and non-dual: dual=%v non-dual=%v", dualResult.IsCorrect, nonDualResult.IsCorrect)
	}
	if dualResult.Score != nonDualResult.Score {
		t.Errorf("Score differs between dual and non-dual: dual=%v non-dual=%v", dualResult.Score, nonDualResult.Score)
	}
	// Sanity: the verdict under test is actually "incorrect", not a trivially
	// true comparison of two zero values.
	if nonDualResult.IsCorrect {
		t.Fatalf("test setup error: expected an incorrect verdict to exercise a non-trivial comparison")
	}

	// And the row-shaped fields DID diverge as expected - proving the scrub
	// still ran on the dual side.
	if dualResult.ValidationDetails != nil {
		t.Errorf("expected dual ValidationDetails nil, got %s", dualResult.ValidationDetails)
	}
	if nonDualResult.ValidationDetails == nil {
		t.Errorf("expected non-dual ValidationDetails populated")
	}
}

// TestValidate_DualSchema_SyntaxErrorFeedbackGenericized is acceptance A4: a
// dual-schema syntax_error result has its executor Feedback (a data-dependent
// oracle over schema B, ARCH-018 e2) replaced by the fixed generic constant,
// while a non-dual question with the identical input keeps the original
// feedback verbatim.
func TestValidate_DualSchema_SyntaxErrorFeedbackGenericized(t *testing.T) {
	ctx := context.Background()
	const leakyFeedback = "SQL Error: ERROR: division by zero (SQLSTATE 22012)"

	question := models.Question{ID: 7, Type: "QUERY", Points: 5.0, ExpectedResult: json.RawMessage(`[]`)}

	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:   "error",
				Feedback: leakyFeedback,
			}, nil
		},
	}
	svc := NewValidationService(mock)

	t.Run("dual replaces feedback with generic constant", func(t *testing.T) {
		dualQuestion := question
		dualQuestion.DualSchema = true
		result, err := svc.Validate(ctx, 123, dualQuestion, "SELECT 1/0", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Feedback != dualExecErrorFeedback {
			t.Errorf("expected generic feedback %q, got %q", dualExecErrorFeedback, result.Feedback)
		}
		for _, leaky := range []string{"SQL Error:", "SQLSTATE", "division by zero"} {
			if strings.Contains(result.Feedback, leaky) {
				t.Errorf("scrubbed feedback %q still contains leaky substring %q", result.Feedback, leaky)
			}
		}
	})

	t.Run("non-dual keeps original feedback verbatim", func(t *testing.T) {
		nonDualQuestion := question
		nonDualQuestion.DualSchema = false
		result, err := svc.Validate(ctx, 123, nonDualQuestion, "SELECT 1/0", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Feedback != leakyFeedback {
			t.Errorf("expected original feedback %q, got %q", leakyFeedback, result.Feedback)
		}
	})
}

// TestValidate_DualSchema_EmptySQLEarlyReturnScrubbed is acceptance A5: the
// empty-SQL early return (before the executor is ever called) is still covered
// by the defer for dual questions.
func TestValidate_DualSchema_EmptySQLEarlyReturnScrubbed(t *testing.T) {
	ctx := context.Background()

	executorCalled := false
	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			executorCalled = true
			return models.ValidationResult{}, nil
		},
	}
	svc := NewValidationService(mock)

	question := models.Question{ID: 9, Type: "QUERY", Points: 5.0, ExpectedResult: json.RawMessage(`[]`), DualSchema: true}

	result, err := svc.Validate(ctx, 123, question, "", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executorCalled {
		t.Error("executor should not be called for empty SQL")
	}
	if result.Status != "syntax_error" {
		t.Errorf("expected syntax_error status, got %q", result.Status)
	}
	if result.ValidationDetails != nil {
		t.Errorf("expected ValidationDetails nil, got %s", result.ValidationDetails)
	}
}

// TestValidate_DualSchema_DMLMismatchFeedbackStillGeneric is acceptance A6: a
// regression guard confirming the pre-existing ARCH-007 generic DML mismatch
// feedback is untouched by this change, and confirming the new scrub still
// clears ValidationDetails/Columns on that path too.
func TestValidate_DualSchema_DMLMismatchFeedbackStillGeneric(t *testing.T) {
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1), "secret": "B-only-value"}}
	teacherJSON, _ := json.Marshal(teacherData)
	studentData := []map[string]interface{}{{"id": float64(2), "secret": "wrong"}}
	studentJSON, _ := json.Marshal(studentData)

	question := models.Question{
		ID:             3,
		Type:           "DML",
		Points:         10.0,
		TargetTable:    "orders",
		ExpectedResult: teacherJSON,
		DualSchema:     true,
	}

	mock := &MockExecutor{
		EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:            "success",
				ValidationDetails: studentJSON,
				Columns:           []string{"id", "secret"},
			}, nil
		},
	}
	svc := NewValidationService(mock)

	result, err := svc.Validate(ctx, 123, question, "INSERT INTO orders VALUES (2, 'wrong')", "sandbox_exam_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsCorrect {
		t.Fatalf("test setup error: expected mismatch to be graded incorrect")
	}
	const wantFeedback = "Your result does not match the expected outcome."
	if result.Feedback != wantFeedback {
		t.Errorf("expected generic ARCH-007 DML feedback %q, got %q", wantFeedback, result.Feedback)
	}
	if strings.Contains(result.Feedback, "B-only-value") {
		t.Errorf("DML mismatch feedback leaked schema B row content: %q", result.Feedback)
	}
	if result.ValidationDetails != nil {
		t.Errorf("expected ValidationDetails nil for dual DML question, got %s", result.ValidationDetails)
	}
	if result.Columns != nil {
		t.Errorf("expected Columns nil for dual DML question, got %v", result.Columns)
	}
}
