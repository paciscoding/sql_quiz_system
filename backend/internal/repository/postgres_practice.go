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

// createCategoryWithSchema inserts a category row, derives its schema_name from
// the new SERIAL id (practice_cat_<id>), and creates+grants the owned schema.
// The two-step insert/update runs in one transaction; the schema is created
// after commit so the id is final. Returns the new category id.
func (r *PostgresRepository) createCategoryWithSchema(ctx context.Context, name string, createdBy int) (int, error) {
	var id int
	err := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO app.practice_categories (name, created_by, schema_name)
			 VALUES ($1, $2, '') RETURNING id`, name, createdBy).Scan(&id); err != nil {
			return err
		}
		schema := PracticeCategorySchemaName(id)
		_, err := tx.Exec(ctx,
			`UPDATE app.practice_categories SET schema_name = $1, updated_at = now() WHERE id = $2`,
			schema, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	if err := r.ensureCategorySchema(ctx, id); err != nil {
		return 0, err
	}
	return id, nil
}

// migratePracticeCategories adds practice_questions.category_id, backfills it
// from the legacy free-text category strings (one category per distinct string,
// plus an "Uncategorized" fallback), and enforces NOT NULL once every row is
// populated. Fully idempotent / re-runnable.
func (r *PostgresRepository) migratePracticeCategories(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx,
		"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS category_id INTEGER REFERENCES app.practice_categories(id)"); err != nil {
		return fmt.Errorf("add category_id column: %w", err)
	}

	// categories.created_by FKs to system_users. On a brand-new database the admin
	// account is seeded AFTER this runs, so there may be no user yet — but then
	// there are also no practice questions to migrate, so we can safely defer
	// category creation to a later run.
	var fallbackUserID int
	if err := r.pool.QueryRow(ctx,
		"SELECT id FROM app.system_users ORDER BY id ASC LIMIT 1").Scan(&fallbackUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("probe system_users: %w", err)
	}

	// Distinct legacy category names (empty/null collapse to Uncategorized), each
	// with the lowest created_by among its questions as the owning lecturer.
	type catSeed struct {
		name      string
		createdBy int
	}
	seeds := []catSeed{}
	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(NULLIF(category, ''), 'Uncategorized') AS name, MIN(created_by) AS cb
		FROM app.practice_questions
		GROUP BY 1`)
	if err != nil {
		return fmt.Errorf("enumerate legacy categories: %w", err)
	}
	for rows.Next() {
		var s catSeed
		if err := rows.Scan(&s.name, &s.createdBy); err != nil {
			rows.Close()
			return err
		}
		seeds = append(seeds, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Always ensure an Uncategorized fallback exists.
	hasUncategorized := false
	for _, s := range seeds {
		if s.name == "Uncategorized" {
			hasUncategorized = true
			break
		}
	}
	if !hasUncategorized {
		seeds = append(seeds, catSeed{name: "Uncategorized", createdBy: fallbackUserID})
	}

	for _, s := range seeds {
		var id int
		e := r.pool.QueryRow(ctx, "SELECT id FROM app.practice_categories WHERE name = $1", s.name).Scan(&id)
		if e == nil {
			// Exists already — make sure its owned schema is present.
			if err := r.ensureCategorySchema(ctx, id); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return fmt.Errorf("probe category %q: %w", s.name, e)
		}
		if _, err := r.createCategoryWithSchema(ctx, s.name, s.createdBy); err != nil {
			return fmt.Errorf("create category %q: %w", s.name, err)
		}
	}

	// Backfill category_id by matching the legacy string to a category name.
	if _, err := r.pool.Exec(ctx, `
		UPDATE app.practice_questions pq
		SET category_id = pc.id
		FROM app.practice_categories pc
		WHERE pq.category_id IS NULL
		  AND pc.name = COALESCE(NULLIF(pq.category, ''), 'Uncategorized')`); err != nil {
		return fmt.Errorf("backfill category_id: %w", err)
	}
	// Safety net: any still-null row goes to Uncategorized.
	if _, err := r.pool.Exec(ctx, `
		UPDATE app.practice_questions
		SET category_id = (SELECT id FROM app.practice_categories WHERE name = 'Uncategorized')
		WHERE category_id IS NULL`); err != nil {
		return fmt.Errorf("backfill uncategorized: %w", err)
	}

	// Enforce NOT NULL only once every row is populated (guards re-runs and
	// avoids erroring on partially-migrated data).
	var nullCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.practice_questions WHERE category_id IS NULL").Scan(&nullCount); err != nil {
		return err
	}
	if nullCount == 0 {
		if _, err := r.pool.Exec(ctx,
			"ALTER TABLE app.practice_questions ALTER COLUMN category_id SET NOT NULL"); err != nil {
			return fmt.Errorf("set category_id NOT NULL: %w", err)
		}
	}
	return nil
}

