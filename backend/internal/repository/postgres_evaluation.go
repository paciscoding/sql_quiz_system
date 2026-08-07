package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

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
//
// Exam questions (ExamID != nil) read from the exam's CurriculumSchema field,
// which GetQuestion populates via a JOIN. The default is 'public' (set by
// COALESCE in GetQuestion), so exams that have never had a per-exam import
// behave identically to before this feature existed.
//
// Practice questions (ExamID == nil) read their category's own schema (the
// sandboxSchema arg from the caller), which is what isolates practice from the
// exam curriculum.
func querySchemaFor(question models.Question, sandboxSchema string) string {
	if question.ExamID != nil {
		// ARCH-028 (0): the question's category wins when present — this is
		// what lets a QUERY question read a different reference database than
		// its exam's curriculum. CategorySchema is populated by GetQuestion's
		// LEFT JOIN and is empty for an uncategorised question, so this branch
		// is a no-op for every pre-existing exam.
		if question.CategorySchema != "" {
			return question.CategorySchema
		}
		// Use the per-exam curriculum schema populated by GetQuestion.
		// Falls back to 'public' if CurriculumSchema is empty (e.g., a Question
		// value that was not loaded through GetQuestion).
		if question.CurriculumSchema != "" {
			return question.CurriculumSchema
		}
		return "public"
	}
	if sandboxSchema == "" {
		return "public"
	}
	return sandboxSchema
}

// ExamGradingSandboxFor resolves the writable DML sandbox schema an exam
// question's grading executes against (ARCH-028 (0)). It is a pure, DB-free
// function: when q has a category AND that category owns its schema
// (ExamCategoryOwnsSchema), the category's own sandbox
// (ExamCategorySandboxName) is used; otherwise the exam-level sandbox
// (ExamSandboxName) is used, exactly as before per-category grading existed.
// Returns "" for a non-exam (practice) question, which has no sandbox concept
// here — practice sandboxing is resolved by the caller via a different path.
func ExamGradingSandboxFor(q models.Question) string {
	if q.ExamID == nil {
		return ""
	}
	if q.CategoryID != nil && ExamCategoryOwnsSchema(int(*q.CategoryID), q.CategorySchema) {
		return ExamCategorySandboxName(int(*q.CategoryID))
	}
	return ExamSandboxName(int(*q.ExamID))
}

// ExamPreviewSandboxFor resolves the writable decoy DML sandbox schema an exam
// question's dual-schema preview executes against (ARCH-028 (0)). Same
// ownership predicate and same DB-free purity as ExamGradingSandboxFor: a
// category that owns its schema previews against its own decoy sandbox
// (ExamCategoryPreviewSandboxName); otherwise the exam-level preview sandbox
// (ExamPreviewSandboxName) is used, unchanged from today. Returns "" for a
// non-exam (practice) question.
func ExamPreviewSandboxFor(q models.Question) string {
	if q.ExamID == nil {
		return ""
	}
	if q.CategoryID != nil && ExamCategoryOwnsSchema(int(*q.CategoryID), q.CategorySchema) {
		return ExamCategoryPreviewSandboxName(int(*q.CategoryID))
	}
	return ExamPreviewSandboxName(int(*q.ExamID))
}

