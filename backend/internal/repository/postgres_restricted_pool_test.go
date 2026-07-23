package repository

// Tests for ARCH-025 (TODO-043/045: the restricted quiz_exec student-SQL
// pool) and ARCH-023 (TODO-044: the pg_temp target-table shadowing fix).
//
// WHY THIS FILE EXISTS, AND WHY IT LOOKS DIFFERENT FROM
// postgres_student_exec_test.go: two previous attempts at this vulnerability
// class shipped green test suites that were still exploitable, because those
// suites asserted the SPELLING of the exploit (a specific error string, a
// specific SQLSTATE) rather than the OBSERVABLE STATE the exploit is actually
// trying to change. A test that only checks "did we get an error" cannot
// distinguish "the escape was blocked" from "the escape succeeded but then
// something else, unrelated, happened to fail." Every test below instead
// asserts one of:
//   - identity: session_user / current_user, read directly off the
//     connection AFTER the student's statement, never postgres;
//   - absence: a secret payload seeded in a schema exam_student has no grant
//     on, never appearing in Feedback or ValidationDetails;
//   - non-vacuity: a companion legitimate write/read on the SAME code path
//     that proves the assertion above is actually falsifiable, not trivially
//     true because e.g. ValidationDetails is always empty regardless.
//
// Uses openTestRepo/uniqueUsername/mkPracticeCategory/mkSecretSchema/
// dropSchema/targetTableCount, shared with postgres_student_exec_test.go /
// postgres_password_events_test.go / postgres_practice_title_test.go in this
// package; every test SKIPs cleanly without TEST_DATABASE_URL, so
// `go test ./...` stays green on a machine with no database.
//
// Run: TEST_DATABASE_URL=postgres://... STUDENT_DB_PASSWORD=... \
//      go test ./internal/repository/... -run 'StudentPool|PgTempForgery|EscapeVectors|PositiveAssertion' -v

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// ─────────────────────────────────────────────────────────────────────────────
// TODO-043: the restricted pool itself connects as quiz_exec, and there is no
// path that falls back to the privileged pool when it cannot.
// ─────────────────────────────────────────────────────────────────────────────

// TestStudentPool_ConnectsAsRestrictedRole proves the pool
// postgres_student_pool.go builds really does authenticate as quiz_exec, not
// as whatever DATABASE_URL/TEST_DATABASE_URL's own user is (postgres, in this
// project's default .env).
func TestStudentPool_ConnectsAsRestrictedRole(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	pool, err := repo.ensureStudentPool(ctx)
	if err != nil {
		t.Fatalf("ensureStudentPool: %v", err)
	}
	var sessionUser, currentUser string
	if err := pool.QueryRow(ctx, "SELECT session_user, current_user").Scan(&sessionUser, &currentUser); err != nil {
		t.Fatalf("query session_user/current_user on the student pool: %v", err)
	}
	t.Logf("student pool identity: session_user=%q current_user=%q", sessionUser, currentUser)
	if sessionUser != studentPoolUser {
		t.Fatalf("expected the student pool's session_user to be %q, got %q", studentPoolUser, sessionUser)
	}
	if sessionUser == "postgres" || currentUser == "postgres" {
		t.Fatalf("SECURITY REGRESSION: student pool connected as postgres (session_user=%q current_user=%q)", sessionUser, currentUser)
	}
}

