package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// CreateQuestion captures the correct-result set and persists the question,
// returning its id. A question with ID==0 is created with a fresh auto-assigned
// id; a question with ID>0 upserts (updates) that existing row.
func (r *PostgresRepository) CreateQuestion(ctx context.Context, q models.Question) (int, error) {
	// Repair typographic quotes pasted from documents (e.g. ‘…’ → '…') BEFORE the
	// Phase-0 capture runs and before the row is persisted, so the teacher's
	// correct SQL captures instead of failing with "column ... does not exist"
	// (SQLSTATE 42703). Mirrors the preview and student-evaluation paths.
	q.CorrectSQL = normalizeSmartQuotes(q.CorrectSQL)

	// Pre-flight: Check if exam is active
	if q.ExamID != nil {
		exam, err := r.GetExamByID(ctx, int(*q.ExamID))
		if err == nil && exam.IsActive {
			return 0, fmt.Errorf("%w: cannot modify questions of an active examination", ErrExamActive)
		}
	}

	// Phase 0: Correct Result Capture
	status := "captured"
	var captureErr string
	var expectedResult, expectedCols []byte
	var expectedRowsAffected *int

	// Resolve the exam's curriculum schema so capture runs against the right
	// database. Defaults to 'public' for practice questions (ExamID == nil) and
	// for exam questions that haven't had a per-exam import yet. This preserves
	// the historic capture behaviour byte-for-byte when curriculum_schema='public'.
	captureSchema := "public"
	if q.ExamID != nil {
		var cs string
		if err := r.pool.QueryRow(ctx,
			`SELECT COALESCE(NULLIF(curriculum_schema, ''), 'public')
			 FROM app.examinations WHERE id = $1`, *q.ExamID).Scan(&cs); err == nil && cs != "" {
			captureSchema = cs
		}
	}

	// D1: resolve target_table to its canonical pg_class.relname against the
	// schema this question will actually execute in, before capture runs. On
	// failure, skip capture entirely (status/captureErr already reflect it) but
	// still persist the row below with the raw trimmed target_table.
	var resolveErr error
	if q.Type != "QUERY" {
		var canonical string
		canonical, resolveErr = r.ResolveTargetTable(ctx, captureSchema, q.TargetTable)
		if resolveErr != nil {
			status = "failed"
			captureErr = resolveErr.Error()
			q.TargetTable = strings.TrimSpace(q.TargetTable)
		} else {
			q.TargetTable = canonical
		}
	}

	var err error
	if resolveErr == nil {
		err = r.WithTransaction(ctx, func(tx pgx.Tx) error {
			// Bug fix (ARCH-002d): set search_path BEFORE executing the lecturer's
			// DML so an exam with a per-exam curriculum_schema mutates the right
			// schema instead of always hitting public.
			safeCaptureSchema := pgx.Identifier{captureSchema}.Sanitize()
			if _, spErr := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeCaptureSchema)); spErr != nil {
				return spErr
			}

			var results []map[string]interface{}
			var orderedCols []string
			var err error

			if q.Type == "QUERY" {
				results, orderedCols, err = r.queryWithTxCols(ctx, tx, captureSchema, q.CorrectSQL)
			} else {
				if q.TargetTable == "" {
					return fmt.Errorf("target table is required for DML questions")
				}
				var tag pgconn.CommandTag
				tag, err = tx.Exec(ctx, q.CorrectSQL)
				if err == nil {
					// D3 (ARCH-008): a model answer that affects 0 rows is unGRADEABLE —
					// every student statement, correct or not, produces the same
					// untouched end state. Hard block, no override.
					if tag.RowsAffected() == 0 {
						status = "failed"
						captureErr = "The model answer affected 0 rows, so this question cannot tell a correct answer apart from a statement that does nothing. Fix the WHERE clause of the correct SQL, or fix the seed data in this category's database."
						return errCaptureFailed
					}
					rowsAffected := int(tag.RowsAffected())
					expectedRowsAffected = &rowsAffected
					safeTable := pgx.Identifier{q.TargetTable}.Sanitize()
					results, orderedCols, err = r.queryWithTxCols(ctx, tx, captureSchema, fmt.Sprintf("SELECT * FROM %s", safeTable))
				}
			}

			if err != nil {
				status = "failed"
				captureErr = err.Error()
				// Return a sentinel so WithTransaction rolls back the (already-aborted)
				// tx instead of attempting a Commit that would surface as
				// "commit unexpectedly resulted in rollback".
				return errCaptureFailed
			}

			if len(results) > 10000 {
				status = "failed"
				captureErr = "result-set too large (max 10,000 rows)"
				return errCaptureFailed
			}

			expectedResult, _ = json.Marshal(results)
			if len(orderedCols) > 0 {
				expectedCols, _ = json.Marshal(orderedCols)
			}

			return errCaptureRollback
		})
	}

	if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
		return 0, err
	}

	// Persist. When no client id is supplied (create), omit the id column so the
	// sequence default assigns a fresh one — this closes the historic id=0
	// collision. When an id IS supplied (update), upsert that specific row.
	// RETURNING id surfaces the real id in both cases.
	var newID int
	if q.ID == 0 {
		const insertQ = `
		INSERT INTO app.questions (
			exam_id, target_table, correct_sql, question_type, points, requirement,
			strict_ordering, strict_attributes, strict_column_order,
			expected_result, expected_columns, expected_result_status,
			expected_result_captured_at, capture_error, expected_rows_affected, created_by, dual_schema, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), $13, $14, $15, $16, NOW())
		RETURNING id`
		err = r.pool.QueryRow(ctx, insertQ,
			q.ExamID, q.TargetTable, q.CorrectSQL, q.Type, q.Points, q.Requirement,
			q.StrictMode.Ordering, q.StrictMode.Attributes, q.StrictMode.ColumnOrder,
			expectedResult, expectedCols, status, captureErr, expectedRowsAffected, q.CreatedBy, q.DualSchema).Scan(&newID)
	} else {
		const upsertQ = `
		INSERT INTO app.questions (
			id, exam_id, target_table, correct_sql, question_type, points, requirement,
			strict_ordering, strict_attributes, strict_column_order,
			expected_result, expected_columns, expected_result_status,
			expected_result_captured_at, capture_error, expected_rows_affected, created_by, dual_schema, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), $14, $15, $16, $17, NOW())
		ON CONFLICT (id) DO UPDATE SET
			exam_id = EXCLUDED.exam_id,
			target_table = EXCLUDED.target_table,
			correct_sql = EXCLUDED.correct_sql,
			question_type = EXCLUDED.question_type,
			points = EXCLUDED.points,
			requirement = EXCLUDED.requirement,
			strict_ordering = EXCLUDED.strict_ordering,
			strict_attributes = EXCLUDED.strict_attributes,
			strict_column_order = EXCLUDED.strict_column_order,
			expected_result = EXCLUDED.expected_result,
			expected_columns = EXCLUDED.expected_columns,
			expected_result_status = EXCLUDED.expected_result_status,
			expected_result_captured_at = NOW(),
			capture_error = EXCLUDED.capture_error,
			expected_rows_affected = EXCLUDED.expected_rows_affected,
			dual_schema = EXCLUDED.dual_schema,
			updated_at = NOW()
		RETURNING id`
		err = r.pool.QueryRow(ctx, upsertQ,
			q.ID, q.ExamID, q.TargetTable, q.CorrectSQL, q.Type, q.Points, q.Requirement,
			q.StrictMode.Ordering, q.StrictMode.Attributes, q.StrictMode.ColumnOrder,
			expectedResult, expectedCols, status, captureErr, expectedRowsAffected, q.CreatedBy, q.DualSchema).Scan(&newID)
	}

	return newID, err
}

