package repository

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// Dual-schema grading (ARCH-002): this file owns all provisioning for the
// decoy schema "A" that a lecturer optionally uploads alongside a practice
// category or exam curriculum. A dual-schema question previews on A while
// grading always stays on the real schema "B" (practice_cat_<id> /
// curriculum_exam_<id> / sandbox_exam_<id>, unchanged). A is same-structure,
// different-data, display-only: it is never captured and never graded. Every
// function here mirrors its non-preview equivalent in postgres_schema.go —
// same denylist, same lock discipline, same grant posture — so the two code
// paths stay behaviourally identical apart from the schema they target.

// ARCH-020 (GAP 3): preview-only database roles. exam_student_readonly /
// exam_student hold USAGE+SELECT (and, for exam_student, full DML) on EVERY
// real curriculum/category schema, so running a dual preview under those
// roles would let a schema-qualified query against the real grading schema
// (`SELECT * FROM curriculum_exam_7.t`) read it in full — the dual-schema
// feature would be cosmetic only. These two roles instead hold privileges
// ONLY on the decoy schemas granted via grantPreviewSchemaPermissions below,
// so the same read fails at the engine with insufficient privilege no matter
// how the student's SQL is spelled. Two roles, not one, so a QUERY preview
// still cannot run a data-modifying CTE against the shared decoy — the
// property exam_student_readonly gives today for real schemas.
const (
	rolePreviewStudent         = "exam_student_preview"
	rolePreviewStudentReadonly = "exam_student_preview_readonly"
)

// grantPreviewSchemaPermissions grants the two ARCH-020 preview-only roles on
// a decoy schema and — this is the asymmetry that makes the fix work — REVOKEs
// exam_student / exam_student_readonly from it. Decoys created by the
// already-merged pre-ARCH-020 code were granted to exam_student /
// exam_student_readonly directly (the same roles as the real schemas), which
// is exactly the leak this closes; the trailing REVOKE statements self-heal
// those decoys on every call, including from reconcilePreviewSchemaGrants.
// allowDML controls whether exam_student_preview additionally gets
// SELECT/INSERT/UPDATE/DELETE (and, since ARCH-049, sequence USAGE for a
// lecturer-authored seed INSERT that omits a serial column): QUERY-only
// decoys (the exam curriculum preview) must never grant a writable role, or
// a dual QUERY preview could mutate the shared decoy via a data-modifying
// CTE. The read-only exam_student_preview_readonly role never gets sequence
// usage.
func (r *PostgresRepository) grantPreviewSchemaPermissions(ctx context.Context, schema string, allowDML bool) error {
	safeSchema := pgx.Identifier{schema}.Sanitize()
	queries := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", safeSchema, rolePreviewStudentReadonly),
		fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO %s", safeSchema, rolePreviewStudentReadonly),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO %s", safeSchema, rolePreviewStudentReadonly),
	}
	if allowDML {
		queries = append(queries,
			fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", safeSchema, rolePreviewStudent),
			fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s", safeSchema, rolePreviewStudent),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", safeSchema, rolePreviewStudent),
			// ARCH-049: a lecturer-authored seed INSERT that omits a serial/
			// identity column calls nextval() as exam_student_preview. Without
			// sequence USAGE that fails with "permission denied for sequence".
			// Only the writable preview role gets this — the read-only
			// exam_student_preview_readonly role must never gain sequence usage.
			fmt.Sprintf("GRANT USAGE ON ALL SEQUENCES IN SCHEMA %s TO %s", safeSchema, rolePreviewStudent),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE ON SEQUENCES TO %s", safeSchema, rolePreviewStudent),
		)
	}
	// Asymmetry vs the real-schema grant sites: decoys must NOT keep the
	// exam_student / exam_student_readonly posture the pre-ARCH-020 code gave
	// them.
	queries = append(queries,
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE ALL ON TABLES FROM exam_student, exam_student_readonly", safeSchema),
		fmt.Sprintf("REVOKE ALL ON ALL TABLES IN SCHEMA %s FROM exam_student, exam_student_readonly", safeSchema),
		fmt.Sprintf("REVOKE ALL ON SCHEMA %s FROM exam_student, exam_student_readonly", safeSchema),
	)

	for _, q := range queries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("failed to grant preview permissions on schema %s: %w", safeSchema, err)
		}
	}
	return nil
}

