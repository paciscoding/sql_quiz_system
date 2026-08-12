package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// clampSubmissionLogPaging enforces limit in [1,200] and offset >= 0, so a
// caller that bypasses the controller (or a controller bug) can never make
// the repository run an unbounded or negative-offset query.
func clampSubmissionLogPaging(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// examLogPartitionWhere builds the WHERE clause applied INSIDE the ranked CTE
// (i.e. before the window functions run). Only filters that cannot change
// which rows share a (student_id, question_id) partition are safe here:
// exam_id, student_id and question_id all narrow whole partitions (a
// question belongs to exactly one exam, and student_id/question_id are
// themselves the partition key), so restricting on them up front cannot
// remove a sibling attempt from its own partition and therefore cannot
// renumber attempt_no or change which attempt is_counted picks.
func examLogPartitionWhere(f models.SubmissionLogFilter, args *[]interface{}) string {
	clauses := []string{"1=1"}
	if f.ExamID != nil {
		*args = append(*args, *f.ExamID)
		clauses = append(clauses, fmt.Sprintf("s.exam_id = $%d", len(*args)))
	}
	if f.StudentID != nil {
		*args = append(*args, *f.StudentID)
		clauses = append(clauses, fmt.Sprintf("s.student_id = $%d", len(*args)))
	}
	if f.QuestionID != nil {
		*args = append(*args, *f.QuestionID)
		clauses = append(clauses, fmt.Sprintf("s.question_id = $%d", len(*args)))
	}
	return strings.Join(clauses, " AND ")
}

// examLogOuterWhere builds the WHERE clause applied AFTER the ranked CTE has
// already computed attempt_no/is_counted. outcome, counted_only and the
// from/to date range are NOT part of the (student_id, question_id) partition
// key, so filtering on them before the window functions run would drop
// sibling attempts out of their own partition and renumber/re-pick the
// winner. They must only ever narrow the already-numbered result.
func examLogOuterWhere(f models.SubmissionLogFilter, args *[]interface{}) string {
	clauses := []string{"1=1"}
	switch f.Outcome {
	case "correct":
		clauses = append(clauses, "r.is_correct = true")
	case "incorrect":
		clauses = append(clauses, "r.is_correct = false")
	}
	if f.CountedOnly {
		clauses = append(clauses, "r.is_counted")
	}
	if f.From != nil {
		*args = append(*args, *f.From)
		clauses = append(clauses, fmt.Sprintf("r.submitted_at >= $%d", len(*args)))
	}
	if f.To != nil {
		*args = append(*args, *f.To)
		clauses = append(clauses, fmt.Sprintf("r.submitted_at <= $%d", len(*args)))
	}
	return strings.Join(clauses, " AND ")
}

// practiceLogPartitionWhere mirrors examLogPartitionWhere for
// app.practice_attempts. student_id and practice_question_id are the
// partition key (PARTITION BY student_id, practice_question_id below), so
// filtering on them up front is safe for the same reason.
func practiceLogPartitionWhere(f models.SubmissionLogFilter, args *[]interface{}) string {
	clauses := []string{"1=1"}
	if f.StudentID != nil {
		*args = append(*args, *f.StudentID)
		clauses = append(clauses, fmt.Sprintf("a.student_id = $%d", len(*args)))
	}
	if f.QuestionID != nil {
		*args = append(*args, *f.QuestionID)
		clauses = append(clauses, fmt.Sprintf("a.practice_question_id = $%d", len(*args)))
	}
	return strings.Join(clauses, " AND ")
}

// practiceLogOuterWhere mirrors examLogOuterWhere for practice attempts.
func practiceLogOuterWhere(f models.SubmissionLogFilter, args *[]interface{}) string {
	clauses := []string{"1=1"}
	switch f.Outcome {
	case "correct":
		clauses = append(clauses, "r.is_correct = true")
	case "incorrect":
		clauses = append(clauses, "r.is_correct = false")
	}
	if f.CountedOnly {
		clauses = append(clauses, "r.is_counted")
	}
	if f.From != nil {
		*args = append(*args, *f.From)
		clauses = append(clauses, fmt.Sprintf("r.submitted_at >= $%d", len(*args)))
	}
	if f.To != nil {
		*args = append(*args, *f.To)
		clauses = append(clauses, fmt.Sprintf("r.submitted_at <= $%d", len(*args)))
	}
	return strings.Join(clauses, " AND ")
}

// examLogSelectSQL returns the ranked-CTE + join SQL shared by the list,
// stream and count paths. selectList is the outer SELECT's column list (the
// caller supplies "COUNT(*)" for counting). limitOffset, when non-empty, is
// appended verbatim (e.g. "LIMIT $5 OFFSET $6") after the ORDER BY.
//
// attempt_no / is_counted: see the doc comment on models.ExamSubmissionLogRow
// — is_counted's ordering must stay identical to the DISTINCT ON ordering in
// postgres_results.go's GetStudentExamResults/GetStudentResultsSummary.
func examLogSelectSQL(selectList, partitionWhere, outerWhere, orderAndLimit string) string {
	return fmt.Sprintf(`
WITH ranked AS (
    SELECT
        s.id, s.exam_id, s.student_id, s.question_id, s.sql_text, s.is_correct, s.score, s.submitted_at,
        -- Selected inside the CTE because the outer query only has ranked (as
        -- r) in scope: referencing s.validation_details out there is a
        -- "missing FROM-clause entry for table s" error, which broke every
        -- exam CSV export (the only caller that asks for the detail columns).
        s.validation_details,
        ROW_NUMBER() OVER (PARTITION BY s.student_id, s.question_id ORDER BY s.submitted_at ASC) AS attempt_no,
        (ROW_NUMBER() OVER (PARTITION BY s.student_id, s.question_id ORDER BY s.score DESC, s.submitted_at DESC) = 1) AS is_counted
    FROM app.submissions s
    WHERE %s
),
question_display AS (
    -- Same ordering expression as GetQuestionsByExamID's display_no
    -- (postgres_questions.go) so the numbers shown here match what the
    -- lecturer sees on the exam's question list. PARTITION BY exam_id makes
    -- it equivalent to that query's per-exam ROW_NUMBER() (which runs under
    -- a WHERE exam_id = $1), computed once for every exam instead of one at
    -- a time.
    SELECT q.id,
           ROW_NUMBER() OVER (PARTITION BY q.exam_id ORDER BY (q.category_id IS NULL) ASC, ec.name ASC, q.id ASC) AS display_no
    FROM app.questions q
    LEFT JOIN app.exam_categories ec ON ec.id = q.category_id
)
SELECT %s
FROM ranked r
JOIN app.examinations e ON e.id = r.exam_id
JOIN app.system_users u ON u.id = r.student_id
JOIN app.questions q ON q.id = r.question_id
LEFT JOIN question_display qd ON qd.id = r.question_id
WHERE %s
%s`, partitionWhere, selectList, outerWhere, orderAndLimit)
}

const examLogRowColumns = `r.id, r.exam_id, e.title, r.student_id, u.username, COALESCE(u.student_number, ''),
        r.question_id, COALESCE(qd.display_no, 0), q.requirement, q.question_type, q.points,
        r.attempt_no, r.is_counted, r.is_correct, r.score, r.submitted_at,
        left(regexp_replace(r.sql_text, '[\r\n\t]+', ' ', 'g'), 200)`

const examLogDetailColumns = `r.id, r.exam_id, e.title, r.student_id, u.username, COALESCE(u.student_number, ''),
        r.question_id, COALESCE(qd.display_no, 0), q.requirement, q.question_type, q.points,
        r.attempt_no, r.is_counted, r.is_correct, r.score, r.submitted_at,
        left(regexp_replace(r.sql_text, '[\r\n\t]+', ' ', 'g'), 200),
        r.sql_text, r.validation_details`

func scanExamLogRow(rows interface{ Scan(dest ...interface{}) error }, row *models.ExamSubmissionLogRow) error {
	return rows.Scan(&row.ID, &row.ExamID, &row.ExamTitle, &row.StudentID, &row.Username, &row.StudentNumber,
		&row.QuestionID, &row.QuestionDisplayNo, &row.Requirement, &row.QuestionType, &row.Points,
		&row.AttemptNo, &row.IsCounted, &row.IsCorrect, &row.Score, &row.SubmittedAt, &row.SQLPreview)
}

// ListExamSubmissionLog returns exam submissions matching f, most recent
// first, plus the total count of rows matching the same filter (ignoring
// limit/offset) for pagination.
func (r *PostgresRepository) ListExamSubmissionLog(ctx context.Context, f models.SubmissionLogFilter) ([]models.ExamSubmissionLogRow, int, error) {
	limit, offset := clampSubmissionLogPaging(f.Limit, f.Offset)

	// Count first, with its own arg numbering.
	var countArgs []interface{}
	countPartitionWhere := examLogPartitionWhere(f, &countArgs)
	countOuterWhere := examLogOuterWhere(f, &countArgs)
	countSQL := examLogSelectSQL("COUNT(*)", countPartitionWhere, countOuterWhere, "")

	var total int
	if err := r.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	var args []interface{}
	partitionWhere := examLogPartitionWhere(f, &args)
	outerWhere := examLogOuterWhere(f, &args)
	args = append(args, limit, offset)
	orderAndLimit := fmt.Sprintf("ORDER BY r.submitted_at DESC, r.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	listSQL := examLogSelectSQL(examLogRowColumns, partitionWhere, outerWhere, orderAndLimit)

	rows, err := r.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []models.ExamSubmissionLogRow{}
	for rows.Next() {
		var row models.ExamSubmissionLogRow
		if err := scanExamLogRow(rows, &row); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// GetExamSubmissionDetail returns the full detail (including sql_text and
// parsed validation_details) for one exam submission. attempt_no/is_counted
// are computed over the submission's own (student_id, question_id) partition
// only — scoping the ranked CTE to that single pair up front is safe by the
// same reasoning as examLogPartitionWhere (student_id/question_id are the
// partition key), so it cannot change the numbering.
func (r *PostgresRepository) GetExamSubmissionDetail(ctx context.Context, submissionID int) (models.ExamSubmissionLogDetail, error) {
	const query = `
WITH target AS (
    SELECT student_id, question_id FROM app.submissions WHERE id = $1
),
ranked AS (
    SELECT
        s.id, s.exam_id, s.student_id, s.question_id, s.sql_text, s.is_correct, s.score, s.submitted_at,
        s.validation_details,
        ROW_NUMBER() OVER (PARTITION BY s.student_id, s.question_id ORDER BY s.submitted_at ASC) AS attempt_no,
        (ROW_NUMBER() OVER (PARTITION BY s.student_id, s.question_id ORDER BY s.score DESC, s.submitted_at DESC) = 1) AS is_counted
    FROM app.submissions s
    JOIN target t ON t.student_id = s.student_id AND t.question_id = s.question_id
),
question_display AS (
    SELECT q.id,
           ROW_NUMBER() OVER (PARTITION BY q.exam_id ORDER BY (q.category_id IS NULL) ASC, ec.name ASC, q.id ASC) AS display_no
    FROM app.questions q
    LEFT JOIN app.exam_categories ec ON ec.id = q.category_id
)
SELECT r.id, r.exam_id, e.title, r.student_id, u.username, COALESCE(u.student_number, ''),
       r.question_id, COALESCE(qd.display_no, 0), q.requirement, q.question_type, q.points,
       r.attempt_no, r.is_counted, r.is_correct, r.score, r.submitted_at,
       left(regexp_replace(r.sql_text, '[\r\n\t]+', ' ', 'g'), 200),
       r.sql_text, r.validation_details
FROM ranked r
JOIN app.examinations e ON e.id = r.exam_id
JOIN app.system_users u ON u.id = r.student_id
JOIN app.questions q ON q.id = r.question_id
LEFT JOIN question_display qd ON qd.id = r.question_id
WHERE r.id = $1`

	var d models.ExamSubmissionLogDetail
	err := r.pool.QueryRow(ctx, query, submissionID).Scan(
		&d.ID, &d.ExamID, &d.ExamTitle, &d.StudentID, &d.Username, &d.StudentNumber,
		&d.QuestionID, &d.QuestionDisplayNo, &d.Requirement, &d.QuestionType, &d.Points,
		&d.AttemptNo, &d.IsCounted, &d.IsCorrect, &d.Score, &d.SubmittedAt, &d.SQLPreview,
		&d.SQLText, &d.ValidationDetails)
	return d, err
}

// StreamExamSubmissionLog iterates every exam submission matching f (no
// limit/offset — the CSV export streams the whole matching set) and hands
// each fully-detailed row to fn without ever holding the full result set in
// memory. Iteration stops immediately if fn returns an error.
func (r *PostgresRepository) StreamExamSubmissionLog(ctx context.Context, f models.SubmissionLogFilter, fn func(models.ExamSubmissionLogDetail) error) error {
	var args []interface{}
	partitionWhere := examLogPartitionWhere(f, &args)
	outerWhere := examLogOuterWhere(f, &args)
	sql := examLogSelectSQL(examLogDetailColumns, partitionWhere, outerWhere, "ORDER BY r.submitted_at DESC, r.id DESC")

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var d models.ExamSubmissionLogDetail
		if err := rows.Scan(&d.ID, &d.ExamID, &d.ExamTitle, &d.StudentID, &d.Username, &d.StudentNumber,
			&d.QuestionID, &d.QuestionDisplayNo, &d.Requirement, &d.QuestionType, &d.Points,
			&d.AttemptNo, &d.IsCounted, &d.IsCorrect, &d.Score, &d.SubmittedAt, &d.SQLPreview,
			&d.SQLText, &d.ValidationDetails); err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return rows.Err()
}

// practiceLogSelectSQL is the practice-attempts equivalent of
// examLogSelectSQL. See models.PracticeSubmissionLogRow's doc comment for why
// is_counted here orders by is_correct DESC instead of a score column.
func practiceLogSelectSQL(selectList, partitionWhere, outerWhere, orderAndLimit string) string {
	return fmt.Sprintf(`
WITH ranked AS (
    SELECT
        a.id, a.student_id, a.practice_question_id, a.sql_text, a.is_correct, a.submitted_at, a.validation_details,
        ROW_NUMBER() OVER (PARTITION BY a.student_id, a.practice_question_id ORDER BY a.submitted_at ASC) AS attempt_no,
        (ROW_NUMBER() OVER (PARTITION BY a.student_id, a.practice_question_id ORDER BY a.is_correct DESC, a.submitted_at DESC) = 1) AS is_counted
    FROM app.practice_attempts a
    WHERE %s
)
SELECT %s
FROM ranked r
JOIN app.system_users u ON u.id = r.student_id
JOIN app.practice_questions pq ON pq.id = r.practice_question_id
LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
WHERE %s
%s`, partitionWhere, selectList, outerWhere, orderAndLimit)
}

const practiceLogRowColumns = `r.id, r.student_id, u.username, COALESCE(u.student_number, ''),
        r.practice_question_id, COALESCE(pc.name, ''), pq.requirement,
        r.attempt_no, r.is_counted, r.is_correct, r.submitted_at,
        left(regexp_replace(r.sql_text, '[\r\n\t]+', ' ', 'g'), 200)`

const practiceLogDetailColumns = `r.id, r.student_id, u.username, COALESCE(u.student_number, ''),
        r.practice_question_id, COALESCE(pc.name, ''), pq.requirement,
        r.attempt_no, r.is_counted, r.is_correct, r.submitted_at,
        left(regexp_replace(r.sql_text, '[\r\n\t]+', ' ', 'g'), 200),
        r.sql_text, r.validation_details`

// ListPracticeSubmissionLog returns practice attempts matching f, most recent
// first, plus the total count matching the same filter.
func (r *PostgresRepository) ListPracticeSubmissionLog(ctx context.Context, f models.SubmissionLogFilter) ([]models.PracticeSubmissionLogRow, int, error) {
	limit, offset := clampSubmissionLogPaging(f.Limit, f.Offset)

	var countArgs []interface{}
	countPartitionWhere := practiceLogPartitionWhere(f, &countArgs)
	countOuterWhere := practiceLogOuterWhere(f, &countArgs)
	countSQL := practiceLogSelectSQL("COUNT(*)", countPartitionWhere, countOuterWhere, "")

	var total int
	if err := r.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	var args []interface{}
	partitionWhere := practiceLogPartitionWhere(f, &args)
	outerWhere := practiceLogOuterWhere(f, &args)
	args = append(args, limit, offset)
	orderAndLimit := fmt.Sprintf("ORDER BY r.submitted_at DESC, r.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	listSQL := practiceLogSelectSQL(practiceLogRowColumns, partitionWhere, outerWhere, orderAndLimit)

	rows, err := r.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []models.PracticeSubmissionLogRow{}
	for rows.Next() {
		var row models.PracticeSubmissionLogRow
		if err := rows.Scan(&row.ID, &row.StudentID, &row.Username, &row.StudentNumber,
			&row.PracticeQuestionID, &row.CategoryName, &row.Requirement,
			&row.AttemptNo, &row.IsCounted, &row.IsCorrect, &row.SubmittedAt, &row.SQLPreview); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// GetPracticeAttemptDetail returns the full detail for one practice attempt.
// Scoped to the attempt's own (student_id, practice_question_id) partition up
// front, same reasoning as GetExamSubmissionDetail.
func (r *PostgresRepository) GetPracticeAttemptDetail(ctx context.Context, attemptID int) (models.PracticeAttemptLogDetail, error) {
	const query = `
WITH target AS (
    SELECT student_id, practice_question_id FROM app.practice_attempts WHERE id = $1
),
ranked AS (
    SELECT
        a.id, a.student_id, a.practice_question_id, a.sql_text, a.is_correct, a.submitted_at, a.validation_details,
        ROW_NUMBER() OVER (PARTITION BY a.student_id, a.practice_question_id ORDER BY a.submitted_at ASC) AS attempt_no,
        (ROW_NUMBER() OVER (PARTITION BY a.student_id, a.practice_question_id ORDER BY a.is_correct DESC, a.submitted_at DESC) = 1) AS is_counted
    FROM app.practice_attempts a
    JOIN target t ON t.student_id = a.student_id AND t.practice_question_id = a.practice_question_id
)
SELECT r.id, r.student_id, u.username, COALESCE(u.student_number, ''),
       r.practice_question_id, COALESCE(pc.name, ''), pq.requirement,
       r.attempt_no, r.is_counted, r.is_correct, r.submitted_at,
       left(regexp_replace(r.sql_text, '[\r\n\t]+', ' ', 'g'), 200),
       r.sql_text, r.validation_details
FROM ranked r
JOIN app.system_users u ON u.id = r.student_id
JOIN app.practice_questions pq ON pq.id = r.practice_question_id
LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
WHERE r.id = $1`

	var d models.PracticeAttemptLogDetail
	err := r.pool.QueryRow(ctx, query, attemptID).Scan(
		&d.ID, &d.StudentID, &d.Username, &d.StudentNumber,
		&d.PracticeQuestionID, &d.CategoryName, &d.Requirement,
		&d.AttemptNo, &d.IsCounted, &d.IsCorrect, &d.SubmittedAt, &d.SQLPreview,
		&d.SQLText, &d.ValidationDetails)
	return d, err
}

// StreamPracticeSubmissionLog is the practice equivalent of
// StreamExamSubmissionLog: iterates every matching row and hands each fully-
// detailed row to fn without accumulating a slice.
func (r *PostgresRepository) StreamPracticeSubmissionLog(ctx context.Context, f models.SubmissionLogFilter, fn func(models.PracticeAttemptLogDetail) error) error {
	var args []interface{}
	partitionWhere := practiceLogPartitionWhere(f, &args)
	outerWhere := practiceLogOuterWhere(f, &args)
	sql := practiceLogSelectSQL(practiceLogDetailColumns, partitionWhere, outerWhere, "ORDER BY r.submitted_at DESC, r.id DESC")

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var d models.PracticeAttemptLogDetail
		if err := rows.Scan(&d.ID, &d.StudentID, &d.Username, &d.StudentNumber,
			&d.PracticeQuestionID, &d.CategoryName, &d.Requirement,
			&d.AttemptNo, &d.IsCounted, &d.IsCorrect, &d.SubmittedAt, &d.SQLPreview,
			&d.SQLText, &d.ValidationDetails); err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return rows.Err()
}
