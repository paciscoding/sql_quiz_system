package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
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
		       COUNT(q.id) AS question_count, c.introduction
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
			&c.CreatedAt, &c.UpdatedAt, &c.QuestionCount, &c.Introduction); err != nil {
			return nil, err
		}
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
// Uncategorized fallback category can never be deleted.
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
			safe := pgx.Identifier{schema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				return err
			}
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

// GetPracticeCategory returns the full row for a single category including its
// live question_count and introduction. Returns ErrCategoryNotFound if missing.
func (r *PostgresRepository) GetPracticeCategory(ctx context.Context, id int) (*models.PracticeCategory, error) {
	var cat models.PracticeCategory
	err := r.pool.QueryRow(ctx, `
		SELECT c.id, c.name, c.schema_name, c.created_by, c.created_at, c.updated_at,
		       COUNT(q.id) AS question_count, c.introduction
		FROM app.practice_categories c
		LEFT JOIN app.practice_questions q ON q.category_id = c.id
		WHERE c.id = $1
		GROUP BY c.id`, id).Scan(
		&cat.ID, &cat.Name, &cat.SchemaName, &cat.CreatedBy, &cat.CreatedAt, &cat.UpdatedAt,
		&cat.QuestionCount, &cat.Introduction,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCategoryNotFound
		}
		return nil, err
	}
	return &cat, nil
}

func (r *PostgresRepository) CreatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	// Repair typographic quotes (e.g. ‘…’ → '…') before capture and persistence,
	// so pasted correct SQL doesn't fail with "column ... does not exist".
	q.CorrectSQL = normalizeSmartQuotes(q.CorrectSQL)
	// Resolve the category's owned schema; the correct-result capture and all
	// student submissions for this question run against it (not public).
	schema, err := r.GetPracticeCategorySchema(ctx, int(q.CategoryID))
	if err != nil {
		return err
	}

	// Phase-0: Correct Result Capture (always rolled back)
	status := "captured"
	var captureErr string
	var expectedResult, expectedCols []byte

	err = r.captureExpectedResult(ctx, schema, q, &status, &captureErr, &expectedResult, &expectedCols)
	if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
		return err
	}

	query := `
		INSERT INTO app.practice_questions (
			created_by, category_id, target_table, correct_sql, question_type, requirement,
			points, strict_ordering, strict_attrs, strict_column_order,
			expected_result, expected_columns, expected_result_status,
			capture_error, title, expected_result_captured_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW(), NOW(), NOW())
		RETURNING id, created_at, updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CreatedBy, q.CategoryID, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs, q.StrictColumnOrder,
		expectedResult, expectedCols, status, captureErr, q.Title,
	).Scan(&q.ID, &q.CreatedAt, &q.UpdatedAt)
}

// captureExpectedResult runs the lecturer's correct SQL inside a rolled-back
// transaction scoped to the category schema, populating status/captureErr and
// the marshalled expected result + columns. Returns errCaptureRollback on
// success (so WithTransaction discards the lecturer's DML side effects) or
// errCaptureFailed when the SQL aborts.
func (r *PostgresRepository) captureExpectedResult(ctx context.Context, schema string, q *models.PracticeQuestion, status *string, captureErr *string, expectedResult *[]byte, expectedCols *[]byte) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		safeSchema := pgx.Identifier{schema}.Sanitize()
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
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
			_, err = tx.Exec(ctx, q.CorrectSQL)
			if err == nil {
				results, orderedCols, err = r.queryWithTxCols(ctx, tx, schema, fmt.Sprintf("SELECT * FROM %s", q.TargetTable))
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

func (r *PostgresRepository) UpdatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	// Repair typographic quotes before capture and persistence (see Create).
	q.CorrectSQL = normalizeSmartQuotes(q.CorrectSQL)
	schema, err := r.GetPracticeCategorySchema(ctx, int(q.CategoryID))
	if err != nil {
		return err
	}

	// Phase-0: Correct Result Capture (always rolled back)
	status := "captured"
	var captureErr string
	var expectedResult, expectedCols []byte

	err = r.captureExpectedResult(ctx, schema, q, &status, &captureErr, &expectedResult, &expectedCols)
	if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
		return err
	}

	query := `
		UPDATE app.practice_questions
		SET category_id=$1, target_table=$2, correct_sql=$3, question_type=$4, requirement=$5,
		    points=$6, strict_ordering=$7, strict_attrs=$8, strict_column_order=$9,
		    expected_result=$10, expected_columns=$11,
		    expected_result_status=$12, capture_error=$13, title=$14,
		    expected_result_captured_at=NOW(), updated_at=NOW()
		WHERE id=$15
		RETURNING updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CategoryID, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs, q.StrictColumnOrder,
		expectedResult, expectedCols, status, captureErr, q.Title, q.ID,
	).Scan(&q.UpdatedAt)
}

func (r *PostgresRepository) ListPracticeQuestions(ctx context.Context) ([]models.PracticeQuestion, error) {
	query := `
		SELECT pq.id, pq.created_by, pq.category, COALESCE(pq.category_id, 0),
		       COALESCE(pc.name, ''), COALESCE(pc.schema_name, ''),
		       pq.target_table, pq.correct_sql, pq.question_type, pq.requirement, pq.points,
		       pq.strict_ordering, pq.strict_attrs, pq.strict_column_order, pq.expected_result, pq.expected_columns,
		       pq.expected_result_status, pq.capture_error, COALESCE(pq.title, ''), pq.expected_result_captured_at,
		       pq.created_at, pq.updated_at
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
			&q.ID, &q.CreatedBy, &q.Category, &q.CategoryID, &q.CategoryName, &q.CategorySchema,
			&q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
			&q.StrictOrdering, &q.StrictAttrs, &q.StrictColumnOrder, &q.ExpectedResult, &q.ExpectedColumns,
			&q.ExpectedResultStatus, &q.CaptureError, &q.Title, &q.ExpectedResultCapturedAt,
			&q.CreatedAt, &q.UpdatedAt)
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
		       COALESCE(pc.name, ''), COALESCE(pc.schema_name, ''),
		       pq.target_table, pq.correct_sql, pq.question_type, pq.requirement, pq.points,
		       pq.strict_ordering, pq.strict_attrs, pq.strict_column_order, pq.expected_result, pq.expected_columns,
		       pq.expected_result_status, pq.capture_error, COALESCE(pq.title, ''), pq.expected_result_captured_at,
		       pq.created_at, pq.updated_at
		FROM app.practice_questions pq
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		WHERE pq.id=$1`

	var q models.PracticeQuestion
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.CreatedBy, &q.Category, &q.CategoryID, &q.CategoryName, &q.CategorySchema,
		&q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
		&q.StrictOrdering, &q.StrictAttrs, &q.StrictColumnOrder, &q.ExpectedResult, &q.ExpectedColumns,
		&q.ExpectedResultStatus, &q.CaptureError, &q.Title, &q.ExpectedResultCapturedAt,
		&q.CreatedAt, &q.UpdatedAt)
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
