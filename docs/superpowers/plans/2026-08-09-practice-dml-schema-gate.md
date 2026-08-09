# Practice DML Schema Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the evaluation engine serialise DML per execution schema, so two concurrent submissions can no longer deadlock each other, without adding any database state and without serialising SELECT.

**Architecture:** A process-local `SchemaGate` (a `map[string]chan struct{}`, one capacity-1 channel per schema) is held by `PostgresRepository` and acquired inside `Evaluate` and `EvaluatePreview` on the DML branch only, keyed on the schema those functions have already resolved as the execution target. A capacity-1 channel rather than `sync.Mutex` because the wait must be cancellable and time-bounded. Waiting past the budget returns the sentinel `ErrSchemaBusy`, which both controllers map to HTTP 503.

**Tech Stack:** Go 1.x, gin, pgx/v5, PostgreSQL. Tests use the existing `openTestRepo` / `openCtrlTestRepo` harnesses, which skip cleanly when `TEST_DATABASE_URL` is unset.

**Spec:** `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md` (ARCH-058 … ARCH-061), **revision 2**.

**Evidence this is needed:** `backend/internal/repository/postgres_practice_concurrent_dml_test.go` already reproduces the defect against unmodified code — `SQLSTATE 40P01`, with a serial control proving concurrency is the cause. `TestPracticeConcurrentDML_CaseB_ProductionPath` currently **fails on purpose**. Task 2 turns it green, and it is the primary regression test for the whole change.

## Read this before Task 1: why the gate is in the engine

An earlier revision of this plan put the gate in `PracticeController`. It was wrong for three reasons, and knowing them prevents "simplifying" it back:

1. **`repo.Evaluate` is the single convergence point.** Its only callers are `validation_service.go:92` and `:123`. A guard above it protects the callers that remembered to ask; a guard inside it protects the property.
2. **The preview key was wrong.** `EvaluatePreview` executes DML against `previewSchema` when a decoy exists and `sandboxSchema` only otherwise (`postgres_evaluation.go:389-397`). A controller-level gate keyed on the grading schema would have gated a schema the DML never touches.
3. **It missed a real contention pair.** Practice preview resolves `question.CategoryPreviewSchema` (`practice_controller.go:258`) and exam preview can resolve the same value (`quiz_controller.go:352`). Two DML previews, one decoy schema, and only one of them is inside the queue.

## Global Constraints

- **Run backend tests with `-p 1`.** They are DB-global; plain `go test ./...` fabricates failures.
- **No Postgres changes.** No table, column, migration, DDL, or persisted state anywhere in this plan.
- **`SubmissionQueue` is untouched** — not its wiring, capacity, timeout, or worker count. `internal/services/submission_queue.go` and `cmd/main.go` get zero edits.
- **SELECT is never gated**, on either path.
- **`schemaGateWaitBudget = 15 * time.Second`** — a named package-level `var` in `internal/repository`, never an inline literal. It is a `var` rather than a `const` solely so tests can shorten it; production never assigns to it.
- **Never pass the wait context into evaluation.** `waitCtx` bounds the gate wait and nothing else; `ctx` is what reaches `evaluateDML` / `evaluateDMLPreview`. Passing `waitCtx` down would let queue-wait consume the execution window — the exact defect the audit found on the exam side.
- **`ErrSchemaBusy` must not wrap `context.DeadlineExceeded`.** `quiz_controller.go:270` already maps `DeadlineExceeded` to 504; a busy schema is a 503.
- **503 body shape is exactly** `{"error": "practice_busy", "message": "Another submission for this category is being graded. Please try again."}` on the practice path, matching the `question_not_ready` shape at `practice_controller.go:191`.

---

## File Structure

| File | Responsibility |
|---|---|
| `backend/internal/schemagate/schema_gate.go` | **Create.** `SchemaGate` — the per-schema serialisation primitive. A leaf package: no project imports, so `repository` can use it without closing the `services` → `repository` cycle. |
| `backend/internal/schemagate/schema_gate_test.go` | **Create.** DB-free unit tests for the primitive. |
| `backend/internal/repository/postgres.go` | **Modify.** One struct field. |
| `backend/internal/repository/postgres_evaluation.go` | **Modify.** `ErrSchemaBusy`, `schemaGateWaitBudget`, `acquireDMLGate`, and the two acquire sites. |
| `backend/internal/repository/postgres_practice_concurrent_dml_test.go` | **Modify.** Flip the deliberate failure into the regression assertion. |
| `backend/internal/repository/postgres_schema_gate_test.go` | **Create.** Engine-level gating behaviour: preview keying, SELECT not gated, distinct schemas. |
| `backend/internal/controllers/practice_controller.go` | **Modify.** Validator seam; map `ErrSchemaBusy` to 503 *before* persisting the attempt. |
| `backend/internal/controllers/quiz_controller.go` | **Modify.** One arm in the existing error ladder. |
| `backend/internal/controllers/practice_gate_test.go` | **Create.** 503-and-no-attempt-row, via a fake validator that returns the sentinel. |
| `backend/internal/repository/postgres_student_pool.go` | **Modify.** Comment only. |

---

### Task 1: `SchemaGate` primitive

**Files:**
- Create: `backend/internal/schemagate/schema_gate.go`
- Test: `backend/internal/schemagate/schema_gate_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: package `schemagate` with `NewSchemaGate() *SchemaGate` and `(*SchemaGate).Acquire(ctx context.Context, schema string) (release func(), err error)`. Task 2 depends on exactly these names and this signature. Two guarantees Task 2 relies on: `Acquire` never returns a nil `release` together with a nil `err`, and **the zero value of `SchemaGate` is usable** without calling `NewSchemaGate`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/schemagate/schema_gate_test.go`:

```go
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

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	held, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("re-Acquire on zero value: %v", err)
	}
	defer held()
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
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd backend && go test ./internal/schemagate/ -run TestSchemaGate -v
```