// reconcilePreviewSchemaGrants sweeps every existing decoy preview schema
// (matched by the three canonical *_preview name patterns) and re-applies
// grantPreviewSchemaPermissions to it. Required so decoys provisioned by the
// already-merged pre-ARCH-020 code — granted to exam_student /
// exam_student_readonly, with no grants for the new preview-only roles — pick
// up the ARCH-020 posture on the next boot (CreateRestrictedUser calls this
// as its last statement) with no migration file and no manual DBA step.
// Per-schema failures are logged and skipped, never fatal, so one broken
// decoy can never block startup; only a failure of the enumeration query
// itself is returned.
func (r *PostgresRepository) reconcilePreviewSchemaGrants(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `
		SELECT nspname FROM pg_catalog.pg_namespace
		WHERE nspname LIKE 'practice\_cat\_%\_preview'
		   OR nspname LIKE 'curriculum\_exam\_%\_preview'
		   OR nspname LIKE 'sandbox\_exam\_%\_preview'
		   OR nspname LIKE 'exam\_cat\_%\_preview'`)
	if err != nil {
		return fmt.Errorf("failed to enumerate preview schemas for grant reconciliation: %w", err)
	}
	var schemas []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan preview schema name during grant reconciliation: %w", err)
		}
		schemas = append(schemas, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to enumerate preview schemas for grant reconciliation: %w", err)
	}

	for _, schema := range schemas {
		// practice_cat_*_preview and sandbox_exam_*_preview run DML previews
		// (practice runs both its QUERY and DML preview on the one decoy;
		// the exam DML preview uses the preview sandbox); curriculum_exam_*_preview
		// is QUERY-only.
		allowDML := strings.HasPrefix(schema, "practice_cat_") || strings.HasPrefix(schema, "sandbox_exam_")
		if err := r.grantPreviewSchemaPermissions(ctx, schema, allowDML); err != nil {
			log.Printf("Warning: failed to reconcile preview grants on schema %s: %v", schema, err)
		}
	}
	return nil
}

// PracticeCategoryPreviewSchemaName returns the canonical decoy schema name
// owned by a practice category's preview. Mirrors PracticeCategorySchemaName.
func PracticeCategoryPreviewSchemaName(categoryID int) string {
	return fmt.Sprintf("practice_cat_%d_preview", categoryID)
}

// ExamPreviewCurriculumSchemaName returns the canonical schema name for an
// exam's decoy curriculum database. Mirrors ExamCurriculumSchemaName.
func ExamPreviewCurriculumSchemaName(examID int) string {
	return fmt.Sprintf("curriculum_exam_%d_preview", examID)
}

// ExamPreviewSandboxName returns the canonical schema name for an exam's
// decoy DML sandbox, cloned from the preview curriculum. Mirrors
// ExamSandboxName.
func ExamPreviewSandboxName(examID int) string {
	return fmt.Sprintf("sandbox_exam_%d_preview", examID)
}

// ensureCategoryPreviewSchema creates the schema owned by a practice
// category's decoy preview (if missing) and grants ONLY the ARCH-020
// preview-only roles: exam_student_preview gets full DML (savepoint-rolled-
// back at evaluation time, used by the DML preview path) and
// exam_student_preview_readonly gets SELECT (used by the QUERY preview path).
// Practice runs BOTH its QUERY and its DML preview against this one decoy
// schema, so it needs the writable role too (allowDML=true). Unlike the real
// schema's ensureCategorySchema, exam_student / exam_student_readonly are
// explicitly REVOKEd from this schema — a dual preview must never be able to
// use the real-schema roles to escape into another decoy or the real
// category schema. Idempotent.
func (r *PostgresRepository) ensureCategoryPreviewSchema(ctx context.Context, categoryID int) error {
	schema := PracticeCategoryPreviewSchemaName(categoryID)
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	return r.grantPreviewSchemaPermissions(ctx, schema, true)
}

// ensureExamPreviewCurriculumSchema creates the per-exam decoy curriculum
// schema (if missing) and grants read-only access to the ARCH-020
// exam_student_preview_readonly role (allowDML=false), mirroring
// ensureExamCurriculumSchema. exam_student_preview does NOT get DML here — a
// dual-DML preview runs against the preview sandbox
// (sandbox_exam_{id}_preview), not the curriculum, exactly like the real
// (non-preview) curriculum. exam_student / exam_student_readonly are
// explicitly REVOKEd (grantPreviewSchemaPermissions), so this decoy can never
// disclose the exam's real curriculum schema via a schema-qualified read from
// inside a dual preview. Idempotent: safe to call repeatedly (e.g., before
// and after import).
func (r *PostgresRepository) ensureExamPreviewCurriculumSchema(ctx context.Context, examID int) error {
	schema := ExamPreviewCurriculumSchemaName(examID)
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	return r.grantPreviewSchemaPermissions(ctx, schema, false)
}

