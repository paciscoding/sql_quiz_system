package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// Evaluate runs the student's SQL against the appropriate sandbox and captures
// the result for comparison. SELECT questions read directly from the frozen
// public.* curriculum under the SELECT-only `exam_student_readonly` role: no
// clone, no lock — reads are inherently parallel-safe and the role
// rejects writes (including data-modifying CTEs) at the engine level. DML
// questions execute against the shared per-exam sandbox `sandbox_exam_{X}`,
// inside a transaction with a savepoint that is always rolled back so the
// sandbox state is restored for the next student in the queue.
// DML evaluations are serialised by the global in-process FIFO SubmissionQueue
// (single worker, capacity 256, 30 s per-job timeout); SELECT remains lock-free
// under `exam_student_readonly`.
func (r *PostgresRepository) Evaluate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
	if question.Type == "QUERY" {
		return r.evaluateQuery(ctx, question, studentSQL, querySchemaFor(question, sandboxSchema))
	}
	return r.evaluateDML(ctx, question, studentSQL, sandboxSchema)
}

// querySchemaFor decides which schema a SELECT (QUERY) question reads from.
//
// Exam questions (ExamID != nil) read from the exam's CurriculumSchema field,
// which GetQuestion populates via a JOIN. The default is 'public' (set by
// COALESCE in GetQuestion), so exams that have never had a per-exam import
// behave identically to before this feature existed.
//
// Practice questions (ExamID == nil) read their category's own schema (the
// sandboxSchema arg from the caller), which is what isolates practice from the
// exam curriculum.
func querySchemaFor(question models.Question, sandboxSchema string) string {
	if question.ExamID != nil {
		// Use the per-exam curriculum schema populated by GetQuestion.
		// Falls back to 'public' if CurriculumSchema is empty (e.g., a Question
		// value that was not loaded through GetQuestion).
		if question.CurriculumSchema != "" {
			return question.CurriculumSchema
		}
		return "public"
	}
	if sandboxSchema == "" {
		return "public"
	}
	return sandboxSchema
}

// acquirePracticeEvalLock takes the practice-reset advisory lock in SHARED mode
// on the given transaction, but only when the evaluation actually runs against
// the shared practice sandbox. Exam evaluations (their own per-exam sandbox, or
// the read-only public curriculum) are never rebuilt by ResetPracticeSandbox,
// so they skip the lock entirely and pay nothing. The lock releases
// automatically when the transaction ends (pg_advisory_xact_lock_shared).
func acquirePracticeEvalLock(ctx context.Context, tx pgx.Tx, schema string) error {
	if schema != practiceSandboxSchema {
		return nil
	}
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1)", practiceResetLockKey)
	return err
}

func (r *PostgresRepository) evaluateQuery(ctx context.Context, question models.Question, studentSQL string, querySchema string) (models.ValidationResult, error) {
	// Repair typographic quotes so pasted SELECTs run instead of failing with a
	// spurious "column ... does not exist". Consistent with the preview path.
	studentSQL = normalizeSmartQuotes(studentSQL)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return models.ValidationResult{}, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return models.ValidationResult{}, err
	}
	// Always rolled back: the readonly role can't write, so commit vs rollback
	// is moot, but rolling back guarantees no session state leaks.
	defer tx.Rollback(ctx)

	if querySchema == "" {
		querySchema = "public"
	}
	// Guard against a concurrent practice-sandbox rebuild dropping the schema
	// out from under this read. Shared advisory lock: many evals run in
	// parallel; ResetPracticeSandbox's exclusive lock waits for us to finish.
	if err := acquirePracticeEvalLock(ctx, tx, querySchema); err != nil {
		return models.ValidationResult{}, err
	}
	safeQuerySchema := pgx.Identifier{querySchema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safeQuerySchema)); err != nil {
		return models.ValidationResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student_readonly"); err != nil {
		return models.ValidationResult{}, err
	}

	var studentResults []map[string]interface{}
	var studentCols []string
	var studentErr error
	rows, queryErr := tx.Query(ctx, studentSQL)
	if queryErr != nil {
		studentErr = queryErr
	} else {
		studentResults, studentCols, studentErr = collectRowsWithColumns(rows)
	}

	capturedJSON, _ := json.Marshal(studentResults)
	result := models.ValidationResult{
		Status:            "success",
		ValidationDetails: capturedJSON,
	}
	if studentErr != nil {
		result.Status = "error"
		result.Feedback = fmt.Sprintf("SQL Error: %v", studentErr)
	} else {
		result.Columns = studentCols
	}
	return result, nil
}

