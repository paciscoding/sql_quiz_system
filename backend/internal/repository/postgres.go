package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ErrExamActive is returned by mutating endpoints that are frozen while any
// examination is active. Controllers should map this to HTTP 409 Conflict.
var ErrExamActive = errors.New("examination is currently active")

// ErrCaptureNotReady is returned by ActivateExamination when a question's
// expected_result is not in 'captured' state. Controllers should map to 409.
var ErrCaptureNotReady = errors.New("expected result not captured for one or more questions")

// ErrEmptyExam is returned by ActivateExamination when the target exam has no
// questions. Controllers should map to 409.
var ErrEmptyExam = errors.New("examination has no questions")

// ErrCategoryNotFound is returned when a practice category id does not exist.
// Controllers should map this to HTTP 404 Not Found.
var ErrCategoryNotFound = errors.New("practice category not found")

// ErrCategoryHasQuestions is returned by DeletePracticeCategory (without force)
// when the category still owns practice questions. Controllers map this to 409.
var ErrCategoryHasQuestions = errors.New("practice category still has questions")

// ErrCategoryProtected is returned when attempting to delete the system
// "Uncategorized" fallback category. Controllers map this to 409.
var ErrCategoryProtected = errors.New("practice category cannot be deleted")

// ErrSchemaQualifiedName is returned by ImportSchemaIntoCategory when the
// uploaded script contains schema-qualified object names (e.g. public.foo),
// which would escape the category schema. Controllers map this to 400.
var ErrSchemaQualifiedName = errors.New("schema-qualified object name not allowed in category import")

// ErrCategoryNotEmpty is returned by ImportSchemaIntoCategory when the target
// category schema already contains tables and the caller did not confirm a
// destructive replace. Controllers map this to 409.
var ErrCategoryNotEmpty = errors.New("category schema already has tables; confirm replace or use a new category")

// ErrCategoryEmpty is returned at submit/preview time when a practice question's
// category schema has no tables imported yet. Controllers map this to 400.
var ErrCategoryEmpty = errors.New("category database is empty; ask your lecturer to import a database for this category")

// errCaptureRollback is the sentinel returned by CreateQuestion's capture tx
// after a successful capture, so WithTransaction rolls back the side effects
// of teacher SQL (DML rows). Recognised and discarded by the outer code.
var errCaptureRollback = errors.New("capture rollback")

// errCaptureFailed is the sentinel returned when teacher SQL aborts the capture
// tx (syntax error, missing table, etc.). It triggers WithTransaction's
// rollback path; the outer code persists the question row with
// expected_result_status = 'failed' and capture_error populated.
var errCaptureFailed = errors.New("capture failed")

// EffectiveState represents the computed runtime state of an examination.
type EffectiveState struct {
	Phase     string // "inactive" | "scheduled" | "active" | "ended"
	ActiveNow bool
}

// ExaminationEffectiveState computes the current effective state of an exam
// from its is_active flag and schedule timestamps. No DB calls.
//
//   - is_active=false                      -> inactive
//   - is_active=true, window not yet open  -> scheduled
//   - is_active=true, window open          -> active
//   - is_active=true, window closed        -> ended
func ExaminationEffectiveState(exam models.Examination) EffectiveState {
	if !exam.IsActive {
		return EffectiveState{Phase: "inactive", ActiveNow: false}
	}
	now := time.Now().UTC()
	windowOpen := exam.ScheduledStartAt == nil || !now.Before(*exam.ScheduledStartAt)
	windowAlive := exam.ScheduledEndAt == nil || now.Before(*exam.ScheduledEndAt)
	if windowOpen && windowAlive {
		return EffectiveState{Phase: "active", ActiveNow: true}
	}
	if !windowOpen {
		return EffectiveState{Phase: "scheduled", ActiveNow: false}
	}
	return EffectiveState{Phase: "ended", ActiveNow: false}
}

// PreviewResult carries the outcome of an EvaluatePreview call: the raw
// executor result (status "success" or "error"), the result rows capped to
// previewRowCap, the column names in projection order, the uncapped row count,
// and a truncation flag.
type PreviewResult struct {
	Result    models.ValidationResult
	Rows      []map[string]interface{}
	Columns   []string
	RowCount  int
	Truncated bool
}

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresRepository(connString string) (*PostgresRepository, error) {
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	// Pool Tuning for high-concurrency exams
	config.MaxConns = 50
	config.MinConns = 10
	config.MaxConnIdleTime = 30 * 60 // 30 mins

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %v", err)
	}

	// Test connection
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("unable to ping database: %v", err)
	}

	return &PostgresRepository{pool: pool}, nil
}

func (r *PostgresRepository) Close() {
	r.pool.Close()
}

// Pool exposes the underlying connection pool for components that need direct access
// to the pgxpool (e.g. for health checks or diagnostics).
func (r *PostgresRepository) Pool() *pgxpool.Pool {
	return r.pool
}

// Query executes a SELECT statement within a specific schema
func (r *PostgresRepository) Query(ctx context.Context, schema, sql string) ([]map[string]interface{}, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Set schema for this session
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", safeSchema)); err != nil {
		return nil, fmt.Errorf("failed to set search_path: %w", err)
	}

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		results = append(results, rowMap)
	}

	return results, rows.Err()
}

// Exec executes a DML statement within a specific schema
func (r *PostgresRepository) Exec(ctx context.Context, schema, sql string) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET search_path TO %s, public", safeSchema)); err != nil {
		return fmt.Errorf("failed to set search_path: %w", err)
	}

	_, err = conn.Exec(ctx, sql)
	return err
}

