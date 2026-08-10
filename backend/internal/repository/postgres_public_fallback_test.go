package repository

// Regression tests for ARCH-062: the `, public` element of the DML
// search_path let an unqualified table name absent from a question's own
// schema resolve to a table in `public`, shared by every category. That is a
// silent cross-category read or write, and a lock in a schema the ARCH-058
// per-schema serialisation gate cannot see.
//
// Uses openTestRepo/uniqueUsername (postgres_password_events_test.go) and
// mkPracticeCategory (postgres_practice_title_test.go), shared in this
// package; skips cleanly without TEST_DATABASE_URL.
//
// The real helper/field names here were grepped from the surrounding
// package, not assumed: CreateQuestion takes models.Question BY VALUE and
// returns (int, error) (postgres_questions.go:18); the read-back accessor is
// GetQuestion, not GetQuestionByID (postgres_questions.go:539), and also
// returns a value, not a pointer. mkExamWithCurriculumSchema does not exist
// anywhere else in the package, so it is defined below, modelled on mkExam
// (postgres_preview_schema_delete_test.go:24) plus an optional
// curriculum_schema override.
//
// Also discovered while running the red step: CreatePracticeQuestion(ctx, q)
// never writes ExpectedResultStatus/CaptureError back onto the *q it was
// given — only q.ID/CreatedAt/UpdatedAt come back from the RETURNING clause
// (postgres_practice.go:420-425). Every existing test that checks capture
// status re-reads the row via GetPracticeQuestion afterwards (see
// postgres_practice_seedsql_test.go:78-83); this file follows that same
// convention rather than the brief's q.ExpectedResultStatus, which is always
// "" and would pin nothing.
//
// Also discovered at the red step: the brief's fixed target_table name "emp",
// reused across both practice tests with two DIFFERENT column shapes, hit the
// pre-existing, documented, out-of-scope cached-plan bug (SQLSTATE 0A000,
// "cached plan must not change result type" — pgx's per-connection statement
// cache is keyed on raw SQL text, and the DML capture's post-image query is
// literally "SELECT * FROM emp" with no schema qualifier, so two categories'
// differently-shaped "emp" tables collide when the pool reuses a connection).
// That produced a false "failed" status for a reason unrelated to the public
// fallback, which would have silently passed the assertion for the wrong
// reason. Every target_table name below is unique per test to stay clear of
// that unrelated landmine, here and against every other test in this binary.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkPublicOnlyTable creates a table in public that does NOT exist in the
// category schema, and registers cleanup. It returns the table name.
//
// The name is unique per test so parallel packages sharing this database do
// not collide, and so a crashed run cannot leave a fixture that silently
// satisfies a later run's NOT EXISTS check.
func mkPublicOnlyTable(t *testing.T, repo *PostgresRepository, ctx context.Context, prefix string) string {
	t.Helper()
	name := uniqueUsername(prefix) // lowercase, unique, identifier-safe
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (id INT PRIMARY KEY, label TEXT)", name)); err != nil {
		t.Fatalf("create public.%s: %v", name, err)
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"INSERT INTO public.%s (id, label) VALUES (1, 'shared')", name)); err != nil {
		t.Fatalf("seed public.%s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(),
			fmt.Sprintf("DROP TABLE IF EXISTS public.%s", name))
	})
	return name
}