// ImportSchemaIntoCategoryPreview loads a SQL script into a practice
// category's decoy preview schema (practice_cat_<id>_preview). Mirrors
// ImportSchemaIntoCategory byte-for-byte except for the target schema: same
// validateNoSchemaQualifiedNames denylist, same acquireSchemaRebuildLockTx-
// guarded check-and-drop-if-confirmReplace transaction, same
// ensureCategoryPreviewSchema-before-and-after-import grant pattern, same
// SET LOCAL search_path to the preview schema only (no public fallback), same
// app.schema_import_audit insert. Unlike ImportSchemaIntoCategory this is NOT
// gated by exam-active (mirrors the real category import, which is also
// exam-independent).
//
// On success, app.practice_categories.preview_schema_name is stamped with the
// preview schema name and the returned parityWarning reports any structural
// drift between the decoy (A) and the category's real schema (B). A
// comparison failure never fails the import — it is logged and an empty
// string is returned for parityWarning instead.
func (r *PostgresRepository) ImportSchemaIntoCategoryPreview(ctx context.Context, categoryID int, sql string, importedBy int, confirmReplace bool) (parityWarning string, err error) {
	// Existence check first, mirroring ImportSchemaIntoCategory: without this,
	// importing a decoy against a category that no longer exists creates and
	// populates an orphaned schema, writes an audit row, and the closing
	// UPDATE ... WHERE id = $2 silently matches zero rows and returns nil.
	if _, err := r.GetPracticeCategorySchema(ctx, categoryID); err != nil {
		return "", err // caller maps pgx.ErrNoRows -> ErrCategoryNotFound -> 404
	}

	schema := PracticeCategoryPreviewSchemaName(categoryID)

	if err := validateNoSchemaQualifiedNames(sql); err != nil {
		return "", err
	}

	// Check-and-drop runs inside a transaction holding the schema's exclusive
	// rebuild lock (ARCH-013), so a preview evaluation in flight against this
	// category's decoy schema is never dropped out from under it: the DROP
	// waits for every shared holder (acquireSchemaEvalLock) to finish, and new
	// evaluations wait until this transaction commits.
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
				return ErrCategoryNotEmpty
			}
			safe := pgx.Identifier{schema}.Sanitize()
			if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
				return fmt.Errorf("failed to drop category preview schema for replace: %w", err)
			}
		}
		return tx.Commit(ctx)
	}(); err != nil {
		return "", err
	}
	// (Re)create the empty schema with role grants before importing.
	if err := r.ensureCategoryPreviewSchema(ctx, categoryID); err != nil {
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
	// preview schema and never fall back to the exam curriculum. Built-in
	// types still resolve via pg_catalog.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return "", fmt.Errorf("import failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	// Grant the student roles on the newly created tables (default privileges
	// only cover future tables created by the granting role, so grant explicitly).
	if err := r.ensureCategoryPreviewSchema(ctx, categoryID); err != nil {
		return "", err
	}
	if _, err := r.pool.Exec(ctx,
		"INSERT INTO app.schema_import_audit (imported_by, byte_size, table_count) VALUES ($1, $2, $3)",
		importedBy, len(sql), tableCount); err != nil {
		fmt.Printf("Warning: failed to write category preview import audit row: %v\n", err)
	}

	if _, err := r.pool.Exec(ctx,
		"UPDATE app.practice_categories SET preview_schema_name = $1 WHERE id = $2",
		schema, categoryID); err != nil {
		return "", fmt.Errorf("failed to update practice_categories.preview_schema_name: %w", err)
	}

	parityWarning, cmpErr := r.compareSchemaStructures(ctx, schema, PracticeCategorySchemaName(categoryID))
	if cmpErr != nil {
		log.Printf("Warning: preview/grading structure comparison failed for practice category %d: %v", categoryID, cmpErr)
		return "", nil
	}
	return parityWarning, nil
}