// GetTableState returns a string representation of the table state
func (r *PostgresRepository) GetTableState(ctx context.Context, schema, tableName string) (string, error) {
	safeTable := pgx.Identifier{tableName}.Sanitize()
	sql := fmt.Sprintf("SELECT * FROM %s", safeTable)
	results, err := r.Query(ctx, schema, sql)
	if err != nil {
		return "", err
	}

	b, err := json.Marshal(results)
	return string(b), err
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

// GrantSchemaPermissions gives the restricted user CRUD access to a schema
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
var schemaQualifiedNameRe = regexp.MustCompile(`(?i)\b(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|practice_cat_[0-9]+)\s*\.`)

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

// GetPublicTables returns the list of all tables in the public schema.
// System tables live in the dedicated `app` schema, so no exclusion list is needed.
func (r *PostgresRepository) GetPublicTables(ctx context.Context) ([]string, error) {
	query := `SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public'`
	rows, err := r.pool.Query(ctx, query)
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
	return tables, nil
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

// ensureTablesInSchema makes sure that every public table exists in the target schema.
// Tables that already exist are left untouched, so the call is cheap on repeat invocations.
// New tables are cloned with `LIKE public.x INCLUDING ALL` so primary keys, NOT NULL,
// defaults and indexes survive — `CREATE TABLE … AS SELECT` would have dropped them.
func (r *PostgresRepository) ensureTablesInSchema(ctx context.Context, schema string) error {
	tables, err := r.GetPublicTables(ctx)
	if err != nil {
		return err
	}

	for _, table := range tables {
		var exists bool
		err := r.pool.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM pg_catalog.pg_tables
				WHERE schemaname = $1 AND tablename = $2
			)`, schema, table).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		studentTbl := pgx.Identifier{schema, table}.Sanitize()
		publicTbl := pgx.Identifier{"public", table}.Sanitize()

		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", studentTbl, publicTbl)); err != nil {
			return fmt.Errorf("failed to clone structure of %s.%s: %w", schema, table, err)
		}
		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", studentTbl, publicTbl)); err != nil {
			return fmt.Errorf("failed to seed data into %s.%s: %w", schema, table, err)
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

// InitializeExamSandbox creates the shared per-exam sandbox schema and clones
// every public table into it once. Called from SetExaminationActive only when
// the exam has at least one DML question. The call is idempotent: missing
// tables are filled in, existing ones are left untouched. The bookkeeping row
// in app.exam_sandboxes is upserted so Phase 3 cleanup can find it later.
func (r *PostgresRepository) InitializeExamSandbox(ctx context.Context, examID int) error {
	schema := ExamSandboxName(examID)

	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	// Grant before cloning so ALTER DEFAULT PRIVILEGES applies to the new tables.
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
		return err
	}
	if err := r.ensureTablesInSchema(ctx, schema); err != nil {
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
	if err := r.ensureTablesInSchema(ctx, schema); err != nil {
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
	const schema = "sandbox_practice"

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
	if err := r.ensureTablesInSchema(ctx, schema); err != nil {
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
			target_table TEXT NOT NULL,
			correct_sql TEXT NOT NULL,
			question_type TEXT NOT NULL CHECK (question_type IN ('QUERY','DML')),
			requirement TEXT NOT NULL,
			points NUMERIC(6,2) NOT NULL DEFAULT 1.0,
			strict_ordering BOOLEAN NOT NULL DEFAULT false,
			strict_attrs BOOLEAN NOT NULL DEFAULT false,
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
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_system_users_student_number ON app.system_users(student_number) WHERE student_number IS NOT NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_system_users_student_id_ext ON app.system_users(student_id_ext) WHERE student_id_ext IS NOT NULL",
		"ALTER TABLE app.practice_categories ADD COLUMN IF NOT EXISTS introduction TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE app.examinations ADD COLUMN IF NOT EXISTS introduction TEXT NOT NULL DEFAULT ''",
	}

	for _, q := range extraMigrations {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
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

// WithTransaction executes a function within a database transaction
func (r *PostgresRepository) WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}

	err = fn(tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}

	return tx.Commit(ctx)
}

func (r *PostgresRepository) CreateUser(ctx context.Context, u models.User) (int, error) {
	query := `INSERT INTO app.system_users (username, password_hash, role, created_by) VALUES ($1, $2, $3, $4) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, u.Username, u.Password, string(u.Role), u.CreatedBy).Scan(&id)
	return id, err
}

func (r *PostgresRepository) UpdateUserPassword(ctx context.Context, id int, hashedPassword string) error {
	query := `UPDATE app.system_users SET password_hash = $1 WHERE id = $2`
	_, err := r.pool.Exec(ctx, query, hashedPassword, id)
	return err
}

func (r *PostgresRepository) GetUserByID(ctx context.Context, id int) (models.User, error) {
	query := `SELECT id, username, password_hash, role, created_by, created_at FROM app.system_users WHERE id = $1`
	var u models.User
	var role string
	err := r.pool.QueryRow(ctx, query, id).Scan(&u.ID, &u.Username, &u.Password, &role, &u.CreatedBy, &u.CreatedAt)
	u.Role = models.Role(role)
	return u, err
}

func (r *PostgresRepository) GetUserByUsername(ctx context.Context, username string) (models.User, error) {
	query := `SELECT id, username, password_hash, role, created_by, created_at FROM app.system_users WHERE username = $1`
	var u models.User
	var role string
	err := r.pool.QueryRow(ctx, query, username).Scan(&u.ID, &u.Username, &u.Password, &role, &u.CreatedBy, &u.CreatedAt)
	u.Role = models.Role(role)
	return u, err
}

func (r *PostgresRepository) GetAllUsers(ctx context.Context) ([]models.User, error) {
	query := `
		SELECT id, username, role, created_by, created_at,
		       student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name
		FROM app.system_users ORDER BY id ASC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		var roleStr string
		err := rows.Scan(
			&u.ID, &u.Username, &roleStr, &u.CreatedBy, &u.CreatedAt,
			&u.StudentNumber, &u.StudentIDExt, &u.SecLec, &u.SecLab, &u.FirstName, &u.LastName,
		)
		if err != nil {
			return nil, err
		}
		u.Role = models.Role(roleStr)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (r *PostgresRepository) UpdateUser(ctx context.Context, u models.User) error {
	query := `
		UPDATE app.system_users SET
			username = $1, role = $2, student_number = $3, student_id_ext = $4,
			sec_lec = $5, sec_lab = $6, first_name = $7, last_name = $8
		WHERE id = $9`

	_, err := r.pool.Exec(ctx, query,
		u.Username, u.Role, u.StudentNumber, u.StudentIDExt,
		u.SecLec, u.SecLab, u.FirstName, u.LastName, u.ID,
	)
	return err
}

func (r *PostgresRepository) DeleteUser(ctx context.Context, id int) error {
	query := `DELETE FROM app.system_users WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, id)
	return err
}