// PreviewCorrectSQL is the backward-compatible wrapper that always previews
// against the 'public' schema. Existing callers (e.g. practice_controller) use
// this signature; new callers that want per-exam curriculum resolution should
// call PreviewCorrectSQLInSchema instead.
func (r *PostgresRepository) PreviewCorrectSQL(ctx context.Context, qType, targetTable, sqlText string) ([]map[string]interface{}, error) {
	return r.PreviewCorrectSQLInSchema(ctx, qType, targetTable, sqlText, 0, false)
}

// PreviewCorrectSQLInSchema dry-runs the lecturer's correct SQL inside a
// transaction that is always rolled back, so the lecturer can see the result
// before they commit to saving the question. For DML the post-image of
// target_table is returned. The schema state is never persistently mutated.
//
// examID: optional exam id (0 or negative = absent). When present and > 0, the
// exam's curriculum_schema is resolved and the preview runs against that schema
// instead of 'public'. An absent exam_id (legacy / global schema) keeps the
// existing 'public' behaviour byte-for-byte.
//
// useDecoy: when true and examID > 0, previews against the exam's decoy
// preview_curriculum_schema (dual-schema grading, ARCH-005) instead of the
// real curriculum_schema — the lecturer's "Preview as student" view. Errors
// immediately if no decoy has been imported for the exam, rather than falling
// back to 'public' or the real curriculum (which would leak grading schema B
// under a "student" preview). When useDecoy is false, behaviour is
// byte-for-byte identical to before this parameter existed.
func (r *PostgresRepository) PreviewCorrectSQLInSchema(ctx context.Context, qType, targetTable, sqlText string, examID int, useDecoy bool) ([]map[string]interface{}, error) {
	if sqlText == "" {
		return nil, fmt.Errorf("sql text is empty")
	}
	if qType == "DML" && targetTable == "" {
		return nil, fmt.Errorf("target_table is required for DML preview")
	}

	// Repair typographic quotes pasted from documents so the SQL executes
	// instead of failing with a spurious `column "‘...’" does not exist`.
	sqlText = normalizeSmartQuotes(sqlText)

	// Resolve the schema to preview against. Defaults to 'public' when examID
	// is absent (0) or when the exam hasn't had a per-exam import yet.
	var previewSchema string
	if useDecoy && examID > 0 {
		decoy, err := r.GetExamPreviewCurriculumSchema(ctx, examID)
		if err != nil {
			return nil, err
		}
		if decoy == "" {
			return nil, fmt.Errorf("no decoy schema uploaded for this exam")
		}
		previewSchema = decoy
	} else {
		previewSchema = "public"
		if examID > 0 {
			var cs string
			if err := r.pool.QueryRow(ctx,
				`SELECT COALESCE(NULLIF(curriculum_schema, ''), 'public')
				 FROM app.examinations WHERE id = $1`, examID).Scan(&cs); err == nil && cs != "" {
				previewSchema = cs
			}
		}
	}

	// D1: resolve target_table to its canonical name before previewing, so
	// preview and save always agree on success/failure (ARCH-003).
	if qType == "DML" {
		canonical, err := r.ResolveTargetTable(ctx, previewSchema, targetTable)
		if err != nil {
			return nil, err
		}
		targetTable = canonical
	}

	var results []map[string]interface{}
	previewErr := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		safe := pgx.Identifier{previewSchema}.Sanitize()
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
			return err
		}
		var err error
		if qType == "QUERY" {
			results, err = r.queryWithTx(ctx, tx, previewSchema, sqlText)
		} else {
			if _, err = tx.Exec(ctx, sqlText); err != nil {
				return err
			}
			safeTable := pgx.Identifier{targetTable}.Sanitize()
			results, err = r.queryWithTx(ctx, tx, previewSchema, fmt.Sprintf("SELECT * FROM %s", safeTable))
		}
		if err != nil {
			return err
		}
		// Roll back so the curriculum schema stays pristine for next preview / capture.
		return errCaptureRollback
	})
	if previewErr != nil && !errors.Is(previewErr, errCaptureRollback) {
		return nil, previewErr
	}
	return results, nil
}

