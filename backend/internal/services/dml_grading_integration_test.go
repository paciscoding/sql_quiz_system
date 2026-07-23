package services

// TestDMLGrading_D2_CorrectUpdateGradesCorrectDespiteReordering supersedes the
// same-named test that used to live in
// backend/internal/repository/postgres_dml_grading_integration_test.go. The
// validator found the old version proved nothing: it compared the two raw
// SELECT * snapshots with a package-local rowSetsEqual/rowValuesEqual helper
// instead of exercising the actual grading code (ValidationService.decideStatus
// -> decideDMLStatus -> diffRowMultiset, ARCH-005), so it passed byte-for-byte
// against pre-ARCH-005 code too.
//
// This test lives in package services (not package repository) specifically
// so it can call the real, unexported decideStatus/decideDMLStatus path via
// ValidationService.Validate. Package repository cannot import package
// services — services already imports repository, so the reverse import
// would be a cycle — but the reverse direction (a _test.go file in services
// importing repository) is legal, since it is the same direction services'
// production code already imports in. That asymmetry is what unlocks driving
// the production comparator from an integration test at all.
//
// Confirmed this test is meaningful (fails against pre-ARCH-005 positional
// comparison, passes against the current multiset comparison): in a scratch
// copy of validation_service.go, decideStatus's `if q.Type != "QUERY"` DML
// branch was temporarily removed so DML questions fell through to the QUERY
// path (index-by-index compareResults with mode.Ordering=true, i.e. exactly
// the pre-ARCH-005 behaviour). With that change, `go test ./internal/services/...
// -run TestDMLGrading_D2 -v` against a live TEST_DATABASE_URL FAILED with
// "expected a heap-reordered-but-value-identical UPDATE to grade correct, got
// status=\"incorrect\" is_correct=false ...". The scratch edit was then
// discarded (git diff on the production file is empty) and the test was
// re-run to confirm it passes again. See the TODO-021 fix report for the
// exact commands and output.

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/testsupport"
)

// mkDMLPracticeCategory creates a lecturer + a fresh practice category (with
// its own owned schema) and returns (categoryID, lecturerUserID). Mirrors
// repository's unexported mkPracticeCategory test helper (not reachable from
// this package) using only PostgresRepository's exported surface.
func mkDMLPracticeCategory(t *testing.T, repo *repository.PostgresRepository, ctx context.Context, prefix string) (int, int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsernameSvc(prefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cat, err := repo.CreatePracticeCategory(ctx, uniqueUsernameSvc(prefix+"_cat"), uid)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	return int(cat.ID), uid
}

func TestDMLGrading_D2_CorrectUpdateGradesCorrectDespiteReordering(t *testing.T) {
	repo := testsupport.SharedRepo(t)
	ctx := context.Background()
	catID, uid := mkDMLPracticeCategory(t, repo, ctx, "d2svc")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	importSQL := `
		CREATE TABLE t (id INTEGER PRIMARY KEY, tag TEXT NOT NULL);
		INSERT INTO t (id, tag) VALUES (1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e');`
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	// Multi-statement correct_sql: no-op rewrites of ids 1,2,3 in that order.
	// Postgres appends every UPDATEd tuple at the heap tail, so a bare
	// SELECT * afterwards deterministically returns 4,5,1,2,3 instead of
	// 1,2,3,4,5 — this is the Postgres-specific precondition ARCH-005 exists
	// for.
	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: "t",
		CorrectSQL:  "UPDATE t SET tag = tag WHERE id = 1; UPDATE t SET tag = tag WHERE id = 2; UPDATE t SET tag = tag WHERE id = 3;",
		Type:        "DML",
		Requirement: "no-op tag rewrite on ids 1,2,3, in that order (reorders the heap)",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	// CreatePracticeQuestion's INSERT only RETURNs id/created_at/updated_at —
	// re-read to see the capture outcome, exactly as the D1 integration test
	// and PracticeController.Upsert already do.
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

	// Same final values, different heap-tuple write order — reaches the
	// identical row set as the teacher's capture via a different write order,
	// but as EXACTLY ONE SQL statement: ARCH-021's execStudentStatement runs
	// student SQL through the PostgreSQL extended query protocol
	// (tx.Conn().PgConn().ExecParams), whose Parse message accepts at most one
	// command, so a chain of semicolon-separated UPDATEs (as this test used
	// before ARCH-021) is rejected server-side with SQLSTATE 42601 before it
	// is even planned. A single WITH ... UPDATE with data-modifying CTEs is
	// one statement to the parser yet still lets us control write order: the
	// CTEs and the outer UPDATE all read the same snapshot, so they must
	// touch disjoint rows (ids 3, 1, 2 respectively) or a later write would
	// silently be dropped. Empirically confirmed against a live Postgres this
	// single statement still reorders the heap relative to the teacher's
	// sequential 1,2,3 capture (see the mandatory precondition check right
	// below, which would fail loudly if it ever stopped doing so).
	studentSQL := `WITH r3 AS (UPDATE t SET tag = tag WHERE id = 3 RETURNING id),
	     r1 AS (UPDATE t SET tag = tag WHERE id = 1 RETURNING id)
	UPDATE t SET tag = tag WHERE id = 2`

	// --- MANDATORY precondition: the fixture must actually reorder the heap ---
	// Run the student SQL once through repository.Evaluate — the exact same
	// call ValidationService.Validate makes internally — purely to inspect the
	// raw row order. evaluateDML always rolls back its writes via
	// "ROLLBACK TO SAVEPOINT student_eval" (ARCH-014), so this has no side
	// effects and does not pre-empt the real grading call below; the table is
	// byte-identical for both calls.
	rawResult, err := repo.Evaluate(ctx, uid, *question, studentSQL, schema)
	if err != nil {
		t.Fatalf("Evaluate (precondition check): %v", err)
	}
	if rawResult.Status == "error" {
		t.Fatalf("student evaluation errored: %s", rawResult.Feedback)
	}
	var teacherRows, studentRows []map[string]interface{}
	if err := json.Unmarshal(saved.ExpectedResult, &teacherRows); err != nil {
		t.Fatalf("unmarshal captured expected_result: %v", err)
	}
	if err := json.Unmarshal(rawResult.ValidationDetails, &studentRows); err != nil {
		t.Fatalf("unmarshal student ValidationDetails: %v", err)
	}
	if reflect.DeepEqual(teacherRows, studentRows) {
		t.Fatalf("fixture no longer reproduces heap reordering: teacher and student SELECT * "+
			"snapshots came back in identical positional order (%v); this test is meaningless "+
			"without divergence — a positional comparison would pass it too. Adjust the fixture "+
			"(more rows, a different update order) so the heap-tail write actually reorders the rows.",
			teacherRows)
	}

	// --- The real assertion: drive the production grading path ---
	// ValidationService.Validate -> decideStatus -> decideDMLStatus ->
	// diffRowMultiset is the actual code path that grades a submitted DML
	// answer. No comparison logic is reimplemented here.
	svc := NewValidationService(repo)
	result, err := svc.Validate(ctx, uid, *question, studentSQL, schema)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if result.Status != "correct" || !result.IsCorrect {
		t.Fatalf("expected a heap-reordered-but-value-identical UPDATE to grade correct, got status=%q is_correct=%v feedback=%q",
			result.Status, result.IsCorrect, result.Feedback)
	}
}