func (r *PostgresRepository) evaluateQuery(ctx context.Context, question models.Question, studentSQL string, querySchema string) (models.ValidationResult, error) {
	// Repair typographic quotes so pasted SELECTs run instead of failing with a
	// spurious "column ... does not exist". Consistent with the preview path.
	studentSQL = normalizeSmartQuotes(studentSQL)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// ARCH-022: graded SELECT executes on the restricted exam_student_readonly
	// pool, never on r.pool (the privileged application connection). poolForRole
	// never falls back to r.pool — see postgres_student_pool.go.
	studentPool, err := r.poolForRole("exam_student_readonly")
	if err != nil {
		return models.ValidationResult{}, err
	}
	conn, err := studentPool.Acquire(ctx)
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
	// Guard against a concurrent schema rebuild (practice reset, category
	// import-with-replace, category delete) dropping the schema out from under
	// this read. Shared advisory lock: many evals run in parallel; the
	// exclusive rebuild lock waits for us to finish.
	if err := acquireSchemaEvalLock(ctx, tx, querySchema); err != nil {
		return models.ValidationResult{}, err
	}
	// ARCH-023: pg_temp appended LAST — see the matching comment in
	// evaluateDML for why position matters.
	safeQuerySchema := pgx.Identifier{querySchema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, pg_temp", safeQuerySchema)); err != nil {
		return models.ValidationResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student_readonly"); err != nil {
		return models.ValidationResult{}, err
	}

	var studentResults []map[string]interface{}
	var studentCols []string
	var studentErr error
	rows, queryErr := tx.Query(ctx, studentSQL)
	if queryErr != nil {
		studentErr = queryErr
	} else {
		studentResults, studentCols, studentErr = collectRowsWithColumns(rows)
	}

	capturedJSON, _ := json.Marshal(studentResults)
	result := models.ValidationResult{
		Status:            "success",
		ValidationDetails: capturedJSON,
	}
	if studentErr != nil {
		result.Status = "error"
		result.Feedback = fmt.Sprintf("SQL Error: %v", studentErr)
	} else {
		result.Columns = studentCols
	}
	return result, nil
}

// AutoSeedFailureFeedback is the student-facing feedback used when replaying
// a question's stored ARCH-038 auto-seed plan (see dml_autoseed.go) fails at
// grade or preview time. This is never the student's fault — the plan was
// already validated once at capture time — so the wording blames the
// question, not the submission, and directs the student to their instructor
// rather than guessing at a fix. It names no schema, table, role or
// SQLSTATE, so it survives the ARCH-018 dual-schema feedback scrub exactly
// like MultiStatementFeedback in postgres_student_exec.go.
const AutoSeedFailureFeedback = "This question could not be prepared for grading due to a configuration problem. Please contact your instructor so the question can be re-saved."

