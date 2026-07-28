package repository

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// examCategoryNameUniqueConstraint is the name of the UNIQUE (exam_id, name)
// constraint on app.exam_categories (see the CREATE TABLE in
// InitializeSystemTables). Used to recognise a duplicate-name insert/rename.
const examCategoryNameUniqueConstraint = "exam_categories_exam_id_name_key"

// migrateExamCategories implements ARCH-026 (4): it auto-adopts each existing
// exam's CURRENT curriculum schema into one "General" category and attaches
// that exam's still-unattached questions to it. It performs exactly the two
// statements below and NO schema DDL whatsoever — it never calls EnsureSchema,
// GrantSchemaPermissions, ensureExamCurriculumSchema, CREATE/DROP/ALTER
// SCHEMA, or ensureTablesInSchema. Because both copied schema-name values are
// read verbatim from the exam row, a pre-existing exam grades byte-
// identically after this migration runs: no schema is created, copied,
// renamed or dropped. Fully idempotent / re-runnable.
func (r *PostgresRepository) migrateExamCategories(ctx context.Context) error {
	// 4a. One "General" category per exam that has no category at all. The
	// NOT EXISTS guard is per-exam, so a re-run is a no-op and an exam whose
	// lecturer later deleted "General" and created their own categories never
	// gets "General" resurrected.
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO app.exam_categories (exam_id, name, schema_name, preview_schema_name, introduction, created_by, created_at, updated_at)
		SELECT e.id, 'General',
		       COALESCE(NULLIF(e.curriculum_schema, ''), 'public'),
		       COALESCE(e.preview_curriculum_schema, ''),
		       '', e.created_by, now(), now()
		FROM app.examinations e
		WHERE NOT EXISTS (SELECT 1 FROM app.exam_categories c WHERE c.exam_id = e.id)`); err != nil {
		return fmt.Errorf("backfill General exam categories: %w", err)
	}

	// 4b. Attach still-unattached questions of that exam to its General
	// category. Two guard predicates keep this backfill behaviourally inert
	// forever, not just on first run: `schema_name <> 'exam_cat_'||id` stops it
	// the moment a lecturer imports a real database into General (at which
	// point General owns its own schema and auto-attaching a new question
	// would change its grading schema); the `COUNT(*) = 1` predicate stops it
	// the moment the exam has more than one category (auto-picking one of
	// several would be a guess).
	if _, err := r.pool.Exec(ctx, `
		UPDATE app.questions q
		SET category_id = c.id
		FROM app.exam_categories c
		WHERE q.category_id IS NULL
		  AND q.exam_id IS NOT NULL
		  AND c.exam_id = q.exam_id
		  AND c.name = 'General'
		  AND c.schema_name <> ('exam_cat_' || c.id::text)
		  AND (SELECT COUNT(*) FROM app.exam_categories c2 WHERE c2.exam_id = q.exam_id) = 1`); err != nil {
		return fmt.Errorf("backfill question category_id: %w", err)
	}

	return nil
}

// ExamCategorySchemaName returns the canonical schema name owned by an exam
// category's grading database. Mirrors ExamCurriculumSchemaName /
// PracticeCategorySchemaName.
func ExamCategorySchemaName(categoryID int) string {
	return fmt.Sprintf("exam_cat_%d", categoryID)
}

// ExamCategoryPreviewSchemaName returns the canonical decoy schema name for an
// exam category's preview. Mirrors ExamPreviewCurriculumSchemaName.
func ExamCategoryPreviewSchemaName(categoryID int) string {
	return fmt.Sprintf("exam_cat_%d_preview", categoryID)
}

// ExamCategorySandboxName returns the canonical schema name for an exam
// category's writable DML sandbox. Mirrors ExamSandboxName.
func ExamCategorySandboxName(categoryID int) string {
	return fmt.Sprintf("sandbox_exam_cat_%d", categoryID)
}

// ExamCategoryPreviewSandboxName returns the canonical schema name for an exam
// category's decoy DML sandbox. Mirrors ExamPreviewSandboxName.
func ExamCategoryPreviewSandboxName(categoryID int) string {
	return fmt.Sprintf("sandbox_exam_cat_%d_preview", categoryID)
}

// ExamCategoryOwnsSchema reports whether a category owns its canonical schema,
// as opposed to being a migrated/legacy-adopted category whose schema_name is
// still a copy of the exam's own curriculum schema (e.g. "public" or
// "curriculum_exam_7"). This is the single discriminator ARCH-028's grading
// resolver keys off to choose between the per-category schema/sandbox and the
// exam-level fallback.
func ExamCategoryOwnsSchema(categoryID int, schemaName string) bool {
	return schemaName == ExamCategorySchemaName(categoryID)
}

// ensureExamCategorySchema creates the schema owned by an exam category's
// grading database (if missing) and grants read-only access to
// exam_student_readonly — copied verbatim from ensureExamCurriculumSchema
// (postgres_schema.go), NOT from the practice-category grant posture.
// exam_student gets NO DML here: exam DML runs in the category's own sandbox
// (ensureExamCategorySandbox), exactly as for the per-exam curriculum — the
// reference database stays a frozen, read-only source. Idempotent: safe to
// call repeatedly (e.g. before and after import).
func (r *PostgresRepository) ensureExamCategorySchema(ctx context.Context, categoryID int) error {
	schema := ExamCategorySchemaName(categoryID)
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	safeSchema := pgx.Identifier{schema}.Sanitize()
	readonlyQueries := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO exam_student_readonly", safeSchema),
		fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO exam_student_readonly", safeSchema),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO exam_student_readonly", safeSchema),
	}
	for _, q := range readonlyQueries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("failed to grant readonly permissions on %s: %w", schema, err)
		}
	}
	return nil
}

// ensureExamCategorySandbox creates the writable DML sandbox schema for an
// exam category (if missing) and clones every table from the category's own
// grading schema into it once. Mirrors InitializeExamSandbox, except: the
// clone source is always the CATEGORY's own schema (ExamCategorySchemaName),
// never the exam's curriculum_schema; and, unlike InitializeExamSandbox, this
// never writes app.exam_sandboxes — that table's primary key is exam_id and is
// owned exclusively by the real per-exam sandbox row (same reasoning as
// InitializeExamPreviewSandbox for the preview family). Existence is tracked
// purely by SchemaHasTables (see EnsureExamCategorySchemasReady). The call is
// idempotent: missing tables are filled in, existing ones left untouched.
func (r *PostgresRepository) ensureExamCategorySandbox(ctx context.Context, categoryID int) error {
	schema := ExamCategorySandboxName(categoryID)
	categorySchema := ExamCategorySchemaName(categoryID)

	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	// Grant before cloning so ALTER DEFAULT PRIVILEGES applies to the new tables.
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
		return err
	}
	return r.ensureTablesInSchema(ctx, schema, categorySchema)
}

// ensureExamCategoryPreviewSchema creates the per-category decoy preview
// schema (if missing) and grants read-only access to the ARCH-020
// exam_student_preview_readonly role (allowDML=false), mirroring
// ensureExamPreviewCurriculumSchema. exam_student_preview does NOT get DML
// here: a dual-DML preview runs against the preview sandbox
// (ensureExamCategoryPreviewSandbox), not this decoy reference schema, exactly
// like the real (non-preview) category schema. exam_student /
// exam_student_readonly are explicitly REVOKEd (grantPreviewSchemaPermissions),
// so this decoy can never disclose the category's real grading schema via a
// schema-qualified read from inside a dual preview.
func (r *PostgresRepository) ensureExamCategoryPreviewSchema(ctx context.Context, categoryID int) error {
	schema := ExamCategoryPreviewSchemaName(categoryID)
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	return r.grantPreviewSchemaPermissions(ctx, schema, false)
}

// ensureExamCategoryPreviewSandbox creates the category's decoy DML sandbox
// (if missing) and clones every table from the category's decoy preview
// schema into it once. Mirrors InitializeExamPreviewSandbox: the clone source
// is always the category's OWN preview_schema_name column, never defaulted
// and never the real category schema — an un-provisioned preview sandbox must
// never silently clone the real grading database. Returns an error when the
// category has no decoy imported yet, rather than cloning the real schema.
func (r *PostgresRepository) ensureExamCategoryPreviewSandbox(ctx context.Context, categoryID int) error {
	schema := ExamCategoryPreviewSandboxName(categoryID)

	var previewSchema string
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(preview_schema_name, ''), '') FROM app.exam_categories WHERE id = $1`,
		categoryID).Scan(&previewSchema); err != nil {
		return err
	}
	if previewSchema == "" {
		return fmt.Errorf("no decoy schema uploaded for this exam category")
	}

	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	// Grant before cloning so ALTER DEFAULT PRIVILEGES applies to the new
	// tables. Students write to the preview sandbox during a dual-DML preview
	// under the ARCH-020 exam_student_preview role (allowDML=true) — never the
	// real exam_student/exam_student_readonly roles, which are explicitly
	// REVOKEd here (grantPreviewSchemaPermissions).
	if err := r.grantPreviewSchemaPermissions(ctx, schema, true); err != nil {
		return err
	}
	return r.ensureTablesInSchema(ctx, schema, previewSchema)
}

