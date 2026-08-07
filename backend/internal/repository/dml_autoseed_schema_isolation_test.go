package repository

// Regression-tester (step [7]) coverage for the dml-autoseed feature
// (2026-08-07-dml-autoseed-design.md, ARCH-037..042), area #1: schema
// isolation is the core safety guarantee. Every scenario here proves that
// capturing, grading, or previewing an auto-seeded question NEVER leaves the
// seeded row behind in the real category schema and NEVER changes its row
// count — capture/grade/preview all run the seed inside a savepoint that is
// unconditionally rolled back. The error paths (refusal, seed-INSERT
// failure, grade/preview replay failure) are covered too: a single path that
// ever committed the seed would permanently corrupt a shared category
// database for every student.
//
// Uses openTestRepo(t) (this package's shared TEST_DATABASE_URL singleton,
// already calling InitStudentPools — see postgres_password_events_test.go),
// mkPracticeCategory (postgres_practice_title_test.go), and the same
// capture/grade/preview call shapes as postgres_dml_grading_integration_test.go
// and postgres_dual_schema_integration_test.go.
//
// Every test below uses its OWN table name (never a bare "employee" reused
// across tests) DELIBERATELY: r.pool (the privileged application pool used
// for capture) runs with pgx.QueryExecModeCacheStatement
// (postgres.go:168), whose plan cache key is the raw SQL text only, not the
// active search_path. Two categories that both happened to name their table
// "employee" with a DIFFERENT column shape, captured back-to-back on the
// same pooled backend connection, reproducibly hit Postgres error "cached
// plan must not change result type" (SQLSTATE 0A000) on the second capture's
// `SELECT * FROM employee` — confirmed while drafting this file (see the
// regression-tester's report). That is a real, load-bearing observation about
// the existing capture pipeline (ANY DML capture using `SELECT * FROM
// <target_table>` is exposed, not just auto-seed's), but it is pre-existing
// architecture predating this feature and orthogonal to it, so it is worked
// around here with distinct table names rather than characterized as an
// auto-seed defect.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run AutoSeed -v -p 1

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// countSchemaTableRows returns the current row count of schema.table. schema
// and table are always test-fixture-controlled constants here, never
// student/lecturer input, so plain string interpolation is fine in this test
// file (production code always uses pgx.Identifier.Sanitize()).
func countSchemaTableRows(t *testing.T, repo *PostgresRepository, ctx context.Context, schema, table string) int {
	t.Helper()
	var n int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s.%s", schema, table)).Scan(&n); err != nil {
		t.Fatalf("count rows in %s.%s: %v", schema, table, err)
	}
	return n
}

func assertSeedRowAbsentBySSN(t *testing.T, repo *PostgresRepository, ctx context.Context, schema, table, ssn string) {
	t.Helper()
	var n int
	if err := repo.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM %s.%s WHERE ssn = $1", schema, table), ssn).Scan(&n); err != nil {
		t.Fatalf("check seed row absence: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected the auto-seeded row (ssn=%s) to be absent from the real schema %s, found %d", ssn, schema, n)
	}
}

// TestAutoSeed_SchemaIsolation_Practice_CaptureGradePreview_LeaveRealSchemaUntouched
// is the headline test for area #1: after ONE capture, THREE grades (mixing a
// correct DELETE and a no-op DELETE) and THREE previews of an auto-seeded
// question, the target table's row count in the real category schema is
// unchanged and the seeded row (ssn='999999999') is absent.
func TestAutoSeed_SchemaIsolation_Practice_CaptureGradePreview_LeaveRealSchemaUntouched(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "autoseed_iso")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_iso"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT, salary INTEGER, dno INTEGER);
		INSERT INTO %s (ssn, name, salary, dno) VALUES
			('111111111', 'Alice', 5000, 1),
			('222222222', 'Bob', 6000, 2);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)
	if baseline != 2 {
		t.Fatalf("fixture precondition: expected 2 baseline rows, got %d", baseline)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Requirement: "delete the employee with ssn 999999999",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected expected_result_status=captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if len(saved.AutoSeed) == 0 {
		t.Fatalf("expected a non-nil auto_seed plan for a rescued 0-row DELETE")
	}
	if saved.ExpectedRowsAffected == nil || *saved.ExpectedRowsAffected != 1 {
		t.Fatalf("expected expected_rows_affected=1 after the seed+rerun, got %v", saved.ExpectedRowsAffected)
	}

	// CORE GUARANTEE, checkpoint 1: capture itself must not have left the
	// seeded row behind — the nested SAVEPOINT autoseed + the outer capture
	// transaction both roll back unconditionally.
	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("capture must roll back its seed: row count changed from %d to %d", baseline, got)
	}
	assertSeedRowAbsentBySSN(t, repo, ctx, schema, tbl, "999999999")

	question := saved.AsQuestion()

	// CORE GUARANTEE, checkpoint 2: three grades (correct DELETE, which
	// consumes the replayed seed row; and a no-op DELETE, which leaves it
	// untouched) must each roll back cleanly.
	for i := 0; i < 3; i++ {
		result, err := repo.Evaluate(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl), schema)
		if err != nil {
			t.Fatalf("Evaluate (correct, iter %d): %v", i, err)
		}
		if result.Status != "success" {
			t.Fatalf("Evaluate (correct, iter %d): unexpected error status: %s", i, result.Feedback)
		}
		if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
			t.Fatalf("Evaluate (correct, iter %d): row count changed from %d to %d", i, baseline, got)
		}
	}
	for i := 0; i < 3; i++ {
		result, err := repo.Evaluate(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = 'no-such-row'", tbl), schema)
		if err != nil {
			t.Fatalf("Evaluate (noop, iter %d): %v", i, err)
		}
		if result.Status != "success" {
			t.Fatalf("Evaluate (noop, iter %d): unexpected error status: %s", i, result.Feedback)
		}
		if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
			t.Fatalf("Evaluate (noop, iter %d): row count changed from %d to %d", i, baseline, got)
		}
	}

	// CORE GUARANTEE, checkpoint 3: three previews (no decoy) must each roll
	// back cleanly too.
	for i := 0; i < 3; i++ {
		pr, err := repo.EvaluatePreview(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl), schema, "")
		if err != nil {
			t.Fatalf("EvaluatePreview (iter %d): %v", i, err)
		}
		if pr.Result.Status != "success" {
			t.Fatalf("EvaluatePreview (iter %d): unexpected error status: %s", i, pr.Result.Feedback)
		}
		if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
			t.Fatalf("EvaluatePreview (iter %d): row count changed from %d to %d", i, baseline, got)
		}
	}

	// Final check, spelled out explicitly per the brief.
	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("after capture+grades+previews, expected row count to remain %d, got %d", baseline, got)
	}
	assertSeedRowAbsentBySSN(t, repo, ctx, schema, tbl, "999999999")
}