// TestStudentPool_MissingPasswordFailsClosed_NoFallback is the acceptance
// test for the brief's hardest requirement: "A silent fallback to the
// privileged pool would restore the exact vulnerability — never do that."
// With STUDENT_DB_PASSWORD unset, a BRAND NEW repository (its own
// sync.Once, independent of the package-wide openTestRepo singleton, so this
// does not disturb any other test) must fail InitStudentPool AND must fail
// every Evaluate call — never silently execute the student's SQL on the
// privileged pool instead.
func TestStudentPool_MissingPasswordFailsClosed_NoFallback(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}

	saved, had := os.LookupEnv(StudentDBPasswordEnv)
	if err := os.Unsetenv(StudentDBPasswordEnv); err != nil {
		t.Fatalf("Unsetenv(%s): %v", StudentDBPasswordEnv, err)
	}
	defer func() {
		if had {
			_ = os.Setenv(StudentDBPasswordEnv, saved)
		}
	}()

	repo, err := NewPostgresRepository(dsn)
	if err != nil {
		t.Fatalf("NewPostgresRepository: %v", err)
	}
	defer repo.Close()

	initErr := repo.InitStudentPool(context.Background())
	if initErr == nil {
		t.Fatal("expected InitStudentPool to fail when STUDENT_DB_PASSWORD is unset, got nil error")
	}
	t.Logf("InitStudentPool error (expected): %v", initErr)
	if !strings.Contains(initErr.Error(), StudentDBPasswordEnv) {
		t.Fatalf("expected the error to name %s so an operator can diagnose it, got: %v", StudentDBPasswordEnv, initErr)
	}

	// The real requirement: NOTHING downstream may fall back to r.pool. A
	// harmless QUERY submission against a real, existing schema must still
	// fail — proving evaluateQuery bailed out at ensureStudentPool and never
	// reached tx.Query on the privileged connection at all.
	_, evalErr := repo.Evaluate(context.Background(), 0, models.Question{Type: "QUERY"}, "SELECT 1", "public")
	if evalErr == nil {
		t.Fatal("SECURITY REGRESSION: Evaluate succeeded with no student pool available — this can only mean it fell back to the privileged pool")
	}
	t.Logf("Evaluate error with no student pool available (expected): %v", evalErr)
}

// ─────────────────────────────────────────────────────────────────────────────
// TODO-044 (ARCH-023): pg_temp target-table shadowing.
// ─────────────────────────────────────────────────────────────────────────────

