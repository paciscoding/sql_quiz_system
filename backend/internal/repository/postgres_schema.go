package repository

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// practiceSandboxSchema is the single shared schema that every practice
// evaluation runs against and that ResetPracticeSandbox rebuilds.
const practiceSandboxSchema = "sandbox_practice"

// practiceResetLockKey is the Postgres advisory-lock key that serialises a
// full practice-sandbox rebuild against in-flight practice evaluations.
// ResetPracticeSandbox takes it EXCLUSIVELY (pg_advisory_lock); each practice
// evaluation takes it SHARED (pg_advisory_xact_lock_shared) for the life of its
// transaction. Shared holders run concurrently; the exclusive reset waits for
// every in-flight eval to finish and blocks new ones until the rebuild is done,
// so no student SQL can ever execute against a half-dropped / half-cloned
// schema. The key is derived once, deterministically, from a stable string.
var practiceResetLockKey = fnv1a64Signed("sql_quiz/practice:reset")

// fnv1a64Signed hashes s with FNV-1a (64-bit) and reinterprets the bits as an
// int64 so it fits Postgres advisory-lock's bigint key argument.
func fnv1a64Signed(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}

// CreateRestrictedUser creates users with limited permissions
func (r *PostgresRepository) CreateRestrictedUser(ctx context.Context) error {
	// Create roles if they don't exist. We don't give them LOGIN permissions
	// because we will use SET ROLE from the superuser connection.
	queries := []string{
		"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'exam_student') THEN CREATE ROLE exam_student; END IF; END $$",
		"REVOKE ALL ON SCHEMA public FROM exam_student",
		"GRANT USAGE ON SCHEMA public TO exam_student",
		"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'exam_student_readonly') THEN CREATE ROLE exam_student_readonly; END IF; END $$",
		"REVOKE ALL ON SCHEMA public FROM exam_student_readonly",
		"GRANT USAGE ON SCHEMA public TO exam_student_readonly",
		"GRANT SELECT ON ALL TABLES IN SCHEMA public TO exam_student_readonly",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO exam_student_readonly",
	}

	for _, q := range queries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("failed to setup restricted roles: %w", err)
		}
	}
	return nil
}

// ExecStudentSQL executes SQL as the restricted student role
func (r *PostgresRepository) ExecStudentSQL(ctx context.Context, schema, sql string) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// 1. Set search path
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s", safeSchema)); err != nil {
		return err
	}

	// 2. Switch to restricted role
	if _, err := conn.Exec(ctx, "SET ROLE exam_student"); err != nil {
		return err
	}

	// 3. Execute student SQL
	_, err = conn.Exec(ctx, sql)

	// 4. Always reset role
	_, _ = conn.Exec(ctx, "RESET ROLE")

	return err
}

// QueryStudentSQL executes a query as the restricted student role
func (r *PostgresRepository) QueryStudentSQL(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s", safeSchema)); err != nil {
		return nil, err
	}

	if _, err := conn.Exec(ctx, "SET ROLE exam_student"); err != nil {
		return nil, err
	}

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		_, _ = conn.Exec(ctx, "RESET ROLE")
		return nil, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			_, _ = conn.Exec(ctx, "RESET ROLE")
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		results = append(results, rowMap)
	}

	_, _ = conn.Exec(ctx, "RESET ROLE")
	return results, rows.Err()
}

