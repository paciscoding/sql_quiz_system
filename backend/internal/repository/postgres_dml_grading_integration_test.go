package repository

// Integration tests for the dml-grading-correctness pipeline (D1/D3/ARCH-013).
// Every defect here is invisible to a mock-based unit test because it is an
// interaction with real Postgres semantics: identifier case folding, command
// tags, and advisory locks. All tests use openTestRepo(t) (shared with
// postgres_password_events_test.go) and SKIP cleanly when TEST_DATABASE_URL is
// unset, so `go test ./...` stays green on a machine with no database. Each
// test creates its own scratch practice category (and owned schema) and
// force-deletes it on return, leaving no residue.
//
// D2 (heap-reorder grading) is NOT covered in this file. It used to live here
// as a package-local multiset-equality comparison, but that never called the
// production grading path (services.ValidationService.decideStatus ->
// decideDMLStatus -> diffRowMultiset) and so passed unchanged against
// pre-ARCH-005 code — see TODO-021's validator verdict. It now lives in
// backend/internal/services/dml_grading_integration_test.go, package
// services, which can legally import package repository (services already
// does so in production code; the reverse import from here would be a
// cycle) and therefore can drive ValidationService.Validate for real.
//
// Run:  TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run DMLGrading

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestDMLGrading_D1_TargetTableCaseResolution proves the shipped D1 bug is
// fixed: a lecturer-entered target_table that only differs in case from the
// real pg_class relation now resolves and persists canonically at save time,
// and a subsequent student evaluation against it does not hit "relation does
// not exist".
func TestDMLGrading_D1_TargetTableCaseResolution(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "d1")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	importSQL := `
		CREATE TABLE works_on (pno INTEGER PRIMARY KEY, hours INTEGER NOT NULL);
		INSERT INTO works_on (pno, hours) VALUES (1, 10), (2, 20), (3, 30);`
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: "WORKS_ON", // deliberately wrong case — the shipped bug's repro shape
		CorrectSQL:  "UPDATE works_on SET hours = hours + 1 WHERE pno = 1",
		Type:        "DML",
		Requirement: "bump pno=1's hours by one",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	if q.TargetTable != "works_on" {
		t.Fatalf("expected canonical target_table 'works_on' after save, got %q", q.TargetTable)
	}

	// CreatePracticeQuestion's INSERT only RETURNs id/created_at/updated_at (see
	// postgres_practice.go) — the capture outcome must be re-read, exactly as
	// PracticeController.Upsert already does ("Re-read to surface capture
	// outcome").
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected expected_result_status=captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}

	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	question := saved.AsQuestion()
	result, err := repo.Evaluate(ctx, uid, *question, "UPDATE works_on SET hours = hours + 1 WHERE pno = 1", schema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Status == "error" {
		t.Fatalf("student evaluation errored (expected success): %s", result.Feedback)
	}
	if strings.Contains(strings.ToLower(result.Feedback), "does not exist") {
		t.Fatalf("student evaluation hit the D1 quoting/case bug: %s", result.Feedback)
	}
}

// TestDMLGrading_D3_ZeroRowModelAnswerHardBlocked proves the reported
// reproduction (a model answer whose WHERE clause matches no rows) is now a
// hard capture failure instead of a vacuously-gradeable question.
func TestDMLGrading_D3_ZeroRowModelAnswerHardBlocked(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "d3")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	importSQL := `
		CREATE TABLE works_on (pno INTEGER PRIMARY KEY, hours INTEGER NOT NULL);
		INSERT INTO works_on (pno, hours) VALUES (62, 48);`
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: "works_on",
		// The reported reproduction: hours=48 already, so hours>50 matches nothing.
		CorrectSQL:  "UPDATE works_on SET hours = 48 WHERE pno = 62 AND hours > 50",
		Type:        "DML",
		Requirement: "vacuous WHERE clause — must be rejected at capture time",
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
		t.Fatalf("expected expected_result_status=failed for a 0-row model answer, got %q", saved.ExpectedResultStatus)
	}
	if !strings.Contains(saved.CaptureError, "affected 0 rows") {
		t.Fatalf("expected capture_error to explain the 0-row block, got %q", saved.CaptureError)
	}
	if saved.ExpectedResult != nil {
		t.Fatalf("expected expected_result to stay NULL on a failed capture, got %s", saved.ExpectedResult)
	}
}

// TestDMLGrading_ARCH013_ImportReplaceBlocksOnInFlightEval proves a category
// import-with-replace takes the schema's exclusive rebuild lock and therefore
// blocks while a practice DML evaluation holds the shared lock against that
// same category schema, unblocking only once the evaluation finishes.
// Mirrors TestResetPracticeSandbox_BlocksOnInFlightEval's pattern.
func TestDMLGrading_ARCH013_ImportReplaceBlocksOnInFlightEval(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "lock")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID, "CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory (initial): %v", err)
	}
	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	// Simulate an in-flight evaluation: open a tx and take the SHARED lock,
	// exactly as acquireSchemaEvalLock does inside evaluateDML/evaluateQuery.
	evalConn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire eval conn: %v", err)
	}
	defer evalConn.Release()
	evalTx, err := evalConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin eval tx: %v", err)
	}
	if _, err := evalTx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1)", schemaRebuildLockKey(schema)); err != nil {
		t.Fatalf("take shared lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- repo.ImportSchemaIntoCategory(ctx, catID, "CREATE TABLE t2 (id INTEGER PRIMARY KEY);", uid, true)
	}()

	// While the shared lock is held, the exclusive replace-import must NOT complete.
	select {
	case err := <-done:
		t.Fatalf("ImportSchemaIntoCategory(confirmReplace) completed while an eval held the shared lock (err=%v); the schema could be dropped mid-eval", err)
	case <-time.After(400 * time.Millisecond):
		// Correctly blocked.
	}

	// Release the eval; the replace-import must now proceed and succeed.
	if err := evalTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback eval tx: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ImportSchemaIntoCategory(confirmReplace) failed after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ImportSchemaIntoCategory(confirmReplace) did not complete after the eval released its lock")
	}
}
