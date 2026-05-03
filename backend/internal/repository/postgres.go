package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

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
// (e.g. the advisory-lock-based LockManager).
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

// CreateRestrictedUser creates a user with limited permissions
func (r *PostgresRepository) CreateRestrictedUser(ctx context.Context) error {
	// Create the role if it doesn't exist. We don't give it LOGIN permissions 
	// because we will use SET ROLE from the superuser connection.
	queries := []string{
		"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'exam_student') THEN CREATE ROLE exam_student; END IF; END $$",
		"REVOKE ALL ON SCHEMA public FROM exam_student",
		"GRANT USAGE ON SCHEMA public TO exam_student",
	}

	for _, q := range queries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("failed to setup restricted role: %w", err)
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

// ExecuteSchemaScript runs a full SQL script in the public schema
func (r *PostgresRepository) ExecuteSchemaScript(ctx context.Context, sql string) error {
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

// InitializeSandbox makes sure the per-student schema exists, has the correct
// permissions, and contains a copy of every public table. The call is idempotent:
// on repeat submissions only missing tables are cloned, so it is cheap to invoke
// before every request.
func (r *PostgresRepository) InitializeSandbox(ctx context.Context, schema string) error {
	if err := r.EnsureSchema(ctx, schema); err != nil {
		return err
	}
	// Granting first ensures ALTER DEFAULT PRIVILEGES is in place so any
	// freshly-created tables below automatically get exam_student GRANTs.
	if err := r.GrantSchemaPermissions(ctx, schema); err != nil {
		return err
	}
	return r.ensureTablesInSchema(ctx, schema)
}

// ResetTable rebuilds a single table inside a student schema from its public
// counterpart. It is meant to be called between submissions for DML questions
// so the previous answer's state changes don't leak into the next attempt.
// Sandbox tables don't carry FK constraints (LIKE … INCLUDING ALL doesn't copy
// foreign keys), so dropping a single table never cascades into siblings.
func (r *PostgresRepository) ResetTable(ctx context.Context, schema, table string) error {
	studentTbl := pgx.Identifier{schema, table}.Sanitize()
	publicTbl := pgx.Identifier{"public", table}.Sanitize()

	if _, err := r.pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", studentTbl)); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", studentTbl, publicTbl)); err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", studentTbl, publicTbl)); err != nil {
		return err
	}
	return nil
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
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS app.questions (
			id INTEGER PRIMARY KEY,
			exam_id INTEGER REFERENCES app.examinations(id),
			target_table TEXT NOT NULL,
			correct_sql TEXT NOT NULL,
			question_type TEXT NOT NULL,
			points DOUBLE PRECISION DEFAULT 10.0,
			strict_ordering BOOLEAN DEFAULT FALSE,
			strict_attributes BOOLEAN DEFAULT FALSE,
			strict_record_order BOOLEAN DEFAULT FALSE
		);`,
		`CREATE TABLE IF NOT EXISTS app.exam_students (
			exam_id INTEGER REFERENCES app.examinations(id),
			student_id INTEGER REFERENCES app.system_users(id),
			PRIMARY KEY (exam_id, student_id)
		);`,
		// Missing indexes for performance
		`CREATE INDEX IF NOT EXISTS idx_exam_students_student_id ON app.exam_students(student_id);`,
		`CREATE INDEX IF NOT EXISTS idx_questions_exam_id ON app.questions(exam_id);`,
	}

	for _, q := range queries {
		if _, err := r.pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
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

func (r *PostgresRepository) CreateQuestion(ctx context.Context, q models.Question) error {
	query := `
	INSERT INTO app.questions (id, exam_id, target_table, correct_sql, question_type, points, strict_ordering, strict_attributes, strict_record_order)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	ON CONFLICT (id) DO UPDATE SET
		exam_id = EXCLUDED.exam_id,
		target_table = EXCLUDED.target_table,
		correct_sql = EXCLUDED.correct_sql,
		question_type = EXCLUDED.question_type,
		points = EXCLUDED.points,
		strict_ordering = EXCLUDED.strict_ordering,
		strict_attributes = EXCLUDED.strict_attributes,
		strict_record_order = EXCLUDED.strict_record_order`

	_, err := r.pool.Exec(ctx, query, q.ID, q.ExamID, q.TargetTable, q.CorrectSQL, q.Type, q.Points,
		q.StrictMode.Ordering, q.StrictMode.Attributes, q.StrictMode.RecordOrder)
	return err
}

func (r *PostgresRepository) GetQuestion(ctx context.Context, id int) (models.Question, error) {
	query := `SELECT id, exam_id, target_table, correct_sql, question_type, points, strict_ordering, strict_attributes, strict_record_order FROM app.questions WHERE id = $1`
	
	var q models.Question
	err := r.pool.QueryRow(ctx, query, id).Scan(
		&q.ID, &q.ExamID, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Points,
		&q.StrictMode.Ordering, &q.StrictMode.Attributes, &q.StrictMode.RecordOrder)
	
	return q, err
}

func (r *PostgresRepository) CreateExamination(ctx context.Context, exam models.Examination) (int, error) {
	query := `INSERT INTO app.examinations (title, is_active, created_by) VALUES ($1, $2, $3) RETURNING id`
	var id int
	err := r.pool.QueryRow(ctx, query, exam.Title, exam.IsActive, exam.CreatedBy).Scan(&id)
	return id, err
}

func (r *PostgresRepository) SetExaminationActive(ctx context.Context, id int, active bool) error {
	query := `UPDATE app.examinations SET is_active = $1 WHERE id = $2`
	_, err := r.pool.Exec(ctx, query, active, id)
	return err
}

func (r *PostgresRepository) AddStudentsToExam(ctx context.Context, examID int, studentIDs []int) error {
	return r.WithTransaction(ctx, func(tx pgx.Tx) error {
		for _, studentID := range studentIDs {
			_, err := tx.Exec(ctx,
				"INSERT INTO app.exam_students (exam_id, student_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
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

	var exams []models.Examination
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

	var exams []models.Examination
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

func (r *PostgresRepository) GetQuestionsByExamID(ctx context.Context, examID int) ([]models.Question, error) {
	query := `SELECT id, exam_id, target_table, correct_sql, question_type, points, strict_ordering, strict_attributes, strict_record_order FROM app.questions WHERE exam_id = $1`
	
	rows, err := r.pool.Query(ctx, query, examID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var questions []models.Question
	for rows.Next() {
		var q models.Question
		err := rows.Scan(
			&q.ID, &q.ExamID, &q.TargetTable, &q.CorrectSQL, &q.Type, &q.Points,
			&q.StrictMode.Ordering, &q.StrictMode.Attributes, &q.StrictMode.RecordOrder)
		if err != nil {
			return nil, err
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