func (r *PostgresRepository) GetAllUsersByRole(ctx context.Context, role models.Role) ([]models.User, error) {
	query := `SELECT id, username, role, created_by, created_at, student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name FROM app.system_users WHERE role = $1 ORDER BY username`
	rows, err := r.pool.Query(ctx, query, string(role))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []models.User{}
	for rows.Next() {
		var u models.User
		var roleStr string
		err := rows.Scan(&u.ID, &u.Username, &roleStr, &u.CreatedBy, &u.CreatedAt, &u.StudentNumber, &u.StudentIDExt, &u.SecLec, &u.SecLab, &u.FirstName, &u.LastName)
		if err != nil {
			return nil, err
		}
		u.Role = models.Role(roleStr)
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpsertStudentByNumber inserts or updates a student profile keyed by
// student_number. The password hash is only used for the insert path.
func (r *PostgresRepository) UpsertStudentByNumber(ctx context.Context, tx pgx.Tx, in models.StudentImportRow, creatorID int) (outcome string, userID int, err error) {
	query := `
		INSERT INTO app.system_users (
			username, password_hash, role, created_by,
			student_number, student_id_ext, sec_lec, sec_lab, first_name, last_name
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (student_number) WHERE student_number IS NOT NULL DO UPDATE SET
			student_id_ext = EXCLUDED.student_id_ext,
			sec_lec = EXCLUDED.sec_lec,
			sec_lab = EXCLUDED.sec_lab,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name
		RETURNING id, (xmax = 0) AS inserted`

	var inserted bool
	err = tx.QueryRow(ctx, query,
		in.StudentNumber,
		in.PasswordHash,
		string(models.RoleStudent),
		creatorID,
		in.StudentNumber,
		in.StudentIDExt,
		in.SecLec,
		in.SecLab,
		in.FirstName,
		in.LastName,
	).Scan(&userID, &inserted)
	if err != nil {
		return "", 0, err
	}
	if inserted {
		return "created", userID, nil
	}
	return "updated", userID, nil
}

func (r *PostgresRepository) CreateQuestion(ctx context.Context, q models.Question) error {
	// Pre-flight: Check if exam is active
	if q.ExamID != nil {
		exam, err := r.GetExamByID(ctx, *q.ExamID)
		if err == nil && exam.IsActive {
			return fmt.Errorf("%w: cannot modify questions of an active examination", ErrExamActive)
		}
	}

	// Phase 0: Correct Result Capture
	status := "captured"
	var captureErr string
	var expectedResult, expectedCols []byte

	err := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		var results []map[string]interface{}
		var err error

		if q.Type == "QUERY" {
			results, err = r.queryWithTx(ctx, tx, "public", q.CorrectSQL)
		} else {
			if q.TargetTable == "" {
				return fmt.Errorf("target table is required for DML questions")
			}
			_, err = tx.Exec(ctx, q.CorrectSQL)
			if err == nil {
				results, err = r.queryWithTx(ctx, tx, "public", fmt.Sprintf("SELECT * FROM %s", q.TargetTable))
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
		if len(results) > 0 {
			cols := []string{}
			for k := range results[0] {
				cols = append(cols, k)
			}
			expectedCols, _ = json.Marshal(cols)
		}

		return errCaptureRollback
	})

	if err != nil && !errors.Is(err, errCaptureRollback) && !errors.Is(err, errCaptureFailed) {
		return err
	}

	query := `
	INSERT INTO app.questions (
		id, exam_id, target_table, correct_sql, question_type, points, requirement,
		strict_ordering, strict_attributes,
		expected_result, expected_columns, expected_result_status,
		expected_result_captured_at, capture_error, created_by, updated_at
	)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), $13, $14, NOW())
	ON CONFLICT (id) DO UPDATE SET
		exam_id = EXCLUDED.exam_id,
		target_table = EXCLUDED.target_table,
		correct_sql = EXCLUDED.correct_sql,
		question_type = EXCLUDED.question_type,
		points = EXCLUDED.points,
		requirement = EXCLUDED.requirement,
		strict_ordering = EXCLUDED.strict_ordering,
		strict_attributes = EXCLUDED.strict_attributes,
		expected_result = EXCLUDED.expected_result,
		expected_columns = EXCLUDED.expected_columns,
		expected_result_status = EXCLUDED.expected_result_status,
		expected_result_captured_at = NOW(),
		capture_error = EXCLUDED.capture_error,
		updated_at = NOW()`

	_, err = r.pool.Exec(ctx, query,
		q.ID, q.ExamID, q.TargetTable, q.CorrectSQL, q.Type, q.Points, q.Requirement,
		q.StrictMode.Ordering, q.StrictMode.Attributes,
		expectedResult, expectedCols, status, captureErr, q.CreatedBy)

	return err
}

// PreviewCorrectSQL dry-runs the lecturer's correct SQL inside a transaction
// that is always rolled back, so the lecturer can see the result before they
// commit to saving the question. For DML the post-image of target_table is
// returned. Public-schema state is never mutated.
func (r *PostgresRepository) PreviewCorrectSQL(ctx context.Context, qType, targetTable, sqlText string) ([]map[string]interface{}, error) {
	if sqlText == "" {
		return nil, fmt.Errorf("sql text is empty")
	}
	if qType == "DML" && targetTable == "" {
		return nil, fmt.Errorf("target_table is required for DML preview")
	}

	var results []map[string]interface{}
	previewErr := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL search_path TO public"); err != nil {
			return err
		}
		var err error
		if qType == "QUERY" {
			results, err = r.queryWithTx(ctx, tx, "public", sqlText)
		} else {
			if _, err = tx.Exec(ctx, sqlText); err != nil {
				return err
			}
			safeTable := pgx.Identifier{targetTable}.Sanitize()
			results, err = r.queryWithTx(ctx, tx, "public", fmt.Sprintf("SELECT * FROM %s", safeTable))
		}
		if err != nil {
			return err
		}
		// Roll back so public.* stays pristine for the next preview / capture.
		return errCaptureRollback
	})
	if previewErr != nil && !errors.Is(previewErr, errCaptureRollback) {
		return nil, previewErr
	}
	return results, nil
}

func (r *PostgresRepository) GetQuestion(ctx context.Context, id int) (models.Question, error) {
	query := `
		SELECT id, exam_id, target_table, correct_sql, question_type, points, requirement,
		       strict_ordering, strict_attributes,
		       expected_result, expected_columns, expected_result_status,
		       expected_result_captured_at, capture_error, created_by, created_at, updated_at
		FROM app.questions WHERE id = $1`

	var q models.Question
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.ExamID, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Points, &q.Requirement,
		&q.StrictMode.Ordering, &q.StrictMode.Attributes,
		&q.ExpectedResult, &q.ExpectedColumns, &q.ExpectedResultStatus,
		&q.ExpectedResultCapturedAt, &q.CaptureError, &q.CreatedBy, &q.CreatedAt, &q.UpdatedAt)

	return q, err
}

func (r *PostgresRepository) GetQuestionsByExamID(ctx context.Context, examID int) ([]models.Question, error) {
	query := `
		SELECT id, exam_id, target_table, correct_sql, question_type, points, requirement,
		       strict_ordering, strict_attributes,
		       expected_result, expected_columns, expected_result_status,
		       expected_result_captured_at, capture_error, created_by, created_at, updated_at
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
			&q.StrictMode.Ordering, &q.StrictMode.Attributes,
			&q.ExpectedResult, &q.ExpectedColumns, &q.ExpectedResultStatus,
			&q.ExpectedResultCapturedAt, &q.CaptureError, &q.CreatedBy, &q.CreatedAt, &q.UpdatedAt)
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

func (r *PostgresRepository) queryWithTx(ctx context.Context, tx pgx.Tx, schema, sql string) ([]map[string]interface{}, error) {
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	var results []map[string]interface{}

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		results = append(results, rowMap)
	}

	return results, rows.Err()
}

func (r *PostgresRepository) CreateExamination(ctx context.Context, exam models.Examination) (int, error) {
	query := `INSERT INTO app.examinations (title, is_active, created_by, introduction) VALUES ($1, $2, $3, $4) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, exam.Title, exam.IsActive, exam.CreatedBy, exam.Introduction).Scan(&id)
	return id, err
}