// CreatePracticeCategory creates a new category owning a fresh empty schema.
func (r *PostgresRepository) CreatePracticeCategory(ctx context.Context, name string, createdBy int) (*models.PracticeCategory, error) {
	id, err := r.createCategoryWithSchema(ctx, name, createdBy)
	if err != nil {
		return nil, err
	}
	var cat models.PracticeCategory
	err = r.pool.QueryRow(ctx,
		`SELECT id, name, schema_name, created_by, created_at, updated_at, introduction
		 FROM app.practice_categories WHERE id = $1`, id).Scan(
		&cat.ID, &cat.Name, &cat.SchemaName, &cat.CreatedBy, &cat.CreatedAt, &cat.UpdatedAt, &cat.Introduction)
	if err != nil {
		return nil, err
	}
	return &cat, nil
}

// UpdatePracticeCategoryIntroduction persists a new description text for a
// category. Returns ErrCategoryNotFound if the id does not exist.
func (r *PostgresRepository) UpdatePracticeCategoryIntroduction(ctx context.Context, id int, introduction string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.practice_categories SET introduction = $1, updated_at = now() WHERE id = $2`,
		introduction, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

// ListPracticeCategories returns all categories with a live question_count.
func (r *PostgresRepository) ListPracticeCategories(ctx context.Context) ([]models.PracticeCategory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.name, c.schema_name, c.created_by, c.created_at, c.updated_at,
		       COUNT(q.id) AS question_count, c.introduction, COALESCE(c.preview_schema_name, '')
		FROM app.practice_categories c
		LEFT JOIN app.practice_questions q ON q.category_id = c.id
		GROUP BY c.id
		ORDER BY c.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cats := []models.PracticeCategory{}
	for rows.Next() {
		var c models.PracticeCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.SchemaName, &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt, &c.QuestionCount, &c.Introduction, &c.PreviewSchemaName); err != nil {
			return nil, err
		}
		c.HasPreviewSchema = c.PreviewSchemaName != ""
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

// RenamePracticeCategory updates only the display name; the owned schema_name is
// never touched so imported data and the practice_cat_<id> mapping stay stable.
func (r *PostgresRepository) RenamePracticeCategory(ctx context.Context, id int, name string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.practice_categories SET name = $1, updated_at = now() WHERE id = $2`, name, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

// DeletePracticeCategory removes a category. Without force it blocks (returns
// ErrCategoryHasQuestions) when the category still has practice questions. With
// force it cascades: deletes the questions (and their attempts via ON DELETE
// CASCADE), drops the owned schema, then removes the category row. The
// Uncategorized fallback category can never be deleted. Also reclaims the
// category's decoy preview schema, if any, by canonical name (ARCH-019); a
// failure to do so is warned-and-continued, not fatal.
func (r *PostgresRepository) DeletePracticeCategory(ctx context.Context, id int, force bool) error {
	var name string
	var schema string
	if err := r.pool.QueryRow(ctx,
		"SELECT name, schema_name FROM app.practice_categories WHERE id = $1", id).Scan(&name, &schema); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCategoryNotFound
		}
		return err
	}
	if name == "Uncategorized" {
		return ErrCategoryProtected
	}

	var questionCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.practice_questions WHERE category_id = $1", id).Scan(&questionCount); err != nil {
		return err
	}
	if questionCount > 0 && !force {
		return fmt.Errorf("%w: %d question(s) still in this category", ErrCategoryHasQuestions, questionCount)
	}

	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		if force {
			if _, err := tx.Exec(ctx, "DELETE FROM app.practice_questions WHERE category_id = $1", id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM app.practice_categories WHERE id = $1", id); err != nil {
			return err
		}
		if schema != "" {
			// ARCH-013: take the schema's exclusive rebuild lock before dropping it,
			// so a practice evaluation in flight against this category is never
			// dropped out from under it.
			if err := acquireSchemaRebuildLockTx(ctx, tx, schema); err != nil {
				return fmt.Errorf("failed to acquire schema rebuild lock: %w", err)
			}
			safe := pgx.Identifier{schema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				return err
			}
		}

		// Decoy preview schema (dual-schema grading, ARCH-019): dropped
		// unconditionally by canonical name (not gated on
		// practice_categories.preview_schema_name), so a failed or partial
		// decoy import that never stamped the column is still reclaimed. Takes
		// the same rebuild-lock discipline as the real schema above — load-
		// bearing here too, because evaluateDMLPreview takes the shared eval
		// lock on this exact schema — but deliberately, unlike the real-schema
		// block above, a failure of EITHER the lock acquisition OR the drop is
		// warned-and-continued rather than aborting the whole category delete:
		// the decoy is display-only, so leaving one reclaimable schema behind
		// is better than failing a delete over it. Mirrors the preview-drop
		// precedent in ArchiveExamination (postgres_exams.go).
		previewSchema := PracticeCategoryPreviewSchemaName(id)
		if err := func() error {
			if err := acquireSchemaRebuildLockTx(ctx, tx, previewSchema); err != nil {
				return fmt.Errorf("failed to acquire preview schema rebuild lock: %w", err)
			}
			safePreview := pgx.Identifier{previewSchema}.Sanitize()
			_, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safePreview))
			return err
		}(); err != nil {
			fmt.Printf("Warning: failed to drop practice category preview schema %s during delete: %v\n", previewSchema, err)
		}

		return nil
	})
}