func (r *PostgresRepository) evaluateDML(ctx context.Context, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error) {
	// Repair typographic quotes so pasted INSERT/UPDATE/DELETE run instead of
	// failing with a spurious "column ... does not exist". Consistent with the
	// preview path.
	studentSQL = normalizeSmartQuotes(studentSQL)
	if question.TargetTable == "" {
		return models.ValidationResult{}, fmt.Errorf("DML question is missing target_table")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	schema := sandboxSchema
	// ARCH-022: graded DML executes on the restricted exam_student pool, never
	// on r.pool (the privileged application connection). poolForRole never falls
	// back to r.pool — see postgres_student_pool.go.
	studentPool, err := r.poolForRole("exam_student")
	if err != nil {
		return models.ValidationResult{}, err
	}
	conn, err := studentPool.Acquire(ctx)
	if err != nil {
		return models.ValidationResult{}, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return models.ValidationResult{}, err
	}
	defer tx.Rollback(ctx)

	// Guard against a concurrent schema rebuild (practice reset, category
	// import-with-replace, category delete) dropping the schema mid-evaluation.
	// Shared: parallel evals fine; the exclusive rebuild lock waits for us.
	if err := acquireSchemaEvalLock(ctx, tx, schema); err != nil {
		return models.ValidationResult{}, err
	}

	if _, err := tx.Exec(ctx, "SAVEPOINT student_eval"); err != nil {
		return models.ValidationResult{}, err
	}

	// ARCH-023: pg_temp is appended LAST, never first or omitted. Postgres
	// searches pg_temp before every schema in search_path regardless of
	// where (or whether) it is listed explicitly — UNLESS it is named
	// explicitly, in which case it is searched in the position given. Naming
	// it last here forces it back to being searched last, so a student's
	// single-statement `CREATE TEMP TABLE <target_table> AS SELECT ...`
	// cannot shadow the real target table for the post-image capture below.
	// See TestPgTempForgery_TargetTableShadowing_Rejected.
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public, pg_temp", safeSchema)); err != nil {
		return models.ValidationResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student"); err != nil {
		return models.ValidationResult{}, err
	}

	// ARCH-041-eval: replay the persisted auto-seed plan (if any) so a DML
	// model answer that only affects rows because a row was missing at
	// capture time still has that row present at grading time. MUST run
	// AFTER SET LOCAL ROLE exam_student above, never before/hoisted: the
	// student login roles are created NOINHERIT (postgres_student_pool.go:133),
	// so before SET LOCAL ROLE the connection holds no privileges at all and
	// the seed INSERT would fail outright; exam_student is the role that
	// actually holds INSERT on the category sandbox schema
	// (postgres_schema.go:179). unmarshalAutoSeed returns (nil, nil) for the
	// overwhelmingly common case of a question with no stored plan, so this
	// issues zero extra statements for every question that doesn't need it.
	if plan, seedErr := unmarshalAutoSeed(question.AutoSeed); seedErr != nil || plan != nil {
		if seedErr == nil {
			seedErr = applyAutoSeed(ctx, tx, plan)
		}
		if seedErr != nil {
			// The seed failed, not the student: this question was captured
			// with a plan that no longer applies (e.g. the category database
			// was reset or replaced since capture). The transaction is left
			// aborted exactly like a failed student statement, so recovery
			// follows the SAME ordering this file enforces everywhere else
			// (ARCH-021/ARCH-024b): ROLLBACK TO SAVEPOINT MUST run BEFORE
			// RESET ROLE, or RESET ROLE itself fails in the aborted state.
			if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval"); err != nil {
				_ = tx.Rollback(ctx)
				return models.ValidationResult{}, fmt.Errorf("failed to roll back student eval savepoint: %w", err)
			}
			if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
				_ = tx.Rollback(ctx)
				return models.ValidationResult{}, fmt.Errorf("failed to reset role after student eval: %w", err)
			}
			// Same "commit the now-empty transaction, log any failure, never
			// turn this into a 500" treatment as the normal success path
			// below: a seed failure must surface as a graded "error" result,
			// not a Go error, or callers turn it into an HTTP 500 with raw
			// internal error text.
			if commitErr := tx.Commit(ctx); commitErr != nil {
				log.Printf("Warning: evaluateDML commit failed after auto-seed failure rollback: %v", commitErr)
			}
			return models.ValidationResult{
				Status:   "error",
				Feedback: AutoSeedFailureFeedback,
			}, nil
		}
	}

	var studentResults []map[string]interface{}
	var studentCols []string
	var studentErr error

	if _, execErr := execStudentStatement(ctx, tx, studentSQL); execErr != nil {
		studentErr = execErr
	} else {
		safeTable := pgx.Identifier{question.TargetTable}.Sanitize()
		rows, queryErr := tx.Query(ctx, fmt.Sprintf("SELECT * FROM %s", safeTable))
		if queryErr != nil {
			studentErr = queryErr
		} else {
			studentResults, studentCols, studentErr = collectRowsWithColumns(rows)
		}
	}

	// Discard the student's writes so the shared sandbox is unchanged for the
	// next evaluation. Both statements' errors MUST be checked: if either one
	// fails, the student's mutations are still live in the transaction, and
	// committing anyway would permanently corrupt the shared sandbox for every
	// subsequent student. Roll the whole transaction back and bail out instead.
	//
	// ORDER MATTERS (ARCH-021): ROLLBACK TO SAVEPOINT runs FIRST. When the
	// student's statement above errors, the transaction is left in the aborted
	// state (SQLSTATE 25P02), in which every statement except ROLLBACK fails —
	// so a RESET ROLE attempted first always fails too, turning every failed
	// student DML submission into a hard error that the controllers surface as
	// an HTTP 500 with raw internal error text. ROLLBACK TO SAVEPOINT succeeds
	// even in the aborted state and, on its own, already restores current_user
	// to the app role, because the savepoint above is taken BEFORE SET LOCAL
	// ROLE; the RESET ROLE that follows is therefore belt-and-braces and cannot
	// fail.
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval"); err != nil {
		_ = tx.Rollback(ctx)
		return models.ValidationResult{}, fmt.Errorf("failed to roll back student eval savepoint: %w", err)
	}
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		_ = tx.Rollback(ctx)
		return models.ValidationResult{}, fmt.Errorf("failed to reset role after student eval: %w", err)
	}

	capturedJSON, _ := json.Marshal(studentResults)
	result := models.ValidationResult{
		Status:            "success",
		ValidationDetails: capturedJSON,
	}
	if studentErr != nil {
		result.Status = "error"
		result.Feedback = studentSQLErrorFeedback(studentErr)
	} else {
		result.Columns = studentCols
	}

	// Invariant: by this point RESET ROLE and ROLLBACK TO SAVEPOINT have both
	// already succeeded, so the transaction contains no student writes — it is
	// equivalent to an empty, read-only transaction. A Commit failure here is
	// therefore a connection-level event with no correctness consequence for
	// grading: there is nothing left to lose by not committing, so we log it
	// and return the already fully-populated result with a nil error rather
	// than turning a correctly-graded submission into a 500.
	if commitErr := tx.Commit(ctx); commitErr != nil {
		log.Printf("Warning: evaluateDML commit failed after empty (fully rolled-back) transaction: %v", commitErr)
	}
	return result, nil
}

