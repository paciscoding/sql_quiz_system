package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

type DBExecutor interface {
	Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error)
	Exec(ctx context.Context, schema, sql string) error
	Evaluate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error)
	EvaluatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (repository.PreviewResult, error)
}

type ValidationService struct {
	executor DBExecutor
}

func NewValidationService(executor DBExecutor) *ValidationService {
	return &ValidationService{executor: executor}
}

func (s *ValidationService) Validate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
	if studentSQL == "" {
		return models.ValidationResult{
			Status:    "syntax_error",
			Score:     0,
			Feedback:  "SQL text cannot be empty",
			IsCorrect: false,
		}, nil
	}

	result, err := s.executor.Evaluate(ctx, studentID, question, studentSQL, sandboxSchema)
	if err != nil {
		return models.ValidationResult{}, err
	}

	if result.Status == "error" {
		result.Status = "syntax_error"
		result.IsCorrect = false
		result.Score = 0
		return result, nil
	}

	var studentData []map[string]interface{}
	if err := json.Unmarshal(result.ValidationDetails, &studentData); err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to parse captured student data: %w", err)
	}

	return s.decideStatus(result, studentData, question), nil
}

// ValidatePreview runs the student SQL in a read-only sandbox and returns the
// three-state status together with the result rows and metadata. Unlike Validate
// it never persists a submission.
func (s *ValidationService) ValidatePreview(ctx context.Context, studentID int, q models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error) {
	if studentSQL == "" {
		return models.ValidationResult{
			Status:   "syntax_error",
			Feedback: "SQL text cannot be empty",
		}, []map[string]interface{}{}, []string{}, 0, false, nil
	}

	pr, err := s.executor.EvaluatePreview(ctx, studentID, q, studentSQL, sandboxSchema)
	if err != nil {
		return models.ValidationResult{}, nil, nil, 0, false, err
	}

	if pr.Result.Status == "error" {
		return models.ValidationResult{
			Status:   "syntax_error",
			Feedback: pr.Result.Feedback,
		}, pr.Rows, pr.Columns, pr.RowCount, pr.Truncated, nil
	}

	finalResult := s.decideStatus(pr.Result, pr.Rows, q)
	return finalResult, pr.Rows, pr.Columns, pr.RowCount, pr.Truncated, nil
}

func (s *ValidationService) decideStatus(execResult models.ValidationResult, studentData []map[string]interface{}, q models.Question) models.ValidationResult {
	var teacherData []map[string]interface{}
	if err := json.Unmarshal(q.ExpectedResult, &teacherData); err != nil {
		// Should not happen for well-formed questions; return incorrect
		execResult.Status = "incorrect"
		execResult.IsCorrect = false
		execResult.Score = 0
		execResult.Feedback = "The result is wrong."
		return execResult
	}

	if s.compareResults(teacherData, studentData, q.StrictMode) {
		execResult.Status = "correct"
		execResult.IsCorrect = true
		execResult.Score = q.Points
		execResult.Feedback = "Correct! Full points awarded."
	} else {
		execResult.Status = "incorrect"
		execResult.IsCorrect = false
		execResult.Score = 0
		execResult.Feedback = "The result is wrong."
	}
	return execResult
}

func (s *ValidationService) compareResults(teacher, student []map[string]interface{}, mode models.StrictMode) bool {
	if len(teacher) != len(student) {
		return false
	}

	if mode.Ordering {
		// Strict order: compare index by index
		for i := range teacher {
			if !s.compareRows(teacher[i], student[i], mode) {
				return false
			}
		}
		return true
	}

	// Non-strict order: every row in teacher must exist in student
	// This is O(N^2) but fine for small exam result sets (~90-150 rows max)
	sCopy := make([]map[string]interface{}, len(student))
	copy(sCopy, student)

	for _, tRow := range teacher {
		found := false
		for i, sRow := range sCopy {
			if s.compareRows(tRow, sRow, mode) {
				found = true
				// Remove matched row to handle duplicates correctly
				sCopy = append(sCopy[:i], sCopy[i+1:]...)
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *ValidationService) compareRows(teacher, student map[string]interface{}, mode models.StrictMode) bool {
	if len(teacher) != len(student) {
		return false
	}

	if mode.Attributes {
		// Strict attributes: keys must match
		for k, v := range teacher {
			sv, ok := student[k]
			if !ok || fmt.Sprintf("%v", v) != fmt.Sprintf("%v", sv) {
				return false
			}
		}
		return true
	}

	// Non-strict attributes: only values must match, regardless of key names
	// Collect all values from both rows
	var tVals, sVals []string
	for _, v := range teacher {
		tVals = append(tVals, fmt.Sprintf("%v", v))
	}
	for _, v := range student {
		sVals = append(sVals, fmt.Sprintf("%v", v))
	}

	// Compare values sets
	for _, tv := range tVals {
		found := false
		for i, sv := range sVals {
			if tv == sv {
				found = true
				sVals = append(sVals[:i], sVals[i+1:]...)
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