func (r *PostgresRepository) GetQuestion(ctx context.Context, id int) (models.Question, error) {
	// LEFT JOIN app.examinations to resolve the exam's curriculum_schema. When
	// the question is a practice question (exam_id IS NULL) or the exam has not
	// had a per-exam import (curriculum_schema = 'public'), COALESCE returns
	// 'public' — exactly the pre-feature behaviour.
	query := `
		SELECT q.id, q.exam_id, q.target_table, q.correct_sql, q.question_type, q.points, q.requirement,
		       q.strict_ordering, q.strict_attributes, q.strict_column_order,
		       q.expected_result, q.expected_columns, q.expected_result_status,
		       q.expected_result_captured_at, q.capture_error, q.expected_rows_affected,
		       q.created_by, q.created_at, q.updated_at, q.dual_schema,
		       COALESCE(e.curriculum_schema, 'public') AS curriculum_schema
		FROM app.questions q
		LEFT JOIN app.examinations e ON e.id = q.exam_id
		WHERE q.id = $1`

	var q models.Question
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.ExamID, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Points, &q.Requirement,
		&q.StrictMode.Ordering, &q.StrictMode.Attributes, &q.StrictMode.ColumnOrder,
		&q.ExpectedResult, &q.ExpectedColumns, &q.ExpectedResultStatus,
		&q.ExpectedResultCapturedAt, &q.CaptureError, &q.ExpectedRowsAffected,
		&q.CreatedBy, &q.CreatedAt, &q.UpdatedAt, &q.DualSchema,
		&q.CurriculumSchema)

	return q, err
}