// migrateQuestionsIdentity repairs the legacy app.questions.id column, which was
// a plain `INTEGER PRIMARY KEY` with no default. A question created without a
// client-supplied id inserted id=0, and every subsequent no-id insert collided
// on that row (ON CONFLICT (id) DO UPDATE), silently overwriting prior questions
// across all exams. This attaches a sequence-backed default so omitted ids
// auto-assign, seeding the sequence past any existing rows. Idempotent: the
// guard only fires when the id column still has no default (covers fresh
// installs and legacy databases alike; explicit ids are still accepted for the
// update path).
func (r *PostgresRepository) migrateQuestionsIdentity(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
DO $$
BEGIN
	IF NOT EXISTS (
		SELECT 1
		FROM pg_attrdef d
		JOIN pg_class c ON c.oid = d.adrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = d.adnum
		WHERE n.nspname = 'app' AND c.relname = 'questions' AND a.attname = 'id'
	) THEN
		CREATE SEQUENCE IF NOT EXISTS app.questions_id_seq OWNED BY app.questions.id;
		ALTER TABLE app.questions ALTER COLUMN id SET DEFAULT nextval('app.questions_id_seq');
		PERFORM setval('app.questions_id_seq',
			GREATEST(COALESCE((SELECT MAX(id) FROM app.questions), 0), 0) + 1, false);
	END IF;
END $$;`)
	if err != nil {
		return fmt.Errorf("questions identity migration: %w", err)
	}
	return nil
}

// GrantSchemaPermissions gives the restricted user CRUD access to a schema
// migrateUniqueEmailIndex enforces that no two accounts share an email address
// via a PARTIAL unique index (NULL emails remain unconstrained, so the many
// students without an address never collide). It is safe to run on a populated
// database: it first scans for existing duplicate emails and, if any exist, logs
// them and SKIPS creating the index so startup still succeeds. The operator
// resolves the duplicates and the index is created on the next boot. Idempotent.
func (r *PostgresRepository) migrateUniqueEmailIndex(ctx context.Context) error {
	// Already enforced? Nothing to do.
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'idx_system_users_email')`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("email index existence check: %w", err)
	}
	if exists {
		return nil
	}

	// Dup-scan: use the SAME predicate as the index (email IS NOT NULL) so the
	// scan can never miss a collision the index would hit.
	rows, err := r.pool.Query(ctx,
		`SELECT email, COUNT(*) AS n FROM app.system_users
		 WHERE email IS NOT NULL
		 GROUP BY email HAVING COUNT(*) > 1
		 ORDER BY n DESC, email ASC`)
	if err != nil {
		return fmt.Errorf("email dup-scan: %w", err)
	}
	var dups []string
	for rows.Next() {
		var email string
		var n int
		if err := rows.Scan(&email, &n); err != nil {
			rows.Close()
			return err
		}
		dups = append(dups, fmt.Sprintf("%s (×%d)", email, n))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if len(dups) > 0 {
		log.Printf("WARNING: email uniqueness NOT enforced — %d duplicate email(s) already exist; "+
			"resolve them and restart to enable the constraint: %s", len(dups), strings.Join(dups, ", "))
		return nil
	}

	if _, err := r.pool.Exec(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_system_users_email ON app.system_users(email) WHERE email IS NOT NULL`,
	); err != nil {
		return fmt.Errorf("create unique email index: %w", err)
	}
	log.Printf("email uniqueness enforced (idx_system_users_email)")
	return nil
}

func (r *PostgresRepository) GrantSchemaPermissions(ctx context.Context, schema string) error {
	safeSchema := pgx.Identifier{schema}.Sanitize()
	queries := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO exam_student", safeSchema),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO exam_student", safeSchema),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO exam_student", safeSchema),
	}

	for _, q := range queries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("failed to grant permissions on schema %s: %w", safeSchema, err)
		}
	}
	return nil
}

// EnsureSchema creates a schema if it doesn't exist
func (r *PostgresRepository) EnsureSchema(ctx context.Context, schema string) error {
	safeSchema := pgx.Identifier{schema}.Sanitize()
	return r.Exec(ctx, "public", fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", safeSchema))
}

// ExecuteSchemaScript runs a full SQL script in the public schema. Refuses to
// run while any examination is active — the curriculum is frozen for the
// duration of the active window so the cached Correct Result and the DML
// sandbox stay valid. importedBy is the ID of the user performing the import
// (recorded in app.schema_import_audit).
func (r *PostgresRepository) ExecuteSchemaScript(ctx context.Context, sql string, importedBy int) error {
	var activeCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.examinations WHERE is_active = TRUE").Scan(&activeCount); err != nil {
		return err
	}
	if activeCount > 0 {
		return fmt.Errorf("%w: cannot import schema while %d examination(s) are active", ErrExamActive, activeCount)
	}

	if _, err := r.pool.Exec(ctx, sql); err != nil {
		return err
	}

	// Record the import in the audit table
	var tableCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = 'public'").Scan(&tableCount); err != nil {
		// Non-fatal: log but don't fail the import
		fmt.Printf("Warning: failed to count public tables for audit: %v\n", err)
	}
	if _, err := r.pool.Exec(ctx,
		"INSERT INTO app.schema_import_audit (imported_by, byte_size, table_count) VALUES ($1, $2, $3)",
		importedBy, len(sql), tableCount); err != nil {
		fmt.Printf("Warning: failed to write schema import audit row: %v\n", err)
	}
	return nil
}

// schemaQualifiedNameRe matches references that would escape a category schema:
// an object qualified by one of the system/shared schemas or another category's
// schema. It is applied AFTER string literals and comments are stripped to avoid
// false positives on data values. This is a denylist (covers public, app, the
// PostgreSQL system schemas, exam sandboxes and other practice categories); it
// deliberately does not try to fully parse SQL — category import scripts are
// expected to use unqualified names that land in the category's own schema.
// curriculum_exam_[0-9]+ added to prevent imports from escaping into another
// exam's curriculum schema (same spirit as practice_cat_[0-9]+).
var schemaQualifiedNameRe = regexp.MustCompile(`(?i)\b(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|practice_cat_[0-9]+|curriculum_exam_[0-9]+)\s*\.`)

// Compiled once at package load. Go's RE2 engine has no backreferences, so the
// dollar-quote stripper handles the common anonymous $$...$$ form only; tagged
// $tag$...$tag$ bodies (rare in data imports) are left intact, which can only
// cause a conservative (over-strict) rejection, never an escape.
var (
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`--[^\n]*`)
	reDollarQuote  = regexp.MustCompile(`(?s)\$\$.*?\$\$`)
	reSingleQuote  = regexp.MustCompile(`'(?:''|[^'])*'`)
)

// stripSQLNoise removes line/block comments and single- and dollar-quoted string
// literals so the qualified-name scan only sees SQL identifiers, not data.
func stripSQLNoise(sql string) string {
	sql = reBlockComment.ReplaceAllString(sql, " ")
	sql = reLineComment.ReplaceAllString(sql, " ")
	sql = reDollarQuote.ReplaceAllString(sql, " ")
	sql = reSingleQuote.ReplaceAllString(sql, " ")
	return sql
}

// validateNoSchemaQualifiedNames rejects scripts that reference system/shared or
// foreign schemas, which would let an import escape its category schema.
func validateNoSchemaQualifiedNames(sql string) error {
	m := schemaQualifiedNameRe.FindStringSubmatch(stripSQLNoise(sql))
	if m != nil {
		return fmt.Errorf("%w: %q", ErrSchemaQualifiedName, m[1])
	}
	return nil
}

