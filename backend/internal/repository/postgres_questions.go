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

	// ARCH-045/046: normalize and validate the lecturer's seed SQL before
	// capture runs, so an invalid seed refuses the save outright rather than
	// surfacing as a confusing capture failure.
	q.SeedSQL = normalizeSeedSQL(q.SeedSQL)
	if q.SeedSQL != "" {
		if _, err := validateSeedSQL(q.SeedSQL); err != nil {
			return 0, err
		}
	}

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
	var autoSeed []byte

	// Resolve the schema capture runs against. Defaults to 'public' for
	// practice questions (ExamID == nil) and for exam questions that haven't
	// had a per-exam import yet. This preserves the historic capture
	// behaviour byte-for-byte when curriculum_schema='public'.
	//
	// ARCH-028 (5): when the question has a category, that category's schema
	// wins over the exam curriculum — a foreign category id (one that does not
	// belong to this exam) is REJECTED outright rather than silently falling
	// back to the exam curriculum, because capturing the model answer against
	// the wrong database would produce an expected_result the student can
	// never legitimately reproduce.
	captureSchema := "public"
	if q.ExamID != nil {
		if q.CategoryID != nil {
			var cs string
			err := r.pool.QueryRow(ctx,
				`SELECT schema_name FROM app.exam_categories WHERE id = $1 AND exam_id = $2`,
				int(*q.CategoryID), int(*q.ExamID)).Scan(&cs)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return 0, ErrExamCategoryMismatch
				}
				return 0, err
			}
			if cs != "" {
				captureSchema = cs
			}
		} else {
			var cs string
			if err := r.pool.QueryRow(ctx,
				`SELECT COALESCE(NULLIF(curriculum_schema, ''), 'public')
				 FROM app.examinations WHERE id = $1`, *q.ExamID).Scan(&cs); err == nil && cs != "" {
				captureSchema = cs
			}
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
				if q.SeedSQL != "" {
					// ARCH-045: the lecturer's seed runs BEFORE the model answer and
					// whenever it is present -- not only on the 0-row branch -- so
					// capture, grading and preview all observe one state. Nested
					// savepoint (ARCH-047) so a failed seed cannot poison the outer
					// capture transaction before the refusal is recorded.
					if _, spErr := tx.Exec(ctx, "SAVEPOINT lecturer_seed"); spErr != nil {
						return spErr
					}
					if seedErr := applyQuestionSeed(ctx, tx, q.SeedSQL, nil); seedErr != nil {
						// ARCH-021 ordering: the tx is aborted, so ROLLBACK TO
						// SAVEPOINT must run before any other statement.
						if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT lecturer_seed"); rbErr != nil {
							return rbErr
						}
						status = "failed"
						captureErr = "Seed INSERT failed: " + seedErr.Error() + "."
						return errCaptureFailed
					}
					tag, err = tx.Exec(ctx, q.CorrectSQL)
					if err == nil && tag.RowsAffected() == 0 {
						status = "failed"
						captureErr = "Your seed rows were inserted, but the correct SQL still affected 0 rows. Check the WHERE clause of the correct SQL against the values you seeded."
						return errCaptureFailed
					}
					// auto_seed stays nil: ARCH-045 forbids both mechanisms at once.
				} else {
					tag, err = tx.Exec(ctx, q.CorrectSQL)
					if err == nil {
						// D3 (ARCH-008): a model answer that affects 0 rows is unGRADEABLE —
						// every student statement, correct or not, produces the same
						// untouched end state. Hard block, no override.
						if tag.RowsAffected() == 0 {
							// ARCH-037: an INSERT that affects 0 rows is an INSERT ... SELECT
							// over an empty source, which seeding cannot fix. q.Type is just
							// "DML" for every statement kind, so the actual leading keyword of
							// the model answer decides — keep today's message unchanged and
							// never attempt derivation for it.
							if isAutoSeedIneligibleInsert(q.CorrectSQL) {
								status = "failed"
								captureErr = "The model answer affected 0 rows, so this question cannot tell a correct answer apart from a statement that does nothing. Fix the WHERE clause of the correct SQL, or fix the seed data in this category's database."
								return errCaptureFailed
							}

							// ARCH-037/038/039: rescue the 0-row DELETE/UPDATE by deriving and
							// seeding the missing row inside a nested savepoint, so a failed
							// seed cannot poison the outer capture transaction.
							if _, spErr := tx.Exec(ctx, "SAVEPOINT autoseed"); spErr != nil {
								return spErr
							}

							cols, colsErr := fetchTableColumns(ctx, tx, captureSchema, q.TargetTable)
							if colsErr != nil {
								return colsErr
							}

							plan, deriveErr := deriveAutoSeed(q.CorrectSQL, q.TargetTable, cols)
							if deriveErr != nil {
								if errors.Is(deriveErr, ErrAutoSeedUnsupported) {
									status = "failed"
									captureErr = "Model answer affected 0 rows. Auto-seed could not build a matching row: " + deriveErr.Error() + ". Use = comparisons on the columns you want seeded, or fix the seed data in this category's database."
									return errCaptureFailed
								}
								return deriveErr
							}

							if applyErr := applyAutoSeed(ctx, tx, plan); applyErr != nil {
								// The transaction is now aborted; ROLLBACK TO SAVEPOINT must run
								// FIRST (ARCH-021 ordering) or every subsequent statement fails.
								if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT autoseed"); rbErr != nil {
									return rbErr
								}
								status = "failed"
								captureErr = "Model answer affected 0 rows. Auto-seed built a row but inserting it failed: " + applyErr.Error() + "."
								return errCaptureFailed
							}

							tag, err = tx.Exec(ctx, q.CorrectSQL)
							if err != nil {
								status = "failed"
								captureErr = err.Error()
								return errCaptureFailed
							}
							if tag.RowsAffected() == 0 {
								status = "failed"
								captureErr = "Model answer affected 0 rows, and still affected 0 rows after auto-seeding a matching row. Check the WHERE clause of the correct SQL."
								return errCaptureFailed
							}

							autoSeed, err = marshalAutoSeed(plan)
							if err != nil {
								return err
							}
						}
					}
				}
				if err == nil {
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
			expected_result_captured_at, capture_error, expected_rows_affected, created_by, dual_schema, category_id, auto_seed, dml_kind, seed_sql, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), $13, $14, $15, $16, $17, $18, NULLIF($19, ''), $20, NOW())
		RETURNING id`
		err = r.pool.QueryRow(ctx, insertQ,
			q.ExamID, q.TargetTable, q.CorrectSQL, q.Type, q.Points, q.Requirement,
			q.StrictMode.Ordering, q.StrictMode.Attributes, q.StrictMode.ColumnOrder,
			expectedResult, expectedCols, status, captureErr, expectedRowsAffected, q.CreatedBy, q.DualSchema, q.CategoryID, autoSeed, q.DMLKind, q.SeedSQL).Scan(&newID)
	} else {
		const upsertQ = `
		INSERT INTO app.questions (
			id, exam_id, target_table, correct_sql, question_type, points, requirement,
			strict_ordering, strict_attributes, strict_column_order,
			expected_result, expected_columns, expected_result_status,
			expected_result_captured_at, capture_error, expected_rows_affected, created_by, dual_schema, category_id, auto_seed, dml_kind, seed_sql, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), $14, $15, $16, $17, $18, $19, NULLIF($20, ''), $21, NOW())
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
			category_id = EXCLUDED.category_id,
			auto_seed = EXCLUDED.auto_seed,
			dml_kind = EXCLUDED.dml_kind,
			seed_sql = EXCLUDED.seed_sql,
			updated_at = NOW()
		RETURNING id`
		err = r.pool.QueryRow(ctx, upsertQ,
			q.ID, q.ExamID, q.TargetTable, q.CorrectSQL, q.Type, q.Points, q.Requirement,
			q.StrictMode.Ordering, q.StrictMode.Attributes, q.StrictMode.ColumnOrder,
			expectedResult, expectedCols, status, captureErr, expectedRowsAffected, q.CreatedBy, q.DualSchema, q.CategoryID, autoSeed, q.DMLKind, q.SeedSQL).Scan(&newID)
	}

	return newID, err
}

// dedupePreviewColumns returns cols with later exact-duplicate names removed,
// keeping the first occurrence of each name in the order given.
//
// ARCH-044. The preview funcs take their ordered column list straight from pgx
// FieldDescriptions, which is the projection order but is NOT deduplicated:
// `SELECT * FROM employee, department` reports `dno` twice. The row maps the
// same code builds are keyed by column name (rowMap[fd.Name] = values[i] in
// queryWithTxCols), so two fields sharing an output name collapse into one map
// key. Handing the frontend more columns than the rows can carry makes
// ResultTable.vue / ExamQuestionsPanel.vue — both `v-for="c in columns"
// :key="c"` over header and cells — emit duplicate v-for keys (Vue warns and
// can mis-patch keyed nodes on re-render) and render a phantom extra column.
// Deduping restores exact parity with the pre-ordering behaviour, which
// derived the header by ranging a row map and so deduped implicitly, while
// keeping the correct projection ordering.
//
// Renaming duplicates (dno, dno_1) is deliberately NOT done: the row map
// physically cannot carry the second column's value, so a renamed header would
// render an always-blank column. Known limitation, pre-dating any of this: the
// surviving cell holds the LAST same-named field's value, because the later
// assignment in the rowMap loop overwrites the earlier one.
//
// Always returns a non-nil slice so callers can hand `[]` (not `null`) to the
// frontend without a further nil check.
func dedupePreviewColumns(cols []string) []string {
	out := make([]string, 0, len(cols))
	seen := make(map[string]struct{}, len(cols))
	for _, c := range cols {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// PreviewCorrectSQL is the backward-compatible wrapper that always previews
// against the 'public' schema. Existing callers (e.g. practice_controller) use
// this signature; new callers that want per-exam curriculum resolution should
// call PreviewCorrectSQLInSchema instead.
func (r *PostgresRepository) PreviewCorrectSQL(ctx context.Context, qType, targetTable, sqlText string) ([]map[string]interface{}, []string, error) {
	return r.PreviewCorrectSQLInSchema(ctx, qType, targetTable, sqlText, 0, 0, false, "")
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
// categoryID: optional exam category id (0 or negative = absent). When
// present and > 0, ARCH-028 (5): the category's own schema wins over the exam
// curriculum, with the same (id, exam_id) ownership check CreateQuestion
// applies — a category id that does not belong to examID returns
// ErrExamCategoryMismatch rather than silently falling back to the exam
// curriculum. When categoryID is absent (0), the exam/public resolution below
// runs byte-for-byte as before this parameter existed.
//
// useDecoy: when true and examID > 0 (and no category, or a category whose
// preview_schema_name is set), previews against the decoy preview curriculum
// (dual-schema grading, ARCH-005) instead of the real curriculum_schema — the
// lecturer's "Preview as student" view. Errors immediately if no decoy has
// been imported for the exam/category, rather than falling back to 'public'
// or the real curriculum (which would leak grading schema B under a
// "student" preview). When useDecoy is false, behaviour is byte-for-byte
// identical to before this parameter existed.
//
// seedSQL: optional lecturer-authored INSERT-only seed block (ARCH-045/047).
// When non-empty it is applied inside the same rolled-back transaction,
// before sqlText runs, so the lecturer can see the seeded state before
// committing to saving the question. Empty means no seed, byte-for-byte
// identical to before this parameter existed.
//
// Returns (rows, orderedColumns, err). The ordered columns come from pgx
// FieldDescriptions, i.e. the SELECT projection order — the row maps
// themselves are Go maps and therefore have no order, so ranging one to build
// the preview header shuffled the columns on every request. For DML the
// columns are the post-image snapshot's (SELECT * FROM target) field order,
// which is the table's physical column order.
func (r *PostgresRepository) PreviewCorrectSQLInSchema(ctx context.Context, qType, targetTable, sqlText string, examID int, categoryID int, useDecoy bool, seedSQL string) ([]map[string]interface{}, []string, error) {
	if sqlText == "" {
		return nil, nil, fmt.Errorf("sql text is empty")
	}
	if qType == "DML" && targetTable == "" {
		return nil, nil, fmt.Errorf("target_table is required for DML preview")
	}

	// Repair typographic quotes pasted from documents so the SQL executes
	// instead of failing with a spurious `column "‘...’" does not exist`.
	sqlText = normalizeSmartQuotes(sqlText)

	// Resolve the schema to preview against. Defaults to 'public' when examID
	// is absent (0) or when the exam hasn't had a per-exam import yet.
	var previewSchema string
	if categoryID > 0 {
		var schemaName, previewSchemaName string
		err := r.pool.QueryRow(ctx,
			`SELECT schema_name, preview_schema_name FROM app.exam_categories WHERE id = $1 AND exam_id = $2`,
			categoryID, examID).Scan(&schemaName, &previewSchemaName)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, ErrExamCategoryMismatch
			}
			return nil, nil, err
		}
		if useDecoy {
			if previewSchemaName == "" {
				return nil, nil, fmt.Errorf("no decoy schema uploaded for this category")
			}
			previewSchema = previewSchemaName
		} else {
			previewSchema = schemaName
		}
	} else if useDecoy && examID > 0 {
		decoy, err := r.GetExamPreviewCurriculumSchema(ctx, examID)
		if err != nil {
			return nil, nil, err
		}
		if decoy == "" {
			return nil, nil, fmt.Errorf("no decoy schema uploaded for this exam")
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
			return nil, nil, err
		}
		targetTable = canonical
	}

	var results []map[string]interface{}
	var orderedCols []string
	previewErr := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		safe := pgx.Identifier{previewSchema}.Sanitize()
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
			return err
		}
		var err error
		if qType == "QUERY" {
			results, orderedCols, err = r.queryWithTxCols(ctx, tx, previewSchema, sqlText)
		} else {
			if seedSQL != "" {
				if seedErr := applyQuestionSeed(ctx, tx, seedSQL, nil); seedErr != nil {
					return seedErr
				}
			}
			if _, err = tx.Exec(ctx, sqlText); err != nil {
				return err
			}
			safeTable := pgx.Identifier{targetTable}.Sanitize()
			results, orderedCols, err = r.queryWithTxCols(ctx, tx, previewSchema, fmt.Sprintf("SELECT * FROM %s", safeTable))
		}
		if err != nil {
			return err
		}
		// Roll back so the curriculum schema stays pristine for next preview / capture.
		return errCaptureRollback
	})
	if previewErr != nil && !errors.Is(previewErr, errCaptureRollback) {
		return nil, nil, previewErr
	}
	// ARCH-044: FieldDescriptions is not deduplicated but the row maps are
	// (same-named fields collapse to one key), so trim the header down to the
	// keys the rows actually carry. Preview only — the capture path that feeds
	// expected_columns for grading is untouched.
	return results, dedupePreviewColumns(orderedCols), nil
}