// GetPracticeCategorySchema resolves a category id to its owned schema name.
func (r *PostgresRepository) GetPracticeCategorySchema(ctx context.Context, id int) (string, error) {
	var schema string
	err := r.pool.QueryRow(ctx,
		"SELECT schema_name FROM app.practice_categories WHERE id = $1", id).Scan(&schema)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCategoryNotFound
	}
	return schema, err
}

// CategoryExists reports whether a category id is present.
func (r *PostgresRepository) CategoryExists(ctx context.Context, id int) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM app.practice_categories WHERE id = $1)", id).Scan(&exists)
	return exists, err
}

// CategoryHasMixedDualSchema reports whether a category holds BOTH a
// dual_schema=true and a dual_schema=false question (GAP 4 mitigation,
// ARCH-018). A category with zero or one question is never mixed. Used to
// warn the lecturer that a student can open a non-dual question's preview in
// the same category to read the grading schema, bypassing the decoy on the
// category's dual-schema questions.
func (r *PostgresRepository) CategoryHasMixedDualSchema(ctx context.Context, categoryID int) (bool, error) {
	var mixed bool
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(bool_or(dual_schema) AND bool_or(NOT dual_schema), false)
		FROM app.practice_questions
		WHERE category_id = $1`, categoryID).Scan(&mixed)
	return mixed, err
}

// GetPracticeCategory returns the full row for a single category including its
// live question_count and introduction. Returns ErrCategoryNotFound if missing.
func (r *PostgresRepository) GetPracticeCategory(ctx context.Context, id int) (*models.PracticeCategory, error) {
	var cat models.PracticeCategory
	err := r.pool.QueryRow(ctx, `
		SELECT c.id, c.name, c.schema_name, c.created_by, c.created_at, c.updated_at,
		       COUNT(q.id) AS question_count, c.introduction, COALESCE(c.preview_schema_name, '')
		FROM app.practice_categories c
		LEFT JOIN app.practice_questions q ON q.category_id = c.id
		WHERE c.id = $1
		GROUP BY c.id`, id).Scan(
		&cat.ID, &cat.Name, &cat.SchemaName, &cat.CreatedBy, &cat.CreatedAt, &cat.UpdatedAt,
		&cat.QuestionCount, &cat.Introduction, &cat.PreviewSchemaName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCategoryNotFound
		}
		return nil, err
	}
	cat.HasPreviewSchema = cat.PreviewSchemaName != ""
	return &cat, nil
}

func (r *PostgresRepository) CreatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	// Repair typographic quotes (e.g. ‘…’ → '…') before capture and persistence,
	// so pasted correct SQL doesn't fail with "column ... does not exist".
	q.CorrectSQL = normalizeSmartQuotes(q.CorrectSQL)
	// ARCH-046: normalize + validate the lecturer's seed before capture runs,
	// so an invalid seed is rejected with the ARCH-046 refusal instead of
	// being persisted and only failing later at grade time.
	q.SeedSQL = normalizeSeedSQL(q.SeedSQL)
	if q.SeedSQL != "" {
		if _, err := validateSeedSQL(q.SeedSQL); err != nil {
			return err
		}
	}
	// Resolve the category's owned schema; the correct-result capture and all
	// student submissions for this question run against it (not public).
	schema, err := r.GetPracticeCategorySchema(ctx, int(q.CategoryID))
	if err != nil {
		return err
	}

	// Phase-0: Correct Result Capture (always rolled back)
	status := "captured"
	var captureErr string
	var expectedResult, expectedCols, autoSeed []byte
	var expectedRowsAffected *int

	if q.Type != "QUERY" {
		if canonical, resolveErr := r.ResolveTargetTable(ctx, schema, q.TargetTable); resolveErr != nil {
			status = "failed"
			captureErr = resolveErr.Error()
			q.TargetTable = strings.TrimSpace(q.TargetTable)
		} else {
			q.TargetTable = canonical
			err = r.captureExpectedResult(ctx, schema, q, &status, &captureErr, &expectedResult, &expectedCols, &expectedRowsAffected, &autoSeed)
			if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
				return err
			}
		}
	} else {
		err = r.captureExpectedResult(ctx, schema, q, &status, &captureErr, &expectedResult, &expectedCols, &expectedRowsAffected, &autoSeed)
		if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
			return err
		}
	}
	q.ExpectedRowsAffected = expectedRowsAffected
	q.AutoSeed = json.RawMessage(autoSeed)

	query := `
		INSERT INTO app.practice_questions (
			created_by, category_id, target_table, correct_sql, question_type, requirement,
			points, strict_ordering, strict_attrs, strict_column_order,
			expected_result, expected_columns, expected_result_status,
			capture_error, title, expected_rows_affected, dual_schema, auto_seed, dml_kind, seed_sql, expected_result_captured_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, NULLIF($19, ''), $20, NOW(), NOW(), NOW())
		RETURNING id, created_at, updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CreatedBy, q.CategoryID, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs, q.StrictColumnOrder,
		expectedResult, expectedCols, status, captureErr, q.Title, expectedRowsAffected, q.DualSchema, autoSeed, q.DMLKind, q.SeedSQL,
	).Scan(&q.ID, &q.CreatedAt, &q.UpdatedAt)
}

