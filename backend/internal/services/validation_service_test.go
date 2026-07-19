package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// MockExecutor implements DBExecutor for testing
type MockExecutor struct {
	EvaluateFunc        func(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error)
	EvaluatePreviewFunc func(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (repository.PreviewResult, error)
}

func (m *MockExecutor) Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *MockExecutor) Exec(ctx context.Context, schema, sql string) error { return nil }
func (m *MockExecutor) Evaluate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
	return m.EvaluateFunc(ctx, studentID, question, studentSQL, sandboxSchema)
}
func (m *MockExecutor) EvaluatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (repository.PreviewResult, error) {
	return m.EvaluatePreviewFunc(ctx, studentID, question, studentSQL, sandboxSchema)
}

func TestValidationService_Validate(t *testing.T) {
	mock := &MockExecutor{}
	service := NewValidationService(mock)
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": 1, "name": "Alice"}}
	teacherJSON, _ := json.Marshal(teacherData)

	question := models.Question{
		ID:             1,
		CorrectSQL:     "SELECT * FROM users",
		Type:           "QUERY",
		Points:         10.0,
		ExpectedResult: teacherJSON,
	}

	t.Run("Correct Submission", func(t *testing.T) {
		studentData := []map[string]interface{}{{"id": 1, "name": "Alice"}}
		studentJSON, _ := json.Marshal(studentData)

		mock.EvaluateFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:            "success",
				ValidationDetails: studentJSON,
			}, nil
		}

		result, err := service.Validate(ctx, 123, question, "SELECT * FROM users", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if !result.IsCorrect || result.Score != 10.0 {
			t.Errorf("Expected correct result and full score, got %+v", result)
		}
	})

	t.Run("Incorrect Results", func(t *testing.T) {
		studentData := []map[string]interface{}{{"id": 1, "name": "Bob"}}
		studentJSON, _ := json.Marshal(studentData)

		mock.EvaluateFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:            "success",
				ValidationDetails: studentJSON,
			}, nil
		}

		result, err := service.Validate(ctx, 123, question, "SELECT * FROM other", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if result.IsCorrect || result.Score != 0 {
			t.Errorf("Expected incorrect result and zero score, got %+v", result)
		}
	})

	t.Run("Execution Error", func(t *testing.T) {
		mock.EvaluateFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{
				Status:   "error",
				Feedback: "SQL Error: syntax error",
			}, nil
		}

		result, err := service.Validate(ctx, 123, question, "INVALID", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Expected no error from Validate, got %v", err)
		}
		if result.Status != "syntax_error" || result.Score != 0 || result.IsCorrect {
			t.Errorf("Expected syntax_error status and zero score, got %+v", result)
		}
	})

	t.Run("Strict Ordering - Success", func(t *testing.T) {
		qStrict := question
		qStrict.StrictMode.Ordering = true

		data := []map[string]interface{}{{"id": 1}, {"id": 2}}
		dataJSON, _ := json.Marshal(data)
		qStrict.ExpectedResult = dataJSON

		mock.EvaluateFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{Status: "success", ValidationDetails: dataJSON}, nil
		}

		result, _ := service.Validate(ctx, 123, qStrict, "...", "sandbox_exam_1")
		if !result.IsCorrect {
			t.Error("Expected correct result with matching order")
		}
	})

	t.Run("Strict Ordering - Failure", func(t *testing.T) {
		qStrict := question
		qStrict.StrictMode.Ordering = true

		teacherData := []map[string]interface{}{{"id": 1}, {"id": 2}}
		teacherJSON, _ := json.Marshal(teacherData)
		qStrict.ExpectedResult = teacherJSON

		studentData := []map[string]interface{}{{"id": 2}, {"id": 1}}
		studentJSON, _ := json.Marshal(studentData)

		mock.EvaluateFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{Status: "success", ValidationDetails: studentJSON}, nil
		}

		result, _ := service.Validate(ctx, 123, qStrict, "...", "sandbox_exam_1")
		if result.IsCorrect {
			t.Error("Expected failure due to mismatched order")
		}
	})

	t.Run("Non-Strict Ordering - Success", func(t *testing.T) {
		qNormal := question
		qNormal.StrictMode.Ordering = false

		teacherData := []map[string]interface{}{{"id": 1}, {"id": 2}}
		teacherJSON, _ := json.Marshal(teacherData)
		qNormal.ExpectedResult = teacherJSON

		studentData := []map[string]interface{}{{"id": 2}, {"id": 1}}
		studentJSON, _ := json.Marshal(studentData)

		mock.EvaluateFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{Status: "success", ValidationDetails: studentJSON}, nil
		}

		result, _ := service.Validate(ctx, 123, qNormal, "...", "sandbox_exam_1")
		if !result.IsCorrect {
			t.Error("Expected success for mismatched order in non-strict mode")
		}
	})
}