// ImportSchemaIntoExamCurriculumPreview loads a SQL script into an exam's
// decoy curriculum schema (curriculum_exam_<id>_preview). Mirrors
// ImportSchemaIntoExamCurriculum: same r.GetExamByID existence check, same
// ExaminationEffectiveState(exam).ActiveNow freeze guard (ErrExamActive), same
// validateNoSchemaQualifiedNames denylist, same tableCount-check-then-
// confirmReplace-drop using ErrExamCurriculumNotEmpty, same
// ensureExamPreviewCurriculumSchema-before-and-after-import grant pattern,
// same SET LOCAL search_path to the preview schema only, same audit insert.
//
// On success, app.examinations.preview_curriculum_schema is stamped with the
// preview schema name and the returned parityWarning reports any structural
// drift between the decoy curriculum (A) and the exam's real curriculum
// schema (B, defaulting to 'public'). A comparison failure never fails the
// import — it is logged and an empty string is returned for parityWarning
// instead.
func (r *PostgresRepository) ImportSchemaIntoExamCurriculumPreview(ctx context.Context, examID int, sql string, importedBy int, confirmReplace bool) (parityWarning string, err error) {
	// Load the exam — also proves it exists.
	exam, err := r.GetExamByID(ctx, examID)
	if err != nil {
		return "", err // caller maps pgx.ErrNoRows -> 404
	}

	// Per-exam freeze: block when the exam is currently active so preview
	// content stays consistent with the frozen curriculum for the duration of
	// the exam window, matching the real curriculum import's freeze rule.
	if ExaminationEffectiveState(exam).ActiveNow {
		return "", fmt.Errorf("%w: cannot import decoy curriculum while the examination is active", ErrExamActive)
	}

	if err := validateNoSchemaQualifiedNames(sql); err != nil {
		return "", err // ErrSchemaQualifiedName
	}

	schema := ExamPreviewCurriculumSchemaName(examID)

	var tableCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", schema).Scan(&tableCount); err != nil {
		return "", err
	}
	if tableCount > 0 {
		if !confirmReplace {
			return "", ErrExamCurriculumNotEmpty
		}
		safe := pgx.Identifier{schema}.Sanitize()
		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
			return "", fmt.Errorf("failed to drop exam preview curriculum schema for replace: %w", err)
		}
	}

	// (Re)create the empty schema with read-only grants before importing.
	if err := r.ensureExamPreviewCurriculumSchema(ctx, examID); err != nil {
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
	// schema only (no ", public" fallback): unqualified objects must resolve to
	// the exam's decoy curriculum schema. Built-in types still resolve via
	// pg_catalog.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return "", fmt.Errorf("exam preview curriculum import failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	// Re-run grants so the newly created tables are visible to the readonly
	// role (default privileges cover future tables from the granting role
	// only, so an explicit post-import grant is needed).
	if err := r.ensureExamPreviewCurriculumSchema(ctx, examID); err != nil {
		return "", err
	}

	// Stamp the preview_curriculum_schema column so the preview resolver picks
	// up the decoy schema immediately.
	if _, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET preview_curriculum_schema = $2 WHERE id = $1`,
		examID, schema); err != nil {
		return "", fmt.Errorf("failed to update examination preview_curriculum_schema: %w", err)
	}

	// ARCH-028 (6) legacy-sync: same reasoning as ImportSchemaIntoExamCurriculum
	// (postgres_schema.go), against preview_schema_name instead. Guarded on
	// schema_name (not preview_schema_name) so a legacy category is identified
	// consistently by both sync statements, and is a no-op once the category
	// owns its own exam_cat_<id> schema.
	if _, err := r.pool.Exec(ctx,
		`UPDATE app.exam_categories SET preview_schema_name = $2, updated_at = now()
		 WHERE exam_id = $1 AND schema_name <> ('exam_cat_' || id::text)`,
		examID, schema); err != nil {
		return "", fmt.Errorf("failed to sync exam_categories.preview_schema_name: %w", err)
	}

	// Audit row (best-effort: failure is logged but does not abort the import).
	var afterCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", schema).Scan(&afterCount); err != nil {
		afterCount = 0
	}
	if _, err := r.pool.Exec(ctx,
		"INSERT INTO app.schema_import_audit (imported_by, byte_size, table_count) VALUES ($1, $2, $3)",
		importedBy, len(sql), afterCount); err != nil {
		fmt.Printf("Warning: failed to write exam preview curriculum import audit row: %v\n", err)
	}

	b := exam.CurriculumSchema
	if b == "" {
		b = "public"
	}
	parityWarning, cmpErr := r.compareSchemaStructures(ctx, schema, b)
	if cmpErr != nil {
		log.Printf("Warning: preview/grading structure comparison failed for exam %d: %v", examID, cmpErr)
		return "", nil
	}
	return parityWarning, nil
}

// InitializeExamPreviewSandbox creates the shared per-exam decoy DML sandbox
// schema and clones every table from the exam's decoy preview curriculum into
// it once. Mirrors InitializeExamSandbox, but the source is always the
// exam's preview_curriculum_schema — never defaulted to 'public' — because an
// un-provisioned preview sandbox must never silently clone the real public
// curriculum. Callers (e.g. EnsureExamPreviewSandboxReady, on the first
// dual-DML preview) must have already confirmed a decoy curriculum exists.
//
// The call is idempotent: missing tables are filled in, existing ones are
// left untouched. Unlike InitializeExamSandbox this does NOT touch
// app.exam_sandboxes — that table's primary key is exam_id and is owned
// exclusively by the real per-exam sandbox row.
func (r *PostgresRepository) InitializeExamPreviewSandbox(ctx context.Context, examID int) error {
	schema := ExamPreviewSandboxName(examID)

	var previewCurriculumSchema string
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(preview_curriculum_schema, ''), '')
		 FROM app.examinations WHERE id = $1`, examID).Scan(&previewCurriculumSchema); err != nil {
		return err
	}
	if previewCurriculumSchema == "" {
		return fmt.Errorf("no decoy curriculum uploaded for exam %d", examID)
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
	return r.ensureTablesInSchema(ctx, schema, previewCurriculumSchema)
}