func (r *PostgresRepository) GetQuestion(ctx context.Context, id int) (models.Question, error) {
	// LEFT JOIN app.examinations to resolve the exam's curriculum_schema. When
	// the question is a practice question (exam_id IS NULL) or the exam has not
	// had a per-exam import (curriculum_schema = 'public'), COALESCE returns
	// 'public' — exactly the pre-feature behaviour. LEFT JOIN app.exam_categories
	// additionally resolves the question's category, when it has one — it stays
	// the no-category fallback (ARCH-028).
	query := `
		SELECT q.id, q.exam_id, q.target_table, q.correct_sql, q.question_type, q.points, q.requirement,
		       q.strict_ordering, q.strict_attributes, q.strict_column_order,
		       q.expected_result, q.expected_columns, q.expected_result_status,
		       q.expected_result_captured_at, q.capture_error, q.expected_rows_affected,
		       q.created_by, q.created_at, q.updated_at, q.dual_schema,
		       COALESCE(e.curriculum_schema, 'public') AS curriculum_schema,
		       q.category_id, COALESCE(ec.name, ''), COALESCE(ec.schema_name, ''), COALESCE(ec.preview_schema_name, ''),
		       q.auto_seed, COALESCE(q.dml_kind, ''), COALESCE(q.seed_sql, '')
		FROM app.questions q
		LEFT JOIN app.examinations e ON e.id = q.exam_id
		LEFT JOIN app.exam_categories ec ON ec.id = q.category_id
		WHERE q.id = $1`

	var q models.Question
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.ExamID, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Points, &q.Requirement,
		&q.StrictMode.Ordering, &q.StrictMode.Attributes, &q.StrictMode.ColumnOrder,
		&q.ExpectedResult, &q.ExpectedColumns, &q.ExpectedResultStatus,
		&q.ExpectedResultCapturedAt, &q.CaptureError, &q.ExpectedRowsAffected,
		&q.CreatedBy, &q.CreatedAt, &q.UpdatedAt, &q.DualSchema,
		&q.CurriculumSchema,
		&q.CategoryID, &q.CategoryName, &q.CategorySchema, &q.CategoryPreviewSchema,
		&q.AutoSeed, &q.DMLKind, &q.SeedSQL)

	return q, err
}