Expected: the package does not exist yet — `no Go files` or a build failure naming `NewSchemaGate`.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/schemagate/schema_gate.go`:

```go
// Package schemagate provides an in-process, per-key serialiser.
//
// It is a leaf package on purpose: internal/services already imports
// internal/repository, so a gate living in services could not be imported by
// repository without closing an import cycle. Nothing here imports anything
// from this project.
package schemagate

import (
	"context"
	"sync"
)

// SchemaGate serialises work per schema name, inside this process.
//
// Practice DML had no serialisation at all: cmd/main.go injects the
// single-worker SubmissionQueue into QuizController only, and
// acquireSchemaEvalLock takes the per-schema advisory lock SHARED so concurrent
// evaluations do not exclude each other. Two practice submissions whose seeds
// touch the same tables in opposite order therefore deadlock — reproduced in
// repository/postgres_practice_concurrent_dml_test.go (SQLSTATE 40P01).
//
// Each schema key owns a buffered channel of capacity 1 used as a mutex.
// A channel rather than sync.Mutex because the wait MUST be cancellable and
// time-bounded: sync.Mutex.Lock is neither, so a wedged evaluation would hang
// every later submitter for that schema with no way to shed them.
//
// The zero value is ready to use; NewSchemaGate is for standalone construction.
//
// Scope note: this is process-local, matching what SubmissionQueue already
// assumes. Two backend instances against one database would each keep their own
// gate. See the design's Known limitations.
type SchemaGate struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

// NewSchemaGate returns a ready SchemaGate.
func NewSchemaGate() *SchemaGate {
	return &SchemaGate{}
}

// gateFor returns the channel for schema, creating it — and the map — on first
// use, which is what makes the zero value usable. Entries are never evicted:
// the population is bounded by the number of schemas, so a deleted category
// leaks one channel and one map entry.
func (g *SchemaGate) gateFor(schema string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = make(map[string]chan struct{})
	}
	ch, ok := g.gates[schema]
	if !ok {
		ch = make(chan struct{}, 1)
		g.gates[schema] = ch
	}
	return ch
}

