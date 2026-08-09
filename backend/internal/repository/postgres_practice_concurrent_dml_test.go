package repository

// Case B probe for feedback.md OPEN-1 / 2026-08-09-practice-dml-schema-gate-design.md.
//
// Practice DML is not serialised: cmd/main.go injects the single-worker
// SubmissionQueue into QuizController only, so practice_controller.go:205
// (Validate) and :268 (ValidatePreview) call the evaluator directly and gin
// runs them concurrently. acquireSchemaEvalLock takes the per-schema advisory
// lock SHARED, so concurrent evaluations against one practice_cat_N do not
// exclude each other.
//
// (Fixed 2026-08-09 by ARCH-059: the gate moved into the engine, so the
// scheduling difference between exam and practice no longer decides whether
// concurrent DML is safe.)
//
// The audit claimed that lets two practice submissions deadlock. This file
// reproduced it (SQLSTATE 40P01, 2026-08-09) and justified ARCH-058..061.
//
// It is now the regression test for that fix. The gate lives inside
// repo.Evaluate, so ProductionPath — which calls Evaluate — must pass, while
// Mechanism issues raw SQL in evaluateDML's transaction shape and bypasses the
// gate on purpose, so it still reports 40P01. That contrast is the point: it
// shows the deadlock is still constructible and that the gate is what stops
// students meeting it.
//
// Construction. Seed SQL is lecturer-authored, INSERT-only, and may carry
// several statements (validateSeedSQL / applyQuestionSeed in dml_autoseed.go),
// and it replays INSIDE the grading transaction after SAVEPOINT student_eval.
// Two questions in one category whose seeds touch the same two tables in
// OPPOSITE order therefore acquire two unique-index locks in opposite order:
//
//	Q1 seed: INSERT alpha(key_a)          then INSERT beta(key_b)
//	Q2 seed: INSERT beta(key_b)           then INSERT alpha(key_a)
//
// Timing is forced rather than raced. pg_sleep is embedded in the statement
// that must run second, so the interleaving is deterministic:
//
//	t=0.00  A inserts alpha  -> holds alpha's key
//	t=0.25  B inserts beta   -> holds beta's key
//	t=0.25  B inserts alpha  -> blocks on A
//	t=0.60  A inserts beta   -> blocks on B      => cycle, SQLSTATE 40P01
//
// Two subtests, deliberately separate:
//
//	Mechanism — replicates evaluateDML's exact transaction shape with raw SQL
//	  on the exam_student pool and asserts the SQLSTATE is literally 40P01.
//	  This pins the CAUSE, which the production path erases (see below).
//
//	ProductionPath — two concurrent repo.Evaluate calls, the thing students
//	  actually hit. evaluateDML catches a seed failure and returns
//	  {Status: "error", Feedback: AutoSeedFailureFeedback} with a NIL Go error
//	  (postgres_evaluation.go:258-289), so the SQLSTATE is unrecoverable here —
//	  which is exactly why the student is told to contact their instructor for
//	  what is really a server-side concurrency defect.
//
// Note both actors seed the SAME key values. Without a deadlock that is
// harmless: the loser blocks on the uncommitted key, and the winner's
// ROLLBACK TO SAVEPOINT removes it, so the loser then succeeds. A seed
// failure in this fixture is therefore not a unique violation.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/ -run TestPracticeConcurrentDML -p 1 -v

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

const (
	caseBTableAlpha = "caseb_alpha"
	caseBTableBeta  = "caseb_beta"

	caseBKeyAlpha = "999999999"
	caseBKeyBeta  = "888888888"

	// caseBLateHold delays the actor that must acquire its SECOND lock last.
	// caseBEarlyHold delays the peer just enough to take its FIRST lock after
	// the other actor already holds its own. Both sit well inside evaluateDML's
	// 30 s ceiling and above Postgres's 1 s default deadlock_timeout.
	caseBLateHold  = "0.60"
	caseBEarlyHold = "0.25"
)

