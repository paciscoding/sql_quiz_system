package services

// Unit tests for ARCH-017/TODO-017. No database required — do NOT gate these
// behind TEST_DATABASE_URL, so `go test ./...` exercises them on every run.
//
// Two groups:
//  1. ValidatePreview must forward its previewSchema argument to
//     executor.EvaluatePreview completely unchanged — including the ""
//     (no-decoy) case. ValidatePreview itself must never substitute
//     sandboxSchema or invent a fallback for an empty previewSchema; that
//     fallback logic belongs one layer down, in EvaluatePreview /
//     postgres_evaluation.go. This is the pinned contract from TODO-008
//     between ValidatePreview and the executor.
//  2. decideDMLStatus must suppress the row-level diff (formatDMLDiff output)
//     iff q.DualSchema, and the suppression must only ever change Feedback
//     text on the incorrect path — never the verdict (Status/IsCorrect/Score),
//     which must be identical for a DualSchema=true and DualSchema=false
//     question given identical data.
//
// Run: go test ./internal/services/... -run TestValidatePreview
// Run: go test ./internal/services/... -run TestDecideDMLStatus

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// TestValidatePreview_PreviewSchemaForwarding proves ValidatePreview is a
// transparent pass-through for its previewSchema argument: whatever the
// caller passes in is exactly what the executor observes, whether that is a
// resolved decoy name or the empty string. If a future refactor ever dropped
// the parameter or substituted sandboxSchema when previewSchema=="", this
// test fails.
func TestValidatePreview_PreviewSchemaForwarding(t *testing.T) {
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1)}}
	teacherJSON := mustMarshal(t, teacherData)
	question := models.Question{
		ID:             1,
		Type:           "QUERY",
		Points:         5.0,
		ExpectedResult: teacherJSON,
	}

	cases := []struct {
		name          string
		previewSchema string
	}{
		{
			name:          "non-empty previewSchema (resolved decoy) is forwarded verbatim",
			previewSchema: "decoy_a",
		},
		{
			name:          "empty previewSchema is forwarded verbatim -- ValidatePreview must not substitute sandboxSchema or invent a fallback",
			previewSchema: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var observed string
			var called bool
			mock := &MockExecutor{
				EvaluatePreviewFunc: func(ctx context.Context, studentID int, q models.Question, sql string, sandboxSchema string, previewSchema string) (repository.PreviewResult, error) {
					called = true
					observed = previewSchema
					return repository.PreviewResult{
						Result:   models.ValidationResult{Status: "success"},
						Rows:     teacherData,
						Columns:  []string{"id"},
						RowCount: 1,
					}, nil
				},
			}
			svc := NewValidationService(mock)

			_, _, _, _, _, err := svc.ValidatePreview(ctx, 123, question, "SELECT * FROM t", "sbx_schema", tc.previewSchema)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called {
				t.Fatal("executor.EvaluatePreview was never invoked")
			}
			if observed != tc.previewSchema {
				t.Errorf("ValidatePreview forwarded previewSchema=%q to the executor, want %q verbatim", observed, tc.previewSchema)
			}
		})
	}
}

// TestValidatePreview_PreviewSchemaForwarding_SandboxSchemaIndependentlyForwarded
// is a companion check: previewSchema and sandboxSchema are two distinct
// parameters, and neither may bleed into the other. Confirms both arrive at
// the executor unchanged and un-swapped in the same call.
func TestValidatePreview_PreviewSchemaForwarding_SandboxSchemaIndependentlyForwarded(t *testing.T) {
	ctx := context.Background()
	teacherData := []map[string]interface{}{{"id": float64(1)}}
	question := models.Question{ID: 2, Type: "QUERY", Points: 5.0, ExpectedResult: mustMarshal(t, teacherData)}

	var observedSandbox, observedPreview string
	mock := &MockExecutor{
		EvaluatePreviewFunc: func(ctx context.Context, studentID int, q models.Question, sql string, sandboxSchema string, previewSchema string) (repository.PreviewResult, error) {
			observedSandbox = sandboxSchema
			observedPreview = previewSchema
			return repository.PreviewResult{Result: models.ValidationResult{Status: "success"}, Rows: teacherData, Columns: []string{"id"}, RowCount: 1}, nil
		},
	}
	svc := NewValidationService(mock)

	_, _, _, _, _, err := svc.ValidatePreview(ctx, 123, question, "SELECT * FROM t", "sandbox_exam_9", "curriculum_exam_9_preview")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if observedSandbox != "sandbox_exam_9" {
		t.Errorf("sandboxSchema forwarded as %q, want %q", observedSandbox, "sandbox_exam_9")
	}
	if observedPreview != "curriculum_exam_9_preview" {
		t.Errorf("previewSchema forwarded as %q, want %q", observedPreview, "curriculum_exam_9_preview")
	}
}