// Acquire blocks until schema is free or ctx ends.
//
// On success it returns a release func that is safe to call more than once —
// callers release with `defer`, and a second call must never free a slot the
// caller no longer owns.
//
// On failure it returns a nil release and ctx's error, so the caller can tell
// context.DeadlineExceeded from context.Canceled. It never returns a nil
// release together with a nil error.
//
// An empty schema is a no-op, mirroring acquireSchemaEvalLock
// (repository/postgres_rebuild_lock.go:27).
func (g *SchemaGate) Acquire(ctx context.Context, schema string) (func(), error) {
	if schema == "" {
		return func() {}, nil
	}
	ch := g.gateFor(schema)
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd backend && go test ./internal/schemagate/ -run TestSchemaGate -race -v
```

Expected: all eight `TestSchemaGate_*` PASS, no race warnings.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/schemagate/
git commit -m "feat(eval): add SchemaGate per-schema serialisation primitive (ARCH-058)"
```

---

### Task 2: Gate the evaluation engine

This is the task that fixes the bug. Everything after it is error plumbing and documentation.

**Files:**
- Modify: `backend/internal/repository/postgres.go:136-150` (one struct field)
- Modify: `backend/internal/repository/postgres_evaluation.go` (`Evaluate` at `:25`, `EvaluatePreview` at `:365`, plus new declarations)
- Modify: `backend/internal/repository/postgres_practice_concurrent_dml_test.go`
- Test: `backend/internal/repository/postgres_schema_gate_test.go` (create)

**Interfaces:**
- Consumes: `schemagate.NewSchemaGate` / `SchemaGate.Acquire` from Task 1.
- Produces: `repository.ErrSchemaBusy` (an `error` sentinel) and the package-level `schemaGateWaitBudget` var. Tasks 3 and 4 map `ErrSchemaBusy` to HTTP 503; Task 3's tests shorten `schemaGateWaitBudget`. Also `(*PostgresRepository).acquireDMLGate(ctx context.Context, schema string) (func(), error)`, used only inside the package.

- [ ] **Step 1: Flip the probe to the assertion it should make once gated**

`TestPracticeConcurrentDML_CaseB_ProductionPath` currently ends in a deliberate `t.Errorf` — that `t.Errorf` **was** the finding. Now that the gate sits below `repo.Evaluate`, this test becomes the direct regression guard for it.

In `backend/internal/repository/postgres_practice_concurrent_dml_test.go`, replace this whole block:

```go
	if len(shed) == 0 {
		t.Fatalf("CASE B NOT REPRODUCED on the production path: both concurrent submissions "+
			"were graded normally (%+v). If the mechanism subtest also passes clean, the "+
			"deadlock half of OPEN-1 is unsupported.", out)
	}

	t.Errorf("CONFIRMED — feedback.md OPEN-1: student(s) %v submitted the model answer verbatim "+
		"and were told %q. The seed deadlocked (SQLSTATE 40P01 — see the mechanism subtest); "+
		"evaluateDML converts that into a graded 'error' with a nil Go error, so a server-side "+
		"concurrency defect reaches the student as a broken-question message. This t.Errorf IS "+
		"the finding; replace it with a pass assertion once the practice schema gate lands.",
		shed, out[shed[0]].res.Feedback)
```

with:

```go
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
```

- [ ] **Step 2: Write the failing engine tests**

Create `backend/internal/repository/postgres_schema_gate_test.go`:

```go
package repository

// ARCH-059/060 coverage at the layer that owns the invariant.
//
// The headline assertion — two concurrent practice DML evaluations no longer
// deadlock — lives in postgres_practice_concurrent_dml_test.go, which
// reproduced the defect first. This file covers the properties that file
// cannot: that preview keys on the schema it actually executes against, that
// SELECT is not gated, that distinct schemas do not block each other, and that
// the wait budget never becomes the evaluation budget.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/ -run TestSchemaGateEngine -p 1 -v

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// gateProbe reports whether two calls were ever inside the gate at once, by
// observing the gate directly rather than by running SQL.
type gateProbe struct {
	inFlight int32
	maxSeen  int32
}

func (p *gateProbe) enter() {
	cur := atomic.AddInt32(&p.inFlight, 1)
	for {
		old := atomic.LoadInt32(&p.maxSeen)
		if cur <= old || atomic.CompareAndSwapInt32(&p.maxSeen, old, cur) {
			break
		}
	}
}

func (p *gateProbe) leave() { atomic.AddInt32(&p.inFlight, -1) }

// holdGate acquires the repository's DML gate for `schema` and holds it for
// `d`, recording overlap in p.
func holdGate(t *testing.T, repo *PostgresRepository, p *gateProbe, schema string, d time.Duration) {
	t.Helper()
	release, err := repo.acquireDMLGate(context.Background(), schema)
	if err != nil {
		t.Errorf("acquireDMLGate(%q): %v", schema, err)
		return
	}
	defer release()
	p.enter()
	time.Sleep(d)
	p.leave()
}

// TestSchemaGateEngine_SameSchemaSerialises is the primitive wired up: two
// acquires on one schema must not overlap.
func TestSchemaGateEngine_SameSchemaSerialises(t *testing.T) {
	repo := openTestRepo(t)

	var p gateProbe
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			holdGate(t, repo, &p, "practice_cat_gatetest", 100*time.Millisecond)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&p.maxSeen); got != 1 {
		t.Fatalf("ARCH-059: %d concurrent DML evaluations on one schema, want 1", got)
	}
}

// TestSchemaGateEngine_DistinctSchemasDoNotSerialise: the gate must be
// per-schema. If this reports 1, all DML in the process is serialised — a much
// worse regression than the bug being fixed, and one no other test would catch.
func TestSchemaGateEngine_DistinctSchemasDoNotSerialise(t *testing.T) {
	repo := openTestRepo(t)

	var p gateProbe
	var wg sync.WaitGroup
	for _, s := range []string{"practice_cat_gate_a", "practice_cat_gate_b"} {
		wg.Add(1)
		go func(schema string) {
			defer wg.Done()
			holdGate(t, repo, &p, schema, 100*time.Millisecond)
		}(s)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&p.maxSeen); got != 2 {
		t.Fatalf("distinct schemas were serialised (max concurrent = %d, want 2)", got)
	}
}

// TestSchemaGateEngine_BusyReturnsErrSchemaBusy: exhausting the wait budget
// must produce ErrSchemaBusy and NOT context.DeadlineExceeded, because
// quiz_controller.go:270 already maps DeadlineExceeded to 504 while a busy
// schema is a retriable 503.
func TestSchemaGateEngine_BusyReturnsErrSchemaBusy(t *testing.T) {
	repo := openTestRepo(t)

	orig := schemaGateWaitBudget
	schemaGateWaitBudget = 50 * time.Millisecond
	defer func() { schemaGateWaitBudget = orig }()

	release, err := repo.acquireDMLGate(context.Background(), "practice_cat_gate_busy")
	if err != nil {
		t.Fatalf("first acquireDMLGate: %v", err)
	}
	defer release()

	_, err = repo.acquireDMLGate(context.Background(), "practice_cat_gate_busy")
	if !errors.Is(err, ErrSchemaBusy) {
		t.Fatalf("want ErrSchemaBusy, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("ErrSchemaBusy must not wrap context.DeadlineExceeded: " +
			"the exam controller maps DeadlineExceeded to 504, but a busy schema is a 503")
	}
}

// TestSchemaGateEngine_CallerCancelPropagates: a cancelled caller must get
// ctx.Err(), not ErrSchemaBusy — the request was abandoned, the schema was not
// necessarily busy.
func TestSchemaGateEngine_CallerCancelPropagates(t *testing.T) {
	repo := openTestRepo(t)

	release, err := repo.acquireDMLGate(context.Background(), "practice_cat_gate_cancel")
	if err != nil {
		t.Fatalf("first acquireDMLGate: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err = repo.acquireDMLGate(ctx, "practice_cat_gate_cancel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if errors.Is(err, ErrSchemaBusy) {
		t.Fatal("a cancelled caller must not be reported as a busy schema")
	}
}

// TestSchemaGateEngine_QueryIsNotGated: SELECT must reach the evaluator while
// the same schema's DML gate is held. Gating SELECT would serialise the hot
// path for no benefit — reads take the advisory lock shared and are
// parallel-safe by construction.
func TestSchemaGateEngine_QueryIsNotGated(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid, schema := mkCaseBFixture(t, repo, ctx)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	// Hold the DML gate for this schema for the whole test.
	release, err := repo.acquireDMLGate(ctx, schema)
	if err != nil {
		t.Fatalf("acquireDMLGate: %v", err)
	}
	defer release()

	// CreatedBy is models.UserID, not int — see caseBQuestions in
	// postgres_practice_concurrent_dml_test.go.
	q := models.Question{
		CreatedBy:  models.UserID(uid),
		Type:       "QUERY",
		CorrectSQL: fmt.Sprintf("SELECT ssn FROM %s ORDER BY ssn", caseBTableAlpha),
	}

	done := make(chan error, 1)
	go func() {
		_, err := repo.Evaluate(ctx, 1000, q, q.CorrectSQL, schema)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("QUERY evaluation errored: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a SELECT evaluation blocked behind the DML gate; SELECT must never be gated")
	}
}

// TestSchemaGateEngine_PreviewKeysOnExecutionSchema pins the defect the
// controller-level design would have shipped. EvaluatePreview runs DML against
// previewSchema when a decoy exists (postgres_evaluation.go:389-397), so the
// gate must key on that, not on sandboxSchema. Holding sandboxSchema's gate
// must NOT block a preview that executes elsewhere.
func TestSchemaGateEngine_PreviewKeysOnExecutionSchema(t *testing.T) {
	repo := openTestRepo(t)

	// Hold the grading schema's gate.
	release, err := repo.acquireDMLGate(context.Background(), "practice_cat_gate_grading")
	if err != nil {
		t.Fatalf("acquireDMLGate: %v", err)
	}
	defer release()

	// A preview that executes against a different (decoy) schema must be free.
	done := make(chan error, 1)
	go func() {
		rel, err := repo.acquireDMLGate(context.Background(), "practice_cat_gate_decoy")
		if err == nil {
			rel()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("decoy-schema acquire failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the decoy schema blocked behind the grading schema's gate — " +
			"the key is not the execution schema")
	}
}

// TestSchemaGateEngine_WaitBudgetIsNotTheEvaluationBudget pins ARCH-060. The
// gate wait is bounded by schemaGateWaitBudget; the evaluation that follows
// must run under the caller's ctx, with the caller's deadline (or none).
func TestSchemaGateEngine_WaitBudgetIsNotTheEvaluationBudget(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid, schema := mkCaseBFixture(t, repo, ctx)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	requireStudentCanSleep(t, repo, ctx, schema)

	orig := schemaGateWaitBudget
	schemaGateWaitBudget = 200 * time.Millisecond
	defer func() { schemaGateWaitBudget = orig }()

	// caseBQuestions[0]'s seed carries a 0.60 s pg_sleep (caseBLateHold), so the
	// evaluation runs materially longer than the shortened 200 ms wait budget.
	// The gate is uncontended here, so the only way this can fail is if waitCtx
	// — rather than ctx — reached the evaluator.
	q := caseBQuestions(uid)[0]

	res, err := repo.Evaluate(ctx, 1000, q, q.CorrectSQL, schema)
	if err != nil {
		t.Fatalf("ARCH-060: evaluation failed under an uncontended gate — the wait "+
			"budget most likely became the evaluation budget: %v", err)
	}
	if res.Status == "error" {
		t.Fatalf("ARCH-060: evaluation reported %q (%s); an evaluation longer than "+
			"schemaGateWaitBudget must not be cut short by it", res.Status, res.Feedback)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/repository/ -run 'TestSchemaGateEngine|TestPracticeConcurrentDML_CaseB_ProductionPath' -p 1 -v
```

Expected: a compile failure naming `acquireDMLGate`, `ErrSchemaBusy` and `schemaGateWaitBudget`. That is the correct starting point — the whole file cannot compile until Step 4 lands.

- [ ] **Step 4: Add the gate to the repository struct**

In `backend/internal/repository/postgres.go`, the `PostgresRepository` struct ends at line 150 with the `studentPools` field. Add one field before the closing brace:

```go
	// dmlGate serialises DML evaluation per execution schema, in-process
	// (ARCH-058/059). Practice DML has no SubmissionQueue and
	// acquireSchemaEvalLock is taken SHARED, so without this two evaluations
	// whose seeds touch the same tables in opposite order deadlock with
	// SQLSTATE 40P01 and a student is told their question is broken. See
	// postgres_practice_concurrent_dml_test.go.
	//
	// A value, not a pointer: schemagate.SchemaGate's zero value is usable, so
	// NewPostgresRepository needs no change and no construction path can
	// produce a repository with a nil gate.
	dmlGate schemagate.SchemaGate
```

Add the import to the same file's import block:

```go
	"github.com/paciscoding/sql-quiz-system/internal/schemagate"
```

Do **not** edit `NewPostgresRepository` (`postgres.go:185`) — `&PostgresRepository{pool: pool}` already yields a working gate.

- [ ] **Step 5: Add the sentinel, the budget and the acquire helper**

In `backend/internal/repository/postgres_evaluation.go`, add after the import block and before `Evaluate`:

```go
// ErrSchemaBusy reports that a DML evaluation could not start because another
// evaluation held the same execution schema for longer than
// schemaGateWaitBudget.
//
// It deliberately does NOT wrap context.DeadlineExceeded. quiz_controller.go
// maps DeadlineExceeded to HTTP 504 ("submission timed out"), which would be a
// lie here: nothing ran, nothing timed out, and the request is retriable. Both
// controllers map this to 503 instead.
var ErrSchemaBusy = errors.New("schema is busy: another DML evaluation is in progress")

// schemaGateWaitBudget bounds how long a DML evaluation waits for its execution
// schema before being shed with ErrSchemaBusy.
//
// Deliberately shorter than evaluateDML's own 30 s ceiling (see :191): a
// genuinely wedged evaluation sheds waiters in half that time instead of
// hanging them, while a full class at a realistic ~300 ms per submission drains
// well inside it.
//
// A var rather than a const solely so tests can shorten it — asserting the
// shed path is otherwise a 15-second test. Production never assigns to it.
var schemaGateWaitBudget = 15 * time.Second

// acquireDMLGate serialises DML evaluation per execution schema (ARCH-059).
//
// The wait is bounded by a context of its own (ARCH-060). The caller's ctx —
// never waitCtx — is what goes on to the evaluator, so time spent queueing can
// never be deducted from the execution window.
//
// The two failure causes are kept distinct: if the CALLER's context ended, that
// is a genuine timeout or cancellation and propagates unchanged; only our own
// wait budget expiring yields ErrSchemaBusy.
func (r *PostgresRepository) acquireDMLGate(ctx context.Context, schema string) (func(), error) {
	waitCtx, cancel := context.WithTimeout(ctx, schemaGateWaitBudget)
	defer cancel()

	release, err := r.dmlGate.Acquire(waitCtx, schema)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %s", ErrSchemaBusy, schema)
	}
	return release, nil
}
```

Add `"errors"` to the file's import block. `context`, `fmt` and `time` are already imported.

- [ ] **Step 6: Acquire the gate in `Evaluate`**

In the same file, replace `Evaluate`'s body:

```go
	if question.Type == "QUERY" {
		return r.evaluateQuery(ctx, question, studentSQL, querySchemaFor(question, sandboxSchema))
	}
	return r.evaluateDML(ctx, question, studentSQL, sandboxSchema)
```

with:

```go
	if question.Type == "QUERY" {
		// SELECT is never gated: it takes the advisory lock shared, runs under
		// the SELECT-only role, and is parallel-safe by construction.
		return r.evaluateQuery(ctx, question, studentSQL, querySchemaFor(question, sandboxSchema))
	}
	// ARCH-059: at most one DML evaluation per execution schema, process-wide.
	// sandboxSchema IS the execution schema for graded DML.
	release, err := r.acquireDMLGate(ctx, sandboxSchema)
	if err != nil {
		return models.ValidationResult{}, err
	}
	defer release()
	return r.evaluateDML(ctx, question, studentSQL, sandboxSchema)
```

- [ ] **Step 7: Acquire the gate in `EvaluatePreview`**

Replace `EvaluatePreview`'s DML tail:

```go
	schema := previewSchema
	if schema == "" {
		schema = sandboxSchema
	}
	role := "exam_student"
	if useDecoy {
		role = rolePreviewStudent
	}
	return r.evaluateDMLPreview(ctx, question, studentSQL, schema, role)
```

with:

```go
	schema := previewSchema
	if schema == "" {
		schema = sandboxSchema
	}
	role := "exam_student"
	if useDecoy {
		role = rolePreviewStudent
	}
	// ARCH-059: keyed on `schema` as resolved immediately above — the schema the
	// preview actually executes DML against — and NOT on sandboxSchema. On a
	// dual-schema category those differ, and gating the grading schema here
	// would leave the decoy unprotected while blocking a schema this call never
	// touches. Keying at the point of resolution is why the gate lives in the
	// engine rather than in a controller.
	release, err := r.acquireDMLGate(ctx, schema)
	if err != nil {
		return PreviewResult{}, err
	}
	defer release()
	return r.evaluateDMLPreview(ctx, question, studentSQL, schema, role)
```

- [ ] **Step 8: Update the `Evaluate` doc comment**

The comment above `Evaluate` (`:14-24`) currently ends by claiming DML evaluations are serialised by the SubmissionQueue. That was only ever true of exam. Replace those final two sentences:

```go
// DML evaluations are serialised by the global in-process FIFO SubmissionQueue
// (single worker, capacity 256, 30 s per-job timeout); SELECT remains lock-free
// under `exam_student_readonly`.
```

with:

```go
// DML evaluations are serialised per execution schema by the in-process
// SchemaGate acquired below (ARCH-059) — this is the engine's own guarantee and
// does not depend on the caller. Exam DML additionally arrives one job at a time
// via the SubmissionQueue (single worker, capacity 256, 30 s per-job timeout),
// which practice never touches; before the gate existed that left concurrent
// practice DML able to deadlock (SQLSTATE 40P01, see
// postgres_practice_concurrent_dml_test.go). SELECT remains lock-free under
// `exam_student_readonly`.
```

- [ ] **Step 9: Run the tests to verify they pass**

```bash
cd backend && go build ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/repository/ -run 'TestSchemaGateEngine|TestPracticeConcurrentDML' -p 1 -v
```

Expected:
- all seven `TestSchemaGateEngine_*` PASS
- `TestPracticeConcurrentDML_CaseB_ProductionPath` PASS — both students graded `success`
- `TestPracticeConcurrentDML_CaseB_SerialControl` PASS
- `TestPracticeConcurrentDML_CaseB_Mechanism` **still PASSes while reporting `SQLSTATE 40P01`** — it issues raw SQL in `evaluateDML`'s transaction shape and never calls `Evaluate`, so it bypasses the gate on purpose. If it stops reporting `40P01`, something changed the seed replay, not the gate.

- [ ] **Step 10: Commit**

```bash
git add backend/internal/repository/postgres.go backend/internal/repository/postgres_evaluation.go backend/internal/repository/postgres_schema_gate_test.go backend/internal/repository/postgres_practice_concurrent_dml_test.go
git commit -m "fix(eval): serialise DML per execution schema in the engine (ARCH-059/060)"
```

---

### Task 3: Practice controller maps `ErrSchemaBusy` to 503

The bug is fixed after Task 2. This task stops the fix from creating a second, quieter one: practice persists the attempt *before* it checks the evaluation error (`practice_controller.go:205-224`), so a shed submission would be written to the student's history as `is_correct=false` for SQL that never ran.

**Files:**
- Modify: `backend/internal/controllers/practice_controller.go`
- Test: `backend/internal/controllers/practice_gate_test.go` (create)

**Interfaces:**
- Consumes: `repository.ErrSchemaBusy` from Task 2.
- Produces: the unexported `practiceValidator` interface (ARCH-061) and `(*PracticeController).abortIfSchemaBusy(c *gin.Context, err error) bool` — returns true when it has already written a 503 and the caller must return immediately. `NewPracticeController`'s parameter list is unchanged in arity and order, so `cmd/main.go:153` needs no edit.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/controllers/practice_gate_test.go`:

```go
package controllers

// ARCH-061 coverage: a submission shed because its schema was busy must be
// answered 503 and must NOT be recorded.
//
// The gate itself lives in the repository (ARCH-059) and is asserted there.
// What these tests pin is the controller's handling of the resulting sentinel,
// using a fake validator that simply returns it — deterministic, with no
// contention to arrange.
//
// They use a REAL repository (the handlers read the question, resolve the
// schema and persist the attempt) with a FAKE validator, and skip cleanly
// without TEST_DATABASE_URL via openCtrlTestRepo.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/ -run TestPracticeGate -p 1 -v

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// doJSON is the request-body counterpart to the package's existing `do` helper
// (password_http_integration_test.go:82), which sends no body.
func doJSON(r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// busyValidator always reports the schema as busy, exactly as the engine does
// when its wait budget expires.
type busyValidator struct{}

func (busyValidator) Validate(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error) {
	return models.ValidationResult{}, fmt.Errorf("%w: %s", repository.ErrSchemaBusy, schema)
}

func (busyValidator) ValidatePreview(ctx context.Context, studentID int, q models.Question, sql, schema, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error) {
	return models.ValidationResult{}, nil, nil, 0, false, fmt.Errorf("%w: %s", repository.ErrSchemaBusy, schema)
}

// buildPracticeGateRouter wires only the two practice student routes under the
// real auth stack, mirroring main.go's practice student group (minus the freeze
// middleware, which is not under test here).
func buildPracticeGateRouter(auth *services.AuthService, repo *repository.PostgresRepository, practiceCtrl *PracticeController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", NewUserController(auth, services.NewStudentImportService(repo)).Login)
	practice := v1.Group("/practice")
	practice.Use(AuthMiddleware(auth), RoleMiddleware(models.RoleStudent))
	{
		practice.POST("/submit", practiceCtrl.Submit)
		practice.POST("/preview", practiceCtrl.Preview)
	}
	return r
}

// mkGateFixture creates a lecturer, a practice category with one table, and one
// practice DML question. The seed_sql row is what makes capture succeed so
// ExpectedResultStatus reaches "captured", which the readiness gate at
// practice_controller.go:191 requires before Submit will call the validator.
func mkGateFixture(t *testing.T, repo *repository.PostgresRepository, ctx context.Context, tbl string) (catID int, questionID models.PracticeQuestionID) {
	t.Helper()

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	cat, err := repo.CreatePracticeCategory(ctx, fmt.Sprintf("gate_cat_%s", tbl), int(lecturer.ID))
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT);
		INSERT INTO %s (ssn, name) VALUES ('111111111', 'Alice');`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, int(cat.ID), importSQL, int(lecturer.ID), false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   lecturer.ID,
		CategoryID:  cat.ID,
		TargetTable: tbl,
		Type:        "DML",
		DMLKind:     "DELETE",
		Requirement: "gate fixture",
		Points:      1,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl),
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("fixture question not captured: status=%q err=%q", saved.ExpectedResultStatus, saved.CaptureError)
	}
	return int(cat.ID), q.ID
}