// EnsureExamCategorySchemasReady lazily provisions the writable DML sandbox
// for every category of examID that (a) owns its schema (ExamCategoryOwnsSchema)
// and (b) has at least one DML question attached, mirroring
// EnsureExamSandboxReady/examHasDMLQuestions at category granularity. Called
// from SetExaminationActive next to the existing InitializeExamSandbox call,
// and also from QuizController.GetExamQuestions (quiz_controller.go) — that
// ARCH-028 call site was wired by TODO-073, alongside every exam-category
// route registration in cmd/main.go. Idempotent and cheap:
// ensureExamCategorySandbox no-ops once the sandbox already has all the
// category's tables.
func (r *PostgresRepository) EnsureExamCategorySchemasReady(ctx context.Context, examID int) error {
	rows, err := r.pool.Query(ctx,
		`SELECT id, schema_name FROM app.exam_categories WHERE exam_id = $1`, examID)
	if err != nil {
		return err
	}
	type category struct {
		id     int
		schema string
	}
	var cats []category
	for rows.Next() {
		var c category
		if err := rows.Scan(&c.id, &c.schema); err != nil {
			rows.Close()
			return err
		}
		cats = append(cats, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range cats {
		if !ExamCategoryOwnsSchema(c.id, c.schema) {
			continue
		}
		var hasDML int
		if err := r.pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM app.questions WHERE category_id = $1 AND question_type = 'DML'",
			c.id).Scan(&hasDML); err != nil {
			return err
		}
		if hasDML == 0 {
			continue
		}
		if err := r.ensureExamCategorySandbox(ctx, c.id); err != nil {
			return err
		}
	}
	return nil
}

// EnsureExamCategoryPreviewSandboxReady lazily provisions a category's decoy
// DML sandbox on first use. Mirrors EnsureExamPreviewSandboxReady: idempotent,
// a second call against an already-populated sandbox is a no-op.
func (r *PostgresRepository) EnsureExamCategoryPreviewSandboxReady(ctx context.Context, categoryID int) error {
	hasTables, err := r.SchemaHasTables(ctx, ExamCategoryPreviewSandboxName(categoryID))
	if err != nil {
		return err
	}
	if hasTables {
		return nil
	}
	return r.ensureExamCategoryPreviewSandbox(ctx, categoryID)
}

// isExamCategoryNameConflict reports whether err is the Postgres unique-
// violation raised by the (exam_id, name) constraint.
func isExamCategoryNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == examCategoryNameUniqueConstraint
}