// UpdateExaminationIntroduction persists a new description text for an exam.
// Returns an error if the exam id does not exist.
func (r *PostgresRepository) UpdateExaminationIntroduction(ctx context.Context, id int, introduction string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET introduction = $1 WHERE id = $2`,
		introduction, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *PostgresRepository) SetExaminationActive(ctx context.Context, id int, active bool, scheduledStartAt *time.Time, scheduledEndAt *time.Time) error {
	if !active {
		return r.ArchiveExamination(ctx, id)
	}

	// Validate schedule timestamps
	if scheduledStartAt != nil && scheduledEndAt != nil && !scheduledEndAt.After(*scheduledStartAt) {
		return fmt.Errorf("scheduled_end_at must be after scheduled_start_at")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.questions WHERE exam_id = $1", id).Scan(&total); err != nil {
		return err
	}
	if total == 0 {
		return fmt.Errorf("%w: define at least one question before activating", ErrEmptyExam)
	}

	var notCaptured int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.questions WHERE exam_id = $1 AND expected_result_status != 'captured'", id).Scan(&notCaptured); err != nil {
		return err
	}
	if notCaptured > 0 {
		return fmt.Errorf("%w: %d question(s) have not successfully captured expected results", ErrCaptureNotReady, notCaptured)
	}

	// Instant mode: if no scheduled_start_at provided, set to now()
	if scheduledStartAt == nil {
		now := time.Now().UTC()
		scheduledStartAt = &now
	}

	query := `UPDATE app.examinations SET is_active = TRUE, activated_at = NOW(), archived_at = NULL, scheduled_start_at = $2, scheduled_end_at = $3 WHERE id = $1`
	if _, err := r.pool.Exec(ctx, query, id, scheduledStartAt, scheduledEndAt); err != nil {
		return err
	}

	// Lazy sandbox prep: only clone if the window is currently open and the exam has DML questions.
	examForState := models.Examination{IsActive: true, ScheduledStartAt: scheduledStartAt, ScheduledEndAt: scheduledEndAt}
	if state := ExaminationEffectiveState(examForState); state.ActiveNow {
		hasDML, err := r.examHasDMLQuestions(ctx, id)
		if err != nil {
			return err
		}
		if hasDML {
			if err := r.InitializeExamSandbox(ctx, id); err != nil {
				return fmt.Errorf("failed to initialize exam sandbox: %w", err)
			}
		}
	}
	return nil
}

// EnsureExamSandboxReady checks whether the exam sandbox already exists; if not,
// and the exam has DML questions, it calls InitializeExamSandbox.
// Used by GetExamQuestions to lazily prep the sandbox on first access.
func (r *PostgresRepository) EnsureExamSandboxReady(ctx context.Context, examID int) error {
	var createdAt *time.Time
	err := r.pool.QueryRow(ctx,
		"SELECT created_at FROM app.exam_sandboxes WHERE exam_id = $1 AND dropped_at IS NULL",
		examID).Scan(&createdAt)
	if err == nil && createdAt != nil {
		// Sandbox already exists
		return nil
	}
	// Check if the exam has DML questions
	hasDML, err := r.examHasDMLQuestions(ctx, examID)
	if err != nil {
		return err
	}
	if !hasDML {
		return nil
	}
	return r.InitializeExamSandbox(ctx, examID)
}

// SetScheduledEndAt updates only the scheduled_end_at timestamp for a given exam.
// Used by the "deactivate at a time" flow to set a future end without dropping the sandbox.
func (r *PostgresRepository) SetScheduledEndAt(ctx context.Context, examID int, scheduledEndAt *time.Time) error {
	_, err := r.pool.Exec(ctx,
		"UPDATE app.examinations SET scheduled_end_at = $1 WHERE id = $2",
		scheduledEndAt, examID)
	return err
}

func (r *PostgresRepository) ArchiveExamination(ctx context.Context, id int) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE app.examinations SET is_active = FALSE, archived_at = NOW() WHERE id = $1", id)
		if err != nil {
			return err
		}

		var schema string
		err = tx.QueryRow(ctx,
			"SELECT schema_name FROM app.exam_sandboxes WHERE exam_id = $1 AND dropped_at IS NULL",
			id).Scan(&schema)
		if err != nil {
			if err == pgx.ErrNoRows {
				// Pure-SELECT exam: no sandbox was ever created, nothing to drop.
				return nil
			}
			return err
		}

		safeSchema := pgx.Identifier{schema}.Sanitize()
		if _, err := tx.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safeSchema)); err != nil {
			fmt.Printf("Warning: failed to drop exam sandbox %s: %v\n", schema, err)
		}

		_, err = tx.Exec(ctx, "UPDATE app.exam_sandboxes SET dropped_at = NOW() WHERE exam_id = $1", id)
		return err
	})
}

func (r *PostgresRepository) AddStudentsToExam(ctx context.Context, examID int, studentIDs []int) error {
	exam, err := r.GetExamByID(ctx, examID)
	if err == nil && ExaminationEffectiveState(exam).ActiveNow {
		return fmt.Errorf("%w: cannot assign students to an active examination", ErrExamActive)
	}

	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, studentID := range studentIDs {
			_, err := tx.Exec(ctx,
				"INSERT INTO app.exam_students (exam_id, student_id, assigned_at) VALUES ($1, $2, NOW()) ON CONFLICT DO NOTHING",
				examID, studentID)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PostgresRepository) IsStudentEnrolledInExam(ctx context.Context, studentID, examID int) (bool, error) {
	var count int
	query := `SELECT COUNT(*) FROM app.exam_students WHERE exam_id = $1 AND student_id = $2`
	err := r.pool.QueryRow(ctx, query, examID, studentID).Scan(&count)
	return count > 0, err
}

func (r *PostgresRepository) GetExamByID(ctx context.Context, id int) (models.Examination, error) {
	query := `SELECT id, title, is_active, created_by, created_at, scheduled_start_at, scheduled_end_at, activated_at, archived_at, block_practice_during_exam, introduction FROM app.examinations WHERE id = $1`
	var e models.Examination
	err := r.pool.QueryRow(ctx, query, id).Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.BlockPracticeDuringExam, &e.Introduction)
	return e, err
}

func (r *PostgresRepository) GetAllExaminations(ctx context.Context) ([]models.Examination, error) {
	query := `SELECT id, title, is_active, created_by, created_at, scheduled_start_at, scheduled_end_at, activated_at, archived_at, block_practice_during_exam, introduction FROM app.examinations ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.BlockPracticeDuringExam, &e.Introduction)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, nil
}