func submitBody(t *testing.T, qid models.PracticeQuestionID, sql string) string {
	t.Helper()
	enc, err := qid.MarshalJSON()
	if err != nil {
		t.Fatalf("encode practice question id: %v", err)
	}
	return fmt.Sprintf(`{"practice_question_id":%s,"sql_text":%q}`, string(enc), sql)
}

// TestPracticeGate_BusySubmitShedsWithoutRecordingAttempt is the assertion most
// likely to regress silently. Practice persists every attempt regardless of
// outcome (practice_controller.go:205-224), so without an explicit branch a
// shed submission is written as is_correct=false for SQL that never ran,
// corrupting the student's history and their score.
func TestPracticeGate_BusySubmitShedsWithoutRecordingAttempt(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_shed")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, busyValidator{}, auth))

	student := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	tok := login(t, r, student.Username, "pw1")

	before, err := repo.ListPracticeAttempts(ctx, int(student.ID))
	if err != nil {
		t.Fatalf("ListPracticeAttempts (before): %v", err)
	}

	body := submitBody(t, qid, "DELETE FROM gate_shed WHERE ssn = '999999999'")
	w := doJSON(r, http.MethodPost, "/api/v1/practice/submit", tok, body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("submit into a busy schema: want 503, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "practice_busy") {
		t.Fatalf("503 body must carry error=practice_busy, got %s", w.Body.String())
	}

	after, err := repo.ListPracticeAttempts(ctx, int(student.ID))
	if err != nil {
		t.Fatalf("ListPracticeAttempts (after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a shed submission was recorded: attempts went %d -> %d; "+
			"evaluation never ran, so there is nothing to persist",
			len(before), len(after))
	}
}

// TestPracticeGate_BusyPreviewSheds: preview persists nothing, but it must
// still answer 503 rather than 500 so the client can retry.
func TestPracticeGate_BusyPreviewSheds(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_shed_preview")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, busyValidator{}, auth))

	student := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	tok := login(t, r, student.Username, "pw1")

	body := submitBody(t, qid, "DELETE FROM gate_shed_preview WHERE ssn = '999999999'")
	w := doJSON(r, http.MethodPost, "/api/v1/practice/preview", tok, body)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("preview into a busy schema: want 503, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "practice_busy") {
		t.Fatalf("503 body must carry error=practice_busy, got %s", w.Body.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate -p 1 -v
```

Expected: a compile failure — `NewPracticeController` does not accept `busyValidator{}` yet, because its parameter is still the concrete `*services.ValidationService`.

- [ ] **Step 3: Add the validator seam**

In `backend/internal/controllers/practice_controller.go`, replace:

```go
type PracticeController struct {
	repo      *repository.PostgresRepository
	validator *services.ValidationService
	auth      *services.AuthService
}

func NewPracticeController(repo *repository.PostgresRepository, validator *services.ValidationService, auth *services.AuthService) *PracticeController {
	return &PracticeController{
		repo:      repo,
		validator: validator,
		auth:      auth,
	}
}
```

with:

```go
// practiceValidator is the narrow slice of *services.ValidationService that the
// practice handlers use (ARCH-061). It exists so the busy-schema branch is
// testable with a fake that returns repository.ErrSchemaBusy directly, instead
// of arranging real contention against a real database.
// *services.ValidationService satisfies it, so cmd/main.go:153 is unchanged.
type practiceValidator interface {
	Validate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error)
	ValidatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error)
}

type PracticeController struct {
	repo      *repository.PostgresRepository
	validator practiceValidator
	auth      *services.AuthService
}

func NewPracticeController(repo *repository.PostgresRepository, validator practiceValidator, auth *services.AuthService) *PracticeController {
	return &PracticeController{
		repo:      repo,
		validator: validator,
		auth:      auth,
	}
}

// abortIfSchemaBusy answers 503 when the engine shed this evaluation because
// another one held the same schema (ARCH-059/060). It returns true when it has
// written a response and the caller must return immediately.
//
// 503 and not 500: nothing ran, nothing is broken, and the request is
// retriable. The body follows the question_not_ready shape used at :191.
func (ctrl *PracticeController) abortIfSchemaBusy(c *gin.Context, err error) bool {
	if !errors.Is(err, repository.ErrSchemaBusy) {
		return false
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":   "practice_busy",
		"message": "Another submission for this category is being graded. Please try again.",
	})
	return true
}
```

`errors`, `net/http`, `models`, `repository` and `services` are already imported by this file.

- [ ] **Step 4: Shed before persisting, in `Submit`**

In `Submit`, replace:

```go
	q := question.AsQuestion()
	result, err := ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)

	// Persist attempt regardless of validation outcome