func (r *PostgresRepository) evaluateDML(ctx context.Context, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
	// Repair typographic quotes so pasted INSERT/UPDATE/DELETE run instead of
	// failing with a spurious "column ... does not exist". Consistent with the
	// preview path.
	studentSQL = normalizeSmartQuotes(studentSQL)
	if question.TargetTable == "" {
		return models.ValidationResult{}, fmt.Errorf("DML question is missing target_table")
	}

	schema := sandboxSchema
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return models.ValidationResult{}, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return models.ValidationResult{}, err
	}
	defer tx.Rollback(ctx)

	// Guard against a concurrent practice-sandbox rebuild dropping the schema
	// mid-evaluation. No-op for exam sandboxes (sandbox_exam_*), which reset
	// never touches. Shared: parallel evals fine; reset waits for us.
	if err := acquirePracticeEvalLock(ctx, tx, schema); err != nil {
		return models.ValidationResult{}, err
	}

	if _, err := tx.Exec(ctx, "SAVEPOINT student_eval"); err != nil {
		return models.ValidationResult{}, err
	}

	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
		return models.ValidationResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student"); err != nil {
		return models.ValidationResult{}, err
	}

	var studentResults []map[string]interface{}
	var studentCols []string
	var studentErr error

	if _, execErr := tx.Exec(ctx, studentSQL); execErr != nil {
		studentErr = execErr
	} else {
		safeTable := pgx.Identifier{question.TargetTable}.Sanitize()
		rows, queryErr := tx.Query(ctx, fmt.Sprintf("SELECT * FROM %s", safeTable))
		if queryErr != nil {
			studentErr = queryErr
		} else {
			studentResults, studentCols, studentErr = collectRowsWithColumns(rows)
		}
	}

	// Restore sandbox state for the next student in the per-target queue.
	_, _ = tx.Exec(ctx, "RESET ROLE")
	_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")

	capturedJSON, _ := json.Marshal(studentResults)
	result := models.ValidationResult{
		Status:            "success",
		ValidationDetails: capturedJSON,
	}
	if studentErr != nil {
		result.Status = "error"
		result.Feedback = fmt.Sprintf("SQL Error: %v", studentErr)
	} else {
		result.Columns = studentCols
	}
	return result, tx.Commit(ctx)
}

const previewRowCap = 200

// EvaluatePreview runs the student SQL in a transaction that is always rolled
// back, returning up to previewRowCap rows plus metadata. The Result field uses
// the internal "success"/"error" status; callers (ValidationService) map that to
// the three-state "correct"/"incorrect"/"syntax_error" vocabulary.
func (r *PostgresRepository) EvaluatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (PreviewResult, error) {
	if question.Type == "QUERY" {
		return r.evaluateQueryPreview(ctx, question, studentSQL, querySchemaFor(question, sandboxSchema))
	}
	return r.evaluateDMLPreview(ctx, question, studentSQL, sandboxSchema)
}

func (r *PostgresRepository) evaluateQueryPreview(ctx context.Context, question models.Question, studentSQL string, querySchema string) (PreviewResult, error) {
	studentSQL = normalizeSmartQuotes(studentSQL)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return PreviewResult{}, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return PreviewResult{}, err
	}
	defer tx.Rollback(ctx)

	if querySchema == "" {
		querySchema = "public"
	}
	safeQuerySchema := pgx.Identifier{querySchema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safeQuerySchema)); err != nil {
		return PreviewResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student_readonly"); err != nil {
		return PreviewResult{}, err
	}

	var pr PreviewResult
	rows, queryErr := tx.Query(ctx, studentSQL)
	if queryErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", queryErr)
		return pr, nil
	}

	data, cols, collectErr := collectRowsWithColumns(rows)
	if collectErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", collectErr)
		return pr, nil
	}

	pr.Columns = cols
	if len(data) > previewRowCap {
		pr.Truncated = true
		pr.RowCount = len(data)
		pr.Rows = data[:previewRowCap]
	} else {
		pr.Rows = data
		pr.RowCount = len(data)
	}
	pr.Result.Status = "success"
	return pr, nil
}

func (r *PostgresRepository) evaluateDMLPreview(ctx context.Context, question models.Question, studentSQL string, sandboxSchema string) (PreviewResult, error) {
	if question.TargetTable == "" {
		return PreviewResult{}, fmt.Errorf("DML question is missing target_table")
	}

	studentSQL = normalizeSmartQuotes(studentSQL)
	schema := sandboxSchema
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return PreviewResult{}, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return PreviewResult{}, err
	}
	defer tx.Rollback(ctx)

	var pr PreviewResult

	if _, err := tx.Exec(ctx, "SAVEPOINT student_eval"); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student"); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public", safeSchema)); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	if _, execErr := tx.Exec(ctx, studentSQL); execErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", execErr)
		_, _ = tx.Exec(ctx, "RESET ROLE")
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
		_ = tx.Rollback(ctx)
		return pr, nil
	}

	safeTable := pgx.Identifier{question.TargetTable}.Sanitize()
	rows, queryErr := tx.Query(ctx, fmt.Sprintf("SELECT * FROM %s", safeTable))
	if queryErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", queryErr)
		_, _ = tx.Exec(ctx, "RESET ROLE")
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
		_ = tx.Rollback(ctx)
		return pr, nil
	}

	data, cols, collectErr := collectRowsWithColumns(rows)
	if collectErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", collectErr)
		_, _ = tx.Exec(ctx, "RESET ROLE")
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
		_ = tx.Rollback(ctx)
		return pr, nil
	}

	pr.Columns = cols
	if len(data) > previewRowCap {
		pr.Truncated = true
		pr.RowCount = len(data)
		pr.Rows = data[:previewRowCap]
	} else {
		pr.Rows = data
		pr.RowCount = len(data)
	}
	pr.Result.Status = "success"

	_, _ = tx.Exec(ctx, "RESET ROLE")
	_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
	_ = tx.Rollback(ctx)
	return pr, nil
}
