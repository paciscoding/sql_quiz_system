package schemagate

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSchemaGate_MutualExclusion is the whole point of the type: for one
// schema key, only one caller may be inside the critical section at a time.
func TestSchemaGate_MutualExclusion(t *testing.T) {
	g := NewSchemaGate()
	ctx := context.Background()

	var inFlight, maxSeen int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.Acquire(ctx, "practice_cat_1")
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			defer release()
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				old := atomic.LoadInt32(&maxSeen)
				if cur <= old || atomic.CompareAndSwapInt32(&maxSeen, old, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxSeen); got != 1 {
		t.Fatalf("max concurrent holders = %d, want 1", got)
	}
}

// TestSchemaGate_ZeroValueIsUsable: PostgresRepository holds a SchemaGate as a
// plain field so NewPostgresRepository needs no edit. If the zero value were
// not usable that would be a nil-map panic on the first DML evaluation.
func TestSchemaGate_ZeroValueIsUsable(t *testing.T) {
	var g SchemaGate
	ctx := context.Background()

	release, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("Acquire on zero value: %v", err)
	}
	release()

	held, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("re-Acquire on zero value: %v", err)
	}
	defer held()
	// waitCtx is created here, immediately before the Acquire it races, not
	// earlier — see TestSchemaGate_DoubleReleaseIsSafe for why: creating it
	// before intervening statements risks the context already being dead by
	// the time Acquire runs, which would make the DeadlineExceeded assertion
	// below pass vacuously instead of proving the gate excluded anything.
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(waitCtx, "practice_cat_1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("zero value does not actually exclude: %v", err)
	}
}

// TestSchemaGate_DistinctKeysDoNotBlock proves the gate is per-schema and not
// global. If these serialised, all DML would be serialised system-wide, which
// is the behaviour this design explicitly rejects.
func TestSchemaGate_DistinctKeysDoNotBlock(t *testing.T) {
	g := NewSchemaGate()
	ctx := context.Background()

	relA, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	defer relA()

	done := make(chan struct{})
	go func() {
		relB, err := g.Acquire(ctx, "practice_cat_2")
		if err == nil {
			relB()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire on a different schema blocked behind practice_cat_1")
	}
}

// TestSchemaGate_DeadlineExceeded_DoesNotLeak: a waiter that times out must
// not consume the slot. If it did, one slow evaluation would wedge a schema
// permanently.
func TestSchemaGate_DeadlineExceeded_DoesNotLeak(t *testing.T) {
	g := NewSchemaGate()

	release, err := g.Acquire(context.Background(), "practice_cat_1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rel2, err := g.Acquire(waitCtx, "practice_cat_1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got err=%v rel=%v", err, rel2 != nil)
	}
	if rel2 != nil {
		t.Fatal("Acquire must not return a release func alongside an error")
	}

	release()

	rel3, err := g.Acquire(context.Background(), "practice_cat_1")
	if err != nil {
		t.Fatalf("gate wedged after a timed-out waiter: %v", err)
	}
	rel3()
}

// TestSchemaGate_CanceledIsDistinguishable: acquireDMLGate tells a caller-side
// cancellation (propagate ctx.Err()) from its own wait budget expiring (return
// ErrSchemaBusy), so the two causes must not collapse into one error value.
func TestSchemaGate_CanceledIsDistinguishable(t *testing.T) {
	g := NewSchemaGate()

	release, err := g.Acquire(context.Background(), "practice_cat_1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer release()

	waitCtx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	if _, err := g.Acquire(waitCtx, "practice_cat_1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// TestSchemaGate_DoubleReleaseIsSafe: the engine releases with defer; a second
// call must never free another caller's slot.
func TestSchemaGate_DoubleReleaseIsSafe(t *testing.T) {
	g := NewSchemaGate()
	ctx := context.Background()

	release, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	release()

	held, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("Acquire after double release: %v", err)
	}
	defer held()

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(waitCtx, "practice_cat_1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("double release freed a slot it did not own: %v", err)
	}
}

// TestSchemaGate_PanicLeavesGateUsable: the engine holds the gate across
// evaluateDML with a deferred release, so a panicking evaluation must not wedge
// the schema.
func TestSchemaGate_PanicLeavesGateUsable(t *testing.T) {
	g := NewSchemaGate()
	ctx := context.Background()

	func() {
		defer func() { _ = recover() }()
		release, err := g.Acquire(ctx, "practice_cat_1")
		if err != nil {
			t.Errorf("Acquire: %v", err)
			return
		}
		defer release()
		panic("evaluation exploded")
	}()

	rel, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("gate wedged after a panic: %v", err)
	}
	rel()
}

// TestSchemaGate_EmptySchemaIsNoop mirrors acquireSchemaEvalLock's treatment of
// the empty schema (repository/postgres_rebuild_lock.go:27).
func TestSchemaGate_EmptySchemaIsNoop(t *testing.T) {
	g := NewSchemaGate()
	ctx := context.Background()

	r1, err := g.Acquire(ctx, "")
	if err != nil {
		t.Fatalf("Acquire(\"\"): %v", err)
	}
	r2, err := g.Acquire(ctx, "")
	if err != nil {
		t.Fatalf("second Acquire(\"\") must not block: %v", err)
	}
	r1()
	r2()
}