func TestValidationService_ValidatePreview(t *testing.T) {
	mock := &MockExecutor{}
	service := NewValidationService(mock)
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1)}}
	teacherJSON, _ := json.Marshal(teacherData)
	examID := models.ExamID(1)
	question := models.Question{
		ID:             1,
		Type:           "QUERY",
		ExamID:         &examID,
		Points:         10.0,
		ExpectedResult: teacherJSON,
	}

	t.Run("Correct Preview", func(t *testing.T) {
		rows := []map[string]interface{}{{"id": float64(1)}}
		cols := []string{"id"}
		mock.EvaluatePreviewFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (repository.PreviewResult, error) {
			return repository.PreviewResult{
				Result:   models.ValidationResult{Status: "success"},
				Rows:     rows,
				Columns:  cols,
				RowCount: 1,
			}, nil
		}

		vResult, vRows, vCols, vCount, vTrunc, err := service.ValidatePreview(ctx, 123, question, "SELECT * FROM t", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if vResult.Status != "correct" {
			t.Errorf("Expected status 'correct', got %q", vResult.Status)
		}
		if len(vRows) != 1 || len(vCols) != 1 || vCols[0] != "id" {
			t.Errorf("Expected rows/cols pass-through, got rows=%v cols=%v", vRows, vCols)
		}
		if vCount != 1 || vTrunc {
			t.Errorf("Expected count=1 trunc=false, got count=%d trunc=%v", vCount, vTrunc)
		}
	})

	t.Run("Incorrect Preview", func(t *testing.T) {
		rows := []map[string]interface{}{{"id": float64(99)}}
		cols := []string{"id"}
		mock.EvaluatePreviewFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (repository.PreviewResult, error) {
			return repository.PreviewResult{
				Result:   models.ValidationResult{Status: "success"},
				Rows:     rows,
				Columns:  cols,
				RowCount: 1,
			}, nil
		}

		vResult, vRows, _, _, _, err := service.ValidatePreview(ctx, 123, question, "SELECT * FROM t", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if vResult.Status != "incorrect" {
			t.Errorf("Expected status 'incorrect', got %q", vResult.Status)
		}
		if len(vRows) != 1 {
			t.Errorf("Expected rows pass-through, got %v", vRows)
		}
	})

	t.Run("Syntax Error Preview", func(t *testing.T) {
		mock.EvaluatePreviewFunc = func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (repository.PreviewResult, error) {
			return repository.PreviewResult{
				Result: models.ValidationResult{Status: "error", Feedback: "SQL Error: foo"},
			}, nil
		}

		vResult, _, _, _, _, err := service.ValidatePreview(ctx, 123, question, "INVALID SQL", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if vResult.Status != "syntax_error" {
			t.Errorf("Expected status 'syntax_error', got %q", vResult.Status)
		}
		if vResult.Feedback == "" || !strings.Contains(vResult.Feedback, "SQL Error") {
			t.Errorf("Expected feedback containing 'SQL Error', got %q", vResult.Feedback)
		}
	})
}

// TestValidationService_SandboxPracticeSchema verifies that when "sandbox_practice"
// is passed as the sandboxSchema, the mock executor receives that exact string,
// and that the existing exam schema ("sandbox_exam_1") behaviour is byte-identical.
func TestValidationService_SandboxPracticeSchema(t *testing.T) {
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"id": float64(1)}}
	teacherJSON, _ := json.Marshal(teacherData)

	question := models.Question{
		ID:             10,
		Type:           "DML",
		Points:         3.0,
		ExpectedResult: teacherJSON,
	}

	t.Run("sandbox_practice schema forwarded verbatim", func(t *testing.T) {
		var capturedSchema string
		mock := &MockExecutor{
			EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
				capturedSchema = schema
				studentJSON, _ := json.Marshal(teacherData) // matching result
				return models.ValidationResult{
					Status:            "success",
					ValidationDetails: studentJSON,
				}, nil
			},
		}
		svc := NewValidationService(mock)

		_, err := svc.Validate(ctx, 1, question, "INSERT INTO t VALUES(1)", "sandbox_practice")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedSchema != "sandbox_practice" {
			t.Errorf("executor received schema %q; want %q", capturedSchema, "sandbox_practice")
		}
	})

	t.Run("sandbox_exam_1 schema behaviour unchanged", func(t *testing.T) {
		var capturedSchema string
		studentData := []map[string]interface{}{{"id": float64(1)}}
		studentJSON, _ := json.Marshal(studentData)

		mock := &MockExecutor{
			EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
				capturedSchema = schema
				return models.ValidationResult{
					Status:            "success",
					ValidationDetails: studentJSON,
				}, nil
			},
		}
		svc := NewValidationService(mock)

		result, err := svc.Validate(ctx, 1, question, "SELECT 1", "sandbox_exam_1")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if capturedSchema != "sandbox_exam_1" {
			t.Errorf("executor received schema %q; want %q", capturedSchema, "sandbox_exam_1")
		}
		// Verify result is correct (byte-identical behaviour to normal exam path)
		if !result.IsCorrect {
			t.Errorf("Expected correct result for exam path, got %+v", result)
		}
		if result.Score != question.Points {
			t.Errorf("Expected score %v, got %v", question.Points, result.Score)
		}
	})

	t.Run("sandbox_practice empty SQL short-circuits before executor", func(t *testing.T) {
		executorCalled := false
		mock := &MockExecutor{
			EvaluateFunc: func(ctx context.Context, studentID int, q models.Question, sql string, schema string) (models.ValidationResult, error) {
				executorCalled = true
				return models.ValidationResult{}, nil
			},
		}
		svc := NewValidationService(mock)

		result, err := svc.Validate(ctx, 1, question, "", "sandbox_practice")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if executorCalled {
			t.Error("Executor should not be called for empty SQL")
		}
		if result.Status != "syntax_error" {
			t.Errorf("Expected syntax_error for empty SQL, got %q", result.Status)
		}
	})
}