// mkExamWithCurriculumSchema creates a lecturer + a fresh examination and, if
// curriculumSchema is non-empty, stamps app.examinations.curriculum_schema to
// that value. Passing "" leaves the column at its schema default ('public'),
// which is what a legacy exam with no per-exam curriculum import looks like.
// Modelled on mkExam (postgres_preview_schema_delete_test.go:24), which does
// not return the author's uid; this variant does, since the exam-capture test
// below needs it for CreatedBy.
func mkExamWithCurriculumSchema(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix, curriculumSchema string) (int, int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername(namePrefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	id, err := repo.CreateExamination(ctx, models.Examination{
		Title:     uniqueUsername(namePrefix + "_exam"),
		CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	if curriculumSchema != "" {
		if _, err := repo.pool.Exec(ctx,
			"UPDATE app.examinations SET curriculum_schema = $1 WHERE id = $2",
			curriculumSchema, id); err != nil {
			t.Fatalf("set curriculum_schema: %v", err)
		}
	}
	return id, uid
}

// TestPracticeCapture_NoPublicFallback_ForJoinedTable pins the removal of the
// `, public` element from the practice capture search_path.
//
// The question's target_table lives in the category's own schema, so
// ResolveTargetTable (postgres_target_table.go:54) accepts it — that gate is
// NOT what this test exercises. The correct_sql additionally reads a table
// that exists only in public. Before the change that resolved through the
// fallback and captured green against a table shared by every category; after
// it must fail at capture, lecturer-facing, while it is still fixable.
func TestPracticeCapture_NoPublicFallback_ForJoinedTable(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid := mkPracticeCategory(t, repo, ctx, "pubfall")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()
	emp := uniqueUsername("pubfall_emp")
	if err := repo.ImportSchemaIntoCategory(ctx, catID, fmt.Sprintf(
		`CREATE TABLE %s (id INT PRIMARY KEY, name TEXT, label TEXT);
		 INSERT INTO %s (id, name) VALUES (1, 'Alice');`, emp, emp), uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	shared := mkPublicOnlyTable(t, repo, ctx, "pubfall_shared")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		Title:       "reads a public-only table",
		Type:        "DML",
		TargetTable: emp,
		CorrectSQL: fmt.Sprintf(
			"UPDATE %s SET label = s.label FROM %s s WHERE s.id = %s.id", emp, shared, emp),
		Requirement: "join a table that only exists in public",
		Points:      1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion returned a hard error: %v", err)
	}

	// CreatePracticeQuestion does not populate q.ExpectedResultStatus /
	// q.CaptureError on the struct it was given (see file-level comment) — the
	// persisted row is the only place the real capture outcome lives.
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}

	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected capture to FAIL because %q resolves only in public; got status %q. "+
			"The public fallback is still present in the capture search_path.",
			shared, saved.ExpectedResultStatus)
	}
	if !strings.Contains(strings.ToLower(saved.CaptureError), "does not exist") {
		t.Errorf("capture_error should name the unresolvable relation, got: %q", saved.CaptureError)
	}
}

// TestPracticeCapture_NoPublicFallback_ForSeedTarget pins the seed-SQL path
// (applyQuestionSeed / postgres_practice.go:461), a second and independent
// site from the correct_sql join covered above.
//
// Named _ForSeedTarget (not _LeavesSharedTableUntouched, its former name):
// the row-count check at the bottom is a secondary, non-discriminating
// belt-and-braces assertion (see below) — the actual pin is the capture
// status, same as the test above.
//
// DEVIATION FROM THE BRIEF, discovered at the red step: the brief's version
// of this test asserted only the public-table row count and expected it to
// be 2 (leaked) pre-fix, 1 (untouched) post-fix. Running it pre-fix gave 1 in
// BOTH cases — a false pass that pinned nothing. The reason: CreatePracticeQuestion's
// Phase-0 capture ALWAYS rolls back (postgres_practice.go:592 returns
// errCaptureRollback unconditionally on the success path, and
// WithTransaction / postgres_core.go:88-90 rolls back on ANY non-nil return,
// errCaptureRollback included). So the seed INSERT against the public-only
// table is executed and then unconditionally discarded whether or not it
// ever resolved — capture can never leave a permanent write in `public`
// regardless of the search_path fallback; that is a pre-existing safety
// property of capture being a dry run, not something this change touches.
//
// The real discriminator is therefore the same shape as the test above:
// whether the seed INSERT resolves at all. Pre-fix, the fallback lets it
// resolve through public and the capture completes ("captured"); post-fix
// the seed INSERT fails to resolve and capture reports "failed" with the
// seed-refusal message. The row-count assertion is kept as a secondary,
// intentionally non-discriminating regression check: it must stay true
// (capture must never leave a write behind) independent of this fix.
func TestPracticeCapture_NoPublicFallback_ForSeedTarget(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid := mkPracticeCategory(t, repo, ctx, "pubwrite")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()
	emp := uniqueUsername("pubwrite_emp")
	if err := repo.ImportSchemaIntoCategory(ctx, catID, fmt.Sprintf(
		`CREATE TABLE %s (id INT PRIMARY KEY, name TEXT);
		 INSERT INTO %s (id, name) VALUES (1, 'Alice');`, emp, emp), uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	shared := mkPublicOnlyTable(t, repo, ctx, "pubwrite_shared")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		Title:       "writes a public-only table via seed",
		Type:        "DML",
		TargetTable: emp,
		CorrectSQL:  fmt.Sprintf("UPDATE %s SET name = 'Bob' WHERE id = 1", emp),
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (id, label) VALUES (2, 'leaked');", shared),
		Requirement: "seed targets a table that only exists in public",
		Points:      1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion returned a hard error: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected capture to FAIL because seed target %q resolves only in public; got status %q. "+
			"The public fallback is still present in the capture search_path.",
			shared, saved.ExpectedResultStatus)
	}
	if !strings.Contains(strings.ToLower(saved.CaptureError), "does not exist") {
		t.Errorf("capture_error should name the unresolvable relation, got: %q", saved.CaptureError)
	}

	// Belt-and-braces only — this is NOT the discriminating assertion (the
	// capture-status check above is). Capture is a dry run and must never
	// leave a write behind, fix or no fix, so this must hold regardless of
	// whether the public fallback is present.
	var n int
	if err := repo.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM public.%s", shared)).Scan(&n); err != nil {
		t.Fatalf("count public.%s: %v", shared, err)
	}
	if n != 1 {
		t.Fatalf("lecturer seed reached public.%s: row count %d, want 1. "+
			"A category's DML must not be able to write a table shared by every category.", shared, n)
	}
}