func (r *PostgresRepository) GetQuestionsByExamID(ctx context.Context, examID int) ([]models.Question, error) {
	query := `
		SELECT id, exam_id, target_table, correct_sql, question_type, points, requirement,
		       strict_ordering, strict_attributes, strict_column_order,
		       expected_result, expected_columns, expected_result_status,
		       expected_result_captured_at, capture_error, expected_rows_affected,
		       created_by, created_at, updated_at, dual_schema
		FROM app.questions WHERE exam_id = $1`

	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	questions := []models.Question{}
	for rows.Next() {
		var q models.Question
		err := rows.Scan(
			&q.ID, &q.ExamID, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Points, &q.Requirement,
			&q.StrictMode.Ordering, &q.StrictMode.Attributes, &q.StrictMode.ColumnOrder,
			&q.ExpectedResult, &q.ExpectedColumns, &q.ExpectedResultStatus,
			&q.ExpectedResultCapturedAt, &q.CaptureError, &q.ExpectedRowsAffected,
			&q.CreatedBy, &q.CreatedAt, &q.UpdatedAt, &q.DualSchema)
		if err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

func (r *PostgresRepository) DeleteQuestion(ctx context.Context, id int) error {
	query := `DELETE FROM app.questions WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, id)
	return err
}

// ExamHasMixedDualSchema reports whether an exam holds BOTH a
// dual_schema=true and a dual_schema=false question (GAP 4 mitigation,
// ARCH-018). An exam with zero or one question is never mixed. Used to warn
// the lecturer that a student can open a non-dual question's preview in the
// same exam to read the grading schema, bypassing the decoy on the exam's
// dual-schema questions.
func (r *PostgresRepository) ExamHasMixedDualSchema(ctx context.Context, examID int) (bool, error) {
	var mixed bool
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(bool_or(dual_schema) AND bool_or(NOT dual_schema), false)
		FROM app.questions
		WHERE exam_id = $1`, examID).Scan(&mixed)
	return mixed, err
}