// TestValidationService_StrictColumnOrder covers the independent Column Order
// grading gate. The row values are identical in every subtest (so the value/set
// comparison always passes); only the column ORDER and the StrictMode.ColumnOrder
// flag vary, isolating the new gate.
func TestValidationService_StrictColumnOrder(t *testing.T) {
	mock := &MockExecutor{}
	service := NewValidationService(mock)
	ctx := context.Background()

	teacherData := []map[string]interface{}{{"name": "Alice", "age": 30}}
	teacherJSON, _ := json.Marshal(teacherData)
	teacherCols, _ := json.Marshal([]string{"name", "age"})
	// Identical row content the student returns; value comparison passes regardless of order.
	studentJSON, _ := json.Marshal(teacherData)

	base := models.Question{
		ID:              1,
		CorrectSQL:      "SELECT name, age FROM users",
		Type:            "QUERY",
		Points:          10.0,
		ExpectedResult:  teacherJSON,
		ExpectedColumns: teacherCols,
	}

	withCols := func(cols []string) func(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error) {
		return func(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error) {
			return models.ValidationResult{Status: "success", ValidationDetails: studentJSON, Columns: cols}, nil
		}
	}

	t.Run("ColumnOrder on, order matches -> correct", func(t *testing.T) {
		q := base
		q.StrictMode.ColumnOrder = true
		mock.EvaluateFunc = withCols([]string{"name", "age"})
		result, _ := service.Validate(ctx, 1, q, "SELECT name, age FROM users", "sandbox_exam_1")
		if !result.IsCorrect || result.Score != 10.0 {
			t.Errorf("Expected correct with matching column order, got %+v", result)
		}
	})

	t.Run("ColumnOrder on, order differs -> incorrect with column-order feedback", func(t *testing.T) {
		q := base
		q.StrictMode.ColumnOrder = true
		mock.EvaluateFunc = withCols([]string{"age", "name"})
		result, _ := service.Validate(ctx, 1, q, "SELECT age, name FROM users", "sandbox_exam_1")
		if result.IsCorrect || result.Score != 0 {
			t.Errorf("Expected incorrect for wrong column order, got %+v", result)
		}
		if !strings.Contains(strings.ToLower(result.Feedback), "column order") {
			t.Errorf("Expected column-order feedback, got %q", result.Feedback)
		}
	})

	t.Run("ColumnOrder off, order differs -> still correct (gate not applied)", func(t *testing.T) {
		q := base
		q.StrictMode.ColumnOrder = false
		mock.EvaluateFunc = withCols([]string{"age", "name"})
		result, _ := service.Validate(ctx, 1, q, "SELECT age, name FROM users", "sandbox_exam_1")
		if !result.IsCorrect {
			t.Errorf("Expected correct when ColumnOrder is off regardless of order, got %+v", result)
		}
	})

	t.Run("ColumnOrder on but ExpectedColumns empty -> gate skipped (legacy safe)", func(t *testing.T) {
		q := base
		q.StrictMode.ColumnOrder = true
		q.ExpectedColumns = nil // legacy question captured before ordered-capture existed
		mock.EvaluateFunc = withCols([]string{"age", "name"})
		result, _ := service.Validate(ctx, 1, q, "SELECT age, name FROM users", "sandbox_exam_1")
		if !result.IsCorrect {
			t.Errorf("Expected correct (gate skipped) when ExpectedColumns is empty, got %+v", result)
		}
	})
}