func (r *PostgresRepository) GetStudentExams(ctx context.Context, studentID int) ([]models.Examination, error) {
	query := `
		SELECT e.id, e.title, e.is_active, e.created_by, e.created_at, e.scheduled_start_at, e.scheduled_end_at, e.activated_at, e.archived_at, e.introduction
		FROM app.examinations e
		JOIN app.exam_students es ON e.id = es.exam_id
		WHERE es.student_id = $1 AND e.is_active = TRUE`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.Introduction)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, rows.Err()
}

// GetExamMaxScores returns a map of exam title -> sum of question points for all examinations.
// Used by the Results tab to show earned/max denominators.
func (r *PostgresRepository) GetExamMaxScores(ctx context.Context) (map[string]float64, error) {
	query := `
		SELECT e.title, COALESCE(SUM(q.points), 0.0)
		FROM app.examinations e
		LEFT JOIN app.questions q ON q.exam_id = e.id
		GROUP BY e.id, e.title
		ORDER BY e.title`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var title string
		var maxPts float64
		if err := rows.Scan(&title, &maxPts); err != nil {
			return nil, err
		}
		result[title] = maxPts
	}
	return result, rows.Err()
}

// GetStudentEnrolledExams returns ALL exams a student is enrolled in, regardless of is_active status.
// Used by the waiting-page flow so students can select an exam before it opens.
func (r *PostgresRepository) GetStudentEnrolledExams(ctx context.Context, studentID int) ([]models.Examination, error) {
	query := `
		SELECT e.id, e.title, e.is_active, e.created_by, e.created_at, e.scheduled_start_at, e.scheduled_end_at, e.activated_at, e.archived_at, e.introduction
		FROM app.examinations e
		JOIN app.exam_students es ON e.id = es.exam_id
		WHERE es.student_id = $1
		ORDER BY e.created_at DESC`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt, &e.ScheduledStartAt, &e.ScheduledEndAt, &e.ActivatedAt, &e.ArchivedAt, &e.Introduction)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, rows.Err()
}

// GetExamRoster returns all students enrolled in an exam with their profile details.
func (r *PostgresRepository) GetExamRoster(ctx context.Context, examID int) ([]models.RosterStudent, error) {
	query := `
		SELECT su.id, su.username,
			COALESCE(su.student_number, ''),
			COALESCE(su.first_name, ''),
			COALESCE(su.last_name, ''),
			COALESCE(su.sec_lec, ''),
			COALESCE(su.sec_lab, ''),
			es.assigned_at
		FROM app.exam_students es
		JOIN app.system_users su ON es.student_id = su.id
		WHERE es.exam_id = $1
		ORDER BY su.student_number ASC NULLS LAST, su.username ASC`

	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roster []models.RosterStudent
	for rows.Next() {
		var s models.RosterStudent
		if err := rows.Scan(&s.StudentID, &s.Username, &s.StudentNumber, &s.FirstName, &s.LastName, &s.SecLec, &s.SecLab, &s.AssignedAt); err != nil {
			return nil, err
		}
		roster = append(roster, s)
	}
	if roster == nil {
		roster = []models.RosterStudent{}
	}
	return roster, rows.Err()
}

// RemoveStudentFromExam removes a student from an exam's roster.
// Returns ErrExamActive if the exam is effectively active.
func (r *PostgresRepository) RemoveStudentFromExam(ctx context.Context, examID, studentID int) error {
	exam, err := r.GetExamByID(ctx, examID)
	if err != nil {
		return err
	}
	if ExaminationEffectiveState(exam).ActiveNow {
		return fmt.Errorf("%w: cannot remove student while exam is effectively active", ErrExamActive)
	}
	_, err = r.pool.Exec(ctx,
		"DELETE FROM app.exam_students WHERE exam_id = $1 AND student_id = $2",
		examID, studentID)
	return err
}

// collectRows drains a pgx.Rows into a slice of column-keyed maps. The caller
// is responsible for closing the rows.
func collectRows(rows pgx.Rows) ([]map[string]interface{}, error) {
	defer rows.Close()
	fieldDescriptions := rows.FieldDescriptions()
	var out []map[string]interface{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		rowMap := make(map[string]interface{})
		for i, fd := range fieldDescriptions {
			rowMap[string(fd.Name)] = values[i]
		}
		out = append(out, rowMap)
	}
	return out, rows.Err()
}

// collectRowsWithColumns drains a pgx.Rows into a slice of column-keyed maps
// and returns both the data and the ordered column names. The caller is
// responsible for not using rows after this call returns.
func collectRowsWithColumns(rows pgx.Rows) (data []map[string]interface{}, columns []string, err error) {
	defer rows.Close()
	fds := rows.FieldDescriptions()
	columns = make([]string, len(fds))
	for i, fd := range fds {
		columns[i] = string(fd.Name)
	}
	for rows.Next() {
		values, vErr := rows.Values()
		if vErr != nil {
			return nil, nil, vErr
		}
		rowMap := make(map[string]interface{})
		for i, fd := range fds {
			rowMap[string(fd.Name)] = values[i]
		}
		data = append(data, rowMap)
	}
	return data, columns, rows.Err()
}

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
// Exam questions (ExamID != nil) always read the frozen public curriculum —
// their callers pass the DML sandbox name, but SELECT must stay on public (and
// the sandbox may not even exist for pure-SELECT exams), so this preserves the
// historic exam behavior byte-for-byte. Practice questions (ExamID == nil) read
// their category's own schema, which is what isolates practice from the exam DB.
func querySchemaFor(question models.Question, sandboxSchema string) string {
	if question.ExamID != nil {
		return "public"
	}
	if sandboxSchema == "" {
		return "public"
	}
	return sandboxSchema
}

func (r *PostgresRepository) evaluateQuery(ctx context.Context, question models.Question, studentSQL string, querySchema string) (models.ValidationResult, error) {
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
	safeQuerySchema := pgx.Identifier{querySchema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safeQuerySchema)); err != nil {
		return models.ValidationResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student_readonly"); err != nil {
		return models.ValidationResult{}, err
	}

	var studentResults []map[string]interface{}
	var studentErr error
	rows, queryErr := tx.Query(ctx, studentSQL)
	if queryErr != nil {
		studentErr = queryErr
	} else {
		studentResults, studentErr = collectRows(rows)
	}

	capturedJSON, _ := json.Marshal(studentResults)
	result := models.ValidationResult{
		Status:            "success",
		ValidationDetails: capturedJSON,
	}
	if studentErr != nil {
		result.Status = "error"
		result.Feedback = fmt.Sprintf("SQL Error: %v", studentErr)
	}
	return result, nil
}