// TestDecideDMLStatus_DualSuppression exercises decideDMLStatus directly
// (unexported method, same package) rather than going through the full
// Validate/executor round trip, isolating the suppression branch itself.
// Table-drives over DualSchema x {rows differ, rows identical}.
func TestDecideDMLStatus_DualSuppression(t *testing.T) {
	svc := NewValidationService(&MockExecutor{})

	// Distinctive values so a leak is unambiguous if it ever appears in Feedback.
	teacherData := []map[string]interface{}{{"id": float64(1), "name": "Alice-teacher-secret"}}
	studentDataDiffers := []map[string]interface{}{{"id": float64(2), "name": "Bob-student-wrong"}}
	studentDataIdentical := []map[string]interface{}{{"id": float64(1), "name": "Alice-teacher-secret"}}

	baseQuestion := models.Question{ID: 42, Type: "DML", Points: 10.0, TargetTable: "accounts"}

	t.Run("dual + mismatch -> generic feedback, no row leak", func(t *testing.T) {
		q := baseQuestion
		q.DualSchema = true
		result := svc.decideDMLStatus(models.ValidationResult{Status: "success"}, teacherData, studentDataDiffers, q)

		if result.Status != "incorrect" || result.IsCorrect || result.Score != 0 {
			t.Fatalf("expected incorrect verdict with zero score, got %+v", result)
		}
		const wantFeedback = "Your result does not match the expected outcome."
		if result.Feedback != wantFeedback {
			t.Errorf("expected generic dual feedback %q, got %q", wantFeedback, result.Feedback)
		}
		for _, leaky := range []string{"Missing:", "Unexpected:", "Alice-teacher-secret", "Bob-student-wrong"} {
			if strings.Contains(result.Feedback, leaky) {
				t.Errorf("dual feedback leaked %q into Feedback: %q", leaky, result.Feedback)
			}
		}
	})

	t.Run("non-dual + mismatch -> detailed diff feedback (formatDMLDiff)", func(t *testing.T) {
		q := baseQuestion
		q.DualSchema = false
		result := svc.decideDMLStatus(models.ValidationResult{Status: "success"}, teacherData, studentDataDiffers, q)

		if result.Status != "incorrect" || result.IsCorrect || result.Score != 0 {
			t.Fatalf("expected incorrect verdict with zero score, got %+v", result)
		}
		if !strings.Contains(result.Feedback, "Missing:") || !strings.Contains(result.Feedback, "Unexpected:") {
			t.Errorf("expected detailed diff feedback containing 'Missing:' and 'Unexpected:', got %q", result.Feedback)
		}
	})

	t.Run("dual + identical rows -> correct (suppression never changes the verdict)", func(t *testing.T) {
		q := baseQuestion
		q.DualSchema = true
		result := svc.decideDMLStatus(models.ValidationResult{Status: "success"}, teacherData, studentDataIdentical, q)

		if result.Status != "correct" || !result.IsCorrect || result.Score != q.Points {
			t.Errorf("expected correct verdict with full score, got %+v", result)
		}
	})

	t.Run("non-dual + identical rows -> correct", func(t *testing.T) {
		q := baseQuestion
		q.DualSchema = false
		result := svc.decideDMLStatus(models.ValidationResult{Status: "success"}, teacherData, studentDataIdentical, q)

		if result.Status != "correct" || !result.IsCorrect || result.Score != q.Points {
			t.Errorf("expected correct verdict with full score, got %+v", result)
		}
	})
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}