// TestExamCapture_PublicCurriculumSchema_StillWorks pins the no-op claim for
// legacy exams. An exam with no per-exam import resolves curriculum_schema to
// 'public' via COALESCE(NULLIF(...), 'public'), so its search_path was
// `public, public, pg_temp` before the change and `public, pg_temp` after.
// Dropping the duplicate must change nothing.
func TestExamCapture_PublicCurriculumSchema_StillWorks(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	tbl := mkPublicOnlyTable(t, repo, ctx, "legacyexam")

	examID, uid := mkExamWithCurriculumSchema(t, repo, ctx, "legacy", "")

	eid := models.ExamID(examID)
	qid, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		CreatedBy:   models.UserID(uid),
		Type:        "DML",
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("UPDATE %s SET label = 'updated' WHERE id = 1", tbl),
		Requirement: "legacy exam, public curriculum schema",
		Points:      1,
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	if qid == 0 {
		t.Fatal("CreateQuestion returned id 0")
	}

	got, err := repo.GetQuestion(ctx, qid)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if got.ExpectedResultStatus != "captured" {
		t.Fatalf("legacy exam on public curriculum schema must still capture; got %q (%s)",
			got.ExpectedResultStatus, got.CaptureError)
	}
}

// TestPracticeGrading_NoPublicFallback_RejectsUnqualifiedPublicOnlyTable pins
// the GRADING site (postgres_evaluation.go:308, evaluateDML). It is the
// discriminating counterpart to TestPracticeCapture_NoPublicFallback_ForJoinedTable
// above: capture already pinned its own site, but nothing previously exercised
// grading, so restoring `, public` at the grading site alone (while capture
// stayed fixed) would reopen the cross-category read/write and the
// gate-invisible deadlock with CI fully green.
//
// Drives a real grading transaction through repo.Evaluate — the same
// production entry point exercised by TestPracticeConcurrentDML_CaseB_*
// (postgres_practice_concurrent_dml_test.go), which is the closest existing
// model for this in the package. A student submits the model answer,
// unqualified, against a table that exists ONLY in public. Before ARCH-062
// that resolved through the fallback and graded normally; after it, the
// grading transaction must fail to resolve the relation rather than reading
// or writing a table shared by every category.
//
// Revert check (recorded in task-2-report.md): this test was run once with
// `, public` temporarily restored at postgres_evaluation.go:308 — it FAILED
// (res.Status came back "success" instead of "error"). With the fix in place
// (no `, public`), it PASSES.
func TestPracticeGrading_NoPublicFallback_RejectsUnqualifiedPublicOnlyTable(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid := mkPracticeCategory(t, repo, ctx, "pubgrade")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	emp := uniqueUsername("pubgrade_emp")
	if err := repo.ImportSchemaIntoCategory(ctx, catID, fmt.Sprintf(
		`CREATE TABLE %s (id INT PRIMARY KEY, name TEXT, label TEXT);
		 INSERT INTO %s (id, name) VALUES (1, 'Alice');`, emp, emp), uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	shared := mkPublicOnlyTable(t, repo, ctx, "pubgrade_shared")
	// mkPublicOnlyTable creates the table as the superuser app pool, with no
	// grants to exam_student. Without this GRANT, a search_path fallback that
	// DID resolve the name would still fail — with "permission denied", not
	// "does not exist" — which would make this test pass in both the fixed
	// and reverted states for the wrong reason (a real production public
	// table, unlike this fixture, predates the category-schema split and
	// already carries exam_student grants from when it lived directly in
	// public). Granting here isolates the assertion to name resolution alone.
	// This is why the reverted-buggy path fails below with "does not exist"
	// rather than "permission denied" — do NOT delete this block as setup
	// noise; the res.Feedback assertion below is what enforces that it stays.
	if _, err := repo.pool.Exec(ctx,
		fmt.Sprintf("GRANT SELECT, UPDATE ON public.%s TO exam_student", shared)); err != nil {
		t.Fatalf("grant on public.%s: %v", shared, err)
	}

	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	studentSQL := fmt.Sprintf(
		"UPDATE %s SET label = s.label FROM %s s WHERE s.id = %s.id", emp, shared, emp)

	q := models.Question{
		ID:          1,
		Type:        "DML",
		DMLKind:     "UPDATE",
		TargetTable: emp,
		CorrectSQL:  studentSQL,
		CreatedBy:   models.UserID(uid),
		Points:      1,
	}

	res, evalErr := repo.Evaluate(ctx, 4000, q, studentSQL, schema)
	if evalErr != nil {
		t.Fatalf("Evaluate returned an unexpected Go error: %v", evalErr)
	}
	if res.Status != "error" {
		t.Fatalf("expected grading to FAIL because %q resolves only in public; got status %q, feedback %q. "+
			"The public fallback is still present in the grading search_path.", shared, res.Status, res.Feedback)
	}
	if !strings.Contains(strings.ToLower(res.Feedback), "does not exist") {
		t.Fatalf("expected feedback to name the unresolvable relation (\"does not exist\"), got: %q. "+
			"A different error (e.g. permission denied) means the failure isn't discriminating name "+
			"resolution — check the GRANT block above is still present.", res.Feedback)
	}

	// Belt-and-braces: the grading transaction is always rolled back
	// regardless of this fix, so the shared table must be untouched either
	// way. Not the discriminating assertion — the status check above is.
	var n int
	if err := repo.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM public.%s", shared)).Scan(&n); err != nil {
		t.Fatalf("count public.%s: %v", shared, err)
	}
	if n != 1 {
		t.Fatalf("grading reached public.%s: row count %d, want 1", shared, n)
	}
}