// captureExpectedResult runs the lecturer's correct SQL inside a rolled-back
// transaction scoped to the category schema, populating status/captureErr and
// the marshalled expected result + columns. Returns errCaptureRollback on
// success (so WithTransaction discards the lecturer's DML side effects) or
// errCaptureFailed when the SQL aborts.
//
// autoSeed receives the ARCH-040 AutoSeedPlan JSON when the 0-row rescue
// (ARCH-037/038) derives and successfully applies a seed row; it is left nil
// for QUERY questions and for any DML question whose model answer affects
// rows on the first run — deriveAutoSeed is never even called on that path,
// which is what keeps already-working questions byte-identical to before.
func (r *PostgresRepository) captureExpectedResult(ctx context.Context, schema string, q *models.PracticeQuestion, status *string, captureErr *string, expectedResult *[]byte, expectedCols *[]byte, expectedRowsAffected **int, autoSeed *[]byte) error {
	// ARCH-059/061: this replays the lecturer's DML against the same
	// practice_cat_N schema a student's Evaluate can be mid-transaction
	// against, so it must take the same per-schema gate — before the
	// transaction below begins, never after. SELECT-only questions (q.Type ==
	// "QUERY") are exempt, mirroring Evaluate/EvaluatePreview: SELECT is
	// parallel-safe by construction and gating it would serialise the hot path
	// for no benefit. A gate timeout here is an infrastructure error, not a
	// lecturer mistake — it is returned unchanged (not folded into
	// status/captureErr, which the caller would persist as capture_error and
	// blame on the lecturer's SQL) so callers propagate it like any other
	// non-capture error.
	if q.Type != "QUERY" {
		release, err := r.acquireDMLGate(ctx, schema)
		if err != nil {
			return err
		}
		defer release()
	}
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		// ARCH-062: no `, public`. Capture must resolve names exactly as
		// grading does (postgres_evaluation.go:301), or a question captures
		// green and fails at grade time — the shape of the shipped
		// target_table case bug.
		//
		// The DML post-image below goes through queryWithTxCols, which re-sets
		// search_path to `<schema>, public` (postgres_core.go:132). That is
		// harmless and is left alone on purpose: the post-image is
		// `SELECT * FROM <target_table>`, and ResolveTargetTable
		// (postgres_target_table.go:54) has already proved target_table exists
		// in <schema>, which is searched first. The re-set can never resolve
		// the post-image through public.
		safeSchema := pgx.Identifier{schema}.Sanitize()
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safeSchema)); err != nil {
			return err
		}

		var results []map[string]interface{}
		var orderedCols []string
		var err error
		if q.Type == "QUERY" {
			results, orderedCols, err = r.queryWithTxCols(ctx, tx, schema, q.CorrectSQL)
		} else {
			if q.TargetTable == "" {
				return fmt.Errorf("target table is required for DML questions")
			}
			var tag pgconn.CommandTag
			if q.SeedSQL != "" {
				// ARCH-045: the lecturer's seed runs BEFORE the model answer and
				// whenever it is present — not only on the 0-row branch — so
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
					*status = "failed"
					*captureErr = "Seed INSERT failed: " + seedErr.Error() + "."
					return errCaptureFailed
				}
				tag, err = tx.Exec(ctx, q.CorrectSQL)
				if err == nil && tag.RowsAffected() == 0 {
					*status = "failed"
					*captureErr = "Your seed rows were inserted, but the correct SQL still affected 0 rows. Check the WHERE clause of the correct SQL against the values you seeded."
					return errCaptureFailed
				}
				// auto_seed stays nil: ARCH-045 forbids both mechanisms at once.
			} else {
				tag, err = tx.Exec(ctx, q.CorrectSQL)
				if err == nil {
					// D3 (ARCH-008): a model answer that affects 0 rows is unGRADEABLE —
					// every student statement, correct or not, produces the same
					// untouched end state. Hard block, no override — but ARCH-037/038
					// lets us rescue it by deriving and seeding the missing row the
					// WHERE clause pins, when the model answer's grammar is narrow
					// enough for deriveAutoSeed to understand.
					if tag.RowsAffected() == 0 {
						if isAutoSeedIneligibleInsert(q.CorrectSQL) {
							// A 0-row INSERT is an INSERT ... SELECT over an empty
							// source, which no amount of row-seeding can fix — skip
							// derivation entirely and keep today's message.
							*status = "failed"
							*captureErr = "The model answer affected 0 rows, so this question cannot tell a correct answer apart from a statement that does nothing. Fix the WHERE clause of the correct SQL, or fix the seed data in this category's database."
							return errCaptureFailed
						}

						// Nested savepoint (ARCH-037): a failed seed must not poison
						// the capture transaction beyond this point.
						if _, spErr := tx.Exec(ctx, "SAVEPOINT autoseed"); spErr != nil {
							return spErr
						}

						cols, colsErr := fetchTableColumns(ctx, tx, schema, q.TargetTable)
						if colsErr != nil {
							return colsErr
						}

						plan, deriveErr := deriveAutoSeed(q.CorrectSQL, q.TargetTable, cols)
						if deriveErr != nil {
							if !errors.Is(deriveErr, ErrAutoSeedUnsupported) {
								return deriveErr
							}
							*status = "failed"
							*captureErr = "Model answer affected 0 rows. Auto-seed could not build a matching row: " + deriveErr.Error() + ". Use = comparisons on the columns you want seeded, or fix the seed data in this category's database."
							return errCaptureFailed
						}

						if applyErr := applyAutoSeed(ctx, tx, plan); applyErr != nil {
							// ARCH-021 ordering: applyErr has already aborted the
							// transaction, so roll back to the savepoint FIRST — any
							// other statement issued before that would itself fail.
							if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT autoseed"); rbErr != nil {
								return rbErr
							}
							*status = "failed"
							*captureErr = "Model answer affected 0 rows. Auto-seed built a row but inserting it failed: " + applyErr.Error() + "."
							return errCaptureFailed
						}

						tag, err = tx.Exec(ctx, q.CorrectSQL)
						if err != nil {
							*status = "failed"
							*captureErr = err.Error()
							return errCaptureFailed
						}
						if tag.RowsAffected() == 0 {
							*status = "failed"
							*captureErr = "Model answer affected 0 rows, and still affected 0 rows after auto-seeding a matching row. Check the WHERE clause of the correct SQL."
							return errCaptureFailed
						}

						*autoSeed, _ = marshalAutoSeed(plan)
					}
				}
			}
			if err == nil {
				rowsAffected := int(tag.RowsAffected())
				*expectedRowsAffected = &rowsAffected
				safeTable := pgx.Identifier{q.TargetTable}.Sanitize()
				results, orderedCols, err = r.queryWithTxCols(ctx, tx, schema, fmt.Sprintf("SELECT * FROM %s", safeTable))
			}
		}

		if err != nil {
			*status = "failed"
			*captureErr = err.Error()
			return errCaptureFailed
		}
		if len(results) > 10000 {
			*status = "failed"
			*captureErr = "result-set too large (max 10,000 rows)"
			return errCaptureFailed
		}

		*expectedResult, _ = json.Marshal(results)
		if len(orderedCols) > 0 {
			*expectedCols, _ = json.Marshal(orderedCols)
		}
		return errCaptureRollback
	})
}

