package repository

// Tests for ARCH-021 (TODO-027): the fix for the RESET ROLE multi-statement
// escape. Pre-fix, student DML ran through argument-less tx.Exec, which
// vendored pgx v5.9.2 conn.go:515 always executes over the SIMPLE query
// protocol - and the simple protocol accepts MULTIPLE statements per query
// string, so "RESET ROLE; <anything>" fully defeated SET LOCAL ROLE
// exam_student and let a student read (and, via the post-image capture, keep)
// any row on the connection regardless of grant. This file proves the fix:
// execStudentStatement (postgres_student_exec.go) goes through
// PgConn().ExecParams, whose Parse message is enforced BY POSTGRES ITSELF to
// contain at most one command.
//
// Uses openTestRepo/uniqueUsername/mkPracticeCategory (shared with
// postgres_password_events_test.go / postgres_practice_title_test.go in this
// package); every test SKIPs cleanly without TEST_DATABASE_URL, so
// `go test ./...` stays green on a machine with no database.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run StudentExec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkSecretSchema creates a schema OUTSIDE any practice category / exam
// curriculum, holding one table with one row of secret data, and grants NO
// privileges on it to exam_student, exam_student_readonly or PUBLIC -
// reproducing the architect's "bee.secret" control exactly: a resource
// exam_student has no business ever reading, so the only way a student's SQL
// could reach it is by escaping the role first. A freshly created,
// non-"public" schema has no default ACL entries for any role other than its
// owner (the pool's connecting superuser, matching this project's
// DB_USER=postgres), so no explicit REVOKE is needed. Returns the schema name
// and the unique secret string seeded into it.
func mkSecretSchema(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix string) (schema string, secret string) {
	t.Helper()
	schema = uniqueUsername(namePrefix + "_secret")
	secret = uniqueUsername(namePrefix + "_payload")
	safeSchema := pgx.Identifier{schema}.Sanitize()
	stmts := []string{
		fmt.Sprintf("CREATE SCHEMA %s", safeSchema),
		fmt.Sprintf("CREATE TABLE %s.secret (id INTEGER PRIMARY KEY, val TEXT NOT NULL)", safeSchema),
		fmt.Sprintf("INSERT INTO %s.secret VALUES (1, '%s')", safeSchema, secret),
	}
	for _, s := range stmts {
		if _, err := repo.pool.Exec(ctx, s); err != nil {
			t.Fatalf("mkSecretSchema setup %q: %v", s, err)
		}
	}
	return schema, secret
}

func dropSchema(t *testing.T, repo *PostgresRepository, ctx context.Context, schema string) {
	t.Helper()
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", safeSchema)); err != nil {
		t.Errorf("dropSchema %s: %v", schema, err)
	}
}

// targetTableCount reads back the row count of schema.table using the pool's
// own (unrestricted) connection, independent of any student-role sandboxing,
// so it can serve as ground truth for "the target table is unchanged".
func targetTableCount(t *testing.T, repo *PostgresRepository, ctx context.Context, schema, table string) int {
	t.Helper()
	safe := pgx.Identifier{schema, table}.Sanitize()
	var n int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", safe)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", safe, err)
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// A1 / A2: headline negative test + non-vacuity control, graded path.
// ─────────────────────────────────────────────────────────────────────────────