// TestPracticePreview_NoPublicFallback_RejectsUnqualifiedPublicOnlyTable pins
// the PREVIEW site (postgres_evaluation.go:604, evaluateDMLPreview).
//
// It was the one production `search_path` site with nothing pinning it:
// restoring `, public` at :604 alone left the entire suite green, so a partial
// revert there would silently reopen the cross-category read/write while CI
// stayed happy. The branch's headline constraint is that all five sites move
// together; this test is what makes that checkable for the fifth.
//
// Structure mirrors TestPracticeGrading_NoPublicFallback_RejectsUnqualifiedPublicOnlyTable
// exactly — same fixture shape, same GRANT (see the long comment there: without
// it the reverted-buggy path fails with "permission denied" instead of
// "does not exist" and the test passes for the wrong reason), and the same
// assertion on the FEEDBACK TEXT rather than on the status alone.
//
// The feedback assertion is the discriminating one and must not be weakened to
// a status check: evaluateDMLPreview returns Result.Status == "error" for ANY
// failing student statement (postgres_evaluation.go:649-658), so a status-only
// assertion passes whether or not the name resolved.
//
// previewSchema is passed as "" on purpose: that is the single, exact rule for
// useDecoy (postgres_evaluation.go:451), so the preview runs under the real
// exam_student role against the category's own schema — the same schema and
// role grading uses — which is the only configuration in which the `, public`
// element is observable at this site.
func TestPracticePreview_NoPublicFallback_RejectsUnqualifiedPublicOnlyTable(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid := mkPracticeCategory(t, repo, ctx, "pubprev")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	emp := uniqueUsername("pubprev_emp")
	if err := repo.ImportSchemaIntoCategory(ctx, catID, fmt.Sprintf(
		`CREATE TABLE %s (id INT PRIMARY KEY, name TEXT, label TEXT);
		 INSERT INTO %s (id, name) VALUES (1, 'Alice');`, emp, emp), uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	shared := mkPublicOnlyTable(t, repo, ctx, "pubprev_shared")
	// Same reason as in the grading test above: isolate the assertion to name
	// resolution, so a fallback that DID resolve fails with "does not exist"
	// only when the name is genuinely unresolvable — never with
	// "permission denied", which would pass in both the fixed and reverted
	// states. Do NOT delete this as setup noise.
	if _, err := repo.pool.Exec(ctx,
		fmt.Sprintf("GRANT SELECT, UPDATE ON public.%s TO exam_student", shared)); err != nil {
		t.Fatalf("grant on public.%s: %v", shared, err)
	}

	schema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	studentSQL := fmt.Sprintf(
		"UPDATE %s SET label = s.label FROM %s s WHERE s.id = %s.id", emp, shared, emp)

	q := models.Question{
		ID:          1,
		Type:        "DML",
		DMLKind:     "UPDATE",
		TargetTable: emp,
		CorrectSQL:  studentSQL,
		CreatedBy:   models.UserID(uid),
		Points:      1,
	}

	pr, evalErr := repo.EvaluatePreview(ctx, 4100, q, studentSQL, schema, "")
	if evalErr != nil {
		t.Fatalf("EvaluatePreview returned an unexpected Go error: %v", evalErr)
	}
	if pr.Result.Status != "error" {
		t.Fatalf("expected preview to FAIL because %q resolves only in public; got status %q, feedback %q. "+
			"The public fallback is still present in the preview search_path.",
			shared, pr.Result.Status, pr.Result.Feedback)
	}
	if !strings.Contains(strings.ToLower(pr.Result.Feedback), "does not exist") {
		t.Fatalf("expected feedback to name the unresolvable relation (\"does not exist\"), got: %q. "+
			"evaluateDMLPreview reports status \"error\" for ANY failing student statement, so this "+
			"text assertion — not the status — is what discriminates a restored `, public`. A different "+
			"error (e.g. permission denied) means the GRANT block above was removed.", pr.Result.Feedback)
	}

	// Belt-and-braces: the preview transaction is always rolled back regardless
	// of this fix, so the shared table must be untouched either way. Not the
	// discriminating assertion — the feedback check above is.
	var n int
	if err := repo.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM public.%s", shared)).Scan(&n); err != nil {
		t.Fatalf("count public.%s: %v", shared, err)
	}
	if n != 1 {
		t.Fatalf("preview reached public.%s: row count %d, want 1", shared, n)
	}
}