const previewRowCap = 200

// EvaluatePreview runs the student SQL in a transaction that is always rolled
// back, returning up to previewRowCap rows plus metadata. The Result field uses
// the internal "success"/"error" status; callers (ValidationService) map that to
// the three-state "correct"/"incorrect"/"syntax_error" vocabulary.
func (r *PostgresRepository) EvaluatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string, previewSchema string) (PreviewResult, error) {
	// ARCH-020 (GAP 3): useDecoy is computed BEFORE the empty-string fallbacks
	// below — previewSchema is non-empty only when a controller resolved a real
	// decoy schema (practice_controller.go / quiz_controller.go). That is the
	// single, exact rule for whether this preview must run under a preview-only
	// role instead of today's exam_student / exam_student_readonly: those two
	// roles hold USAGE+SELECT (and, for exam_student, full DML) on EVERY real
	// schema, so a dual preview running under them could read grading schema B
	// via a schema-qualified name. When previewSchema == "" the preview target
	// already IS the real schema (no decoy uploaded), so keeping today's role
	// there discloses nothing extra and — per spec §6.1 — must never hard-fail
	// the preview.
	useDecoy := previewSchema != ""
	if question.Type == "QUERY" {
		schema := previewSchema
		if schema == "" {
			schema = querySchemaFor(question, sandboxSchema)
		}
		role := "exam_student_readonly"
		if useDecoy {
			role = rolePreviewStudentReadonly
		}
		return r.evaluateQueryPreview(ctx, question, studentSQL, schema, role)
	}
	schema := previewSchema
	if schema == "" {
		schema = sandboxSchema
	}
	role := "exam_student"
	if useDecoy {
		role = rolePreviewStudent
	}
	return r.evaluateDMLPreview(ctx, question, studentSQL, schema, role)
}

