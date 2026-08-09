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
// controller-level design would have shipped, by calling EvaluatePreview for
// real while the grading schema's gate is held.
//
// EvaluatePreview resolves its DML target as previewSchema when non-empty and
// sandboxSchema only otherwise (postgres_evaluation.go:389-397), so both halves
// must hold:
//
//	previewSchema == ""  → executes against sandboxSchema → MUST block
//	previewSchema != ""  → executes elsewhere             → MUST NOT block
//
// The second half deliberately passes a schema that does not exist. The preview
// will fail — that is fine and expected. What is being asserted is that it
// fails *promptly* instead of waiting on a gate it should never have taken.
func TestSchemaGateEngine_PreviewKeysOnExecutionSchema(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid, schema := mkCaseBFixture(t, repo, ctx)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	// The budget must stay comfortably ABOVE half 2's timeout below. If it were
	// shorter, a wrongly-gated preview would be shed before the timeout fired
	// and half 2 would pass while asserting nothing.
	orig := schemaGateWaitBudget
	schemaGateWaitBudget = 2 * time.Second
	defer func() { schemaGateWaitBudget = orig }()

	q := caseBQuestions(uid)[0]

	// Hold the grading schema's gate for the rest of the test.
	release, err := repo.acquireDMLGate(ctx, schema)
	if err != nil {
		t.Fatalf("acquireDMLGate: %v", err)
	}
	defer release()

	// Half 1: no decoy — the preview executes against `schema`, so it must be
	// gated, and with the budget shortened it is shed as ErrSchemaBusy.
	if _, err := repo.EvaluatePreview(ctx, 1000, q, q.CorrectSQL, schema, ""); !errors.Is(err, ErrSchemaBusy) {
		t.Fatalf("a preview executing against the held schema was not gated: err=%v", err)
	}

	// Half 2: a decoy target — the preview executes elsewhere, so it must not
	// wait on this schema's gate. Any error is acceptable; blocking is not.
	done := make(chan struct{})
	go func() {
		_, _ = repo.EvaluatePreview(ctx, 1000, q, q.CorrectSQL, schema, "gate_nonexistent_decoy")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a preview targeting a different schema blocked on the grading " +
			"schema's gate — the key is sandboxSchema, not the execution schema")
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