// ImportSchemaIntoCategory loads a SQL script into a practice category's owned
// schema. Unqualified DDL/DML lands in the category schema (search_path is set
// to that schema only, so it cannot fall back to public). Schema-qualified names
// are rejected up front. If the schema already has tables, the caller must pass
// confirmReplace=true, which drops and recreates the schema before importing;
// otherwise ErrCategoryNotEmpty is returned. Unlike ExecuteSchemaScript this is
// NOT gated by the exam-active freeze — category schemas are independent of the
// exam curriculum.
func (r *PostgresRepository) ImportSchemaIntoCategory(ctx context.Context, categoryID int, sql string, importedBy int, confirmReplace bool) error {
	schema, err := r.GetPracticeCategorySchema(ctx, categoryID)
	if err != nil {
		return err
	}
	if schema == "" {
		schema = PracticeCategorySchemaName(categoryID)
	}

	if err := validateNoSchemaQualifiedNames(sql); err != nil {
		return err
	}

	var tableCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", schema).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount > 0 {
		if !confirmReplace {
			return ErrCategoryNotEmpty
		}
		safe := pgx.Identifier{schema}.Sanitize()
		if _, err := r.pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
			return fmt.Errorf("failed to drop category schema for replace: %w", err)
		}
	}
	// (Re)create the empty schema with role grants before importing.
	if err := r.ensureCategorySchema(ctx, categoryID); err != nil {
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
	// category schema and never fall back to the exam curriculum. Built-in types
	// still resolve via pg_catalog.
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
	if err := r.ensureCategorySchema(ctx, categoryID); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx,
		"INSERT INTO app.schema_import_audit (imported_by, byte_size, table_count) VALUES ($1, $2, $3)",
		importedBy, len(sql), tableCount); err != nil {
		fmt.Printf("Warning: failed to write category import audit row: %v\n", err)
	}
	return nil
}

// ImportSchemaIntoExamCurriculum loads a SQL script into a per-exam curriculum
// schema. The exam gets its own `curriculum_exam_{id}` schema; after import the
// examinations.curriculum_schema column is updated to that name so the capture
// (Phase 0), sandbox clone (Phase 1), and eval (Phase 2) paths all read from
// the correct source.
//
// Backward compatibility: if this method is never called, curriculum_schema
// stays 'public' and every existing code path behaves identically to before.
//
// Freeze rule: if the exam is currently active the import is blocked with
// ErrExamActive (same guard as question mutations).
func (r *PostgresRepository) ImportSchemaIntoExamCurriculum(ctx context.Context, examID int, sql string, importedBy int, confirmReplace bool) error {
	// Load the exam — also proves it exists.
	exam, err := r.GetExamByID(ctx, examID)
	if err != nil {
		return err // caller maps pgx.ErrNoRows -> 404
	}

	// Per-exam freeze: block when the exam is currently active so the capture
	// results and sandbox remain consistent with the curriculum.
	if ExaminationEffectiveState(exam).ActiveNow {
		return fmt.Errorf("%w: cannot import curriculum while the examination is active", ErrExamActive)
	}

	if err := validateNoSchemaQualifiedNames(sql); err != nil {
		return err // ErrSchemaQualifiedName
	}

	schema := ExamCurriculumSchemaName(examID)

	var tableCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM pg_catalog.pg_tables WHERE schemaname = $1", schema).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount > 0 {
		if !confirmReplace {
			return ErrExamCurriculumNotEmpty
		}
		safe := pgx.Identifier{schema}.Sanitize()
		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
			return fmt.Errorf("failed to drop exam curriculum schema for replace: %w", err)
		}
	}

	// (Re)create the empty schema with read-only grants before importing.
	if err := r.ensureExamCurriculumSchema(ctx, examID); err != nil {
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
	// schema only (no ", public" fallback): unqualified objects must resolve to
	// the exam's curriculum schema. Built-in types still resolve via pg_catalog.
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safe)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("exam curriculum import failed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Re-run grants so the newly created tables are visible to the readonly role
	// (default privileges cover future tables from the granting role only, so an
	// explicit post-import grant is needed — mirrors the practice category flow).
	if err := r.ensureExamCurriculumSchema(ctx, examID); err != nil {
		return err
	}

	// Stamp the curriculum_schema column so Phase 0/1/2 paths pick up the
	// per-exam schema immediately.
	if _, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET curriculum_schema = $2 WHERE id = $1`,
		examID, schema); err != nil {
		return fmt.Errorf("failed to update examination curriculum_schema: %w", err)
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
		fmt.Printf("Warning: failed to write exam curriculum import audit row: %v\n", err)
	}
	return nil
}

// GetTablesInSchema returns the list of all tables in the given schema.
// It is the parameterised generalisation of GetPublicTables.
func (r *PostgresRepository) GetTablesInSchema(ctx context.Context, schema string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = $1`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// GetPublicTables returns the list of all tables in the public schema.
// System tables live in the dedicated `app` schema, so no exclusion list is needed.
// Thin wrapper around GetTablesInSchema for backward compatibility.
func (r *PostgresRepository) GetPublicTables(ctx context.Context) ([]string, error) {
	return r.GetTablesInSchema(ctx, "public")
}

// SchemaHasTables reports whether the given schema contains at least one table.
// Used to reject practice submissions against a category whose database has not
// been imported yet.
func (r *PostgresRepository) SchemaHasTables(ctx context.Context, schema string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = $1)", schema).Scan(&exists)
	return exists, err
}

