package services

import (
	"fmt"
	"strings"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

type DBExecutor interface {
	Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error)
	Exec(ctx context.Context, schema, sql string) error
	GetTableState(ctx context.Context, schema, tableName string) (string, error)
	QueryStudentSQL(ctx context.Context, schema, sql string) ([]map[string]interface{}, error)
	ExecStudentSQL(ctx context.Context, schema, sql string) error
}

type ValidationService struct {
	executor DBExecutor
}

func NewValidationService(executor DBExecutor) *ValidationService {
	return &ValidationService{executor: executor}
}

func (s *ValidationService) Validate(ctx context.Context, studentID string, question models.Question, studentSQL string) (models.ValidationResult, error) {
	schema := fmt.Sprintf("student_%s", studentID)

	// 1. Basic Syntax Check
	if studentSQL == "" {
		return models.ValidationResult{
			Status:    "error",
			Score:     0,
			Feedback:  "SQL text cannot be empty",
			IsCorrect: false,
		}, nil
	}

	if question.Type == "QUERY" {
		return s.validateQuery(ctx, schema, question, studentSQL)
	} else {
		return s.validateDML(ctx, schema, question, studentSQL)
	}
}

func (s *ValidationService) validateQuery(ctx context.Context, schema string, question models.Question, studentSQL string) (models.ValidationResult, error) {
	// 1. Command Ordering Check (Optional)
	if question.StrictMode.Ordering {
		if !s.checkSQLOrdering(question.CorrectSQL, studentSQL) {
			return models.ValidationResult{
				Status:    "success",
				Score:     question.Points * 0.5,
				Feedback:  "Correct result, but SQL command ordering does not match requirements.",
				IsCorrect: false,
			}, nil
		}
	}

	// Get teacher result
	teacherResults, err := s.executor.Query(ctx, schema, question.CorrectSQL)
	if err != nil {
		return models.ValidationResult{}, fmt.Errorf("teacher query failed: %w", err)
	}

	// Get student result
	studentResults, err := s.executor.QueryStudentSQL(ctx, studentSQL)
	if err != nil {
		return models.ValidationResult{
			Status:    "success",
			Score:     0,
			Feedback:  fmt.Sprintf("SQL Error: %v", err),
			IsCorrect: false,
		}, nil
	}

	if s.compareResults(teacherResults, studentResults, question.StrictMode) {
		return models.ValidationResult{
			Status:    "success",
			Score:     question.Points,
			Feedback:  "Correct!",
			IsCorrect: true,
		}, nil
	}

	return models.ValidationResult{
		Status:    "success",
		Score:     0,
		Feedback:  "Result sets do not match.",
		IsCorrect: false,
	}, nil
}

func (s *ValidationService) checkSQLOrdering(teacherSQL, studentSQL string) bool {
	keywords := []string{"SELECT", "FROM", "WHERE", "GROUP BY", "HAVING", "ORDER BY", "LIMIT"}
	extractSequence := func(sql string) []string {
		upper := strings.ToUpper(sql)
		var seq []string
		for _, kw := range keywords {
			if strings.Contains(upper, kw) {
				seq = append(seq, kw)
			}
		}
		return seq
	}
	tSeq, sSeq := extractSequence(teacherSQL), extractSequence(studentSQL)
	if len(tSeq) != len(sSeq) {
		return false
	}
	for i := range tSeq {
		if tSeq[i] != sSeq[i] {
			return false
		}
	}
	return true
}

func (s *ValidationService) validateDML(ctx context.Context, schema string, question models.Question, studentSQL string) (models.ValidationResult, error) {
	// Full implementation of Backup -> Teacher -> Capture -> Restore -> Student -> Compare
	targetTable := question.TargetTable
	backupTable := targetTable + "_backup"

	// 1. Backup the main table
	s.executor.Exec(ctx, schema, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", backupTable))
	if err := s.executor.Exec(ctx, schema, fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", backupTable, targetTable)); err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to create backup structure: %w", err)
	}
	if err := s.executor.Exec(ctx, schema, fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", backupTable, targetTable)); err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to backup data: %w", err)
	}
	defer s.executor.Exec(ctx, schema, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", backupTable))

	// 2. Execute Teacher's SQL
	if err := s.executor.Exec(ctx, schema, question.CorrectSQL); err != nil {
		return models.ValidationResult{}, fmt.Errorf("teacher SQL execution failed: %w", err)
	}

	// 3. Get Teacher's Result State
	teacherResults, err := s.executor.Query(ctx, schema, fmt.Sprintf("SELECT * FROM %s", targetTable))
	if err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to get teacher results: %w", err)
	}

	// 4. Restore for student check
	if err := s.restoreTable(ctx, schema, targetTable, backupTable); err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to restore table for student: %w", err)
	}

	// 5. Execute Student SQL
	if err := s.executor.ExecStudentSQL(ctx, schema, studentSQL); err != nil {
		return models.ValidationResult{
			Status:    "success",
			Score:     0,
			Feedback:  fmt.Sprintf("SQL Error: %v", err),
			IsCorrect: false,
		}, nil
	}

	// 6. Get Student's Result State
	studentResults, err := s.executor.Query(ctx, schema, fmt.Sprintf("SELECT * FROM %s", targetTable))
	if err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to get student results: %w", err)
	}

	// 7. Check if student query was idempotent (compare with backup)
	backupResults, err := s.executor.Query(ctx, schema, fmt.Sprintf("SELECT * FROM %s", backupTable))
	if err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed to get backup results: %w", err)
	}

	if s.compareResults(backupResults, studentResults, question.StrictMode) {
		return models.ValidationResult{
			Status:    "success",
			Score:     0,
			Feedback:  "No changes detected (Idempotent query).",
			IsCorrect: false,
		}, nil
	}

	// 8. Compare
	if s.compareResults(teacherResults, studentResults, question.StrictMode) {
		return models.ValidationResult{
			Status:    "success",
			Score:     question.Points,
			Feedback:  "Correct!",
			IsCorrect: true,
		}, nil
	}

	// 9. Reset (Restore back to original)
	if err := s.restoreTable(ctx, schema, targetTable, backupTable); err != nil {
		return models.ValidationResult{}, fmt.Errorf("failed final restore: %w", err)
	}

	return models.ValidationResult{
		Status:    "success",
		Score:     0,
		Feedback:  "The state changes do not match the expected result.",
		IsCorrect: false,
	}, nil
}

func (s *ValidationService) compareResults(teacher, student []map[string]interface{}, mode models.StrictMode) bool {
	if len(teacher) != len(student) {
		return false
	}

	if mode.RecordOrder {
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
