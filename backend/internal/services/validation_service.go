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

	return s.decideStatus(result, studentData, result.Columns, question), nil
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

	finalResult := s.decideStatus(pr.Result, pr.Rows, pr.Columns, q)
	return finalResult, pr.Rows, pr.Columns, pr.RowCount, pr.Truncated, nil
}

func (s *ValidationService) decideStatus(execResult models.ValidationResult, studentData []map[string]interface{}, studentColumns []string, q models.Question) models.ValidationResult {
	var teacherData []map[string]interface{}
	if err := json.Unmarshal(q.ExpectedResult, &teacherData); err != nil {
		// Should not happen for well-formed questions; return incorrect
		execResult.Status = "incorrect"
		execResult.IsCorrect = false
		execResult.Score = 0
		execResult.Feedback = "The result is wrong."
		return execResult
	}

	if q.StrictMode.ColumnOrder {
		var teacherCols []string
		// q.ExpectedColumns is a JSONB array of column-name strings. If it is empty
		// or fails to parse (e.g. legacy rows captured before ordered-capture existed),
		// skip the gate rather than hard-failing.
		if len(q.ExpectedColumns) > 0 && json.Unmarshal(q.ExpectedColumns, &teacherCols) == nil && len(teacherCols) > 0 {
			if !equalStringSlices(teacherCols, studentColumns) {
				execResult.Status = "incorrect"
				execResult.IsCorrect = false
				execResult.Score = 0
				execResult.Feedback = "The column order does not match the expected output."
				return execResult
			}
		}
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

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
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
			if !ok || !valuesEqual(v, sv) {
				return false
			}
		}
		return true
	}

	// Non-strict attributes: only values must match, regardless of key names
	// Collect all values from both rows
	var tVals, sVals []any
	for _, v := range teacher {
		tVals = append(tVals, v)
	}
	for _, v := range student {
		sVals = append(sVals, v)
	}

	// Compare values sets
	for _, tv := range tVals {
		found := false
		for i, sv := range sVals {
			if valuesEqual(tv, sv) {
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

// numericEpsilon is the tolerance used when comparing two numeric cell values.
// It absorbs float-formatting drift (e.g. 1.0000000001 vs 1.0) without merging
// genuinely different values.
const numericEpsilon = 1e-9

// valuesEqual compares two SQL result cell values with type awareness. It is a
// strict superset of the previous fmt.Sprintf("%v", ...) equality for the common
// fixed-schema case:
//   - both nil            -> equal; exactly one nil -> not equal
//   - both numeric        -> compare magnitudes within numericEpsilon, so
//     int64(1) equals float64(1.0)
//   - both string/[]byte  -> compare as strings
//   - both bool           -> compare as bools
//   - otherwise/mismatched-> fall back to fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			diff := af - bf
			if diff < 0 {
				diff = -diff
			}
			return diff <= numericEpsilon
		}
	}

	if as, aok := toStringValue(a); aok {
		if bs, bok := toStringValue(b); bok {
			return as == bs
		}
	}

	if ab, aok := a.(bool); aok {
		if bb, bok := b.(bool); bok {
			return ab == bb
		}
	}

	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// toFloat reports whether v is a supported numeric type and, if so, its float64
// value. json.Number is included because encoding/json can decode numbers into it.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// toStringValue reports whether v is a string or []byte and, if so, its string form.
func toStringValue(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	default:
		return "", false
	}
}