// PreviewPracticeCorrectSQL dry-runs a lecturer's correct SQL for a practice
// question against its owning category's schema (never 'public'), inside a
// transaction that is always rolled back. For DML the post-image of the
// resolved target_table is returned. Mirrors PreviewCorrectSQLInSchema
// (postgres_questions.go) but resolves the category schema instead of an exam
// curriculum schema (ARCH-003). When useDecoy is true, previews against the
// category's decoy preview schema (ARCH-006 / "Preview as student" for
// dual-schema questions) instead of the real grading schema; it never falls
// back silently to the real schema when no decoy has been imported.
//
// Returns (rows, orderedColumns, err); the ordered columns are the pgx
// FieldDescriptions order, i.e. the SELECT projection order (for DML, the
// post-image snapshot's column order). Row maps are unordered, so callers must
// use these columns instead of ranging a row to build the preview header.
//
// When seedSQL is non-empty it is applied via applyQuestionSeed (ARCH-047)
// inside the same rolled-back transaction, before sqlText runs, so the
// lecturer previews the state their seed rows produce.
func (r *PostgresRepository) PreviewPracticeCorrectSQL(ctx context.Context, categoryID int, qType, targetTable, sqlText string, useDecoy bool, seedSQL string) ([]map[string]interface{}, []string, error) {
	if sqlText == "" {
		return nil, nil, fmt.Errorf("sql text is empty")
	}
	if qType == "DML" && targetTable == "" {
		return nil, nil, fmt.Errorf("target_table is required for DML preview")
	}

	sqlText = normalizeSmartQuotes(sqlText)

	var previewSchema string
	var err error
	if useDecoy {
		previewSchema, err = r.GetPracticeCategoryPreviewSchema(ctx, categoryID)
		if err != nil {
			return nil, nil, err
		}
		if previewSchema == "" {
			return nil, nil, fmt.Errorf("no decoy schema uploaded for this category")
		}
	} else {
		previewSchema, err = r.GetPracticeCategorySchema(ctx, categoryID)
		if err != nil {
			return nil, nil, err
		}
	}

	if qType == "DML" {
		canonical, resolveErr := r.ResolveTargetTable(ctx, previewSchema, targetTable)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
		targetTable = canonical
	}

	// ARCH-059/061: this runs the lecturer's DML against `previewSchema`,
	// which for useDecoy==false IS the schema students are graded against —
	// so it must take the same per-schema gate a student's Evaluate does,
	// before the transaction below begins. Keyed on previewSchema (the
	// resolved decoy or the real category schema), matching EvaluatePreview's
	// own keying. QUERY previews are exempt, same as everywhere else: SELECT
	// is parallel-safe and gating it buys nothing. A gate timeout is returned
	// unchanged rather than surfaced as a SQL error, since it never reached
	// the lecturer's SQL at all.
	if qType != "QUERY" {
		release, err := r.acquireDMLGate(ctx, previewSchema)
		if err != nil {
			return nil, nil, err
		}
		defer release()
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
		// Roll back so the category schema stays pristine for next preview / capture.
		return errCaptureRollback
	})
	if previewErr != nil && !errors.Is(previewErr, errCaptureRollback) {
		return nil, nil, previewErr
	}
	// ARCH-044: see dedupePreviewColumns (postgres_questions.go). Same-named
	// projection fields collapse into one row-map key, so the header must be
	// deduped to stay in step with the rows. Preview only.
	return results, dedupePreviewColumns(orderedCols), nil
}