// TestPgTempForgery_TargetTableShadowing_Rejected is the regression test
// referenced by the ARCH-023 comment in postgres_evaluation.go. Pre-fix, a
// student's single statement "CREATE TEMP TABLE <target_table> AS SELECT
// <anything>" shadowed the real target table for the post-image capture,
// because Postgres searches pg_temp BEFORE every schema named in
// search_path unless pg_temp is named explicitly. Measured (see the report):
// with search_path = "<sbx>, public" a temp table t makes "SELECT * FROM t"
// return the FORGED row; with pg_temp appended last it returns the REAL row.
// This test drives the exact same scenario through the real repo.Evaluate
// DML path (not raw SQL), so it exercises the fix as students actually would.
func TestPgTempForgery_TargetTableShadowing_Rejected(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "pgtemp_forgery")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (v TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	// Ground truth: seed the REAL target table with a distinguishable value,
	// via the privileged pool directly (independent of any student-role
	// sandboxing, matching targetTableCount's own ground-truth pattern).
	safeTable := pgx.Identifier{catSchema, "t"}.Sanitize()
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf("INSERT INTO %s VALUES ('REAL_SANDBOX_ROW')", safeTable)); err != nil {
		t.Fatalf("seed real row: %v", err)
	}

	q := models.Question{Type: "DML", TargetTable: "t"}
	// A single statement: CREATE TABLE ... AS SELECT is one command, so it
	// passes execStudentStatement's one-statement rule cleanly — this is not
	// the ARCH-021 multi-statement bypass, it is a completely different
	// mechanism (search_path resolution order), which is exactly why it
	// needed its own fix.
	forgery := "CREATE TEMP TABLE t AS SELECT 'FAKE_CORRECT'::text AS v"

	result, err := repo.Evaluate(ctx, uid, q, forgery, catSchema)
	if err != nil {
		t.Fatalf("Evaluate returned a hard error for the pg_temp forgery attempt: %v", err)
	}
	t.Logf("Evaluate(pg_temp forgery) = Status=%q Feedback=%q ValidationDetails=%s", result.Status, result.Feedback, result.ValidationDetails)

	// The CREATE TEMP TABLE statement itself is valid SQL and exam_student
	// has TEMP privilege by default, so it succeeds — the point of the fix is
	// what the FOLLOWING post-image SELECT sees, not whether this statement
	// errors.
	if result.Status != "success" {
		t.Fatalf("expected the CREATE TEMP TABLE statement to execute successfully (this is not what the fix rejects), got Status=%q Feedback=%q", result.Status, result.Feedback)
	}
	if strings.Contains(string(result.ValidationDetails), "FAKE_CORRECT") {
		t.Fatalf("SECURITY REGRESSION: pg_temp shadowed the real target table — post-image was forged: %s", result.ValidationDetails)
	}
	if !strings.Contains(string(result.ValidationDetails), "REAL_SANDBOX_ROW") {
		t.Fatalf("expected the post-image to reflect the REAL target table's row, got %s", result.ValidationDetails)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TODO-045: escape vectors on BOTH the QUERY and DML paths. Every case
// asserts Status=="error" plus the ABSENCE of a seeded secret payload in
// Feedback/ValidationDetails — never the specific error string or SQLSTATE.
// ─────────────────────────────────────────────────────────────────────────────

type escapeVectorCase struct {
	name string
	sql  string
}

// escapeVectors returns the five required vectors. secretTable is a
// schema-qualified, already-sanitized reference to a table exam_student has
// no grant on (see mkSecretSchema); targetInsert is how to smuggle a read of
// it into the DML path's own writable target table "t(id INTEGER, txt
// TEXT)".
func escapeVectors(secretTable string, dmlPath bool) []escapeVectorCase {
	cases := []escapeVectorCase{
		{"bare SET ROLE postgres", "SET ROLE postgres"},
		{
			"DO block RESET ROLE + RAISE-based exfiltration attempt",
			fmt.Sprintf(`DO $$ DECLARE v text; BEGIN RESET ROLE; SELECT val INTO v FROM %s LIMIT 1; RAISE EXCEPTION 'leaked: %%', v; END $$`, secretTable),
		},
		{"set_config('role','postgres',false) role escalation", "SELECT set_config('role','postgres',false)"},
	}
	if dmlPath {
		cases = append(cases,
			escapeVectorCase{"COPY TO PROGRAM shell exec", "COPY (SELECT 1) TO PROGRAM 'touch /tmp/claude_arch025_pwn_dml_proof'"},
			escapeVectorCase{"direct read of app.system_users", "INSERT INTO t(id, txt) SELECT 999, username FROM app.system_users LIMIT 1"},
		)
	} else {
		cases = append(cases,
			escapeVectorCase{"COPY TO PROGRAM shell exec", "COPY (SELECT 1) TO PROGRAM 'touch /tmp/claude_arch025_pwn_query_proof'"},
			escapeVectorCase{"direct read of app.system_users", "SELECT username, password_hash FROM app.system_users LIMIT 1"},
		)
	}
	return cases
}

// TestEscapeVectors_DML runs all five vectors through repo.Evaluate on the
// DML path (target table "t"). Every vector must yield Status=="error", must
// never leak the secret payload, and must leave the target table's row count
// unchanged. The COPY vector is additionally verified never to have reached
// the filesystem, independently of this Go test, by inspecting the database
// server's own filesystem — see the report for that command's real output;
// Postgres's own "must be superuser or have privileges of the
// pg_execute_server_program role" check runs BEFORE the subprocess is ever
// spawned, so the clean "error" verdict asserted here is itself already
// proof the command never ran, not merely a proxy for it.
func TestEscapeVectors_DML(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "escape_dml")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, txt TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	secretSchema, secretVal := mkSecretSchema(t, repo, ctx, "escape_dml")
	defer dropSchema(t, repo, ctx, secretSchema)
	safeSecretTable := pgx.Identifier{secretSchema, "secret"}.Sanitize()

	q := models.Question{Type: "DML", TargetTable: "t"}
	before := targetTableCount(t, repo, ctx, catSchema, "t")

	for _, c := range escapeVectors(safeSecretTable, true) {
		t.Run(c.name, func(t *testing.T) {
			result, err := repo.Evaluate(ctx, uid, q, c.sql, catSchema)
			if err != nil {
				t.Fatalf("Evaluate returned a hard error (should be a clean 'error' verdict, not a Go error / HTTP 500): %v", err)
			}
			t.Logf("%s -> Status=%q Feedback=%q", c.name, result.Status, result.Feedback)
			if result.Status != "error" {
				t.Fatalf("expected Status=error, got %q (feedback=%q details=%s)", result.Status, result.Feedback, result.ValidationDetails)
			}
			if strings.Contains(result.Feedback, secretVal) || strings.Contains(string(result.ValidationDetails), secretVal) {
				t.Fatalf("SECURITY REGRESSION: secret payload leaked: feedback=%q details=%s", result.Feedback, result.ValidationDetails)
			}
			if got := targetTableCount(t, repo, ctx, catSchema, "t"); got != before {
				t.Fatalf("expected target table row count unchanged at %d, got %d", before, got)
			}
		})
	}
}