// GetSchemaTablesWithColumns returns a slice of TableIntrospection describing
// every table in the given schema — including per-column attributes (type,
// nullability, PK/FK membership). The schema name is passed as a parameterised
// data value to information_schema, never interpolated into SQL, so there is no
// injection surface. Returns an empty (non-nil) slice when the schema has no
// tables. No user-table row data is read; only catalog views are queried.
func (r *PostgresRepository) GetSchemaTablesWithColumns(ctx context.Context, schema string) ([]models.TableIntrospection, error) {
	// One query: all columns for the schema, with PK and FK info resolved via
	// LEFT JOINs against the constraint catalogs.
	query := `
		SELECT
			c.table_name,
			c.column_name,
			c.data_type,
			c.is_nullable,
			c.ordinal_position,
			COALESCE(
				(SELECT kcu2.column_name
				 FROM information_schema.table_constraints tc2
				 JOIN information_schema.key_column_usage kcu2
				   ON kcu2.constraint_name = tc2.constraint_name
				   AND kcu2.table_schema = tc2.table_schema
				 WHERE tc2.constraint_type = 'PRIMARY KEY'
				   AND tc2.table_schema = c.table_schema
				   AND tc2.table_name  = c.table_name
				   AND kcu2.column_name = c.column_name
				 LIMIT 1),
				''
			) AS pk_column,
			COALESCE(
				(SELECT ccu.table_name
				 FROM information_schema.table_constraints tc3
				 JOIN information_schema.key_column_usage kcu3
				   ON kcu3.constraint_name = tc3.constraint_name
				   AND kcu3.table_schema = tc3.table_schema
				 JOIN information_schema.constraint_column_usage ccu
				   ON ccu.constraint_name = tc3.constraint_name
				   AND ccu.constraint_schema = tc3.constraint_schema
				 WHERE tc3.constraint_type = 'FOREIGN KEY'
				   AND tc3.table_schema = c.table_schema
				   AND tc3.table_name  = c.table_name
				   AND kcu3.column_name = c.column_name
				 LIMIT 1),
				''
			) AS fk_ref_table,
			COALESCE(
				(SELECT ccu.column_name
				 FROM information_schema.table_constraints tc3
				 JOIN information_schema.key_column_usage kcu3
				   ON kcu3.constraint_name = tc3.constraint_name
				   AND kcu3.table_schema = tc3.table_schema
				 JOIN information_schema.constraint_column_usage ccu
				   ON ccu.constraint_name = tc3.constraint_name
				   AND ccu.constraint_schema = tc3.constraint_schema
				 WHERE tc3.constraint_type = 'FOREIGN KEY'
				   AND tc3.table_schema = c.table_schema
				   AND tc3.table_name  = c.table_name
				   AND kcu3.column_name = c.column_name
				 LIMIT 1),
				''
			) AS fk_ref_column
		FROM information_schema.columns c
		WHERE c.table_schema = $1
		ORDER BY c.table_name ASC, c.ordinal_position ASC`

	rows, err := r.pool.Query(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tableIndex := map[string]int{} // table_name -> index in result slice
	result := []models.TableIntrospection{}

	for rows.Next() {
		var (
			tableName   string
			columnName  string
			dataType    string
			isNullable  string
			ordinal     int
			pkColumn    string
			fkRefTable  string
			fkRefColumn string
		)
		if err := rows.Scan(
			&tableName, &columnName, &dataType, &isNullable, &ordinal,
			&pkColumn, &fkRefTable, &fkRefColumn,
		); err != nil {
			return nil, err
		}

		col := models.ColumnIntrospection{
			Name:             columnName,
			DataType:         dataType,
			Nullable:         isNullable == "YES",
			IsPrimaryKey:     pkColumn != "",
			IsForeignKey:     fkRefTable != "",
			ReferencesTable:  fkRefTable,
			ReferencesColumn: fkRefColumn,
		}

		idx, exists := tableIndex[tableName]
		if !exists {
			result = append(result, models.TableIntrospection{
				Name:    tableName,
				Columns: []models.ColumnIntrospection{},
			})
			idx = len(result) - 1
			tableIndex[tableName] = idx
		}
		result[idx].Columns = append(result[idx].Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Return non-nil empty slice when schema has no tables.
	if result == nil {
		result = []models.TableIntrospection{}
	}
	return result, nil
}

// ensureTablesInSchema makes sure that every table from sourceSchema exists in
// destSchema. Tables that already exist are left untouched, so the call is
// cheap on repeat invocations. New tables are cloned with `LIKE src.x INCLUDING
// ALL` so primary keys, NOT NULL, defaults and indexes survive — `CREATE TABLE
// … AS SELECT` would have dropped them.
//
// When sourceSchema is 'public' this is identical to the previous behaviour;
// passing an exam's curriculum_schema (which defaults to 'public') means legacy
// exams continue to clone from public exactly as before.
func (r *PostgresRepository) ensureTablesInSchema(ctx context.Context, destSchema, sourceSchema string) error {
	tables, err := r.GetTablesInSchema(ctx, sourceSchema)
	if err != nil {
		return err
	}

	for _, table := range tables {
		var exists bool
		err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM pg_catalog.pg_tables
				WHERE schemaname = $1 AND tablename = $2
			)`, destSchema, table).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		destTbl := pgx.Identifier{destSchema, table}.Sanitize()
		sourceTbl := pgx.Identifier{sourceSchema, table}.Sanitize()

		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", destTbl, sourceTbl)); err != nil {
			return fmt.Errorf("failed to clone structure of %s.%s from %s: %w", destSchema, table, sourceSchema, err)
		}
		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", destTbl, sourceTbl)); err != nil {
			return fmt.Errorf("failed to seed data into %s.%s from %s: %w", destSchema, table, sourceSchema, err)
		}
	}
	return nil
}

// ExamSandboxName returns the canonical schema name for an exam's shared DML
// sandbox: every student enrolled in `examID` reads and writes the same
// physical tables under this schema, serialized via the global in-process
// FIFO submission queue at submission time.
func ExamSandboxName(examID int) string {
	return fmt.Sprintf("sandbox_exam_%d", examID)
}

// PracticeCategorySchemaName returns the canonical schema name owned by a
// practice category. Each category owns exactly one schema, into which a
// lecturer imports a database; all practice questions in the category run
// against this schema (kept separate from the exam curriculum in public).
// Mirrors the ExamSandboxName convention.
func PracticeCategorySchemaName(categoryID int) string {
	return fmt.Sprintf("practice_cat_%d", categoryID)
}

// ExamCurriculumSchemaName returns the canonical schema name for an exam's
// own curriculum database. Lecturers optionally import a per-exam database into
// this schema; if they never do, the exam's curriculum_schema column stays
// 'public' and everything behaves identically to before this feature existed.
func ExamCurriculumSchemaName(examID int) string {
	return fmt.Sprintf("curriculum_exam_%d", examID)
}

// InitializeExamSandbox creates the shared per-exam sandbox schema and clones
// every table from the exam's curriculum schema into it once. Called from
// SetExaminationActive only when the exam has at least one DML question. The
// call is idempotent: missing tables are filled in, existing ones are left
// untouched. The bookkeeping row in app.exam_sandboxes is upserted so Phase 3
// cleanup can find it later.
//
// Curriculum source resolution: if the exam's curriculum_schema column is
// 'public' (the default — all pre-import exams), this is byte-for-byte
// identical to the previous behaviour.
func (r *PostgresRepository) InitializeExamSandbox(ctx context.Context, examID int) error {
	schema := ExamSandboxName(examID)

	// Resolve the exam's curriculum schema so the sandbox is cloned from the
	// right source. Default to 'public' if the column is empty or missing.
	var curriculumSchema string
	if err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(curriculum_schema, ''), 'public')
		 FROM app.examinations WHERE id = $1`, examID).Scan(&curriculumSchema); err != nil {
		// If the exam row is missing or the column doesn't exist yet (migration
		// hasn't run), fall back to 'public' so activation is never blocked.
		curriculumSchema = "public"
	}

	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	// Grant before cloning so ALTER DEFAULT PRIVILEGES applies to the new tables.
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
		return err
	}
	if err := r.ensureTablesInSchema(ctx, schema, curriculumSchema); err != nil {
		return err
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO app.exam_sandboxes (exam_id, schema_name, created_at, dropped_at)
		VALUES ($1, $2, NOW(), NULL)
		ON CONFLICT (exam_id) DO UPDATE SET schema_name = EXCLUDED.schema_name, dropped_at = NULL`,
		examID, schema)
	return err
}

// InitializePracticeSandbox creates the shared practice sandbox schema and
// clones every public table into it once. The call is idempotent: if the
// schema already exists, it returns nil immediately without issuing any DDL.
// Permissions are granted to both exam_student (full DML) and
// exam_student_readonly (SELECT only) so that practice evaluations can run.
func (r *PostgresRepository) InitializePracticeSandbox(ctx context.Context) error {
	const schema = "sandbox_practice"

	// Idempotency check: if the schema already exists, do nothing.
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`,
		schema).Scan(&exists); err != nil {
		return fmt.Errorf("failed to check practice sandbox existence: %w", err)
	}
	if exists {
		return nil
	}

	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	// Grant before cloning so ALTER DEFAULT PRIVILEGES applies to the new tables.
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
		return err
	}
	// Grant read-only access to exam_student_readonly.
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
	// Practice sandbox always clones from public (unchanged behaviour).
	if err := r.ensureTablesInSchema(ctx, schema, "public"); err != nil {
		return err
	}

	_, err := r.pool.Exec(ctx,
		`UPDATE app.practice_sandbox_state SET created_at = NOW() WHERE id = 1`)
	return err
}

