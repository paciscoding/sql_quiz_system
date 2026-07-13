package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// ErrExamCurriculumNotEmpty is returned by ImportSchemaIntoExamCurriculum when
// the target exam curriculum schema already contains tables and the caller did
// not pass confirm_replace=true. Controllers map this to 409 Conflict.
var ErrExamCurriculumNotEmpty = errors.New("exam curriculum schema already has tables; confirm replace or use a new import")

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
	config.MaxConns = 20
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
