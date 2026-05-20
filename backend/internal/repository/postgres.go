package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// errCaptureRollback is the sentinel returned by CreateQuestion's capture tx
// after a successful capture, so WithTransaction rolls back the side effects
// of teacher SQL (DML rows). Recognised and discarded by the outer code.
var errCaptureRollback = errors.New("capture rollback")

// errCaptureFailed is the sentinel returned when teacher SQL aborts the capture
// tx (syntax error, missing table, etc.). It triggers WithTransaction's
// rollback path; the outer code persists the question row with
// expected_result_status = 'failed' and capture_error populated.
var errCaptureFailed = errors.New("capture failed")

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
// sandbox stay valid.
func (r *PostgresRepository) ExecuteSchemaScript(ctx context.Context, sql string) error {
	var activeCount int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM app.examinations WHERE is_active = TRUE").Scan(&activeCount); err != nil {
		return err
	}
	if activeCount > 0 {
		return fmt.Errorf("%w: cannot import schema while %d examination(s) are active", ErrExamActive, activeCount)
	}

	_, err := r.pool.Exec(ctx, sql)
	return err
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
			archived_at TIMESTAMP WITH TIME ZONE
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
	}

	for _, q := range extraMigrations {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
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
	query := `INSERT INTO app.examinations (title, is_active, created_by) VALUES ($1, $2, $3) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, exam.Title, exam.IsActive, exam.CreatedBy).Scan(&id)
	return id, err
}

func (r *PostgresRepository) SetExaminationActive(ctx context.Context, id int, active bool) error {
	if !active {
		return r.ArchiveExamination(ctx, id)
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

	hasDML, err := r.examHasDMLQuestions(ctx, id)
	if err != nil {
		return err
	}
	if hasDML {
		if err := r.InitializeExamSandbox(ctx, id); err != nil {
			return fmt.Errorf("failed to initialize exam sandbox: %w", err)
		}
	}

	query := `UPDATE app.examinations SET is_active = TRUE, activated_at = NOW(), archived_at = NULL WHERE id = $1`
	_, err = r.pool.Exec(ctx, query, id)
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
	if err == nil && exam.IsActive {
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
	query := `SELECT id, title, is_active, created_by, created_at FROM app.examinations WHERE id = $1`
	var e models.Examination
	err := r.pool.QueryRow(ctx, query, id).Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt)
	return e, err
}

func (r *PostgresRepository) GetAllExaminations(ctx context.Context) ([]models.Examination, error) {
	query := `SELECT id, title, is_active, created_by, created_at FROM app.examinations ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	exams := []models.Examination{}
	for rows.Next() {
		var e models.Examination
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, nil
}

func (r *PostgresRepository) GetStudentExams(ctx context.Context, studentID int) ([]models.Examination, error) {
	query := `
		SELECT e.id, e.title, e.is_active, e.created_by, e.created_at
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
		err := rows.Scan(&e.ID, &e.Title, &e.IsActive, &e.CreatedBy, &e.CreatedAt)
		if err != nil {
			return nil, err
		}
		exams = append(exams, e)
	}
	return exams, rows.Err()
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
		return r.evaluateQuery(ctx, question, studentSQL)
	}
	return r.evaluateDML(ctx, question, studentSQL, sandboxSchema)
}

func (r *PostgresRepository) evaluateQuery(ctx context.Context, question models.Question, studentSQL string) (models.ValidationResult, error) {
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

	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO public"); err != nil {
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
		return r.evaluateQueryPreview(ctx, question, studentSQL)
	}
	return r.evaluateDMLPreview(ctx, question, studentSQL, sandboxSchema)
}

func (r *PostgresRepository) evaluateQueryPreview(ctx context.Context, question models.Question, studentSQL string) (PreviewResult, error) {
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

	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO public"); err != nil {
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
	// Phase-0: Correct Result Capture (always rolled back)
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
		INSERT INTO app.practice_questions (
			created_by, category, target_table, correct_sql, question_type, requirement,
			points, strict_ordering, strict_attrs,
			expected_result, expected_columns, expected_result_status,
			capture_error, expected_result_captured_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW(), NOW())
		RETURNING id, created_at, updated_at`

	return r.pool.QueryRow(ctx, query,
		q.CreatedBy, q.Category, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs,
		expectedResult, expectedCols, status, captureErr,
	).Scan(&q.ID, &q.CreatedAt, &q.UpdatedAt)
}

func (r *PostgresRepository) UpdatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error {
	// Phase-0: Correct Result Capture (always rolled back)
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
		UPDATE app.practice_questions
		SET category=$1, target_table=$2, correct_sql=$3, question_type=$4, requirement=$5,
		    points=$6, strict_ordering=$7, strict_attrs=$8,
		    expected_result=$9, expected_columns=$10,
		    expected_result_status=$11, capture_error=$12,
		    expected_result_captured_at=NOW(), updated_at=NOW()
		WHERE id=$13
		RETURNING updated_at`

	return r.pool.QueryRow(ctx, query,
		q.Category, q.TargetTable, q.CorrectSQL, q.Type, q.Requirement,
		q.Points, q.StrictOrdering, q.StrictAttrs,
		expectedResult, expectedCols, status, captureErr, q.ID,
	).Scan(&q.UpdatedAt)
}

func (r *PostgresRepository) ListPracticeQuestions(ctx context.Context) ([]models.PracticeQuestion, error) {
	query := `
		SELECT id, created_by, category, target_table, correct_sql, question_type, requirement, points,
		       strict_ordering, strict_attrs, expected_result, expected_columns,
		       expected_result_status, capture_error, expected_result_captured_at,
		       created_at, updated_at
		FROM app.practice_questions ORDER BY id ASC`

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	questions := []models.PracticeQuestion{}
	for rows.Next() {
		var q models.PracticeQuestion
		err := rows.Scan(
			&q.ID, &q.CreatedBy, &q.Category, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
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
		SELECT id, created_by, category, target_table, correct_sql, question_type, requirement, points,
		       strict_ordering, strict_attrs, expected_result, expected_columns,
		       expected_result_status, capture_error, expected_result_captured_at,
		       created_at, updated_at
		FROM app.practice_questions WHERE id=$1`

	var q models.PracticeQuestion
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.CreatedBy, &q.Category, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Requirement, &q.Points,
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
			pq.id, pq.category, pq.requirement, pq.points,
			COALESCE(bool_or(pa.is_correct), false) as is_solved,
			count(pa.id) as attempt_count
		FROM app.practice_questions pq
		LEFT JOIN app.practice_attempts pa ON pq.id = pa.practice_question_id AND pa.student_id = $1
		GROUP BY pq.id, pq.category, pq.requirement, pq.points
		ORDER BY pq.category ASC, pq.id ASC`

	rows, err := r.pool.Query(ctx, query, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []models.PracticeSummary
	for rows.Next() {
		var s models.PracticeSummary
		err := rows.Scan(&s.QuestionID, &s.Category, &s.Requirement, &s.Points, &s.IsSolved, &s.AttemptCount)
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