// TestExamCapture_NonPublicCurriculumSchema_RejectsUnqualifiedPublicOnlyTable
// pins the EXAM-CAPTURE site (postgres_questions.go:129, CreateQuestion) with
// a NON-public curriculum schema.
//
// TestExamCapture_PublicCurriculumSchema_StillWorks above drives
// captureSchema = "public", where `TO "public"` and `TO "public", public`
// are identical — it pins nothing about this site. This test exercises the
// mkExamWithCurriculumSchema curriculumSchema != "" branch (previously
// unused) so captureSchema is a real, distinct schema, which is the only way
// `, public` in the search_path is observable at all.
//
// Revert check (recorded in task-2-report.md): this test was run once with
// `, public` temporarily restored at postgres_questions.go:129 — it FAILED
// (capture status came back "captured" instead of "failed"). With the fix in
// place, it PASSES.
func TestExamCapture_NonPublicCurriculumSchema_RejectsUnqualifiedPublicOnlyTable(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	curriculumSchema := uniqueUsername("examcurric")
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", curriculumSchema)); err != nil {
		t.Fatalf("create curriculum schema: %v", err)
	}
	defer func() {
		_, _ = repo.pool.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", curriculumSchema))
	}()

	emp := uniqueUsername("examcurric_emp")
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %s.%s (id INT PRIMARY KEY, name TEXT, label TEXT);
		 INSERT INTO %s.%s (id, name) VALUES (1, 'Alice');`,
		curriculumSchema, emp, curriculumSchema, emp)); err != nil {
		t.Fatalf("create curriculum table: %v", err)
	}

	shared := mkPublicOnlyTable(t, repo, ctx, "examcurric_shared")

	examID, uid := mkExamWithCurriculumSchema(t, repo, ctx, "nonpublic", curriculumSchema)

	eid := models.ExamID(examID)
	qid, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		CreatedBy:   models.UserID(uid),
		Type:        "DML",
		TargetTable: emp,
		CorrectSQL: fmt.Sprintf(
			"UPDATE %s SET label = s.label FROM %s s WHERE s.id = %s.id", emp, shared, emp),
		Requirement: "reads a public-only table from a non-public curriculum schema",
		Points:      1,
	})
	if err != nil {
		t.Fatalf("CreateQuestion returned a hard error: %v", err)
	}
	if qid == 0 {
		t.Fatal("CreateQuestion returned id 0")
	}

	got, err := repo.GetQuestion(ctx, qid)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if got.ExpectedResultStatus != "failed" {
		t.Fatalf("expected capture to FAIL because %q resolves only in public; got status %q (%s). "+
			"The public fallback is still present in the exam capture search_path.",
			shared, got.ExpectedResultStatus, got.CaptureError)
	}
	if !strings.Contains(strings.ToLower(got.CaptureError), "does not exist") {
		t.Errorf("capture_error should name the unresolvable relation, got: %q", got.CaptureError)
	}
}