// CreateExamCategory creates a new category owning a fresh empty schema under
// the given exam. Mirrors createCategoryWithSchema + CreatePracticeCategory
// (postgres_practice.go): INSERT with schema_name=” RETURNING id, then
// UPDATE schema_name = ExamCategorySchemaName(id) in the SAME transaction (the
// id is needed to derive the canonical name), then ensureExamCategorySchema
// AFTER commit so the id is final. A duplicate (exam_id, name) is mapped to
// ErrExamCategoryNameTaken.
func (r *PostgresRepository) CreateExamCategory(ctx context.Context, examID int, name string, createdBy *int) (*models.ExamCategory, error) {
	var id int
	err := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO app.exam_categories (exam_id, name, created_by, schema_name)
			 VALUES ($1, $2, $3, '') RETURNING id`, examID, name, createdBy).Scan(&id); err != nil {
			if isExamCategoryNameConflict(err) {
				return ErrExamCategoryNameTaken
			}
			return err
		}
		schema := ExamCategorySchemaName(id)
		_, err := tx.Exec(ctx,
			`UPDATE app.exam_categories SET schema_name = $1, updated_at = now() WHERE id = $2`,
			schema, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := r.ensureExamCategorySchema(ctx, id); err != nil {
		return nil, err
	}
	return r.GetExamCategory(ctx, examID, id)
}

// ListExamCategories returns every category of an exam with a live
// question_count. ARCH-026 (3): there is no sort_order column — presentation
// order is lecturer-controlled via naming, so every read path orders by
// name ASC, id ASC (id as a total-order tie-break) for deterministic output.
func (r *PostgresRepository) ListExamCategories(ctx context.Context, examID int) ([]models.ExamCategory, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.exam_id, c.name, c.schema_name, c.preview_schema_name,
		       c.introduction, COUNT(q.id) AS question_count, c.created_by,
		       c.created_at, c.updated_at
		FROM app.exam_categories c
		LEFT JOIN app.questions q ON q.category_id = c.id
		WHERE c.exam_id = $1
		GROUP BY c.id
		ORDER BY c.name ASC, c.id ASC`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cats := []models.ExamCategory{}
	for rows.Next() {
		var c models.ExamCategory
		var createdBy *int
		if err := rows.Scan(&c.ID, &c.ExamID, &c.Name, &c.SchemaName, &c.PreviewSchemaName,
			&c.Introduction, &c.QuestionCount, &createdBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if createdBy != nil {
			uid := models.UserID(*createdBy)
			c.CreatedBy = &uid
		}
		c.HasPreviewSchema = c.PreviewSchemaName != ""
		c.OwnsSchema = ExamCategoryOwnsSchema(int(c.ID), c.SchemaName)
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

// GetExamCategory fetches one category scoped to its owning exam — the
// WHERE c.id=$2 AND c.exam_id=$1 predicate IS the ownership check (ARCH-029
// (1)): a legitimate token for a category belonging to a DIFFERENT exam still
// returns ErrExamCategoryNotFound, exactly like an id that does not exist.
func (r *PostgresRepository) GetExamCategory(ctx context.Context, examID, categoryID int) (*models.ExamCategory, error) {
	var c models.ExamCategory
	var createdBy *int
	err := r.pool.QueryRow(ctx, `
		SELECT c.id, c.exam_id, c.name, c.schema_name, c.preview_schema_name,
		       c.introduction, COUNT(q.id) AS question_count, c.created_by,
		       c.created_at, c.updated_at
		FROM app.exam_categories c
		LEFT JOIN app.questions q ON q.category_id = c.id
		WHERE c.id = $2 AND c.exam_id = $1
		GROUP BY c.id`, examID, categoryID).Scan(
		&c.ID, &c.ExamID, &c.Name, &c.SchemaName, &c.PreviewSchemaName,
		&c.Introduction, &c.QuestionCount, &createdBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrExamCategoryNotFound
		}
		return nil, err
	}
	if createdBy != nil {
		uid := models.UserID(*createdBy)
		c.CreatedBy = &uid
	}
	c.HasPreviewSchema = c.PreviewSchemaName != ""
	c.OwnsSchema = ExamCategoryOwnsSchema(int(c.ID), c.SchemaName)
	return &c, nil
}

// RenameExamCategory updates only the display name; schema_name is NEVER
// touched here (mirrors RenamePracticeCategory), so imported data and the
// exam_cat_<id> mapping stay stable across a rename.
func (r *PostgresRepository) RenameExamCategory(ctx context.Context, examID, categoryID int, name string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.exam_categories SET name = $1, updated_at = now() WHERE id = $2 AND exam_id = $3`,
		name, categoryID, examID)
	if err != nil {
		if isExamCategoryNameConflict(err) {
			return ErrExamCategoryNameTaken
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrExamCategoryNotFound
	}
	return nil
}

// UpdateExamCategoryIntroduction persists a new description text for a
// category. Returns ErrExamCategoryNotFound if the (exam, category) pair does
// not exist.
func (r *PostgresRepository) UpdateExamCategoryIntroduction(ctx context.Context, examID, categoryID int, introduction string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.exam_categories SET introduction = $1, updated_at = now() WHERE id = $2 AND exam_id = $3`,
		introduction, categoryID, examID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrExamCategoryNotFound
	}
	return nil
}

// DeleteExamCategory removes a category, but ONLY when it has zero attached
// questions. Per ARCH-036 (overriding ARCH-029's original force-detach text),
// there is NO force parameter on this function: detaching a question would
// silently re-point it at the exam-level curriculum_schema, so it would grade
// against different data than it was authored and captured against. The
// lecturer must reassign or delete the attached questions first.
//
// Race-free by construction (closes a TOCTOU the validator caught): the
// ownership check, the row lock, and the question COUNT all happen INSIDE the
// same transaction, in that order, on tx rather than r.pool. The initial
// `SELECT ... FOR UPDATE` both re-does the (id, exam_id) ownership check
// GetExamCategory used to do outside the transaction AND takes an exclusive
// row lock on the category — this serialises against a concurrent question
// INSERT that references this category_id, because enforcing that foreign key
// requires the inserting transaction to hold a FOR KEY SHARE lock on this same
// row for as long as its INSERT is uncommitted, and FOR KEY SHARE conflicts
// with FOR UPDATE (verified directly against a live PostgreSQL instance: a
// session holding an uncommitted FK-referencing INSERT blocks a concurrent
// FOR UPDATE on the row it references, and symmetrically a FOR UPDATE holder
// blocks a concurrent FK-referencing INSERT, in both interleavings). So by the
// time this transaction's COUNT runs, either the concurrent attach has fully
// committed (and COUNT sees it, so this returns ErrExamCategoryHasQuestions
// and rolls back untouched) or it is blocked behind this transaction and will
// fail its own FK check once this commits the DELETE (the category row is
// gone). Either way, a question can never end up with category_id silently
// SET NULL by this delete — the one outcome ARCH-036 forbids.
//
// Once the row is deleted, all four canonical schemas are dropped by
// CANONICAL NAME (never by the row's stored schema_name — a legacy-adopted
// category's schema_name may literally be "public"), each guarded by
// acquireSchemaRebuildLockTx and warn-and-continued, mirroring
// DeletePracticeCategory's cleanup shape (postgres_practice.go) minus its
// force-cascade branch.
func (r *PostgresRepository) DeleteExamCategory(ctx context.Context, examID, categoryID int) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		var exists int
		if err := tx.QueryRow(ctx,
			"SELECT 1 FROM app.exam_categories WHERE id = $1 AND exam_id = $2 FOR UPDATE",
			categoryID, examID).Scan(&exists); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrExamCategoryNotFound // also the ownership check
			}
			return err
		}

		var questionCount int
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM app.questions WHERE category_id = $1", categoryID).Scan(&questionCount); err != nil {
			return err
		}
		if questionCount > 0 {
			return fmt.Errorf("%w: %d question(s) still attached to this category", ErrExamCategoryHasQuestions, questionCount)
		}

		if _, err := tx.Exec(ctx, "DELETE FROM app.exam_categories WHERE id = $1", categoryID); err != nil {
			return err
		}

		for _, schema := range []string{
			ExamCategorySchemaName(categoryID),
			ExamCategoryPreviewSchemaName(categoryID),
			ExamCategorySandboxName(categoryID),
			ExamCategoryPreviewSandboxName(categoryID),
		} {
			if err := acquireSchemaRebuildLockTx(ctx, tx, schema); err != nil {
				fmt.Printf("Warning: failed to acquire schema rebuild lock for %s during exam category delete: %v\n", schema, err)
				continue
			}
			safe := pgx.Identifier{schema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				fmt.Printf("Warning: failed to drop exam category schema %s during delete: %v\n", schema, err)
			}
		}
		return nil
	})
}