// EnsureExamPreviewSandboxReady lazily provisions the exam's decoy DML
// sandbox on first use. Mirrors EnsureExamSandboxReady but simpler: unlike
// the real sandbox, this is NOT gated by examHasDMLQuestions — the caller
// (the preview resolver, invoked only for a question already known to be
// dual+DML) has already established that a preview sandbox is needed.
// Idempotent: a second call against an already-populated sandbox is a no-op.
func (r *PostgresRepository) EnsureExamPreviewSandboxReady(ctx context.Context, examID int) error {
	hasTables, err := r.SchemaHasTables(ctx, ExamPreviewSandboxName(examID))
	if err != nil {
		return err
	}
	if hasTables {
		return nil
	}
	return r.InitializeExamPreviewSandbox(ctx, examID)
}

// GetPracticeCategoryPreviewSchema returns the decoy schema name imported for
// a practice category's student previews, or "" if none has been imported.
func (r *PostgresRepository) GetPracticeCategoryPreviewSchema(ctx context.Context, categoryID int) (string, error) {
	var schema string
	err := r.pool.QueryRow(ctx,
		"SELECT COALESCE(preview_schema_name, '') FROM app.practice_categories WHERE id = $1", categoryID).Scan(&schema)
	return schema, err
}

// GetExamPreviewCurriculumSchema returns the decoy curriculum schema name
// imported for an exam's student previews, or "" if none has been imported.
func (r *PostgresRepository) GetExamPreviewCurriculumSchema(ctx context.Context, examID int) (string, error) {
	var schema string
	err := r.pool.QueryRow(ctx,
		"SELECT COALESCE(preview_curriculum_schema, '') FROM app.examinations WHERE id = $1", examID).Scan(&schema)
	return schema, err
}

// compareSchemaStructures reports non-blocking structural drift between two
// schemas: tables present in schemaB (the grading schema) but missing from
// schemaA (the decoy), and — for tables present in both — column-name sets
// that differ. Column TYPE differences are not compared. Returns "" when the
// two schemas are structurally equal (same table set, same column-name set
// per shared table); otherwise a short human-readable warning string. Never
// returns a fatal error for structural mismatches — mismatches are reported
// via the returned string, not via err (err is only for genuine DB failures).
func (r *PostgresRepository) compareSchemaStructures(ctx context.Context, schemaA, schemaB string) (string, error) {
	tablesA, err := r.GetSchemaTablesWithColumns(ctx, schemaA)
	if err != nil {
		return "", err
	}
	tablesB, err := r.GetSchemaTablesWithColumns(ctx, schemaB)
	if err != nil {
		return "", err
	}

	byNameA := make(map[string]models.TableIntrospection, len(tablesA))
	for _, t := range tablesA {
		byNameA[t.Name] = t
	}

	var issues []string
	for _, tb := range tablesB {
		ta, ok := byNameA[tb.Name]
		if !ok {
			issues = append(issues, fmt.Sprintf("missing table %q", tb.Name))
			continue
		}
		if !sameColumnNames(ta, tb) {
			issues = append(issues, fmt.Sprintf("table %q has different columns", tb.Name))
		}
	}

	if len(issues) == 0 {
		return "", nil
	}
	return "Warning: the decoy schema's structure differs from the grading schema: " +
		strings.Join(issues, "; ") + ".", nil
}

// sameColumnNames reports whether two tables have the same set of column
// names (order and data type are not considered).
func sameColumnNames(a, b models.TableIntrospection) bool {
	if len(a.Columns) != len(b.Columns) {
		return false
	}
	names := make(map[string]bool, len(a.Columns))
	for _, c := range a.Columns {
		names[c.Name] = true
	}
	for _, c := range b.Columns {
		if !names[c.Name] {
			return false
		}
	}
	return true
}