func (r *PostgresRepository) evaluateQueryPreview(ctx context.Context, question models.Question, studentSQL string, querySchema string, role string) (PreviewResult, error) {
	studentSQL = normalizeSmartQuotes(studentSQL)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// ARCH-022: the QUERY preview executes on the restricted pool for `role`
	// (exam_student_readonly, or the decoy exam_student_preview_readonly) — never
	// on r.pool. poolForRole never falls back to r.pool — see
	// postgres_student_pool.go.
	studentPool, err := r.poolForRole(role)
	if err != nil {
		return PreviewResult{}, err
	}
	conn, err := studentPool.Acquire(ctx)
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
	// ARCH-023: pg_temp appended LAST — see the matching comment in
	// evaluateDML for why position matters.
	safeQuerySchema := pgx.Identifier{querySchema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, pg_temp", safeQuerySchema)); err != nil {
		return PreviewResult{}, err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL ROLE %s", pgx.Identifier{role}.Sanitize())); err != nil {
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

func (r *PostgresRepository) evaluateDMLPreview(ctx context.Context, question models.Question, studentSQL string, sandboxSchema string, role string) (PreviewResult, error) {
	if question.TargetTable == "" {
		return PreviewResult{}, fmt.Errorf("DML question is missing target_table")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	studentSQL = normalizeSmartQuotes(studentSQL)
	schema := sandboxSchema
	// ARCH-022: the DML preview executes on the restricted pool for `role`
	// (exam_student, or the decoy exam_student_preview) — never on r.pool.
	// poolForRole never falls back to r.pool — see postgres_student_pool.go.
	studentPool, err := r.poolForRole(role)
	if err != nil {
		return PreviewResult{}, err
	}
	conn, err := studentPool.Acquire(ctx)
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

	// Guard against a concurrent schema rebuild dropping the schema
	// mid-preview — this was previously missing, unlike evaluateDML.
	if err := acquireSchemaEvalLock(ctx, tx, schema); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	if _, err := tx.Exec(ctx, "SAVEPOINT student_eval"); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL ROLE %s", pgx.Identifier{role}.Sanitize())); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	// ARCH-023: pg_temp appended LAST — see the matching comment in
	// evaluateDML for why position matters.
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public, pg_temp", safeSchema)); err != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", err)
		return pr, nil
	}

	// ARCH-041-eval: replay the persisted auto-seed plan (if any), mirroring
	// evaluateDML above. MUST run AFTER SET LOCAL ROLE <role> and the
	// SET LOCAL search_path above, never before/hoisted: the student login
	// roles (and the decoy preview roles) are created NOINHERIT
	// (postgres_student_pool.go:133), so before SET LOCAL ROLE the connection
	// holds no privileges at all and the seed INSERT would fail outright;
	// exam_student_preview is the role that holds full DML on the decoy
	// schema this preview writes to (postgres_preview_schema.go:150).
	// unmarshalAutoSeed returns (nil, nil) for the overwhelmingly common case
	// of a question with no stored plan, so this issues zero extra statements
	// for every question that doesn't need it.
	if plan, seedErr := unmarshalAutoSeed(question.AutoSeed); seedErr != nil || plan != nil {
		if seedErr == nil {
			seedErr = applyAutoSeed(ctx, tx, plan)
		}
		if seedErr != nil {
			// The seed failed, not the student: this question is
			// misconfigured (the stored plan no longer applies) and must be
			// re-saved by the lecturer. Feedback names no schema, table,
			// role or SQLSTATE so it survives the ARCH-018 dual-schema scrub.
			pr.Result.Status = "error"
			pr.Result.Feedback = AutoSeedFailureFeedback
			// ORDER MATTERS (ARCH-021/ARCH-024b): ROLLBACK TO SAVEPOINT
			// before RESET ROLE, matching every other branch in this
			// function, so recovery still succeeds when the seed left the
			// transaction aborted.
			_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
			_, _ = tx.Exec(ctx, "RESET ROLE")
			_ = tx.Rollback(ctx)
			return pr, nil
		}
	}

	if _, execErr := execStudentStatement(ctx, tx, studentSQL); execErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = studentSQLErrorFeedback(execErr)
		// ORDER MATTERS (ARCH-021): see the matching comment in evaluateDML —
		// ROLLBACK TO SAVEPOINT must run before RESET ROLE so recovery still
		// succeeds when the student's statement left the transaction aborted.
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
		_, _ = tx.Exec(ctx, "RESET ROLE")
		_ = tx.Rollback(ctx)
		return pr, nil
	}

	safeTable := pgx.Identifier{question.TargetTable}.Sanitize()
	rows, queryErr := tx.Query(ctx, fmt.Sprintf("SELECT * FROM %s", safeTable))
	if queryErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", queryErr)
		// ORDER MATTERS (ARCH-021/ARCH-024b): ROLLBACK TO SAVEPOINT before RESET
		// ROLE, matching the exec-error branch above, so recovery still succeeds
		// if the failed post-image query left the transaction aborted.
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
		_, _ = tx.Exec(ctx, "RESET ROLE")
		_ = tx.Rollback(ctx)
		return pr, nil
	}

	data, cols, collectErr := collectRowsWithColumns(rows)
	if collectErr != nil {
		pr.Result.Status = "error"
		pr.Result.Feedback = fmt.Sprintf("SQL Error: %v", collectErr)
		// ORDER MATTERS (ARCH-021/ARCH-024b): ROLLBACK TO SAVEPOINT before RESET ROLE.
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
		_, _ = tx.Exec(ctx, "RESET ROLE")
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

	// ORDER MATTERS (ARCH-021/ARCH-024b): ROLLBACK TO SAVEPOINT before RESET
	// ROLE, uniform with every other branch and with evaluateDML, so no future
	// reader re-introduces the split. Both succeed on this (non-aborted) path.
	_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval")
	_, _ = tx.Exec(ctx, "RESET ROLE")
	_ = tx.Rollback(ctx)
	return pr, nil
}