// TestEscapeVectors_QUERY runs the same five vectors through repo.Evaluate on
// the QUERY path (SELECT-only, exam_student_readonly). Every vector must
// yield Status=="error" (or, for the DO block, at minimum never surface the
// secret — see the inline comment) with no secret payload leaked.
func TestEscapeVectors_QUERY(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "escape_query")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	secretSchema, secretVal := mkSecretSchema(t, repo, ctx, "escape_query")
	defer dropSchema(t, repo, ctx, secretSchema)
	safeSecretTable := pgx.Identifier{secretSchema, "secret"}.Sanitize()

	q := models.Question{Type: "QUERY"}

	for _, c := range escapeVectors(safeSecretTable, false) {
		t.Run(c.name, func(t *testing.T) {
			result, err := repo.Evaluate(ctx, uid, q, c.sql, catSchema)
			if err != nil {
				t.Fatalf("Evaluate returned a hard error (should be a clean 'error' verdict, not a Go error / HTTP 500): %v", err)
			}
			t.Logf("%s -> Status=%q Feedback=%q Details=%s", c.name, result.Status, result.Feedback, result.ValidationDetails)
			if result.Status != "error" {
				t.Fatalf("expected Status=error, got %q (feedback=%q details=%s)", result.Status, result.Feedback, result.ValidationDetails)
			}
			if strings.Contains(result.Feedback, secretVal) || strings.Contains(string(result.ValidationDetails), secretVal) {
				t.Fatalf("SECURITY REGRESSION: secret payload leaked: feedback=%q details=%s", result.Feedback, result.ValidationDetails)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Required positive assertions: identity, checked directly, not inferred
// from an error string.
// ─────────────────────────────────────────────────────────────────────────────

// TestPositiveAssertion_QuerySessionUserNeverPostgres is the brief's required
// positive check for the QUERY path: a legitimate "SELECT session_user,
// current_user" submission must return quiz_exec / exam_student_readonly and
// NEVER postgres.
func TestPositiveAssertion_QuerySessionUserNeverPostgres(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "posassert_query")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	q := models.Question{Type: "QUERY"}
	result, err := repo.Evaluate(ctx, uid, q, "SELECT session_user, current_user", catSchema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("expected the plain identity SELECT to succeed, got Status=%q Feedback=%q", result.Status, result.Feedback)
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal(result.ValidationDetails, &rows); err != nil {
		t.Fatalf("unmarshal ValidationDetails %s: %v", result.ValidationDetails, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row, got %d: %s", len(rows), result.ValidationDetails)
	}
	sessionUser, _ := rows[0]["session_user"].(string)
	currentUser, _ := rows[0]["current_user"].(string)
	t.Logf("QUERY path identity: session_user=%q current_user=%q", sessionUser, currentUser)

	if sessionUser != "quiz_exec" {
		t.Fatalf("expected session_user=quiz_exec, got %q", sessionUser)
	}
	if currentUser != "exam_student_readonly" {
		t.Fatalf("expected current_user=exam_student_readonly, got %q", currentUser)
	}
	if sessionUser == "postgres" || currentUser == "postgres" {
		t.Fatalf("SECURITY REGRESSION: identity check observed postgres (session_user=%q current_user=%q)", sessionUser, currentUser)
	}
}

// TestPositiveAssertion_DMLPostImageShowsQuizExec is the brief's required
// positive check for the DML path: a legitimate "INSERT INTO <target>(txt)
// SELECT session_user" submission must show quiz_exec in the post-image.
func TestPositiveAssertion_DMLPostImageShowsQuizExec(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "posassert_dml")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, txt TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	q := models.Question{Type: "DML", TargetTable: "t"}
	result, err := repo.Evaluate(ctx, uid, q, "INSERT INTO t(id, txt) SELECT 1, session_user", catSchema)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	t.Logf("DML post-image: Status=%q ValidationDetails=%s", result.Status, result.ValidationDetails)
	if result.Status != "success" {
		t.Fatalf("expected the legitimate INSERT to succeed, got Status=%q Feedback=%q", result.Status, result.Feedback)
	}
	if !strings.Contains(string(result.ValidationDetails), "quiz_exec") {
		t.Fatalf("expected the post-image to show quiz_exec as session_user, got %s", result.ValidationDetails)
	}
	if strings.Contains(string(result.ValidationDetails), "postgres") {
		t.Fatalf("SECURITY REGRESSION: post-image shows postgres: %s", result.ValidationDetails)
	}
}