func (r *PostgresRepository) evaluateDML(ctx context.Context, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
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
	var studentErr error

	if _, execErr := tx.Exec(ctx, studentSQL); execErr != nil {
		studentErr = execErr
	} else {
		safeTable := pgx.Identifier{question.TargetTable}.Sanitize()
		rows, queryErr := tx.Query(ctx, fmt.Sprintf("SELECT * FROM %s", safeTable))
		if queryErr != nil {
			studentErr = queryErr
		} else {
			studentResults, studentErr = collectRows(rows)
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

func (r *PostgresRepository) GetSubmissionsByExamID(ctx context.Context, examID int) ([]models.Submission, error) {
	query := `
		SELECT id, exam_id, student_id, question_id, sql_text, is_correct, score, validation_details, submitted_at
		FROM app.submissions WHERE exam_id = $1 ORDER BY submitted_at DESC`

	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var submissions []models.Submission
	for rows.Next() {
		var s models.Submission
		err := rows.Scan(
			&s.ID, &s.ExamID, &s.StudentID, &s.QuestionID, &s.SQLText, &s.IsCorrect, &s.Score, &s.ValidationDetails, &s.SubmittedAt)
		if err != nil {
			return nil, err
		}
		submissions = append(submissions, s)
	}
	return submissions, rows.Err()
}

func (r *PostgresRepository) CreateSubmission(ctx context.Context, s models.Submission) error {
	query := `
		INSERT INTO app.submissions (exam_id, student_id, question_id, sql_text, is_correct, score, validation_details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.pool.Exec(ctx, query, s.ExamID, s.StudentID, s.QuestionID, s.SQLText, s.IsCorrect, s.Score, s.ValidationDetails)
	return err
}

func (r *PostgresRepository) GetStudentExamResults(ctx context.Context, studentID, examID int) ([]models.StudentExamResult, error) {
	enrolled, err := r.IsStudentEnrolledInExam(ctx, studentID, examID)
	if err != nil {
		return nil, err
	}
	if !enrolled {
		return nil, errors.New("student not enrolled in exam")
	}

	query := `
		SELECT DISTINCT ON (s.question_id)
			s.question_id, q.requirement, q.question_type, q.points, s.sql_text, s.is_correct, s.score, s.submitted_at, s.validation_details
		FROM app.submissions s
		INNER JOIN app.questions q ON s.question_id = q.id
		WHERE s.student_id = $1 AND s.exam_id = $2
		ORDER BY s.question_id, s.submitted_at DESC
	`
	rows, err := r.pool.Query(ctx, query, studentID, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []models.StudentExamResult
	for rows.Next() {
		var res models.StudentExamResult
		err := rows.Scan(
			&res.QuestionID, &res.Requirement, &res.Type, &res.Points, &res.SQLText, &res.IsCorrect, &res.Score, &res.SubmittedAt, &res.ValidationDetails)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return results, rows.Err()
}

func (r *PostgresRepository) GetStudentResultsSummary(ctx context.Context, studentID int) ([]models.ExamResultSummary, error) {
	query := `
WITH latest_submissions AS (
    SELECT DISTINCT ON (question_id)
        question_id, exam_id, student_id, is_correct, score
    FROM app.submissions
    WHERE student_id = $1
    ORDER BY question_id, submitted_at DESC
),
exam_stats AS (
    SELECT 
        e.id AS exam_id,
        e.title AS exam_title,
        e.is_active,
        e.created_at,
        COUNT(q.id) AS question_count,
        COALESCE(SUM(q.points), 0) AS max_score,
        COUNT(ls.question_id) AS answered_count,
        COUNT(ls.question_id) FILTER (WHERE ls.is_correct) AS correct_count,
        COALESCE(SUM(ls.score), 0) AS total_score
    FROM app.examinations e
    JOIN app.exam_students es ON e.id = es.exam_id
    LEFT JOIN app.questions q ON e.id = q.exam_id
    LEFT JOIN latest_submissions ls ON q.id = ls.question_id AND ls.student_id = $1
    WHERE es.student_id = $1
    GROUP BY e.id, e.title, e.is_active, e.created_at
)
SELECT 
    exam_id, exam_title, is_active,
    question_count, answered_count, correct_count, total_score, max_score
FROM exam_stats
ORDER BY created_at DESC
	`
	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []models.ExamResultSummary
	for rows.Next() {
		var s models.ExamResultSummary
		err := rows.Scan(
			&s.ExamID, &s.ExamTitle, &s.IsActive, &s.QuestionCount, &s.AnsweredCount, &s.CorrectCount, &s.TotalScore, &s.MaxScore)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

// ─── Practice Questions ───────────────────────────────────────────────────────

func (r *PostgresRepository) CreatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	// Resolve the category's owned schema; the correct-result capture and all
	// student submissions for this question run against it (not public).
	schema, err := r.GetPracticeCategorySchema(ctx, q.CategoryID)
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
			points, strict_ordering, strict_attrs,
			expected_result, expected_columns, expected_result_status,
			capture_error, expected_result_captured_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW(), NOW())
		RETURNING id, created_at, updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CreatedBy, q.CategoryID, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs,
		expectedResult, expectedCols, status, captureErr,
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
		var err error
		if q.Type == "QUERY" {
			results, err = r.queryWithTx(ctx, tx, schema, q.CorrectSQL)
		} else {
			if q.TargetTable == "" {
				return fmt.Errorf("target table is required for DML questions")
			}
			_, err = tx.Exec(ctx, q.CorrectSQL)
			if err == nil {
				results, err = r.queryWithTx(ctx, tx, schema, fmt.Sprintf("SELECT * FROM %s", q.TargetTable))
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
		if len(results) > 0 {
			cols := []string{}
			for k := range results[0] {
				cols = append(cols, k)
			}
			*expectedCols, _ = json.Marshal(cols)
		}
		return errCaptureRollback
	})
}

func (r *PostgresRepository) UpdatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	schema, err := r.GetPracticeCategorySchema(ctx, q.CategoryID)
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
		    points=$6, strict_ordering=$7, strict_attrs=$8,
		    expected_result=$9, expected_columns=$10,
		    expected_result_status=$11, capture_error=$12,
		    expected_result_captured_at=NOW(), updated_at=NOW()
		WHERE id=$13
		RETURNING updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CategoryID, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs,
		expectedResult, expectedCols, status, captureErr, q.ID,
	).Scan(&q.UpdatedAt)
}

func (r *PostgresRepository) ListPracticeQuestions(ctx context.Context) ([]models.PracticeQuestion, error) {
	query := `
		SELECT pq.id, pq.created_by, pq.category, COALESCE(pq.category_id, 0),
		       COALESCE(pc.name, ''), COALESCE(pc.schema_name, ''),
		       pq.target_table, pq.correct_sql, pq.question_type, pq.requirement, pq.points,
		       pq.strict_ordering, pq.strict_attrs, pq.expected_result, pq.expected_columns,
		       pq.expected_result_status, pq.capture_error, pq.expected_result_captured_at,
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
			&q.StrictOrdering, &q.StrictAttrs, &q.ExpectedResult, &q.ExpectedColumns,
			&q.ExpectedResultStatus, &q.CaptureError, &q.ExpectedResultCapturedAt,
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
		       pq.strict_ordering, pq.strict_attrs, pq.expected_result, pq.expected_columns,
		       pq.expected_result_status, pq.capture_error, pq.expected_result_captured_at,
		       pq.created_at, pq.updated_at
		FROM app.practice_questions pq
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		WHERE pq.id=$1`

	var q models.PracticeQuestion
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.CreatedBy, &q.Category, &q.CategoryID, &q.CategoryName, &q.CategorySchema,
		&q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
		&q.StrictOrdering, &q.StrictAttrs, &q.ExpectedResult, &q.ExpectedColumns,
		&q.ExpectedResultStatus, &q.CaptureError, &q.ExpectedResultCapturedAt,
		&q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

func (r *PostgresRepository) GetConsolidatedScores(ctx context.Context) ([]models.ConsolidatedScore, error) {
	// 1. Fetch all students with their basic info
	queryStudents := `
		SELECT id, username, COALESCE(student_number, ''), COALESCE(first_name, ''), 
		       COALESCE(last_name, ''), COALESCE(sec_lec, ''), COALESCE(sec_lab, '')
		FROM app.system_users
		WHERE role = 'STUDENT'
		ORDER BY student_number ASC, username ASC`

	rows, err := r.pool.Query(ctx, queryStudents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scoresMap := make(map[int]*models.ConsolidatedScore)
	var studentIDs []int

	for rows.Next() {
		var s models.ConsolidatedScore
		err := rows.Scan(&s.StudentID, &s.Username, &s.StudentNumber, &s.FirstName, &s.LastName, &s.SecLec, &s.SecLab)
		if err != nil {
			return nil, err
		}
		s.ExamScores = make(map[string]float64)
		scoresMap[s.StudentID] = &s
		studentIDs = append(studentIDs, s.StudentID)
	}

	if len(studentIDs) == 0 {
		return []models.ConsolidatedScore{}, nil
	}

	// 2. Fetch exam scores (latest attempt per question, summed per exam)
	queryExamScores := `
		WITH LatestSubmissions AS (
			SELECT DISTINCT ON (student_id, question_id) 
			       student_id, exam_id, score
			FROM app.submissions
			ORDER BY student_id, question_id, submitted_at DESC
		)
		SELECT ls.student_id, e.title, SUM(ls.score)
		FROM LatestSubmissions ls
		JOIN app.examinations e ON ls.exam_id = e.id
		GROUP BY ls.student_id, e.title`

	examRows, err := r.pool.Query(ctx, queryExamScores)
	if err != nil {
		return nil, err
	}
	defer examRows.Close()

	for examRows.Next() {
		var studentID int
		var examTitle string
		var totalScore float64
		if err := examRows.Scan(&studentID, &examTitle, &totalScore); err != nil {
			return nil, err
		}
		if s, ok := scoresMap[studentID]; ok {
			s.ExamScores[examTitle] = totalScore
			s.GrandTotal += totalScore
		}
	}

	// 3. Fetch practice scores (best attempt per question)
	queryPracticeScores := `
		SELECT student_id, SUM(points)
		FROM (
			SELECT DISTINCT ON (student_id, practice_question_id)
			       student_id, points
			FROM app.practice_attempts pa
			JOIN app.practice_questions pq ON pa.practice_question_id = pq.id
			WHERE pa.is_correct = true
			ORDER BY student_id, practice_question_id, submitted_at ASC
		) BestPractice
		GROUP BY student_id`

	practiceRows, err := r.pool.Query(ctx, queryPracticeScores)
	if err != nil {
		return nil, err
	}
	defer practiceRows.Close()

	for practiceRows.Next() {
		var studentID int
		var totalPoints float64
		if err := practiceRows.Scan(&studentID, &totalPoints); err != nil {
			return nil, err
		}
		if s, ok := scoresMap[studentID]; ok {
			s.PracticeTotal = totalPoints
			s.GrandTotal += totalPoints
		}
	}

	// Flatten map to slice
	result := make([]models.ConsolidatedScore, 0, len(scoresMap))
	for _, id := range studentIDs {
		result = append(result, *scoresMap[id])
	}
	return result, nil
}

func (r *PostgresRepository) GetPracticeSummary(ctx context.Context, studentID int) ([]models.PracticeSummary, error) {
	query := `
		SELECT
			pq.id, COALESCE(pq.category_id, 0), COALESCE(pc.name, pq.category),
			pq.category, pq.requirement, pq.points,
			COALESCE(bool_or(pa.is_correct), false) as is_solved,
			count(pa.id) as attempt_count
		FROM app.practice_questions pq
		LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
		LEFT JOIN app.practice_attempts pa ON pq.id = pa.practice_question_id AND pa.student_id = $1
		GROUP BY pq.id, pq.category_id, pc.name, pq.category, pq.requirement, pq.points
		ORDER BY COALESCE(pc.name, pq.category) ASC, pq.id ASC`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []models.PracticeSummary
	for rows.Next() {
		var s models.PracticeSummary
		err := rows.Scan(&s.QuestionID, &s.CategoryID, &s.CategoryName, &s.Category, &s.Requirement, &s.Points, &s.IsSolved, &s.AttemptCount)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
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

// ─── Exam Status Probe ────────────────────────────────────────────────────────

func (r *PostgresRepository) IsAnyExamActive(ctx context.Context) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.examinations WHERE is_active = true)`).Scan(&active)
	return active, err
}

// IsPracticeFrozen reports whether practice should be blocked right now: true
// only when at least one ACTIVE examination has its block_practice_during_exam
// toggle on. With the toggle off (the default), practice stays live during
// exams — the payoff of giving each practice category its own schema.
func (r *PostgresRepository) IsPracticeFrozen(ctx context.Context) (bool, error) {
	var frozen bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM app.examinations WHERE is_active = true AND block_practice_during_exam = true)`).Scan(&frozen)
	return frozen, err
}

// SetExamPracticeFreeze flips the per-exam practice-freeze toggle.
func (r *PostgresRepository) SetExamPracticeFreeze(ctx context.Context, examID int, block bool) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE app.examinations SET block_practice_during_exam = $1 WHERE id = $2`, block, examID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("examination %d not found", examID)
	}
	return nil
}