// mkCaseBFixture creates a practice category holding two independently-keyed
// tables, and returns the category id, owner id and resolved schema.
func mkCaseBFixture(t *testing.T, repo *PostgresRepository, ctx context.Context) (catID, uid int, schema string) {
	t.Helper()

	// Order-independence. Several tests in this package re-run
	// CreateRestrictedUser (postgres_exam_categories_test.go:566,
	// postgres_exam_categories_gap_test.go:105, postgres_preview_role_test.go,
	// postgres_restricted_pool_test.go:467), which regenerates the student role
	// passwords. openTestRepo builds the pools once behind a sync.Once, so any
	// test running afterwards inherits pools holding a stale password and every
	// poolForRole("exam_student") acquire fails 28P01. InitStudentPools is
	// idempotent by design (postgres_restricted_pool_test.go:475) — calling it
	// here makes this file's result independent of what ran before it.
	if err := repo.InitStudentPools(ctx); err != nil {
		t.Fatalf("InitStudentPools (refresh stale pools): %v", err)
	}

	catID, uid = mkPracticeCategory(t, repo, ctx, "caseb")
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);
		CREATE TABLE %s (eid TEXT PRIMARY KEY, label TEXT);
		INSERT INTO %s (ssn, name) VALUES ('111111111', 'Alice');
		INSERT INTO %s (eid, label) VALUES ('222222222', 'Beta');`,
		caseBTableAlpha, caseBTableBeta, caseBTableAlpha, caseBTableBeta)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	var err error
	schema, err = repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	return catID, uid, schema
}

// insertAlpha / insertBeta build the seed statements. hold is a pg_sleep
// duration in seconds applied BEFORE the row is inserted, so the lock is taken
// after the delay rather than before it.
func insertAlpha(hold string) string {
	if hold == "" {
		return fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('%s', 'seed-alpha')",
			caseBTableAlpha, caseBKeyAlpha)
	}
	return fmt.Sprintf(
		"INSERT INTO %s (ssn, name) SELECT '%s', 'seed-alpha' FROM (SELECT pg_sleep(%s)) AS _w",
		caseBTableAlpha, caseBKeyAlpha, hold)
}

func insertBeta(hold string) string {
	if hold == "" {
		return fmt.Sprintf("INSERT INTO %s (eid, label) VALUES ('%s', 'seed-beta')",
			caseBTableBeta, caseBKeyBeta)
	}
	return fmt.Sprintf(
		"INSERT INTO %s (eid, label) SELECT '%s', 'seed-beta' FROM (SELECT pg_sleep(%s)) AS _w",
		caseBTableBeta, caseBKeyBeta, hold)
}

// caseBQuestions builds the two practice DML questions whose seeds acquire the
// alpha and beta unique-index locks in OPPOSITE order. Q1 takes alpha at once
// and reaches for beta late; Q2 takes beta after a short delay and reaches for
// alpha immediately. Both answers are the model answer, so a shed submission
// can never be blamed on the student's SQL.
func caseBQuestions(uid int) []models.Question {
	return []models.Question{
		{
			ID:          1,
			Type:        "DML",
			DMLKind:     "DELETE",
			TargetTable: caseBTableAlpha,
			CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '%s'", caseBTableAlpha, caseBKeyAlpha),
			SeedSQL:     insertAlpha("") + "; " + insertBeta(caseBLateHold),
			CreatedBy:   models.UserID(uid),
			Points:      5,
		},
		{
			ID:          2,
			Type:        "DML",
			DMLKind:     "DELETE",
			TargetTable: caseBTableBeta,
			CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE eid = '%s'", caseBTableBeta, caseBKeyBeta),
			SeedSQL:     insertBeta(caseBEarlyHold) + "; " + insertAlpha(""),
			CreatedBy:   models.UserID(uid),
			Points:      5,
		},
	}
}

// runSeedShapedTxn replicates evaluateDML's transaction preamble exactly —
// restricted pool, shared advisory lock, savepoint, search_path with pg_temp
// last, SET LOCAL ROLE — then executes stmts in order. It returns the first
// statement error, unwrapped, so the caller can inspect the SQLSTATE that
// evaluateDML swallows.
func runSeedShapedTxn(ctx context.Context, repo *PostgresRepository, schema string, stmts []string) error {
	pool, err := repo.poolForRole("exam_student")
	if err != nil {
		return fmt.Errorf("poolForRole: %w", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := acquireSchemaEvalLock(ctx, tx, schema); err != nil {
		return fmt.Errorf("eval lock: %w", err)
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT student_eval"); err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	safeSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public, pg_temp", safeSchema)); err != nil {
		return fmt.Errorf("search_path: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE exam_student"); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	for i, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("seed stmt %d: %w", i+1, err)
		}
	}
	return nil
}

// requireStudentCanSleep fails loudly if exam_student cannot execute pg_sleep.
// The whole construction depends on it; a permission failure would otherwise
// masquerade as "no deadlock reproduced" and retire a real finding.
func requireStudentCanSleep(t *testing.T, repo *PostgresRepository, ctx context.Context, schema string) {
	t.Helper()
	if err := runSeedShapedTxn(ctx, repo, schema, []string{"SELECT pg_sleep(0)"}); err != nil {
		t.Fatalf("precondition failed: exam_student cannot execute pg_sleep (%v); "+
			"the Case B construction cannot run and a null result here would be meaningless", err)
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestPracticeConcurrentDML_CaseB_Mechanism pins the CAUSE: two evaluation-
// shaped transactions against one practice schema, seeding the same two tables
// in opposite order, produce SQLSTATE 40P01.
func TestPracticeConcurrentDML_CaseB_Mechanism(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, _, schema := mkCaseBFixture(t, repo, ctx)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	requireStudentCanSleep(t, repo, ctx, schema)

	// A: take alpha immediately, reach for beta late.
	stmtsA := []string{insertAlpha(""), insertBeta(caseBLateHold)}
	// B: take beta after a short delay, then reach for alpha at once.
	stmtsB := []string{insertBeta(caseBEarlyHold), insertAlpha("")}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})

	for i, stmts := range [][]string{stmtsA, stmtsB} {
		wg.Add(1)
		go func(idx int, s []string) {
			defer wg.Done()
			<-start
			errs[idx] = runSeedShapedTxn(ctx, repo, schema, s)
		}(i, stmts)
	}
	close(start)
	wg.Wait()

	var deadlocked []int
	for i, err := range errs {
		t.Logf("actor %d: err=%v sqlstate=%q", i, err, sqlState(err))
		// An actor that never reached its seed statements proves nothing either
		// way. Reporting that as "no deadlock" would argue for retiring a real
		// finding on the strength of a connection failure.
		if err != nil && !strings.HasPrefix(err.Error(), "seed stmt ") {
			t.Fatalf("INCONCLUSIVE: actor %d failed before running its seed statements: %v. "+
				"This is an infrastructure failure, NOT evidence about the deadlock.", i, err)
		}
		if sqlState(err) == "40P01" {
			deadlocked = append(deadlocked, i)
		}
	}

	if len(deadlocked) == 0 {
		t.Fatalf("CASE B NOT REPRODUCED: neither actor got SQLSTATE 40P01 (errs=%v). "+
			"The deadlock half of feedback.md OPEN-1 is unsupported; do not ship the "+
			"schema gate on the strength of it. Re-check Case A blocking latency instead.", errs)
	}
	if len(deadlocked) != 1 {
		t.Errorf("expected exactly one deadlock victim, got %d (%v)", len(deadlocked), deadlocked)
	}
	t.Logf("CASE B REPRODUCED: actor %v was chosen as the deadlock victim", deadlocked)
}

// TestPracticeConcurrentDML_CaseB_SerialControl is the control for the two
// subtests above. Same category, same two opposite-order seeds, same model
// answers — run one after the other instead of at the same time. Both must
// grade cleanly. If this fails, the fixture itself is broken and the
// concurrency result above means nothing.
//
// It is also the behaviour the practice schema gate is meant to restore: the
// gate turns the concurrent case into this one.
func TestPracticeConcurrentDML_CaseB_SerialControl(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid, schema := mkCaseBFixture(t, repo, ctx)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	requireStudentCanSleep(t, repo, ctx, schema)

	for i, q := range caseBQuestions(uid) {
		res, err := repo.Evaluate(ctx, 2000+i, q, q.CorrectSQL, schema)
		if err != nil {
			t.Fatalf("serial student %d: unexpected Go error: %v", i, err)
		}
		t.Logf("serial student %d: status=%q feedback=%q", i, res.Status, res.Feedback)
		if res.Status == "error" {
			t.Fatalf("serial student %d was shed (%q) — the fixture is broken, not the "+
				"concurrency. Every conclusion drawn from the concurrent subtests is void.",
				i, res.Feedback)
		}
	}
}

// TestPracticeConcurrentDML_CaseB_ProductionPath is the finding that matters:
// two students submitting CORRECT answers to two different questions in one
// practice category, concurrently, and one of them is told their question is
// broken. No student did anything wrong.
func TestPracticeConcurrentDML_CaseB_ProductionPath(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid, schema := mkCaseBFixture(t, repo, ctx)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	requireStudentCanSleep(t, repo, ctx, schema)

	type outcome struct {
		res models.ValidationResult
		err error
	}
	out := make([]outcome, 2)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, q := range caseBQuestions(uid) {
		wg.Add(1)
		go func(idx int, question models.Question) {
			defer wg.Done()
			<-start
			// Each student submits the model answer verbatim — a correct answer.
			res, err := repo.Evaluate(ctx, 1000+idx, question, question.CorrectSQL, schema)
			out[idx] = outcome{res: res, err: err}
		}(i, q)
	}
	close(start)
	wg.Wait()

	var shed []int
	for i, o := range out {
		t.Logf("student %d: status=%q feedback=%q goErr=%v", i, o.res.Status, o.res.Feedback, o.err)
		// evaluateDML returns a nil Go error for every graded outcome, seed
		// failures included. A non-nil error therefore means the evaluator never
		// ran — infrastructure, not evidence.
		if o.err != nil {
			t.Fatalf("INCONCLUSIVE: student %d's evaluation never ran: %v. "+
				"This is an infrastructure failure, NOT evidence about the deadlock.", i, o.err)
		}
		if o.res.Status == "error" && strings.Contains(o.res.Feedback, "could not be prepared for grading") {
			shed = append(shed, i)
		}
	}

	// Before ARCH-058..061 this assertion was inverted: the deadlock was the
	// finding, and this test failed on purpose to record it. The gate now lives
	// inside repo.Evaluate, one level below this call, so the same two
	// concurrent submissions must both be graded normally.
	//
	// If this fires again, the gate is gone or mis-keyed. It is the regression
	// test for the entire change — do not soften it into a t.Logf.
	if len(shed) > 0 {
		t.Fatalf("REGRESSION — ARCH-059: student(s) %v submitted the model answer verbatim "+
			"and were told %q. The seed deadlocked (SQLSTATE 40P01 — see the mechanism "+
			"subtest), which means concurrent DML reached one schema and the per-schema "+
			"gate in Evaluate did not serialise them.", shed, out[shed[0]].res.Feedback)
	}

	for i, o := range out {
		if o.res.Status != "success" {
			t.Fatalf("student %d: status=%q feedback=%q — both concurrent submissions "+
				"submitted the model answer verbatim and must grade success", i, o.res.Status, o.res.Feedback)
		}
	}

	// The reference schema must survive regardless of which actor lost.
	for _, tc := range []struct {
		table string
		key   string
		col   string
	}{
		{caseBTableAlpha, caseBKeyAlpha, "ssn"},
		{caseBTableBeta, caseBKeyBeta, "eid"},
	} {
		var n int
		row := repo.pool.QueryRow(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s.%s WHERE %s = $1",
				pgx.Identifier{schema}.Sanitize(), pgx.Identifier{tc.table}.Sanitize(), tc.col), tc.key)
		if err := row.Scan(&n); err != nil {
			t.Fatalf("post-run count on %s: %v", tc.table, err)
		}
		if n != 0 {
			t.Fatalf("seed row %q leaked into the reference schema %s.%s (count=%d) — "+
				"savepoint rollback did not hold", tc.key, schema, tc.table, n)
		}
	}
}