// ImportSchemaIntoExamCategory loads a SQL script into an exam category's
// owned schema (exam_cat_<id>). Mirrors ImportSchemaIntoCategory /
// ImportSchemaIntoExamCurriculum: existence+ownership via GetExamCategory,
// the same exam-active freeze as the exam curriculum import
// (ErrExamActive), the same validateNoSchemaQualifiedNames denylist, the same
// acquireSchemaRebuildLockTx-guarded check-and-drop-if-confirmReplace
// transaction (ErrExamCategoryNotEmpty unless confirmReplace), the same
// SET LOCAL search_path to the target schema only (no public fallback), and
// the same app.schema_import_audit insert. On success, schema_name is stamped
// to ExamCategorySchemaName(id) — this is what flips a migrated General from
// sharing the exam's schema (often literally "public") to owning its own —
// and the category's now-stale DML sandbox is dropped so the next activation
// re-clones from the freshly imported data.
func (r *PostgresRepository) ImportSchemaIntoExamCategory(ctx context.Context, examID, categoryID int, sql string, importedBy int, confirmReplace bool) error {
	if _, err := r.GetExamCategory(ctx, examID, categoryID); err != nil {
		return err // ErrExamCategoryNotFound (also the ownership check)
	}

	exam, err := r.GetExamByID(ctx, examID)
	if err != nil {
		return err
	}
	if ExaminationEffectiveState(exam).ActiveNow {
		return fmt.Errorf("%w: cannot import a category schema while the examination is active", ErrExamActive)
	}

	if err := validateNoSchemaQualifiedNames(sql); err != nil {
		return err
	}

	schema := ExamCategorySchemaName(categoryID)

	// Check-and-drop runs inside a transaction holding the schema's exclusive
	// rebuild lock (ARCH-013), so an evaluation in flight against this
	// category's schema is never dropped out from under it.
	var tableCount int
	if err := func() error {
		conn, err := r.pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()

		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		if err := acquireSchemaRebuildLockTx(ctx, tx, schema); err != nil {
			return fmt.Errorf("failed to acquire schema rebuild lock: %w", err)
		}

		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", schema).Scan(&tableCount); err != nil {
			return err
		}
		if tableCount > 0 {
			if !confirmReplace {
				return ErrExamCategoryNotEmpty
			}
			safe := pgx.Identifier{schema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				return fmt.Errorf("failed to drop exam category schema for replace: %w", err)
			}
		}
		return tx.Commit(ctx)
	}(); err != nil {
		return err
	}

	// (Re)create the empty schema with role grants before importing.
	if err := r.ensureExamCategorySchema(ctx, categoryID); err != nil {
		return err
	}

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	safe := pgx.Identifier{schema}.Sanitize()
	// schema only (no ", public"): unqualified objects must resolve to the
	// category schema and never fall back to the exam curriculum.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("import failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Grant the student roles on the newly created tables (default privileges
	// only cover future tables created by the granting role, so grant explicitly).
	if err := r.ensureExamCategorySchema(ctx, categoryID); err != nil {
		return err
	}

	// Stamp schema_name so a migrated General flips from sharing the exam's
	// schema to owning its own.
	if _, err := r.pool.Exec(ctx,
		`UPDATE app.exam_categories SET schema_name = $1, updated_at = now() WHERE id = $2`,
		schema, categoryID); err != nil {
		return fmt.Errorf("failed to update exam_categories.schema_name: %w", err)
	}

	// Invalidate the stale DML sandbox so the next activation re-clones from
	// the newly imported data.
	sandboxSchema := ExamCategorySandboxName(categoryID)
	safeSandbox := pgx.Identifier{sandboxSchema}.Sanitize()
	if _, err := r.pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safeSandbox)); err != nil {
		fmt.Printf("Warning: failed to drop stale exam category sandbox %s: %v\n", sandboxSchema, err)
	}

	if _, err := r.pool.Exec(ctx,
		"INSERT INTO app.schema_import_audit (imported_by, byte_size, table_count) VALUES ($1, $2, $3)",
		importedBy, len(sql), tableCount); err != nil {
		fmt.Printf("Warning: failed to write exam category import audit row: %v\n", err)
	}
	return nil
}