// ResetPracticeSandbox drops the practice sandbox schema and recreates it from
// scratch, re-cloning every public table with fresh data. It stamps
// last_reset_at in app.practice_sandbox_state and returns that timestamp so
// the caller can include it in API responses.
func (r *PostgresRepository) ResetPracticeSandbox(ctx context.Context) (time.Time, error) {
	const schema = practiceSandboxSchema

	// Serialise the whole rebuild against in-flight practice evaluations. We
	// hold a dedicated connection and take the advisory lock EXCLUSIVELY at the
	// SESSION level: pg_advisory_lock blocks until every shared holder (each an
	// in-flight eval, see evaluate*) has released, then keeps new evals waiting
	// until we unlock. Advisory locks are cooperative and cross-connection, so
	// the DDL below may keep using the pool — only the lock discipline matters.
	lockConn, err := r.pool.Acquire(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to acquire lock connection for practice reset: %w", err)
	}
	defer lockConn.Release()
	if _, err := lockConn.Exec(ctx, "SELECT pg_advisory_lock($1)", practiceResetLockKey); err != nil {
		return time.Time{}, fmt.Errorf("failed to acquire practice reset lock: %w", err)
	}
	// Best-effort explicit unlock. Releasing the connection would also drop the
	// session lock, but unlocking eagerly frees waiting evals a moment sooner.
	defer func() {
		_, _ = lockConn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", practiceResetLockKey)
	}()

	// Drop and recreate for a clean slate.
	if _, err := r.pool.Exec(ctx, "DROP SCHEMA IF EXISTS sandbox_practice CASCADE"); err != nil {
		return time.Time{}, fmt.Errorf("failed to drop practice sandbox schema: %w", err)
	}
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return time.Time{}, err
	}
	// Grant before cloning so ALTER DEFAULT PRIVILEGES applies to the new tables.
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
		return time.Time{}, err
	}
	// Grant read-only access to exam_student_readonly.
	safeSchema := pgx.Identifier{schema}.Sanitize()
	readonlyQueries := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO exam_student_readonly", safeSchema),
		fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO exam_student_readonly", safeSchema),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO exam_student_readonly", safeSchema),
	}
	for _, q := range readonlyQueries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return time.Time{}, fmt.Errorf("failed to grant readonly permissions on %s: %w", schema, err)
		}
	}
	// Reset always clones from public (unchanged behaviour).
	if err := r.ensureTablesInSchema(ctx, schema, "public"); err != nil {
		return time.Time{}, err
	}

	var resetAt time.Time
	if err := r.pool.QueryRow(ctx,
		`UPDATE app.practice_sandbox_state SET last_reset_at = NOW() WHERE id = 1 RETURNING last_reset_at`,
	).Scan(&resetAt); err != nil {
		return time.Time{}, fmt.Errorf("failed to update practice_sandbox_state: %w", err)
	}
	return resetAt, nil
}