```

with:

```go
	q := question.AsQuestion()
	result, err := ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)

	// ARCH-061: this check MUST stay above the persistence below. Practice
	// persists every attempt regardless of validation outcome, so a shed
	// submission would otherwise be recorded as is_correct=false for SQL that
	// never reached the database — corrupting the student's history for a
	// request the server itself refused.
	if ctrl.abortIfSchemaBusy(c, err) {
		return
	}

	// Persist attempt regardless of validation outcome
```

- [ ] **Step 5: Shed in `Preview`**

In `Preview`, replace:

```go
	vResult, rows, columns, rowCount, truncated, err := ctrl.validator.ValidatePreview(ctx, int(student.ID), *q, req.SQLText, schema, previewSchema)
	if err != nil {
```

with:

```go
	vResult, rows, columns, rowCount, truncated, err := ctrl.validator.ValidatePreview(ctx, int(student.ID), *q, req.SQLText, schema, previewSchema)
	if ctrl.abortIfSchemaBusy(c, err) {
		return
	}
	if err != nil {
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
cd backend && go build ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate -p 1 -v
```

Expected: both `TestPracticeGate_*` PASS. `cmd/main.go:153` must still compile untouched — if it errors, the interface signatures do not match `*services.ValidationService` and must be rechecked against `internal/services/validation_service.go`.

- [ ] **Step 7: Confirm nothing else in the package regressed**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -p 1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/controllers/practice_controller.go backend/internal/controllers/practice_gate_test.go
git commit -m "feat(practice): answer 503 without recording a shed submission (ARCH-061)"
```

---

### Task 4: Exam controller maps `ErrSchemaBusy` to 503

Exam DML can now meet the gate in exactly one situation: a DML *preview* on a dual-schema category, where exam (`quiz_controller.go:352`) and practice (`practice_controller.go:258`) both resolve `question.CategoryPreviewSchema`. Without this arm that surfaces as a 500.

**Files:**
- Modify: `backend/internal/controllers/quiz_controller.go` (the error ladder at `:265-276`)

**Interfaces:**
- Consumes: `repository.ErrSchemaBusy` from Task 2.
- Produces: nothing.

- [ ] **Step 1: Add the arm**

In `SubmitAnswer`, replace:

```go
	if err != nil {
		if errors.Is(err, services.ErrQueueFull) || errors.Is(err, services.ErrQueueShutdown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
```

with:

```go
	if err != nil {
		if errors.Is(err, services.ErrQueueFull) || errors.Is(err, services.ErrQueueShutdown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		// ARCH-059: the engine shed this evaluation because another one held the
		// same execution schema. Must sit ABOVE the DeadlineExceeded arm and
		// answer 503, not 504 — nothing ran, so nothing timed out, and the
		// request is retriable.
		if errors.Is(err, repository.ErrSchemaBusy) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "schema_busy",
				"message": "Another submission is being graded against this data. Please try again."})
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
```

Check the file's import block already contains `"github.com/paciscoding/sql-quiz-system/internal/repository"` — it does, `quiz_controller.go:357` calls `repository.ExamCategoryPreviewSandboxName`. If `go build` disagrees, add it.

- [ ] **Step 2: Apply the same arm to `PreviewAnswer`**

`PreviewAnswer` is the handler that can actually hit this. Locate its error ladder — the block following its `ValidatePreview` call, which has the same `ErrQueueFull` / `DeadlineExceeded` shape — and insert the identical `ErrSchemaBusy` arm above its `DeadlineExceeded` check, with the same body.

If `PreviewAnswer` has no such ladder and returns 500 directly, add the `ErrSchemaBusy` check immediately before that 500, using the same `c.JSON(http.StatusServiceUnavailable, ...)` body.

- [ ] **Step 3: Verify**

```bash
cd backend && go build ./... && go vet ./internal/controllers/ && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -p 1
```

Expected: all silent / PASS.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/controllers/quiz_controller.go
git commit -m "fix(exam): map a busy schema to 503 rather than 500 (ARCH-061)"
```

---

### Task 5: Update the surrounding documentation and verify the whole suite

**Files:**
- Modify: `backend/internal/repository/postgres_student_pool.go` (comment only)
- Modify: `backend/internal/repository/postgres_practice_concurrent_dml_test.go` (header comment only)
- Modify: `feedback.md`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Update the pool-sizing comment**

In `backend/internal/repository/postgres_student_pool.go`, the `studentPoolMaxConns` comment justifies the small DML pool by citing the `SubmissionQueue` alone — an assumption practice violated. Replace:

```go
// studentPoolMaxConns sizes each class's pool. exam_student_readonly carries
// graded SELECT (the hot path) so it is the largest; the DML pool is small
// because graded DML is serialised by the single-worker SubmissionQueue; the
// two decoy-preview pools are idle in most deployments. Worst case is
// 20 (privileged r.pool) + 20 (these four) = 40 backends, well within
// postgres:15-alpine's default max_connections of 100.
```

with:

```go
// studentPoolMaxConns sizes each class's pool. exam_student_readonly carries
// graded SELECT (the hot path) so it is the largest; the DML pool is small
// because DML is serialised per execution schema by the SchemaGate in
// Evaluate/EvaluatePreview (ARCH-059) before it ever reaches this pool, with
// exam DML additionally arriving one job at a time via the SubmissionQueue.
// Until 2026-08-09 only the queue existed, which left practice DML
// unserialised and able to deadlock — see
// postgres_practice_concurrent_dml_test.go. The two decoy-preview pools are
// idle in most deployments. Worst case is 20 (privileged r.pool) + 20 (these
// four) = 40 backends, well within postgres:15-alpine's default
// max_connections of 100.
```

- [ ] **Step 2: Update the probe's header comment**

In `backend/internal/repository/postgres_practice_concurrent_dml_test.go`, replace:

```go
// The audit claimed that lets two practice submissions deadlock. That claim
// was never reproduced. This file is the evidence gate: it runs against
// UNMODIFIED code and decides whether the fix is justified.
```

with:

```go
// The audit claimed that lets two practice submissions deadlock. This file
// reproduced it (SQLSTATE 40P01, 2026-08-09) and justified ARCH-058..061.
//
// It is now the regression test for that fix. The gate lives inside
// repo.Evaluate, so ProductionPath — which calls Evaluate — must pass, while
// Mechanism issues raw SQL in evaluateDML's transaction shape and bypasses the
// gate on purpose, so it still reports 40P01. That contrast is the point: it
// shows the deadlock is still constructible and that the gate is what stops
// students meeting it.
```

The opening paragraph above it still describes practice as unserialised; leave it, and append one line after it:

```go
// (Fixed 2026-08-09 by ARCH-059: the gate moved into the engine, so the
// scheduling difference between exam and practice no longer decides whether
// concurrent DML is safe.)
```

- [ ] **Step 3: Close OPEN-1 in `feedback.md`**

In `feedback.md`, change the OPEN-1 severity line from:

```
Severity: **high** (data-integrity + availability) — **deadlock confirmed by
test, 2026-08-09**
```

to:

```
Severity: **high** (data-integrity + availability) — **deadlock confirmed by
test 2026-08-09; CLOSED the same day by ARCH-058…061**
```

and replace the `Design:` line near the end of the section:

```
Design: `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`
(ARCH-058 … ARCH-061) — in-process per-schema gate, practice paths only.
```

with:

```
**Fixed 2026-08-09** — `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`
(ARCH-058 … ARCH-061). An in-process per-schema gate inside
`Evaluate`/`EvaluatePreview`, keyed on the schema those functions resolve as the
execution target, so the guarantee belongs to the engine rather than to whichever
controller remembered to ask for it. `TestPracticeConcurrentDML_CaseB_ProductionPath`
now asserts both concurrent submissions grade `success`.

No backfill. Any practice attempt already recorded as incorrect because its seed
deadlocked is indistinguishable in `practice_attempts` from a genuinely wrong
answer, so past practice scores cannot be repaired retroactively.
```

- [ ] **Step 4: Run the full backend suite**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./... -p 1
```

Expected: every package PASS, no `FAIL` lines. This is the first point in the plan where the whole suite must be green — `_CaseB_ProductionPath` included.

- [ ] **Step 5: Run the race detector over the new concurrency code**

```bash
cd backend && go test ./internal/schemagate/ -run TestSchemaGate -race -count=3
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/repository/ -run 'TestSchemaGateEngine|TestPracticeConcurrentDML' -race -p 1
```

Expected: PASS, no `DATA RACE` reports.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/repository/postgres_student_pool.go backend/internal/repository/postgres_practice_concurrent_dml_test.go feedback.md
git commit -m "docs: record the engine-level schema gate and close OPEN-1"
```

---

## Verification checklist

Run before considering the plan complete:

- [ ] `cd backend && go build ./...` — silent
- [ ] `cd backend && go vet ./...` — silent
- [ ] `cd backend && TEST_DATABASE_URL=... go test ./... -p 1` — no FAIL
- [ ] `cd backend && go test ./internal/schemagate/ -race -count=3` — no DATA RACE
- [ ] `git diff main --stat -- backend/internal/services/submission_queue.go backend/cmd/main.go` — **empty**; the queue and its wiring are untouched
- [ ] `git diff main -- backend/ | grep -iE '^\+.*(CREATE TABLE|ALTER TABLE|migrat)'` — **empty**; no database changes
- [ ] `grep -rn "15 \* time.Second" backend/internal/repository/` — appears only in the `schemaGateWaitBudget` declaration
- [ ] `grep -rn "acquireDMLGate" backend/internal/repository/*.go | grep -v _test.go` — exactly three hits: the definition, `Evaluate`, `EvaluatePreview`

## Not in scope

Recorded so a reviewer does not mistake them for oversights:

- **No practice sandbox clone.** The gate closes concurrency hazards only. Practice DML still runs against `practice_cat_N`, the lecturer's real imported data, with the savepoint as sole barrier. Separate decision, separate spec.
- **Multi-instance safety.** The gate is process-local, matching what `SubmissionQueue` already assumes. Two backend instances would each keep their own.
- **Pool resizing.** `exam_student` stays at 4 connections; only its comment changes.
- **The exam job's 30 s budget.** Gate wait comes out of it, because `context.WithTimeout` keeps the earlier deadline and the engine cannot manufacture budget the caller did not give it. Only reachable in the shared-decoy preview case.
- **Case A latency measurement.** The spec asks for a baseline of strict serialisation cost under a full class. Worth doing; not a prerequisite for shipping the correctness fix.
- **`feedback.md` OPEN-2 … OPEN-7.** Including OPEN-7 (tests re-running `CreateRestrictedUser` invalidate pools built once behind a `sync.Once`), which was found while building the probe and is a suite-wide fragility deserving its own fix.
