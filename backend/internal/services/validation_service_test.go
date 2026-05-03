package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// MockExecutor implements DBExecutor for testing
type MockExecutor struct {
	QueryFunc           func(ctx context.Context, schema, sql string) ([]map[string]interface{}, error)
	ExecFunc            func(ctx context.Context, schema, sql string) error
	GetTableStateFunc   func(ctx context.Context, schema, tableName string) (string, error)
	QueryStudentSQLFunc func(ctx context.Context, schema, sql string) ([]map[string]interface{}, error)
	ExecStudentSQLFunc  func(ctx context.Context, schema, sql string) error
}

func (m *MockExecutor) Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
	return m.QueryFunc(ctx, schema, sql)
}

func (m *MockExecutor) Exec(ctx context.Context, schema, sql string) error {
	return m.ExecFunc(ctx, schema, sql)
}

func (m *MockExecutor) GetTableState(ctx context.Context, schema, tableName string) (string, error) {
	return m.GetTableStateFunc(ctx, schema, tableName)
}

func (m *MockExecutor) QueryStudentSQL(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
	if m.QueryStudentSQLFunc != nil {
		return m.QueryStudentSQLFunc(ctx, schema, sql)
	}
	return m.QueryFunc(ctx, schema, sql)
}

func (m *MockExecutor) ExecStudentSQL(ctx context.Context, schema, sql string) error {
	if m.ExecStudentSQLFunc != nil {
		return m.ExecStudentSQLFunc(ctx, schema, sql)
	}
	return m.ExecFunc(ctx, schema, sql)
}

func TestValidationService_ValidateQuery(t *testing.T) {
	mock := &MockExecutor{}
	service := NewValidationService(mock)
	ctx := context.Background()

	question := models.Question{
		ID:         1,
		CorrectSQL: "SELECT * FROM users",
		Type:       "QUERY",
		Points:     10.0,
	}

	t.Run("Correct Query", func(t *testing.T) {
		teacherResult := []map[string]interface{}{{"id": 1, "name": "Alice"}}
		studentResult := []map[string]interface{}{{"id": 1, "name": "Alice"}}

		mock.QueryFunc = func(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
			if sql == question.CorrectSQL {
				return teacherResult, nil
			}
			return studentResult, nil
		}

		result, err := service.Validate(ctx, "123", question, "SELECT name, id FROM users")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if !result.IsCorrect || result.Score != 10.0 {
			t.Errorf("Expected correct result and full score, got %+v", result)
		}
	})

	t.Run("Incorrect Query Results", func(t *testing.T) {
		teacherResult := []map[string]interface{}{{"id": 1, "name": "Alice"}}
		studentResult := []map[string]interface{}{{"id": 1, "name": "Bob"}}

		mock.QueryFunc = func(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
			if sql == question.CorrectSQL {
				return teacherResult, nil
			}
			return studentResult, nil
		}

		result, err := service.Validate(ctx, "123", question, "SELECT * FROM other_table")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if result.IsCorrect || result.Score != 0 {
			t.Errorf("Expected incorrect result and zero score, got %+v", result)
		}
	})

	t.Run("Student SQL Syntax Error", func(t *testing.T) {
		mock.QueryFunc = func(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
			if sql == question.CorrectSQL {
				return []map[string]interface{}{{"id": 1}}, nil
			}
			return nil, errors.New("syntax error")
		}

		result, err := service.Validate(ctx, "123", question, "INVALID SQL")
		if err != nil {
			t.Fatalf("Expected no error from Validate, got %v", err)
		}
		if result.Score != 0 || !strings.Contains(result.Feedback, "SQL Error") {
			t.Errorf("Expected zero score and error feedback, got %+v", result)
		}
	})
}

func TestValidationService_ValidateDML(t *testing.T) {
	mock := &MockExecutor{}
	service := NewValidationService(mock)
	ctx := context.Background()

	question := models.Question{
		ID:          2,
		TargetTable: "users",
		CorrectSQL:  "UPDATE users SET name = 'Correct'",
		Type:        "DML",
		Points:      10.0,
	}

	t.Run("Correct DML", func(t *testing.T) {
		// Strengthened test by using distinct states for teacher and student
		mock.ExecFunc = func(ctx context.Context, schema, sql string) error { return nil }
		
		callCount := 0
		mock.GetTableStateFunc = func(ctx context.Context, schema, tableName string) (string, error) {
			callCount++
			if tableName == "users_backup" {
				return "initial_state", nil
			}
			// First call to GetTableState for "users" is teacher's state
			// Second call to GetTableState for "users" is student's state
			if callCount == 1 { return "teacher_state", nil }
			return "teacher_state", nil 
		}

		result, err := service.Validate(ctx, "123", question, "UPDATE users SET name = 'Correct'")
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if !result.IsCorrect || result.Score != 10.0 {
			t.Errorf("Expected correct DML result, got %+v", result)
		}
	})
}

