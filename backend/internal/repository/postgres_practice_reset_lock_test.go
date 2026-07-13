package repository

import (
	"context"
	"testing"
	"time"
)

// TestPracticeResetLockKey_Stable pins the advisory-lock key so it can never
// drift silently. The key MUST be identical on the reset side and the eval side
// (both derive it from practiceResetLockKey); if a refactor changed the seed
// string on only one side the two would stop mutually excluding and the race
// this whole feature closes would come back undetected. No database needed.
func TestPracticeResetLockKey_Stable(t *testing.T) {
	if practiceResetLockKey == 0 {
		t.Fatal("practiceResetLockKey must be non-zero")
	}
	if got := fnv1a64Signed("sql_quiz/practice:reset"); got != practiceResetLockKey {
		t.Fatalf("practiceResetLockKey drifted: got %d want %d", practiceResetLockKey, got)
	}
	// acquirePracticeEvalLock is a no-op for non-practice schemas and must not
	// touch the (here nil) tx — exam evaluations pay nothing.
	if err := acquirePracticeEvalLock(context.Background(), nil, "sandbox_exam_9"); err != nil {
		t.Fatalf("non-practice schema must skip the lock, got %v", err)
	}
	if err := acquirePracticeEvalLock(context.Background(), nil, "public"); err != nil {
		t.Fatalf("public schema must skip the lock, got %v", err)
	}
}

// TestResetPracticeSandbox_BlocksOnInFlightEval is the race regression test.
// It simulates an in-flight practice evaluation by holding the shared advisory
// lock in an open transaction, then asserts ResetPracticeSandbox blocks until
// that transaction releases — i.e. the rebuild can never drop the schema out
// from under a running eval.
func TestResetPracticeSandbox_BlocksOnInFlightEval(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// Make sure the schema and state row exist before we start racing.
	if err := repo.InitializePracticeSandbox(ctx); err != nil {
		t.Fatalf("InitializePracticeSandbox: %v", err)
	}

	// Simulate an in-flight eval: open a tx and take the SHARED lock, exactly
	// as acquirePracticeEvalLock does inside evaluateDML/evaluateQuery.
	evalConn, err := repo.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire eval conn: %v", err)
	}
	defer evalConn.Release()
	evalTx, err := evalConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin eval tx: %v", err)
	}
	if _, err := evalTx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1)", practiceResetLockKey); err != nil {
		t.Fatalf("take shared lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, resetErr := repo.ResetPracticeSandbox(ctx)
		done <- resetErr
	}()

	// While the shared lock is held, the exclusive reset must NOT complete.
	select {
	case err := <-done:
		t.Fatalf("ResetPracticeSandbox completed while an eval held the shared lock (err=%v); the schema could be dropped mid-eval", err)
	case <-time.After(400 * time.Millisecond):
		// Correctly blocked.
	}

	// Release the eval; the reset must now proceed and succeed.
	if err := evalTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback eval tx: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ResetPracticeSandbox failed after lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResetPracticeSandbox did not complete after the eval released its lock")
	}
}