// ─── Practice Category Intro Images ──────────────────────────────────────────

// ListCategoryIntroImages returns the metadata rows for all images attached to a
// practice category, ordered by (sort_order, id). image_bytes is excluded.
func (r *PostgresRepository) ListCategoryIntroImages(ctx context.Context, catID int) ([]models.IntroImage, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, category_id AS owner_id, sort_order, mime_type, byte_size, created_at, updated_at
		 FROM app.practice_category_intro_images
		 WHERE category_id = $1
		 ORDER BY sort_order, id`, catID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	imgs := []models.IntroImage{}
	for rows.Next() {
		var img models.IntroImage
		if err := rows.Scan(&img.ID, &img.OwnerID, &img.SortOrder, &img.MimeType, &img.ByteSize,
			&img.CreatedAt, &img.UpdatedAt); err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, rows.Err()
}

// GetCategoryIntroImageBytes returns the raw bytes, mime type, and updated_at
// timestamp for a specific image belonging to catID. Returns pgx.ErrNoRows if
// the image does not exist or does not belong to catID.
func (r *PostgresRepository) GetCategoryIntroImageBytes(ctx context.Context, catID, imgID int) (mime string, data []byte, updatedAt time.Time, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT mime_type, image_bytes, updated_at
		 FROM app.practice_category_intro_images
		 WHERE id = $1 AND category_id = $2`, imgID, catID).Scan(&mime, &data, &updatedAt)
	return
}