// TestAutoSeed_CaptureRefusal_NonEqualityOperator_LeavesSchemaUntouched covers
// the capture-refusal error path (ARCH-038/039): a WHERE clause using '>'
// does not pin a single value, so derivation refuses before ever touching the
// schema, and the ARCH-039 message names the operator and column.
func TestAutoSeed_CaptureRefusal_NonEqualityOperator_LeavesSchemaUntouched(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "autoseed_refuse")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_refuse"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);
		INSERT INTO %s (ssn, salary) VALUES ('111111111', 5000);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf(`DELETE FROM %s WHERE salary > 50000`, tbl),
		Type:        "DML",
		Requirement: "delete high earners",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected a failed capture for a non-= WHERE operator, got %q", saved.ExpectedResultStatus)
	}
	wantSub := `'>' on "salary"`
	if !strings.Contains(saved.CaptureError, wantSub) {
		t.Fatalf("expected capture_error to contain %q (ARCH-039), got %q", wantSub, saved.CaptureError)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to stay NULL on a refused derivation, got %s", saved.AutoSeed)
	}
	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("a refused derivation must not touch the real schema: baseline=%d got=%d", baseline, got)
	}
}

// TestAutoSeed_CaptureSeedInsertFailure_ForeignKeyViolation_LeavesSchemaUntouched
// covers the capture seed-INSERT-failure error path (design doc "Known
// limitations" #2): the WHERE clause pins a FK column to a value whose parent
// row does not exist, so the seed derives successfully but applying it
// violates the FK constraint. Both the employee and dept tables must be
// completely unaffected.
func TestAutoSeed_CaptureSeedInsertFailure_ForeignKeyViolation_LeavesSchemaUntouched(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "autoseed_fkfail")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const (
		deptTbl = "dept_fk"
		empTbl  = "employee_fk"
	)
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (dno INTEGER PRIMARY KEY);
		INSERT INTO %s (dno) VALUES (1);
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, dno INTEGER NOT NULL REFERENCES %s(dno));
		INSERT INTO %s (ssn, dno) VALUES ('111111111', 1);`, deptTbl, deptTbl, empTbl, deptTbl, empTbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	baselineEmployee := countSchemaTableRows(t, repo, ctx, schema, empTbl)
	baselineDept := countSchemaTableRows(t, repo, ctx, schema, deptTbl)

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: empTbl,
		// dno=42 has no parent row in dept — both columns are pinned (both are
		// NOT NULL with no default), so derivation succeeds but the seed INSERT
		// itself violates the FK.
		CorrectSQL:  fmt.Sprintf(`DELETE FROM %s WHERE ssn = '999999999' AND dno = 42`, empTbl),
		Type:        "DML",
		Requirement: "delete a specific employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected a failed capture when the seed INSERT violates a FK, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	wantPrefix := `Model answer affected 0 rows. Auto-seed built a row but inserting it failed:`
	if !strings.HasPrefix(saved.CaptureError, wantPrefix) {
		t.Fatalf("expected capture_error to start with %q, got %q", wantPrefix, saved.CaptureError)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to stay NULL when the seed insert fails, got %s", saved.AutoSeed)
	}
	if got := countSchemaTableRows(t, repo, ctx, schema, empTbl); got != baselineEmployee {
		t.Fatalf("a failed seed insert must not touch %s: baseline=%d got=%d", empTbl, baselineEmployee, got)
	}
	if got := countSchemaTableRows(t, repo, ctx, schema, deptTbl); got != baselineDept {
		t.Fatalf("a failed seed insert must not touch %s: baseline=%d got=%d", deptTbl, baselineDept, got)
	}
}

// TestAutoSeed_GradeError_StalePlan_ReturnsErrorStatusNilGoError covers the
// grade-error path (area #8): a stored plan that no longer matches the real
// table (design doc "Known limitations" #3 — e.g. a category re-import
// dropped the pinned column) must surface as ValidationResult{Status:"error",
// Feedback: AutoSeedFailureFeedback} with a NIL Go error — a non-nil error
// would surface as an HTTP 500 with raw internal text instead of a graded
// result — and must leave the real schema untouched.
func TestAutoSeed_GradeError_StalePlan_ReturnsErrorStatusNilGoError(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "autoseed_stale_grade")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_stale_grade"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);
		INSERT INTO %s (ssn, salary) VALUES ('111111111', 5000);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Requirement: "delete a specific employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" || len(saved.AutoSeed) == 0 {
		t.Fatalf("fixture precondition: expected a captured question with a non-nil auto_seed, got status=%q auto_seed=%s capture_error=%q", saved.ExpectedResultStatus, saved.AutoSeed, saved.CaptureError)
	}

	// Corrupt the stored plan directly: pin a column that no longer exists on
	// the table, simulating a stale plan surviving a category re-import.
	corrupted := `{"table":"` + tbl + `","columns":[{"name":"no_such_column","value":"999999999"}]}`
	if _, err := repo.pool.Exec(ctx, "UPDATE app.practice_questions SET auto_seed = $1 WHERE id = $2", corrupted, int(saved.ID)); err != nil {
		t.Fatalf("corrupt auto_seed: %v", err)
	}
	reread, err := repo.GetPracticeQuestion(ctx, int(saved.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion (reread): %v", err)
	}
	question := reread.AsQuestion()

	before := countSchemaTableRows(t, repo, ctx, schema, tbl)
	result, err := repo.Evaluate(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl), schema)
	if err != nil {
		t.Fatalf("Evaluate must return a nil Go error on a seed-replay failure (a non-nil error surfaces as HTTP 500 with raw internal text), got: %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("expected Status=\"error\" for a corrupted stored plan, got %q", result.Status)
	}
	if result.Feedback != AutoSeedFailureFeedback {
		t.Fatalf("expected feedback=%q, got %q", AutoSeedFailureFeedback, result.Feedback)
	}
	after := countSchemaTableRows(t, repo, ctx, schema, tbl)
	if after != before {
		t.Fatalf("a failed seed replay must not touch the real schema: before=%d after=%d", before, after)
	}
}

// TestAutoSeed_PreviewError_StalePlan_ReturnsErrorStatusNilGoError is the
// preview sibling of the grade-error test above, driving evaluateDMLPreview
// instead of evaluateDML.
func TestAutoSeed_PreviewError_StalePlan_ReturnsErrorStatusNilGoError(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "autoseed_stale_preview")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	const tbl = "employee_stale_preview"
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, salary INTEGER);
		INSERT INTO %s (ssn, salary) VALUES ('111111111', 5000);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		Requirement: "delete a specific employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" || len(saved.AutoSeed) == 0 {
		t.Fatalf("fixture precondition: expected a captured question with a non-nil auto_seed, got status=%q auto_seed=%s capture_error=%q", saved.ExpectedResultStatus, saved.AutoSeed, saved.CaptureError)
	}

	corrupted := `{"table":"` + tbl + `","columns":[{"name":"no_such_column","value":"999999999"}]}`
	if _, err := repo.pool.Exec(ctx, "UPDATE app.practice_questions SET auto_seed = $1 WHERE id = $2", corrupted, int(saved.ID)); err != nil {
		t.Fatalf("corrupt auto_seed: %v", err)
	}
	reread, err := repo.GetPracticeQuestion(ctx, int(saved.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion (reread): %v", err)
	}
	question := reread.AsQuestion()

	before := countSchemaTableRows(t, repo, ctx, schema, tbl)
	pr, err := repo.EvaluatePreview(ctx, uid, *question, fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl), schema, "")
	if err != nil {
		t.Fatalf("EvaluatePreview must return a nil Go error on a seed-replay failure, got: %v", err)
	}
	if pr.Result.Status != "error" {
		t.Fatalf("expected Status=\"error\" for a corrupted stored plan, got %q", pr.Result.Status)
	}
	if pr.Result.Feedback != AutoSeedFailureFeedback {
		t.Fatalf("expected feedback=%q, got %q", AutoSeedFailureFeedback, pr.Result.Feedback)
	}
	after := countSchemaTableRows(t, repo, ctx, schema, tbl)
	if after != before {
		t.Fatalf("a failed seed replay must not touch the real schema: before=%d after=%d", before, after)
	}
}