// examHasDMLQuestions reports whether `examID` has at least one DML question.
// Used by SetExaminationActive to decide whether to spend the cost of cloning
// public.* into the exam sandbox — pure-SELECT exams skip cloning entirely.
func (r *PostgresRepository) examHasDMLQuestions(ctx context.Context, examID int) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.questions WHERE exam_id = $1 AND question_type = 'DML'",
		examID).Scan(&n)
	return n > 0, err
}

// InitializeSystemTables creates the necessary tables for the quiz system.
// They live in their own `app` schema so that the public schema can be cloned
// wholesale into student sandboxes without any hardcoded exclusion list.
func (r *PostgresRepository) InitializeSystemTables(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS app"); err != nil {
		return fmt.Errorf("failed to create app schema: %w", err)
	}

	// system_users must be created first because every other table FKs to it.
	queries := []string{
		`CREATE TABLE IF NOT EXISTS app.system_users (
			id SERIAL PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			created_by INTEGER REFERENCES app.system_users(id),
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS app.examinations (
			id SERIAL PRIMARY KEY,
			title TEXT NOT NULL,
			is_active BOOLEAN DEFAULT FALSE,
			created_by INTEGER REFERENCES app.system_users(id),
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			activated_at TIMESTAMP WITH TIME ZONE,
			archived_at TIMESTAMP WITH TIME ZONE,
			scheduled_start_at TIMESTAMP WITH TIME ZONE,
			scheduled_end_at TIMESTAMP WITH TIME ZONE,
			block_practice_during_exam BOOLEAN NOT NULL DEFAULT FALSE,
			introduction TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS app.questions (
			id INTEGER PRIMARY KEY,
			exam_id INTEGER REFERENCES app.examinations(id),
			target_table TEXT NOT NULL,
			correct_sql TEXT NOT NULL,
			question_type TEXT NOT NULL,
			points DOUBLE PRECISION DEFAULT 10.0,
			requirement TEXT DEFAULT '',
			strict_ordering BOOLEAN DEFAULT FALSE,
			strict_attributes BOOLEAN DEFAULT FALSE,
			strict_column_order BOOLEAN NOT NULL DEFAULT FALSE,
			expected_result JSONB,
			expected_columns JSONB,
			expected_result_status VARCHAR(16) DEFAULT 'pending',
			expected_result_captured_at TIMESTAMP WITH TIME ZONE,
			capture_error TEXT,
			created_by INTEGER REFERENCES app.system_users(id),
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS app.exam_students (
			exam_id INTEGER REFERENCES app.examinations(id),
			student_id INTEGER REFERENCES app.system_users(id),
			assigned_by INTEGER REFERENCES app.system_users(id),
			assigned_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (exam_id, student_id)
		);`,
		`CREATE TABLE IF NOT EXISTS app.submissions (
			id SERIAL PRIMARY KEY,
			exam_id INTEGER REFERENCES app.examinations(id),
			student_id INTEGER REFERENCES app.system_users(id),
			question_id INTEGER REFERENCES app.questions(id),
			sql_text TEXT NOT NULL,
			is_correct BOOLEAN DEFAULT FALSE,
			score DOUBLE PRECISION DEFAULT 0.0,
			validation_details JSONB,
			submitted_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS app.exam_sandboxes (
			exam_id INTEGER PRIMARY KEY REFERENCES app.examinations(id),
			schema_name TEXT NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			dropped_at TIMESTAMP WITH TIME ZONE
		);`,
		// Indexes for performance
		`CREATE INDEX IF NOT EXISTS idx_exam_students_student_id ON app.exam_students(student_id);`,
		`CREATE INDEX IF NOT EXISTS idx_questions_exam_id ON app.questions(exam_id);`,
		`CREATE INDEX IF NOT EXISTS idx_submissions_lookup ON app.submissions(exam_id, student_id, question_id, submitted_at DESC);`,
		`CREATE TABLE IF NOT EXISTS app.practice_questions (
			id SERIAL PRIMARY KEY,
			created_by INTEGER NOT NULL REFERENCES app.system_users(id),
			category TEXT NOT NULL DEFAULT 'General',
			title TEXT,
			target_table TEXT NOT NULL,
			correct_sql TEXT NOT NULL,
			question_type TEXT NOT NULL CHECK (question_type IN ('QUERY','DML')),
			requirement TEXT NOT NULL,
			points NUMERIC(6,2) NOT NULL DEFAULT 1.0,
			strict_ordering BOOLEAN NOT NULL DEFAULT false,
			strict_attrs BOOLEAN NOT NULL DEFAULT false,
			strict_column_order BOOLEAN NOT NULL DEFAULT false,
			expected_result JSONB,
			expected_columns JSONB,
			expected_result_status TEXT NOT NULL DEFAULT 'pending' CHECK (expected_result_status IN ('pending','captured','failed')),
			capture_error TEXT,
			expected_result_captured_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE TABLE IF NOT EXISTS app.practice_attempts (
			id SERIAL PRIMARY KEY,
			student_id INTEGER NOT NULL REFERENCES app.system_users(id),
			practice_question_id INTEGER NOT NULL REFERENCES app.practice_questions(id) ON DELETE CASCADE,
			sql_text TEXT NOT NULL,
			is_correct BOOLEAN NOT NULL,
			validation_details JSONB,
			submitted_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_practice_attempts_student ON app.practice_attempts(student_id, submitted_at DESC);`,
		`CREATE TABLE IF NOT EXISTS app.practice_sandbox_state (
			id INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			schema_name TEXT NOT NULL DEFAULT 'sandbox_practice',
			created_at TIMESTAMPTZ,
			last_reset_at TIMESTAMPTZ
		);`,
		`INSERT INTO app.practice_sandbox_state (id) VALUES (1) ON CONFLICT (id) DO NOTHING;`,
		// Practice categories: each row owns a schema named practice_cat_<id>.
		// schema_name is filled in after INSERT (the SERIAL id is needed to derive
		// it) by CreatePracticeCategory / the backfill migration.
		`CREATE TABLE IF NOT EXISTS app.practice_categories (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			schema_name TEXT NOT NULL DEFAULT '' UNIQUE,
			created_by INTEGER NOT NULL REFERENCES app.system_users(id),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			introduction TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS app.schema_import_audit (
			id SERIAL PRIMARY KEY,
			imported_by INTEGER REFERENCES app.system_users(id),
			imported_at TIMESTAMPTZ DEFAULT now(),
			byte_size INTEGER,
			table_count INTEGER
		);`,
		// Intro images: binary attachments for practice-category and exam intro pages.
		`CREATE TABLE IF NOT EXISTS app.practice_category_intro_images (
			id           SERIAL PRIMARY KEY,
			category_id  INT  NOT NULL REFERENCES app.practice_categories(id) ON DELETE CASCADE,
			sort_order   INT  NOT NULL,
			mime_type    TEXT NOT NULL CHECK (mime_type IN ('image/png','image/jpeg')),
			byte_size    INT  NOT NULL CHECK (byte_size > 0 AND byte_size <= 5242880),
			image_bytes  BYTEA NOT NULL,
			created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
			updated_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pcat_intro_images_order ON app.practice_category_intro_images(category_id, sort_order)`,
		`CREATE TABLE IF NOT EXISTS app.examination_intro_images (
			id              SERIAL PRIMARY KEY,
			examination_id  INT  NOT NULL REFERENCES app.examinations(id) ON DELETE CASCADE,
			sort_order      INT  NOT NULL,
			mime_type       TEXT NOT NULL CHECK (mime_type IN ('image/png','image/jpeg')),
			byte_size       INT  NOT NULL CHECK (byte_size > 0 AND byte_size <= 5242880),
			image_bytes     BYTEA NOT NULL,
			created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
			updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_exam_intro_images_order ON app.examination_intro_images(examination_id, sort_order)`,
		// Auditable password-change log. new_password_enc holds AES-256-GCM
		// ciphertext of the new password value (the key lives only in env), nonce
		// is the 12-byte GCM nonce. changed_by is the acting user (= user_id for
		// self_change). ON DELETE CASCADE keeps events tied to the user's lifetime.
		`CREATE TABLE IF NOT EXISTS app.password_events (
			id               SERIAL PRIMARY KEY,
			user_id          INTEGER NOT NULL REFERENCES app.system_users(id) ON DELETE CASCADE,
			changed_by       INTEGER REFERENCES app.system_users(id),
			source           TEXT NOT NULL CHECK (source IN ('create','self_change','admin_reset','forgot_reset')),
			new_password_enc BYTEA NOT NULL,
			nonce            BYTEA NOT NULL,
			notified_admin   BOOLEAN NOT NULL DEFAULT FALSE,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_password_events_user ON app.password_events(user_id, created_at DESC);`,
		`CREATE TABLE IF NOT EXISTS app.export_state (
			id             INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			enabled        BOOLEAN NOT NULL,
			last_export_at TIMESTAMPTZ NOT NULL,
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
		);`,
	}

	for _, q := range queries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return err
		}
	}

	// Idempotent column additions for existing installations
	extraMigrations := []string{
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS activated_at TIMESTAMP WITH TIME ZONE",
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS archived_at TIMESTAMP WITH TIME ZONE",
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS scheduled_start_at TIMESTAMP WITH TIME ZONE",
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS scheduled_end_at TIMESTAMP WITH TIME ZONE",
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS block_practice_during_exam BOOLEAN NOT NULL DEFAULT FALSE",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS expected_result JSONB",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS expected_columns JSONB",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS expected_result_status VARCHAR(16) DEFAULT 'pending'",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS expected_result_captured_at TIMESTAMP WITH TIME ZONE",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS capture_error TEXT",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS created_by INTEGER REFERENCES app.system_users(id)",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS requirement TEXT DEFAULT ''",
		"ALTER TABLE app.exam_students ADD COLUMN IF NOT EXISTS assigned_by INTEGER REFERENCES app.system_users(id)",
		"ALTER TABLE app.exam_students ADD COLUMN IF NOT EXISTS assigned_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP",
		"ALTER TABLE app.questions DROP COLUMN IF EXISTS strict_record_order",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS student_number TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS student_id_ext TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS sec_lec TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS sec_lab TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS first_name TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS last_name TEXT",
		"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'General'",
		// student_number is duplicate-able by design (several students may share
		// one). Drop the legacy UNIQUE index if present and replace it with a
		// plain (non-unique) index used only for lookups. Idempotent: the DO block
		// only drops when the existing index is still unique, so re-runs no-op.
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM pg_index i
				JOIN pg_class c     ON c.oid = i.indexrelid
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'app'
				  AND c.relname = 'idx_system_users_student_number'
				  AND i.indisunique
			) THEN
				DROP INDEX app.idx_system_users_student_number;
			END IF;
		END $$;`,
		"CREATE INDEX IF NOT EXISTS idx_system_users_student_number ON app.system_users(student_number) WHERE student_number IS NOT NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_system_users_student_id_ext ON app.system_users(student_id_ext) WHERE student_id_ext IS NOT NULL",
		"ALTER TABLE app.practice_categories ADD COLUMN IF NOT EXISTS introduction TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS introduction TEXT NOT NULL DEFAULT ''",
		// Per-exam curriculum schema. Defaults to 'public' so all existing exams
		// continue to use the global public schema — behaviour is unchanged until
		// a lecturer explicitly runs ImportSchemaIntoExamCurriculum.
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS curriculum_schema TEXT NOT NULL DEFAULT 'public'",
		// Column-order grading toggle. DEFAULT FALSE so existing questions are unaffected.
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS strict_column_order BOOLEAN NOT NULL DEFAULT FALSE",
		"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS strict_column_order BOOLEAN NOT NULL DEFAULT FALSE",
		// Auth/password-management (2026-07-01). email stays NULL (reserved for
		// future delivery; no capture UI). token_version powers immediate session
		// invalidation on password change/reset.
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS email TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS token_version INTEGER NOT NULL DEFAULT 0",
		// Forgot-password throttle clock. Independent of PASSWORD_LOG_KEY: the
		// 5-minute throttle reads THIS column, so it fires even when password-event
		// logging is disabled (no encryption key). Nullable: never reset = never set.
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS last_forgot_reset_at TIMESTAMPTZ",
		// Forgot-password OTP verification (2026-07-02). Step 1 emails a one-time
		// code; step 2 verifies it before any password is regenerated. Only the
		// SHA-256 hash of the code is stored, with its expiry and an attempt
		// counter that caps verification tries.
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS reset_otp_hash TEXT",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS reset_otp_expires_at TIMESTAMPTZ",
		"ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS reset_otp_attempts INTEGER NOT NULL DEFAULT 0",
		// Forgot-password (2026-07-01): widen the password_events.source CHECK to
		// admit the new 'forgot_reset' value. Drop+recreate is idempotent and does
		// not break existing rows (all existing sources remain in the widened set).
		"ALTER TABLE app.password_events DROP CONSTRAINT IF EXISTS password_events_source_check",
		"ALTER TABLE app.password_events ADD CONSTRAINT password_events_source_check CHECK (source IN ('create','self_change','admin_reset','forgot_reset'))",
		// Practice-question display title (2026-07-19). Nullable/optional: existing
		// rows read back as empty string; the UI falls back to an ordinal when unset.
		"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS title TEXT",
	}

	for _, q := range extraMigrations {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	// Enforce unique account emails (partial: NULL emails stay unconstrained).
	// Dup-scans first so pre-existing duplicates don't brick startup.
	if err := r.migrateUniqueEmailIndex(ctx); err != nil {
		return fmt.Errorf("email uniqueness migration failed: %w", err)
	}

	// Attach an auto-increment default to app.questions.id so questions created
	// without a client id no longer collide on id=0. Idempotent.
	if err := r.migrateQuestionsIdentity(ctx); err != nil {
		return fmt.Errorf("questions identity migration failed: %w", err)
	}

	// Practice-categories migration: add practice_questions.category_id, backfill
	// from the legacy free-text category strings, and enforce NOT NULL. Idempotent.
	if err := r.migratePracticeCategories(ctx); err != nil {
		return fmt.Errorf("practice category migration failed: %w", err)
	}

	// Drop orphaned per-student sandbox schemas left over from the pre-2026-05-07
	// per-student-per-exam clone model. Idempotent: a fresh database has none of
	// these schemas, so the loop is a no-op. Done before retiring the bookkeeping
	// table so we never lose the chance to enumerate them.
	orphanRows, err := r.pool.Query(ctx,
		"SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'student\\_%' ESCAPE '\\'")
	if err != nil {
		return fmt.Errorf("orphan sandbox enumeration failed: %w", err)
	}
	var orphanSchemas []string
	for orphanRows.Next() {
		var s string
		if err := orphanRows.Scan(&s); err != nil {
			orphanRows.Close()
			return err
		}
		orphanSchemas = append(orphanSchemas, s)
	}
	orphanRows.Close()
	for _, s := range orphanSchemas {
		safe := pgx.Identifier{s}.Sanitize()
		if _, err := r.pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safe)); err != nil {
			return fmt.Errorf("failed to drop orphan sandbox %s: %w", s, err)
		}
	}

	if _, err := r.pool.Exec(ctx, "DROP TABLE IF EXISTS app.student_sandboxes"); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}

// ensureCategorySchema creates the schema owned by a practice category (if
// missing) and grants the student roles: exam_student gets full DML (savepoint-
// rolled-back at evaluation time) and exam_student_readonly gets SELECT (used by
// the QUERY evaluation path). Idempotent.
func (r *PostgresRepository) ensureCategorySchema(ctx context.Context, categoryID int) error {
	schema := PracticeCategorySchemaName(categoryID)
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
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

// ensureExamCurriculumSchema creates the per-exam curriculum schema (if missing)
// and grants read-only access to exam_student_readonly. The exam_student role
// does NOT get DML here because exam DML runs in the per-exam sandbox schema
// (sandbox_exam_{id}), not in the curriculum — the curriculum is a frozen
// reference database that students SELECT from via the readonly evaluator path.
// Idempotent: safe to call repeatedly (e.g., before and after import).
func (r *PostgresRepository) ensureExamCurriculumSchema(ctx context.Context, examID int) error {
	schema := ExamCurriculumSchemaName(examID)
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