// InsertCategoryIntroImage appends a new image to a practice category. The
// sort_order is set to MAX(sort_order)+1 (or 0 for the first image) inside a
// single transaction. Returns the full metadata row on success.
func (r *PostgresRepository) InsertCategoryIntroImage(ctx context.Context, catID int, mime string, data []byte) (models.IntroImage, error) {
	var img models.IntroImage
	err := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		var nextOrder int
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(sort_order), -1)+1
			 FROM app.practice_category_intro_images
			 WHERE category_id = $1`, catID).Scan(&nextOrder); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO app.practice_category_intro_images
			   (category_id, sort_order, mime_type, byte_size, image_bytes)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, category_id, sort_order, mime_type, byte_size, created_at, updated_at`,
			catID, nextOrder, mime, len(data), data).Scan(
			&img.ID, &img.OwnerID, &img.SortOrder, &img.MimeType, &img.ByteSize,
			&img.CreatedAt, &img.UpdatedAt)
	})
	return img, err
}

// DeleteCategoryIntroImage removes a single image by (id, category_id) so a
// wrong category scope cannot cross-delete. Returns pgx.ErrNoRows if not found.
func (r *PostgresRepository) DeleteCategoryIntroImage(ctx context.Context, catID, imgID int) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM app.practice_category_intro_images WHERE id = $1 AND category_id = $2`,
		imgID, catID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ReorderCategoryIntroImages updates sort_order for each (id, sort_order) pair
// in order. All ids must belong to catID; the operation is rejected and rolled
// back if any id is foreign to the category.
func (r *PostgresRepository) ReorderCategoryIntroImages(ctx context.Context, catID int, order []struct{ ID, SortOrder int }) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, item := range order {
			tag, err := tx.Exec(ctx,
				`UPDATE app.practice_category_intro_images
				 SET sort_order = $1, updated_at = now()
				 WHERE id = $2 AND category_id = $3`,
				item.SortOrder, item.ID, catID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("image %d not found in category %d", item.ID, catID)
			}
		}
		return nil
	})
}

// CountCategoryIntroImageBytes returns the total stored byte_size for all images
// in a practice category. Used for the 25 MB aggregate soft cap.
func (r *PostgresRepository) CountCategoryIntroImageBytes(ctx context.Context, catID int) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(byte_size), 0)
		 FROM app.practice_category_intro_images
		 WHERE category_id = $1`, catID).Scan(&total)
	return total, err
}

// ─── Examination Intro Images ─────────────────────────────────────────────────

// ListExamIntroImages returns the metadata rows for all images attached to an
// examination, ordered by (sort_order, id). image_bytes is excluded.
func (r *PostgresRepository) ListExamIntroImages(ctx context.Context, examID int) ([]models.IntroImage, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, examination_id AS owner_id, sort_order, mime_type, byte_size, created_at, updated_at
		 FROM app.examination_intro_images
		 WHERE examination_id = $1
		 ORDER BY sort_order, id`, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	imgs := []models.IntroImage{}
	for rows.Next() {
		var img models.IntroImage
		if err := rows.Scan(&img.ID, &img.OwnerID, &img.SortOrder, &img.MimeType, &img.ByteSize,
			&img.CreatedAt, &img.UpdatedAt); err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	return imgs, rows.Err()
}

// GetExamIntroImageBytes returns the raw bytes, mime type, and updated_at
// timestamp for a specific image belonging to examID. Returns pgx.ErrNoRows if
// the image does not exist or does not belong to examID.
func (r *PostgresRepository) GetExamIntroImageBytes(ctx context.Context, examID, imgID int) (mime string, data []byte, updatedAt time.Time, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT mime_type, image_bytes, updated_at
		 FROM app.examination_intro_images
		 WHERE id = $1 AND examination_id = $2`, imgID, examID).Scan(&mime, &data, &updatedAt)
	return
}

// InsertExamIntroImage appends a new image to an examination. sort_order is
// MAX(sort_order)+1 (or 0) inside a transaction. Returns full metadata on success.
func (r *PostgresRepository) InsertExamIntroImage(ctx context.Context, examID int, mime string, data []byte) (models.IntroImage, error) {
	var img models.IntroImage
	err := r.WithTransaction(ctx, func(tx pgx.Tx) error {
		var nextOrder int
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(sort_order), -1)+1
			 FROM app.examination_intro_images
			 WHERE examination_id = $1`, examID).Scan(&nextOrder); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO app.examination_intro_images
			   (examination_id, sort_order, mime_type, byte_size, image_bytes)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id, examination_id, sort_order, mime_type, byte_size, created_at, updated_at`,
			examID, nextOrder, mime, len(data), data).Scan(
			&img.ID, &img.OwnerID, &img.SortOrder, &img.MimeType, &img.ByteSize,
			&img.CreatedAt, &img.UpdatedAt)
	})
	return img, err
}

// DeleteExamIntroImage removes a single image by (id, examination_id) so a
// wrong exam scope cannot cross-delete. Returns pgx.ErrNoRows if not found.
func (r *PostgresRepository) DeleteExamIntroImage(ctx context.Context, examID, imgID int) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM app.examination_intro_images WHERE id = $1 AND examination_id = $2`,
		imgID, examID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ReorderExamIntroImages updates sort_order for each (id, sort_order) pair in
// order. All ids must belong to examID; rolled back on any foreign id.
func (r *PostgresRepository) ReorderExamIntroImages(ctx context.Context, examID int, order []struct{ ID, SortOrder int }) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, item := range order {
			tag, err := tx.Exec(ctx,
				`UPDATE app.examination_intro_images
				 SET sort_order = $1, updated_at = now()
				 WHERE id = $2 AND examination_id = $3`,
				item.SortOrder, item.ID, examID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("image %d not found in examination %d", item.ID, examID)
			}
		}
		return nil
	})
}

// CountExamIntroImageBytes returns the total stored byte_size for all images
// in an examination. Used for the 25 MB aggregate soft cap.
func (r *PostgresRepository) CountExamIntroImageBytes(ctx context.Context, examID int) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(byte_size), 0)
		 FROM app.examination_intro_images
		 WHERE examination_id = $1`, examID).Scan(&total)
	return total, err
}
