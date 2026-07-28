package repository

// Regression-tester (step [7]) gap coverage for ARCH-026/ARCH-028, on top of
// the already-comprehensive postgres_exam_categories_test.go. Three gaps the
// validator explicitly flagged as accepted caveats, closed here:
//
//  1. ARCH-026 A6 (the empty-DB migration case) was never exercised, because
//     the shared TEST_DATABASE_URL singleton (openTestRepo) always has rows
//     left over from every other test in this package by the time any test
//     runs. TestMigrateExamCategories_PristineDatabase_ZeroExaminations
//     builds a genuinely pristine sibling database inside the SAME Postgres
//     instance (CREATE DATABASE / DROP DATABASE), independent of openTestRepo,
//     and boots a fresh PostgresRepository against it — the one place the
//     migration can be observed running against literally zero
//     app.examinations AND zero app.system_users rows (the exact pre-admin-
//     seed boot moment; see cmd/main.go, which seeds the admin AFTER
//     InitializeSystemTables).
//  2. ARCH-028 C2's "public fallback" byte-identity subtest in
//     postgres_exam_categories_test.go (TestByteIdenticalGrading_BeforeAfterMigration
//     /with_public_fallback_QUERY_only) covers QUERY only.
//     TestByteIdenticalGrading_PublicFallback_DML is its DML sibling.
//  3. ARCH-028 (5)'s claim that a question MOVED to a different category gets
//     its expected_result RE-CAPTURED against the new category's schema
//     (never silently left grading against a stale expectation) had no test
//     anywhere. TestQuestionMovedBetweenCategories_RecapturesExpectedResult
//     closes it.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run 'PristineDatabase|PublicFallback_DML|MovedBetweenCategories' -v

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// dsnWithDatabase returns dsn with its path (database name) replaced by
// dbName, leaving user/password/host/port/query params untouched.
func dsnWithDatabase(t *testing.T, dsn, dbName string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + dbName
	return u.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Gap 1 — ARCH-026 A6: migrateExamCategories against a database with ZERO
// app.examinations rows (and, since this is also the pre-admin-seed boot
// moment, zero app.system_users rows too).
// ─────────────────────────────────────────────────────────────────────────────

func TestMigrateExamCategories_PristineDatabase_ZeroExaminations(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()

	adminDSN := dsnWithDatabase(t, dsn, "postgres")
	dbName := fmt.Sprintf("examcat_pristine_%d", time.Now().UnixNano())
	safeDBName := pgx.Identifier{dbName}.Sanitize()

	adminConn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect to admin (maintenance) database: %v", err)
	}
	if _, err := adminConn.Exec(ctx, "CREATE DATABASE "+safeDBName); err != nil {
		_ = adminConn.Close(ctx)
		t.Fatalf("CREATE DATABASE %s: %v", dbName, err)
	}
	_ = adminConn.Close(ctx)

	t.Cleanup(func() {
		cctx := context.Background()
		c, err := pgx.Connect(cctx, adminDSN)
		if err != nil {
			t.Logf("cleanup: connect to admin database: %v", err)
			return
		}
		defer c.Close(cctx)
		// Postgres 13+: WITH (FORCE) disconnects any lingering sessions first,
		// so cleanup never leaves the throwaway database behind because some
		// pool connection hadn't yet noticed it should close.
		if _, err := c.Exec(cctx, "DROP DATABASE IF EXISTS "+safeDBName+" WITH (FORCE)"); err != nil {
			t.Logf("cleanup: DROP DATABASE %s: %v", dbName, err)
		}
	})

	pristineDSN := dsnWithDatabase(t, dsn, dbName)
	repo, err := NewPostgresRepository(pristineDSN)
	if err != nil {
		t.Fatalf("NewPostgresRepository(pristine): %v", err)
	}
	defer repo.Close()

	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser: %v", err)
	}
	if err := repo.InitStudentPools(ctx); err != nil {
		t.Fatalf("InitStudentPools: %v", err)
	}

	// This is the exact real boot order (cmd/main.go): InitializeSystemTables
	// — which runs migratePracticeCategories then migrateExamCategories
	// internally (ARCH-026 (4)) — executes BEFORE main.go's admin-seeding
	// step. At this instant both app.system_users and app.examinations are
	// empty: the one boot state the shared-DB tests in
	// postgres_exam_categories_test.go can never reach, because
	// openTestRepo's singleton accumulates rows from every other test in the
	// package by the time any test body runs.
	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("ARCH-026 A6 VIOLATED: InitializeSystemTables (incl. migrateExamCategories) failed against a pristine database with zero app.examinations rows and zero app.system_users rows: %v", err)
	}

	var examCount, catCount, userCount int
	if err := repo.pool.QueryRow(ctx, "SELECT COUNT(*) FROM app.examinations").Scan(&examCount); err != nil {
		t.Fatalf("count app.examinations: %v", err)
	}
	if examCount != 0 {
		t.Fatalf("test invariant violated: expected 0 examinations on a pristine database, got %d", examCount)
	}
	if err := repo.pool.QueryRow(ctx, "SELECT COUNT(*) FROM app.exam_categories").Scan(&catCount); err != nil {
		t.Fatalf("count app.exam_categories: %v", err)
	}
	if catCount != 0 {
		t.Fatalf("ARCH-026 A6 VIOLATED: expected migrateExamCategories to create ZERO rows against zero examinations, got %d", catCount)
	}
	if err := repo.pool.QueryRow(ctx, "SELECT COUNT(*) FROM app.system_users").Scan(&userCount); err != nil {
		t.Fatalf("count app.system_users: %v", err)
	}
	if userCount != 0 {
		t.Fatalf("test invariant violated: expected InitializeSystemTables alone (before main.go's admin seed) to leave app.system_users empty, got %d", userCount)
	}

	// Idempotence against the still-empty tables: rerunning must not error and
	// must not create any rows (ARCH-026 A4, extended to the zero-exam case).
	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("InitializeSystemTables (2nd call, still pristine): %v", err)
	}
	if err := repo.pool.QueryRow(ctx, "SELECT COUNT(*) FROM app.exam_categories").Scan(&catCount); err != nil {
		t.Fatalf("count app.exam_categories (after rerun): %v", err)
	}
	if catCount != 0 {
		t.Fatalf("ARCH-026 A6 VIOLATED: rerunning InitializeSystemTables against still-zero examinations created %d exam_categories rows", catCount)
	}

	// Prove the pristine boot path is not a permanently-stuck state: the
	// NEXT exam ever created on this database still gets a General category
	// on the following boot cycle, exactly as ARCH-026 describes for a
	// normal (non-empty) database.
	uid, err := repo.CreateUser(ctx, models.User{
		Username: fmt.Sprintf("examcat_pristine_author_%d", time.Now().UnixNano()),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	examID, err := repo.CreateExamination(ctx, models.Examination{
		Title:     fmt.Sprintf("examcat_pristine_exam_%d", time.Now().UnixNano()),
		CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("InitializeSystemTables (3rd call, post-first-exam): %v", err)
	}
	cats, err := repo.ListExamCategories(ctx, examID)
	if err != nil || len(cats) != 1 || cats[0].Name != "General" {
		t.Fatalf("expected the pristine database's first-ever exam to get exactly one General category on the next boot cycle, got %+v (err=%v)", cats, err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gap 2 — ARCH-028 C2: 'public' fallback byte-identity, DML sibling of
// postgres_exam_categories_test.go's QUERY-only subtest.
// ─────────────────────────────────────────────────────────────────────────────

func TestByteIdenticalGrading_PublicFallback_DML(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "byteident_pub_dml")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	tbl := fmt.Sprintf("byteident_pub_dml_%d", examID)
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO public.%s VALUES (1, 55)", tbl, tbl)); err != nil {
		t.Fatalf("seed public table: %v", err)
	}
	t.Cleanup(func() { _, _ = repo.pool.Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl) })

	// The exam has never had a curriculum import, so its DML sandbox clones
	// straight from 'public' (InitializeExamSandbox's documented default) —
	// exactly the pre-existing-exam shape this subtest needs.
	if err := repo.InitializeExamSandbox(ctx, examID); err != nil {
		t.Fatalf("InitializeExamSandbox: %v", err)
	}

	eid := models.ExamID(examID)
	dmlSQL := fmt.Sprintf("UPDATE %s SET val = val + 1 WHERE id = 1", tbl)
	dmlID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, TargetTable: tbl, CorrectSQL: dmlSQL, Type: "DML",
		Points: 1, Requirement: "r", CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	qBefore, err := repo.GetQuestion(ctx, dmlID)
	if err != nil {
		t.Fatalf("GetQuestion(before): %v", err)
	}
	if qBefore.CurriculumSchema != "public" {
		t.Fatalf("test setup: expected curriculum_schema='public', got %q", qBefore.CurriculumSchema)
	}
	if qBefore.CategoryID != nil {
		t.Fatalf("test setup: expected no category before migration")
	}
	sandboxBefore := ExamGradingSandboxFor(qBefore)

	resBefore, err := repo.Evaluate(ctx, uid, qBefore, dmlSQL, sandboxBefore)
	if err != nil {
		t.Fatalf("Evaluate(before): %v", err)
	}
	if resBefore.Status != "success" {
		t.Fatalf("evaluate before migration failed: %s", resBefore.Feedback)
	}

	if err := repo.migrateExamCategories(ctx); err != nil {
		t.Fatalf("migrateExamCategories: %v", err)
	}

	qAfter, err := repo.GetQuestion(ctx, dmlID)
	if err != nil {
		t.Fatalf("GetQuestion(after): %v", err)
	}
	if qAfter.CategoryID == nil {
		t.Fatalf("test setup: expected migration to attach the question to a General category")
	}
	if ExamCategoryOwnsSchema(int(*qAfter.CategoryID), qAfter.CategorySchema) {
		t.Fatalf("test setup: expected General to be legacy-adopted (not owning) for the public-fallback case")
	}
	sandboxAfter := ExamGradingSandboxFor(qAfter)
	if sandboxBefore != sandboxAfter {
		t.Fatalf("ARCH-028 C2 VIOLATED (public fallback DML): the resolved DML sandbox schema changed after migration for a legacy-adopted General — before=%q after=%q", sandboxBefore, sandboxAfter)
	}

	resAfter, err := repo.Evaluate(ctx, uid, qAfter, dmlSQL, sandboxAfter)
	if err != nil {
		t.Fatalf("Evaluate(after): %v", err)
	}

	if resBefore.Status != resAfter.Status || string(resBefore.ValidationDetails) != string(resAfter.ValidationDetails) || resBefore.Feedback != resAfter.Feedback {
		t.Fatalf("ARCH-028 C2 VIOLATED (public fallback DML): grading diverged after migration:\nbefore=%+v\nafter=%+v", resBefore, resAfter)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Gap 3 — ARCH-028 (5): a question moved between two categories with
// DIFFERENT schemas re-captures its expected_result against the NEW
// category's schema; grading never silently compares against a stale
// expectation captured under the old one.
// ─────────────────────────────────────────────────────────────────────────────

func TestQuestionMovedBetweenCategories_RecapturesExpectedResult(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, uid := mkExamWithAuthor(t, repo, ctx, "moved_cat")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

	catA, err := repo.CreateExamCategory(ctx, examID, "MovedFrom", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory(A): %v", err)
	}
	catB, err := repo.CreateExamCategory(ctx, examID, "MovedTo", &uid)
	if err != nil {
		t.Fatalf("CreateExamCategory(B): %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(catA.ID),
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 100);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory(A): %v", err)
	}
	if err := repo.ImportSchemaIntoExamCategory(ctx, examID, int(catB.ID),
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO t VALUES (1, 999);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoExamCategory(B): %v", err)
	}

	eid := models.ExamID(examID)
	q := models.Question{
		ExamID: &eid, CategoryID: &catA.ID, CorrectSQL: "SELECT * FROM t", Type: "QUERY",
		Points: 5, Requirement: "r", CreatedBy: models.UserID(uid),
	}
	qID, err := repo.CreateQuestion(ctx, q)
	if err != nil {
		t.Fatalf("CreateQuestion (in A): %v", err)
	}

	saved, err := repo.GetQuestion(ctx, qID)
	if err != nil {
		t.Fatalf("GetQuestion (in A): %v", err)
	}
	if saved.CategoryID == nil || int(*saved.CategoryID) != int(catA.ID) {
		t.Fatalf("test setup: expected the question captured in category A, got %+v", saved)
	}
	rowsA := unmarshalRows(t, saved.ExpectedResult)
	if fmt.Sprintf("%v", rowVal(t, rowsA, "val")) != "100" {
		t.Fatalf("expected the initial capture to read category A's val=100, got %+v", rowsA)
	}

	// "Move" the question: re-save the SAME id (CreateQuestion's ID>0 path is
	// the upsert/update path — see the ARCH-028 (5) comment at
	// postgres_questions.go CreateQuestion) with CategoryID now pointing at B.
	moved := saved
	moved.CategoryID = &catB.ID
	movedID, err := repo.CreateQuestion(ctx, moved)
	if err != nil {
		t.Fatalf("CreateQuestion (move to B): %v", err)
	}
	if movedID != qID {
		t.Fatalf("expected the same question id to survive the move, got %d want %d", movedID, qID)
	}

	afterMove, err := repo.GetQuestion(ctx, qID)
	if err != nil {
		t.Fatalf("GetQuestion (after move): %v", err)
	}
	if afterMove.CategoryID == nil || int(*afterMove.CategoryID) != int(catB.ID) {
		t.Fatalf("expected category_id updated to B after the move, got %+v", afterMove.CategoryID)
	}
	if afterMove.CategorySchema != ExamCategorySchemaName(int(catB.ID)) {
		t.Fatalf("expected CategorySchema resolved to B's schema after the move, got %q", afterMove.CategorySchema)
	}
	rowsAfter := unmarshalRows(t, afterMove.ExpectedResult)
	if fmt.Sprintf("%v", rowVal(t, rowsAfter, "val")) != "999" {
		t.Fatalf("ARCH-028 (5) VIOLATED: expected the move to RE-CAPTURE expected_result against category B's val=999, but it still reads %+v (stale capture from category A)", rowsAfter)
	}

	// The decisive grading-side proof: submit the OBJECTIVELY correct query
	// (SELECT * FROM t) and confirm it grades against B's live data (999), not
	// a stale expectation frozen at A's data (100). If the recapture were
	// missing, this submission would be marked incorrect because grading
	// resolves the schema from the CURRENT category_id (B, val=999) while
	// expected_result would still be A's val=100.
	gradeResult, err := repo.Evaluate(ctx, uid, afterMove, "SELECT * FROM t", ExamGradingSandboxFor(afterMove))
	if err != nil {
		t.Fatalf("Evaluate (after move): %v", err)
	}
	if gradeResult.Status != "success" {
		t.Fatalf("grading evaluation failed after move: %s", gradeResult.Feedback)
	}
	studentRows := unmarshalRows(t, gradeResult.ValidationDetails)
	if !reflect.DeepEqual(rowsAfter, studentRows) {
		t.Fatalf("ARCH-028 (5) VIOLATED: post-move grading data %+v does not match the re-captured expected_result %+v — a student's objectively-correct answer against B would be marked incorrect against a stale A expectation", studentRows, rowsAfter)
	}
}