func (r *PostgresRepository) UpdatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	// Repair typographic quotes before capture and persistence (see Create).
	q.CorrectSQL = normalizeSmartQuotes(q.CorrectSQL)
	// ARCH-046: normalize + validate the lecturer's seed before capture runs
	// (see CreatePracticeQuestion).
	q.SeedSQL = normalizeSeedSQL(q.SeedSQL)
	if q.SeedSQL != "" {
		if _, err := validateSeedSQL(q.SeedSQL); err != nil {
			return err
		}
	}
	schema, err := r.GetPracticeCategorySchema(ctx, int(q.CategoryID))
	if err != nil {
		return err
	}

	// Phase-0: Correct Result Capture (always rolled back)
	status := "captured"
	var captureErr string
	var expectedResult, expectedCols, autoSeed []byte
	var expectedRowsAffected *int

	if q.Type != "QUERY" {
		if canonical, resolveErr := r.ResolveTargetTable(ctx, schema, q.TargetTable); resolveErr != nil {
			status = "failed"
			captureErr = resolveErr.Error()
			q.TargetTable = strings.TrimSpace(q.TargetTable)
		} else {
			q.TargetTable = canonical
			err = r.captureExpectedResult(ctx, schema, q, &status, &captureErr, &expectedResult, &expectedCols, &expectedRowsAffected, &autoSeed)
			if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
				return err
			}
		}
	} else {
		err = r.captureExpectedResult(ctx, schema, q, &status, &captureErr, &expectedResult, &expectedCols, &expectedRowsAffected, &autoSeed)
		if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
			return err
		}
	}
	q.ExpectedRowsAffected = expectedRowsAffected
	q.AutoSeed = json.RawMessage(autoSeed)

	query := `
		UPDATE app.practice_questions
		SET category_id=$1, target_table=$2, correct_sql=$3, question_type=$4, requirement=$5,
		    points=$6, strict_ordering=$7, strict_attrs=$8, strict_column_order=$9,
		    expected_result=$10, expected_columns=$11,
		    expected_result_status=$12, capture_error=$13, title=$14,
		    expected_rows_affected=$15, dual_schema=$16, auto_seed=$17,
		    dml_kind=NULLIF($18, ''), seed_sql=$19,
		    expected_result_captured_at=NOW(), updated_at=NOW()
		WHERE id=$20
		RETURNING updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CategoryID, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs, q.StrictColumnOrder,
		expectedResult, expectedCols, status, captureErr, q.Title, expectedRowsAffected, q.DualSchema, autoSeed, q.DMLKind, q.SeedSQL, q.ID,
	).Scan(&q.UpdatedAt)
}

func (r *PostgresRepository) ListPracticeQuestions(ctx context.Context) ([]models.PracticeQuestion, error) {
	query := `
		SELECT pq.id, pq.created_by, pq.category, COALESCE(pq.category_id, 0),
		       COALESCE(pc.name, ''), COALESCE(pc.schema_name, ''), pq.dual_schema, COALESCE(pc.preview_schema_name, ''),
		       pq.target_table, pq.correct_sql, pq.question_type, pq.requirement, pq.points,
		       pq.strict_ordering, pq.strict_attrs, pq.strict_column_order, pq.expected_result, pq.expected_columns,
		       pq.expected_result_status, pq.capture_error, COALESCE(pq.title, ''), pq.expected_result_captured_at,
		       pq.expected_rows_affected, pq.auto_seed, COALESCE(pq.dml_kind, ''), COALESCE(pq.seed_sql, ''), pq.created_at, pq.updated_at
		FROM app.practice_questions pq
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		ORDER BY pq.id ASC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	questions := []models.PracticeQuestion{}
	for rows.Next() {
		var q models.PracticeQuestion
		err := rows.Scan(
			&q.ID, &q.CreatedBy, &q.Category, &q.CategoryID, &q.CategoryName, &q.CategorySchema, &q.DualSchema, &q.CategoryPreviewSchema,
			&q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
			&q.StrictOrdering, &q.StrictAttrs, &q.StrictColumnOrder, &q.ExpectedResult, &q.ExpectedColumns,
			&q.ExpectedResultStatus, &q.CaptureError, &q.Title, &q.ExpectedResultCapturedAt,
			&q.ExpectedRowsAffected, &q.AutoSeed, &q.DMLKind, &q.SeedSQL, &q.CreatedAt, &q.UpdatedAt)
		if err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

func (r *PostgresRepository) GetPracticeQuestion(ctx context.Context, id int) (*models.PracticeQuestion, error) {
	query := `
		SELECT pq.id, pq.created_by, pq.category, COALESCE(pq.category_id, 0),
		       COALESCE(pc.name, ''), COALESCE(pc.schema_name, ''), pq.dual_schema, COALESCE(pc.preview_schema_name, ''),
		       pq.target_table, pq.correct_sql, pq.question_type, pq.requirement, pq.points,
		       pq.strict_ordering, pq.strict_attrs, pq.strict_column_order, pq.expected_result, pq.expected_columns,
		       pq.expected_result_status, pq.capture_error, COALESCE(pq.title, ''), pq.expected_result_captured_at,
		       pq.expected_rows_affected, pq.auto_seed, COALESCE(pq.dml_kind, ''), COALESCE(pq.seed_sql, ''), pq.created_at, pq.updated_at
		FROM app.practice_questions pq
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		WHERE pq.id=$1`

	var q models.PracticeQuestion
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.CreatedBy, &q.Category, &q.CategoryID, &q.CategoryName, &q.CategorySchema, &q.DualSchema, &q.CategoryPreviewSchema,
		&q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
		&q.StrictOrdering, &q.StrictAttrs, &q.StrictColumnOrder, &q.ExpectedResult, &q.ExpectedColumns,
		&q.ExpectedResultStatus, &q.CaptureError, &q.Title, &q.ExpectedResultCapturedAt,
		&q.ExpectedRowsAffected, &q.AutoSeed, &q.DMLKind, &q.SeedSQL, &q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

func (r *PostgresRepository) DeletePracticeQuestion(ctx context.Context, id int) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM app.practice_questions WHERE id=$1`, id)
	return err
}

// ─── Practice Attempts ────────────────────────────────────────────────────────

func (r *PostgresRepository) CreatePracticeAttempt(ctx context.Context, a *models.PracticeAttempt) error {
	query := `
		INSERT INTO app.practice_attempts (student_id, practice_question_id, sql_text, is_correct, validation_details)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, submitted_at`

	return r.pool.QueryRow(ctx, query,
		a.StudentID, a.PracticeQuestionID, a.SQLText, a.IsCorrect, a.ValidationDetails,
	).Scan(&a.ID, &a.SubmittedAt)
}

func (r *PostgresRepository) ListPracticeAttempts(ctx context.Context, studentID int) ([]models.PracticeAttempt, error) {
	query := `
		SELECT id, student_id, practice_question_id, sql_text, is_correct, validation_details, submitted_at
		FROM app.practice_attempts WHERE student_id=$1 ORDER BY submitted_at DESC`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	attempts := []models.PracticeAttempt{}
	for rows.Next() {
		var a models.PracticeAttempt
		err := rows.Scan(
			&a.ID, &a.StudentID, &a.PracticeQuestionID, &a.SQLText, &a.IsCorrect, &a.ValidationDetails, &a.SubmittedAt)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	return attempts, rows.Err()
}
