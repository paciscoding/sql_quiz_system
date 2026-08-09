package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/schemagate"
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

// ErrExamCurriculumNotEmpty is returned by ImportSchemaIntoExamCurriculum when
// the target exam curriculum schema already contains tables and the caller did
// not pass confirm_replace=true. Controllers map this to 409 Conflict.
var ErrExamCurriculumNotEmpty = errors.New("exam curriculum schema already has tables; confirm replace or use a new import")

// ErrExamCategoryNotFound is returned when an exam category id does not exist
// (or does not belong to the exam named in the request path). Controllers
// should map this to HTTP 404 Not Found.
var ErrExamCategoryNotFound = errors.New("exam category not found")

// ErrExamCategoryNameTaken is returned by CreateExamCategory when the exam
// already has a category with that name (UNIQUE (exam_id, name)). Controllers
// map this to 409 Conflict.
var ErrExamCategoryNameTaken = errors.New("exam category name already in use for this exam")

// ErrExamCategoryHasQuestions is returned by DeleteExamCategory when the
// category still has one or more attached questions. Per ARCH-036 (overriding
// ARCH-029's original force-detach rule), there is no force escape hatch:
// callers must reassign or delete the questions first. Controllers map this
// to 409 Conflict.
var ErrExamCategoryHasQuestions = errors.New("exam category still has attached questions")

// ErrExamCategoryNotEmpty is returned by ImportSchemaIntoExamCategory (and its
// preview twin) when the target category schema already contains tables and
// the caller did not pass confirm_replace=true. Controllers map this to 409.
var ErrExamCategoryNotEmpty = errors.New("exam category schema already has tables; confirm replace or use a new category")

// ErrExamCategoryMismatch is returned when a question's category_id refers to
// a category that does not belong to the question's own exam. Controllers map
// this to HTTP 400 Bad Request.
var ErrExamCategoryMismatch = errors.New("exam category does not belong to this examination")

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

	// studentPools holds one restricted connection pool per privilege class,
	// keyed by sandbox-role name (ARCH-022): "exam_student_readonly",
	// "exam_student", "exam_student_preview_readonly", "exam_student_preview".
	// Each pool's LOGIN role is a non-superuser that owns nothing and is a
	// member of exactly that one sandbox role, so `SET ROLE postgres` /
	// `RESET ROLE` issued from inside student SQL land on a zero-privilege role
	// instead of the superuser session_user that `pool` connects as. Populated
	// once at boot by InitStudentPools; nil until then. poolForRole reads it and
	// NEVER falls back to the privileged `pool` above. See
	// postgres_student_pool.go for the full rationale and construction.
	studentPools map[string]*pgxpool.Pool

	// dmlGate serialises DML evaluation per execution schema, in-process
	// (ARCH-058/059). Practice DML has no SubmissionQueue and
	// acquireSchemaEvalLock is taken SHARED, so without this two evaluations
	// whose seeds touch the same tables in opposite order deadlock with
	// SQLSTATE 40P01 and a student is told their question is broken. See
	// postgres_practice_concurrent_dml_test.go.
	//
	// A value, not a pointer: schemagate.SchemaGate's zero value is usable, so
	// NewPostgresRepository needs no change and no construction path can
	// produce a repository with a nil gate.
	dmlGate schemagate.SchemaGate
}

func NewPostgresRepository(connString string) (*PostgresRepository, error) {
	ctx := context.Background()

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}

	// Pin the QUERY-path exec mode to the extended protocol so a
	// "?default_query_exec_mode=simple_protocol" parameter in the DSN cannot
	// downgrade tx.Query to the simple protocol, which accepts multiple
	// statements per call (e.g. "RESET ROLE; SELECT ..."). This deliberately
	// does NOT affect the argument-less multi-statement tx.Exec used by the
	// lecturer schema-import sites: pgx always forces simple protocol for
	// Exec calls with no arguments regardless of this setting (conn.go:515),
	// so whole-file schema imports keep working unchanged.
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement

	// Pool Tuning for high-concurrency exams
	config.MaxConns = 20
	config.MinConns = 10
	config.MaxConnIdleTime = 30 * time.Minute

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
	for _, p := range r.studentPools {
		if p != nil {
			p.Close()
		}
	}
	r.pool.Close()
}

// Pool exposes the underlying connection pool for components that need direct access
// to the pgxpool (e.g. for health checks or diagnostics).
func (r *PostgresRepository) Pool() *pgxpool.Pool {
	return r.pool
}