// TestStudentExec_RESETROLEInjection_Rejected_Graded is acceptance A1+A2. Pre-
// fix, repo.Evaluate with the "RESET ROLE; INSERT ... SELECT ... FROM
// <secret>.secret" studentSQL returned a nil error, Status "success", and
// ValidationDetails containing the harvested secret row (measured by the
// architect via a live tx.Exec against postgres:15-alpine: CommandTag
// "INSERT 0 1", err nil). Post-fix it must be rejected cleanly by Postgres
// itself before it is ever planned.
func TestStudentExec_RESETROLEInjection_Rejected_Graded(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "resetrole_graded")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	secretSchema, secretVal := mkSecretSchema(t, repo, ctx, "resetrole_graded")
	defer dropSchema(t, repo, ctx, secretSchema)

	q := models.Question{Type: "DML", TargetTable: "t"}
	before := targetTableCount(t, repo, ctx, catSchema, "t")

	safeSecretTable := pgx.Identifier{secretSchema, "secret"}.Sanitize()
	injected := fmt.Sprintf("RESET ROLE; INSERT INTO t SELECT id, val FROM %s", safeSecretTable)

	// A1: the injection must be rejected cleanly - a nil Go error (no HTTP
	// 500), an "error" verdict, our own MultiStatementFeedback constant (no
	// schema/table/role/SQLSTATE named), no trace of the secret anywhere in
	// the captured output, and the target table left exactly as it was.
	result, err := repo.Evaluate(ctx, uid, q, injected, catSchema)
	if err != nil {
		t.Fatalf("Evaluate returned a hard error for the injection attempt (would surface as HTTP 500): %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("expected Status=error for the injection attempt, got %q (feedback=%q, details=%s)", result.Status, result.Feedback, result.ValidationDetails)
	}
	if result.Feedback != MultiStatementFeedback {
		t.Fatalf("expected Feedback to equal the exact MultiStatementFeedback constant, got %q", result.Feedback)
	}
	if strings.Contains(string(result.ValidationDetails), secretVal) {
		t.Fatalf("SECURITY REGRESSION: ValidationDetails leaked the secret payload: %s", result.ValidationDetails)
	}
	if got := targetTableCount(t, repo, ctx, catSchema, "t"); got != before {
		t.Fatalf("expected target table row count unchanged at %d, got %d", before, got)
	}

	// A2: non-vacuity control. The IDENTICAL statement WITHOUT the leading
	// "RESET ROLE; " escape must still fail - with a permission-denied error -
	// proving the secret schema really is out of exam_student's reach and that
	// A1 above is not passing merely because the underlying SELECT would have
	// failed on its own merits regardless of the escape. Pre-fix, this
	// WITHOUT-prefix control already failed this way (SQLSTATE 42501); it is
	// specifically the WITH-prefix variant in A1 that pre-fix succeeded and
	// leaked the secret.
	control := fmt.Sprintf("INSERT INTO t SELECT id, val FROM %s", safeSecretTable)
	controlResult, err := repo.Evaluate(ctx, uid, q, control, catSchema)
	if err != nil {
		t.Fatalf("Evaluate (non-vacuity control): %v", err)
	}
	if controlResult.Status != "error" {
		t.Fatalf("non-vacuity control: expected Status=error (permission denied), got %q", controlResult.Status)
	}
	if !strings.Contains(controlResult.Feedback, "permission denied") && !strings.Contains(controlResult.Feedback, "42501") {
		t.Fatalf("non-vacuity control: expected a permission-denied SQL error, got feedback=%q", controlResult.Feedback)
	}
	if strings.Contains(controlResult.Feedback, secretVal) {
		t.Fatalf("non-vacuity control: feedback unexpectedly contains the secret payload: %q", controlResult.Feedback)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// A3: same assertion for preview, both previewSchema forms.
// ─────────────────────────────────────────────────────────────────────────────

// TestStudentExec_RESETROLEInjection_Rejected_Preview is acceptance A3: the
// same injection through repo.EvaluatePreview, for both the previewSchema=""
// (real schema) and previewSchema!="" (decoy schema) resolver forms.
func TestStudentExec_RESETROLEInjection_Rejected_Preview(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "resetrole_preview")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	if _, err := repo.ImportSchemaIntoCategoryPreview(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategoryPreview: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	decoySchema, err := repo.GetPracticeCategoryPreviewSchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategoryPreviewSchema: %v", err)
	}
	if decoySchema == "" {
		t.Fatal("test setup error: expected a decoy schema after ImportSchemaIntoCategoryPreview")
	}
	secretSchema, secretVal := mkSecretSchema(t, repo, ctx, "resetrole_preview")
	defer dropSchema(t, repo, ctx, secretSchema)

	q := models.Question{Type: "DML", TargetTable: "t"}
	safeSecretTable := pgx.Identifier{secretSchema, "secret"}.Sanitize()
	injected := fmt.Sprintf("RESET ROLE; INSERT INTO t SELECT id, val FROM %s", safeSecretTable)

	assertRejected := func(t *testing.T, label string, previewSchema string) {
		t.Helper()
		pr, err := repo.EvaluatePreview(ctx, uid, q, injected, catSchema, previewSchema)
		if err != nil {
			t.Fatalf("%s: EvaluatePreview returned a hard error for the injection attempt: %v", label, err)
		}
		if pr.Result.Status != "error" {
			t.Fatalf("%s: expected Status=error, got %q (feedback=%q)", label, pr.Result.Status, pr.Result.Feedback)
		}
		if pr.Result.Feedback != MultiStatementFeedback {
			t.Fatalf("%s: expected Feedback to equal MultiStatementFeedback, got %q", label, pr.Result.Feedback)
		}
		for _, row := range pr.Rows {
			for _, v := range row {
				if s, ok := v.(string); ok && strings.Contains(s, secretVal) {
					t.Fatalf("%s: SECURITY REGRESSION: preview row leaked the secret payload: %+v", label, row)
				}
			}
		}
	}

	t.Run("previewSchema empty (real schema)", func(t *testing.T) {
		assertRejected(t, "previewSchema=\"\"", "")
	})
	t.Run("previewSchema non-empty (decoy schema)", func(t *testing.T) {
		assertRejected(t, "previewSchema=decoy", decoySchema)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// A4: helper unit behaviour - the multi-statement rejection plus every
// legitimate single-statement form that must NOT regress.
// ─────────────────────────────────────────────────────────────────────────────

// TestStudentExec_ExecStudentStatement_MultipleStatementsRejected is part of acceptance
// A4: execStudentStatement must return an error satisfying
// errors.Is(err, errMultipleStatements) for a plainly multi-statement input.
func TestStudentExec_ExecStudentStatement_MultipleStatementsRejected(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	conn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, execErr := execStudentStatement(ctx, tx, "SELECT 1; SELECT 2")
	if execErr == nil {
		t.Fatal("expected an error for a two-statement input, got nil")
	}
	if !errors.Is(execErr, errMultipleStatements) {
		t.Fatalf("expected errors.Is(err, errMultipleStatements) to hold, got %v", execErr)
	}
	if studentSQLErrorFeedback(execErr) != MultiStatementFeedback {
		t.Fatalf("expected studentSQLErrorFeedback to return MultiStatementFeedback, got %q", studentSQLErrorFeedback(execErr))
	}
}

// TestStudentExec_ExecStudentStatement_LegitimateSingleStatementsStillWork is the other
// half of acceptance A4: five legitimate single-statement forms - a bare
// statement, one trailing semicolon, two trailing semicolons, a leading
// comment line that itself contains a semicolon, and INSERT ... RETURNING -
// must all still execute with err == nil and tag.RowsAffected() == 1. All five
// were measured by the architect to work against a live postgres:15-alpine.
func TestStudentExec_ExecStudentStatement_LegitimateSingleStatementsStillWork(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "legit_single")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	conn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	safeSchema := pgx.Identifier{catSchema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
		t.Fatalf("SET LOCAL search_path: %v", err)
	}

	cases := []struct {
		name string
		sql  string
	}{
		{"bare statement", "INSERT INTO t VALUES (1)"},
		{"one trailing semicolon", "INSERT INTO t VALUES (2);"},
		{"two trailing semicolons", "INSERT INTO t VALUES (3);;"},
		{"leading comment containing a semicolon", "-- comment containing ; a semicolon\nINSERT INTO t VALUES (4)"},
		{"INSERT ... RETURNING", "INSERT INTO t VALUES (5) RETURNING id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tag, execErr := execStudentStatement(ctx, tx, c.sql)
			if execErr != nil {
				t.Fatalf("execStudentStatement(%q): unexpected error: %v", c.sql, execErr)
			}
			if tag.RowsAffected() != 1 {
				t.Fatalf("execStudentStatement(%q): expected RowsAffected()==1, got %d", c.sql, tag.RowsAffected())
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// A5: the adjacent savepoint/RESET ordering fix - a failed single-statement
// student DML must no longer 500.
// ─────────────────────────────────────────────────────────────────────────────

// TestStudentExec_InvalidSingleStatementDML_NoLongerReturns500 is acceptance
// A5. Pre-fix, when the student's single statement errored, the transaction
// was left aborted (SQLSTATE 25P02) and the following "RESET ROLE" (issued
// BEFORE the savepoint rollback) itself failed, so evaluateDML returned a hard
// Go error ("failed to reset role after student eval: ... 25P02") that the
// controllers turned into HTTP 500 with raw internal error text. Post-fix,
// ROLLBACK TO SAVEPOINT runs first and always succeeds, so this must now
// return a nil Go error with a clean "error" verdict.
func TestStudentExec_InvalidSingleStatementDML_NoLongerReturns500(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "no500")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}

	q := models.Question{Type: "DML", TargetTable: "t"}
	// A single, plainly invalid statement - references a column that does not
	// exist. This is a genuine single-statement SQL error, not a rejection by
	// execStudentStatement's multi-statement check.
	result, err := repo.Evaluate(ctx, uid, q, "INSERT INTO t VALUES (notacolumn)", catSchema)
	if err != nil {
		t.Fatalf("Evaluate returned a hard error for an invalid single-statement DML (this is the pre-fix 500 bug): %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("expected Status=error, got %q", result.Status)
	}
	if !strings.HasPrefix(result.Feedback, "SQL Error:") {
		t.Fatalf("expected Feedback to begin with %q, got %q", "SQL Error:", result.Feedback)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// A6: the QUERY path was already single-statement-safe before this change -
// prove it stays that way, with no code change to evaluateQuery/evaluateQueryPreview.
// ─────────────────────────────────────────────────────────────────────────────

// TestStudentExec_QueryPathAlreadyRejectsInjection is acceptance A6:
// repo.Evaluate on a QUERY question with a "RESET ROLE; SELECT ..." injection
// must already yield an "error" verdict with no secret leaked, because pgx's
// Conn.Query (unlike Conn.Exec) never forces the simple protocol. This does
// not exercise execStudentStatement at all - evaluateQuery/evaluateQueryPreview
// in postgres_evaluation.go were deliberately left untouched by ARCH-021.
func TestStudentExec_QueryPathAlreadyRejectsInjection(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "querypath")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	secretSchema, secretVal := mkSecretSchema(t, repo, ctx, "querypath")
	defer dropSchema(t, repo, ctx, secretSchema)

	q := models.Question{Type: "QUERY"}
	safeSecretTable := pgx.Identifier{secretSchema, "secret"}.Sanitize()
	injected := fmt.Sprintf("RESET ROLE; SELECT * FROM %s", safeSecretTable)

	result, err := repo.Evaluate(ctx, uid, q, injected, catSchema)
	if err != nil {
		t.Fatalf("Evaluate returned a hard error for the QUERY-path injection attempt: %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("expected Status=error for the QUERY-path injection attempt, got %q (feedback=%q)", result.Status, result.Feedback)
	}
	if strings.Contains(string(result.ValidationDetails), secretVal) {
		t.Fatalf("SECURITY REGRESSION: QUERY-path ValidationDetails leaked the secret payload: %s", result.ValidationDetails)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Extra verification: the RAW rejection (with SQLSTATE) below
// execStudentStatement's display-only classification, plus a rigorous,
// non-vacuous proof of the actual OBSERVABLE STATE that decides whether the
// secret ever reaches the caller.
//
// IMPORTANT DESIGN NOTE on what "observable state" has to mean here: the
// target table's PERSISTED row count is NOT a useful signal, in either the
// vulnerable or the fixed code - evaluateDML always rolls back to
// student_eval (a savepoint taken BEFORE the injected statement even runs),
// so the database itself ends up with zero extra rows in the target table
// whether or not the second statement executed. That is precisely what makes
// this vulnerability dangerous: the exploit never needed to persist a write -
// it only needed the POST-IMAGE "SELECT * FROM target" (run inside the same
// still-open transaction, before the rollback) to capture the leaked row into
// Go memory, from where it is JSON-marshalled into ValidationDetails and
// returned to the caller (and, for a real submission, persisted to
// app.submissions) - a step the rollback can never undo. So the real
// observable state to assert on is result.ValidationDetails itself: does it
// contain a captured row at all. Pre-fix, the malicious statement produced no
// error, so the "else" branch in evaluateDML ran the post-image SELECT and
// captured the harvested secret row. Post-fix, execStudentStatement errors
// immediately, so that branch - the second statement's only possible effect
// on anything the caller can observe - is never reached, and
// ValidationDetails stays exactly "null".
func TestStudentExec_RawRejectionAndObservableNonExecution(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "rawreject")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		"CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT);", uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	catSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	secretSchema, secretVal := mkSecretSchema(t, repo, ctx, "rawreject")
	defer dropSchema(t, repo, ctx, secretSchema)
	safeSecretTable := pgx.Identifier{secretSchema, "secret"}.Sanitize()

	// --- (1) capture the RAW rejection, unwrapped, with its SQLSTATE, one
	// level below execStudentStatement's display-only classification - so the
	// actual Postgres verdict is on record verbatim, not just asserted
	// indirectly through the generic student-facing feedback string. ---
	conn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	safeSchema := pgx.Identifier{catSchema}.Sanitize()
	if _, err := tx.Exec(ctx, "SAVEPOINT student_eval"); err != nil {
		t.Fatalf("SAVEPOINT: %v", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public", safeSchema)); err != nil {
		t.Fatalf("SET LOCAL search_path: %v", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student"); err != nil {
		t.Fatalf("SET LOCAL ROLE exam_student: %v", err)
	}

	injected := fmt.Sprintf("RESET ROLE; INSERT INTO t SELECT id, val FROM %s", safeSecretTable)
	rr := tx.Conn().PgConn().ExecParams(ctx, injected, nil, nil, nil, nil)
	rawTag, rawErr := rr.Close()
	if rawErr == nil {
		t.Fatalf("expected the raw ExecParams call to fail for a multi-statement injection, got tag=%v err=nil", rawTag)
	}
	t.Logf("RAW rejection error (unwrapped, pre-classification): %v", rawErr)
	var pgErr *pgconn.PgError
	if !errors.As(rawErr, &pgErr) {
		t.Fatalf("expected the raw error to be a *pgconn.PgError, got %T: %v", rawErr, rawErr)
	}
	t.Logf("RAW pgconn.PgError fields: Code=%q Routine=%q Message=%q", pgErr.Code, pgErr.Routine, pgErr.Message)
	if pgErr.Code != "42601" {
		t.Fatalf("expected SQLSTATE 42601, got %q (full error: %v)", pgErr.Code, rawErr)
	}
	if pgErr.Routine != "exec_parse_message" {
		t.Fatalf("expected Routine exec_parse_message, got %q (full error: %v)", pgErr.Routine, rawErr)
	}

	// Recover the connection exactly as evaluateDML now does (savepoint
	// rollback before RESET ROLE) so it can be released back to the pool
	// cleanly.
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT student_eval"); err != nil {
		t.Fatalf("ROLLBACK TO SAVEPOINT: %v", err)
	}
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatalf("RESET ROLE: %v", err)
	}
	_ = tx.Rollback(ctx)
	conn.Release()

	// --- (2) the OBSERVABLE STATE proof: run the identical injection through
	// the real repo.Evaluate path and assert ValidationDetails is byte-exactly
	// "null" - i.e. the post-image capture branch (the only place the second
	// statement's effect could ever reach the caller) was never entered at
	// all, not merely "happens not to mention the secret". ---
	q := models.Question{Type: "DML", TargetTable: "t"}
	result, err := repo.Evaluate(ctx, uid, q, injected, catSchema)
	if err != nil {
		t.Fatalf("Evaluate returned a hard error for the injection attempt: %v", err)
	}
	t.Logf("Evaluate(injection).ValidationDetails = %s", result.ValidationDetails)
	if string(result.ValidationDetails) != "null" {
		t.Fatalf("SECURITY REGRESSION: expected ValidationDetails==\"null\" (post-image capture never entered), got %s", result.ValidationDetails)
	}

	// --- (3) non-vacuity control: prove check (2) is a real, falsifiable
	// assertion and not trivially true because ValidationDetails is always
	// "null" regardless of what happens. A genuine single-statement write to
	// the SAME table, through the SAME repo.Evaluate path, MUST make the
	// written value observable in ValidationDetails. ---
	legitResult, err := repo.Evaluate(ctx, uid, q, "INSERT INTO t VALUES (1, 'legitimate-student-write')", catSchema)
	if err != nil {
		t.Fatalf("Evaluate (legitimate single-statement control): %v", err)
	}
	t.Logf("Evaluate(legitimate write).ValidationDetails = %s", legitResult.ValidationDetails)
	if legitResult.Status != "success" {
		t.Fatalf("expected the legitimate single-statement DML to succeed, got Status=%q Feedback=%q", legitResult.Status, legitResult.Feedback)
	}
	if !strings.Contains(string(legitResult.ValidationDetails), "legitimate-student-write") {
		t.Fatalf("non-vacuity control failed: expected the legitimate write to be visible in ValidationDetails (proving check (2) can actually detect a captured row), got %s", legitResult.ValidationDetails)
	}
	if strings.Contains(string(legitResult.ValidationDetails), secretVal) {
		t.Fatalf("non-vacuity control unexpectedly contains the secret payload: %s", legitResult.ValidationDetails)
	}
}