// ImportSchemaIntoExamCategoryPreview loads a SQL script into an exam
// category's decoy preview schema (exam_cat_<id>_preview). Mirrors
// ImportSchemaIntoExamCategory byte-for-byte except for the target schema and
// grant posture (ensureExamCategoryPreviewSchema instead of
// ensureExamCategorySchema), and additionally returns a non-blocking
// parityWarning comparing the decoy against the category's real grading
// schema — a comparison failure never fails the import, it is logged and an
// empty string is returned instead.
func (r *PostgresRepository) ImportSchemaIntoExamCategoryPreview(ctx context.Context, examID, categoryID int, sql string, importedBy int, confirmReplace bool) (parityWarning string, err error) {
	cat, err := r.GetExamCategory(ctx, examID, categoryID)
	if err != nil {
		return "", err // ErrExamCategoryNotFound (also the ownership check)
	}

	exam, err := r.GetExamByID(ctx, examID)
	if err != nil {
		return "", err
	}
	if ExaminationEffectiveState(exam).ActiveNow {
		return "", fmt.Errorf("%w: cannot import a category decoy schema while the examination is active", ErrExamActive)
	}

	if err := validateNoSchemaQualifiedNames(sql); err != nil {
		return "", err
	}

	schema := ExamCategoryPreviewSchemaName(categoryID)

	var tableCount int
	if err := func() error {
		conn, err := r.pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()

		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)

		if err := acquireSchemaRebuildLockTx(ctx, tx, schema); err != nil {
			return fmt.Errorf("failed to acquire schema rebuild lock: %w", err)
		}

		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", schema).Scan(&tableCount); err != nil {
			return err
		}
		if tableCount > 0 {
			if !confirmReplace {
				return ErrExamCategoryNotEmpty
			}
			safe := pgx.Identifier{schema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				return fmt.Errorf("failed to drop exam category preview schema for replace: %w", err)
			}
		}
		return tx.Commit(ctx)
	}(); err != nil {
		return "", err
	}

	// (Re)create the empty decoy schema with role grants before importing.
	if err := r.ensureExamCategoryPreviewSchema(ctx, categoryID); err != nil {
		return "", err
	}

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	safe := pgx.Identifier{schema}.Sanitize()
	// schema only (no ", public"): unqualified objects must resolve to the
	// decoy preview schema and never fall back to the exam curriculum.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return "", fmt.Errorf("import failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	// Grant the student roles on the newly created tables.
	if err := r.ensureExamCategoryPreviewSchema(ctx, categoryID); err != nil {
		return "", err
	}

	if _, err := r.pool.Exec(ctx,
		`UPDATE app.exam_categories SET preview_schema_name = $1, updated_at = now() WHERE id = $2`,
		schema, categoryID); err != nil {
		return "", fmt.Errorf("failed to update exam_categories.preview_schema_name: %w", err)
	}

	// Invalidate the stale decoy DML sandbox so the next preview re-clones
	// from the freshly imported decoy data.
	sandboxSchema := ExamCategoryPreviewSandboxName(categoryID)
	safeSandbox := pgx.Identifier{sandboxSchema}.Sanitize()
	if _, err := r.pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safeSandbox)); err != nil {
		fmt.Printf("Warning: failed to drop stale exam category preview sandbox %s: %v\n", sandboxSchema, err)
	}

	if _, err := r.pool.Exec(ctx,
		"INSERT INTO app.schema_import_audit (imported_by, byte_size, table_count) VALUES ($1, $2, $3)",
		importedBy, len(sql), tableCount); err != nil {
		fmt.Printf("Warning: failed to write exam category preview import audit row: %v\n", err)
	}

	realSchema := cat.SchemaName
	if realSchema == "" {
		realSchema = ExamCategorySchemaName(categoryID)
	}
	parityWarning, cmpErr := r.compareSchemaStructures(ctx, schema, realSchema)
	if cmpErr != nil {
		log.Printf("Warning: preview/grading structure comparison failed for exam category %d: %v", categoryID, cmpErr)
		return "", nil
	}
	return parityWarning, nil
}