func (r *PostgresRepository) GetQuestionsByExamID(ctx context.Context, examID int) ([]models.Question, error) {
	// LEFT JOIN app.exam_categories resolves each question's category for
	// display (category_id/category_name) and for the evaluator's schema
	// resolution (category_schema/category_preview_schema, both json:"-").
	// ORDER BY groups categorised questions contiguously (name ASC), with
	// uncategorised questions sorted last (ARCH-028 (2)(b) / ARCH-030).
	query := `
		SELECT q.id, q.exam_id, q.target_table, q.correct_sql, q.question_type, q.points, q.requirement,
		       q.strict_ordering, q.strict_attributes, q.strict_column_order,
		       q.expected_result, q.expected_columns, q.expected_result_status,
		       q.expected_result_captured_at, q.capture_error, q.expected_rows_affected,
		       q.created_by, q.created_at, q.updated_at, q.dual_schema,
		       q.category_id, COALESCE(ec.name, ''), COALESCE(ec.schema_name, ''), COALESCE(ec.preview_schema_name, ''),
		       q.auto_seed, COALESCE(q.dml_kind, ''), COALESCE(q.seed_sql, '')
		FROM app.questions q
		LEFT JOIN app.exam_categories ec ON ec.id = q.category_id
		WHERE q.exam_id = $1
		ORDER BY (q.category_id IS NULL) ASC, ec.name ASC, q.id ASC`

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
			&q.CreatedBy, &q.CreatedAt, &q.UpdatedAt, &q.DualSchema,
			&q.CategoryID, &q.CategoryName, &q.CategorySchema, &q.CategoryPreviewSchema,
			&q.AutoSeed, &q.DMLKind, &q.SeedSQL)
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
