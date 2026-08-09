# Practice DML Schema Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serialise practice DML evaluation per category schema so two concurrent practice submissions can no longer deadlock each other, without touching the exam path or adding any database state.

**Architecture:** A process-local `SchemaGate` (a `map[string]chan struct{}`, one capacity-1 channel per schema) is held across the validator call on the two practice DML paths — submit and preview — keyed on the `practice_cat_N` schema so the two exclude each other. A capacity-1 channel rather than `sync.Mutex` because the wait must be cancellable and time-bounded. The exam path keeps its existing `SubmissionQueue` and gets zero new lines.

**Tech Stack:** Go 1.x, gin, pgx/v5, PostgreSQL. Tests use the existing `openTestRepo` / `openCtrlTestRepo` harnesses, which skip cleanly when `TEST_DATABASE_URL` is unset.

**Spec:** `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md` (ARCH-058 … ARCH-061)

**Evidence this is needed:** `backend/internal/repository/postgres_practice_concurrent_dml_test.go` already reproduces the defect against unmodified code — `SQLSTATE 40P01`, with a serial control proving concurrency is the cause. `TestPracticeConcurrentDML_CaseB_ProductionPath` currently **fails on purpose**. Task 5 turns it green.

## Global Constraints

- **Run backend tests with `-p 1`.** They are DB-global; plain `go test ./...` fabricates failures.
- **Never change the exam path.** `internal/controllers/quiz_controller.go`, `internal/services/submission_queue.go` and `cmd/main.go`'s queue wiring are out of scope for every task.
- **No Postgres changes.** No table, column, migration, DDL, or persisted state anywhere in this plan.
- **`practiceGateWaitBudget = 15 * time.Second`** — named constant, never an inline literal.
- **503 body shape is exactly** `{"error": "practice_busy", "message": "Another submission for this category is being graded. Please try again."}`, matching the `question_not_ready` shape at `practice_controller.go:191`.
- **Never pass the wait context into the validator.** The gate wait budget and the evaluation context are separate `context.Context` values. Passing the wait context into `Validate` would let queue-wait consume the execution window — the exact defect the audit found on the exam side.
- **Practice SELECT is never gated.**

### Correction to the spec, applied in this plan

The spec's *Testing → Layer 2* implies the controller tests are DB-free once ARCH-061's seam exists. **They are not.** `Submit` and `Preview` call `ctrl.repo.GetPracticeQuestion`, `resolveCategorySchema` and `CreatePracticeAttempt` before and after the validator, so the handler still needs a real repository. What the seam actually buys is **determinism and speed**: a fake validator with a controllable delay makes overlap assertions exact instead of timing-dependent, and skips real SQL evaluation. Layer 2 tests therefore use `openCtrlTestRepo` (skips without `TEST_DATABASE_URL`) plus a fake validator. Only Task 1's `SchemaGate` tests are genuinely DB-free.

---

## File Structure

| File | Responsibility |
|---|---|
| `backend/internal/services/schema_gate.go` | **Create.** `SchemaGate` — the per-schema serialisation primitive. No knowledge of practice, HTTP, or the database. |
| `backend/internal/services/schema_gate_test.go` | **Create.** DB-free unit tests for the primitive. |
| `backend/internal/controllers/practice_controller.go` | **Modify.** Validator seam, gate field, gate helper, two gated call sites. |
| `backend/internal/controllers/practice_gate_test.go` | **Create.** DB-backed HTTP tests for the gating branch, with a fake validator. |
| `backend/internal/repository/postgres_practice_concurrent_dml_test.go` | **Modify.** Flip the deliberate failure into the regression assertion. |
| `backend/internal/repository/postgres_student_pool.go:70-72` | **Modify.** Comment only — cite the gate for practice alongside the queue for exam. |

---

### Task 1: `SchemaGate` primitive

**Files:**
- Create: `backend/internal/services/schema_gate.go`
- Test: `backend/internal/services/schema_gate_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `services.NewSchemaGate() *services.SchemaGate` and `(*SchemaGate).Acquire(ctx context.Context, schema string) (release func(), err error)`. Task 3 and Task 4 depend on exactly these names and this signature. `Acquire` never returns a nil `release` together with a nil `err`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/services/schema_gate_test.go`:

```go
package services

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

// TestSchemaGate_DistinctKeysDoNotBlock proves the gate is per-schema and not
// global. If these serialised, practice would be serialised system-wide, which
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
// not consume the slot. If it did, one slow evaluation would wedge a category
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

// TestSchemaGate_CanceledIsDistinguishable: a disconnected client must be
// distinguishable from a busy category, because the handler answers 503 for
// one and aborts silently for the other.
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

// TestSchemaGate_DoubleReleaseIsSafe: the handler releases via defer inside a
// closure; a second call must never free another caller's slot.
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

// TestSchemaGate_PanicLeavesGateUsable: the handler holds the gate across the
// validator with a deferred release, so a panicking validator must not wedge
// the category.
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
		panic("validator exploded")
	}()

	rel, err := g.Acquire(ctx, "practice_cat_1")
	if err != nil {
		t.Fatalf("gate wedged after a panic: %v", err)
	}
	rel()
}

// TestSchemaGate_EmptySchemaIsNoop mirrors acquireSchemaEvalLock's treatment of
// the empty schema (postgres_rebuild_lock.go:27).
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
cd backend && go test ./internal/services/ -run TestSchemaGate -v
```

Expected: compile failure — `undefined: NewSchemaGate`.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/services/schema_gate.go`:

```go
package services

import (
	"context"
	"sync"
)

// SchemaGate serialises work per schema name, inside this process.
//
// Practice DML has no other serialisation. The single-worker SubmissionQueue is
// injected into QuizController only (cmd/main.go), and acquireSchemaEvalLock
// takes the per-schema advisory lock SHARED so concurrent evaluations do not
// exclude each other. Two practice submissions whose seeds touch the same
// tables in opposite order therefore deadlock — reproduced in
// repository/postgres_practice_concurrent_dml_test.go (SQLSTATE 40P01).
//
// Each schema key owns a buffered channel of capacity 1 used as a mutex.
// A channel rather than sync.Mutex because the wait MUST be cancellable and
// time-bounded: sync.Mutex.Lock is neither, so a wedged evaluation would hang
// every later submitter for that category with no way to shed them.
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
	return &SchemaGate{gates: make(map[string]chan struct{})}
}

// gateFor returns the channel for schema, creating it on first use. Entries are
// never evicted: the population is bounded by the number of category schemas,
// so a deleted category leaks one channel and one map entry.
func (g *SchemaGate) gateFor(schema string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
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
// callers release with `defer` inside a closure, and a second call must never
// free a slot the caller no longer owns.
//
// On failure it returns a nil release and ctx's error, so callers can tell
// context.DeadlineExceeded (the category is busy — answer 503) from
// context.Canceled (the client went away — answer nothing). It never returns a
// nil release together with a nil error.
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
cd backend && go test ./internal/services/ -run TestSchemaGate -race -v
```

Expected: all seven `TestSchemaGate_*` PASS, no race warnings.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/schema_gate.go backend/internal/services/schema_gate_test.go
git commit -m "feat(practice): add SchemaGate per-schema serialisation primitive (ARCH-058)"
```

---

### Task 2: Validator seam and gate field

Compile-only change. No behaviour changes in this task — that is deliberate, so a reviewer can confirm the refactor is inert before any gating lands on top of it.

**Files:**
- Modify: `backend/internal/controllers/practice_controller.go:85-97`

**Interfaces:**
- Consumes: `services.NewSchemaGate` from Task 1.
- Produces: the unexported `practiceValidator` interface and the `PracticeController.dmlGate` field, both used by Tasks 3 and 4. `NewPracticeController`'s parameter list is unchanged in arity and order, so `cmd/main.go:153` needs no edit.

- [ ] **Step 1: Replace the struct and constructor**

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
// practice handlers use (ARCH-061). It exists so the gating branch is testable
// with a fake whose timing is controllable: whether SELECT bypasses the gate,
// whether concurrent DML calls overlap, and whether a shed request writes an
// attempt row. *services.ValidationService satisfies it, so cmd/main.go:153 is
// unchanged. QuizController is deliberately NOT converted — the seam is
// practice-only, like everything else in this design.
type practiceValidator interface {
	Validate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error)
	ValidatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error)
}

// practiceGateWaitBudget bounds how long a practice DML submission waits for
// its category's gate before being shed with 503.
//
// It is deliberately shorter than evaluateDML's own 30 s ceiling
// (postgres_evaluation.go:191): a genuinely wedged evaluation sheds waiters in
// half that time instead of hanging them, while a full class at a realistic
// ~300 ms per submission drains well inside it.
//
// A var rather than a const solely so tests can shorten it — asserting the
// shed-with-503 path is otherwise a 15-second test. Production never assigns
// to it.
var practiceGateWaitBudget = 15 * time.Second

type PracticeController struct {
	repo      *repository.PostgresRepository
	validator practiceValidator
	auth      *services.AuthService
	// dmlGate serialises practice DML per category schema. Practice has no
	// SubmissionQueue; without this, two submissions whose seeds touch the same
	// tables in opposite order deadlock (SQLSTATE 40P01) and one student is told
	// their question is broken. See ARCH-059.
	dmlGate *services.SchemaGate
}

func NewPracticeController(repo *repository.PostgresRepository, validator practiceValidator, auth *services.AuthService) *PracticeController {
	return &PracticeController{
		repo:      repo,
		validator: validator,
		auth:      auth,
		dmlGate:   services.NewSchemaGate(),
	}
}
```

- [ ] **Step 2: Add the `time` import**

In the same file's import block (lines 3-18), add `"time"` to the standard-library group, so it reads:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)
```

- [ ] **Step 3: Verify it compiles and nothing regressed**

```bash
cd backend && go build ./... && go vet ./internal/controllers/
```

Expected: both silent. In particular `cmd/main.go:153` must still compile untouched — if it errors, the interface does not match `*services.ValidationService` and the signatures above need rechecking against `internal/services/validation_service.go`.

- [ ] **Step 4: Confirm the refactor is inert**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -p 1
```

Expected: same result as before this task (all PASS, or SKIP without `TEST_DATABASE_URL`).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/controllers/practice_controller.go
git commit -m "refactor(practice): narrow validator seam and gate field (ARCH-061)"
```

---

### Task 3: Gate the submit path

**Files:**
- Modify: `backend/internal/controllers/practice_controller.go` (add the gate helper; gate the `Validate` call at ~line 205)
- Test: `backend/internal/controllers/practice_gate_test.go` (create)

**Interfaces:**
- Consumes: `practiceValidator`, `practiceGateWaitBudget`, `PracticeController.dmlGate` from Task 2.
- Produces: `(*PracticeController).acquirePracticeDMLGate(c *gin.Context, ctx context.Context, schema string) (release func(), ok bool)` — Task 4 reuses it verbatim. When `ok` is false the response has already been written and the caller must `return` immediately.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/controllers/practice_gate_test.go`:

```go
package controllers

// ARCH-059/060 coverage: practice DML serialises per category schema, practice
// SELECT does not, and a shed submission records nothing.
//
// These use a REAL repository (the handlers read the question, resolve the
// schema and persist the attempt) with a FAKE validator, so overlap assertions
// are exact instead of timing-dependent and no SQL is actually evaluated.
// Skips cleanly without TEST_DATABASE_URL via openCtrlTestRepo.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/controllers/ -run TestPracticeGate -p 1 -v

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// fakeGateValidator records how many calls are inside Validate at once and
// holds each call for `delay`, so concurrency is observable without running SQL.
type fakeGateValidator struct {
	delay    time.Duration
	inFlight int32
	maxSeen  int32
	calls    int32
	// gotWaitCtx is set when a caller passes a context carrying a deadline at
	// or below practiceGateWaitBudget, which would mean the gate wait budget
	// leaked into the evaluation context (ARCH-060 violation).
	sawDeadline int32
}

func (f *fakeGateValidator) enter(ctx context.Context) {
	atomic.AddInt32(&f.calls, 1)
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= practiceGateWaitBudget {
		atomic.StoreInt32(&f.sawDeadline, 1)
	}
	cur := atomic.AddInt32(&f.inFlight, 1)
	for {
		old := atomic.LoadInt32(&f.maxSeen)
		if cur <= old || atomic.CompareAndSwapInt32(&f.maxSeen, old, cur) {
			break
		}
	}
	time.Sleep(f.delay)
	atomic.AddInt32(&f.inFlight, -1)
}

func (f *fakeGateValidator) Validate(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error) {
	f.enter(ctx)
	return models.ValidationResult{Status: "success", IsCorrect: true}, nil
}

func (f *fakeGateValidator) ValidatePreview(ctx context.Context, studentID int, q models.Question, sql, schema, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error) {
	f.enter(ctx)
	return models.ValidationResult{Status: "success"}, nil, nil, 0, false, nil
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
// practice question of the requested type. The DML question uses a seed_sql
// row so its capture succeeds and ExpectedResultStatus reaches "captured",
// which the readiness gate at practice_controller.go:191 requires.
func mkGateFixture(t *testing.T, repo *repository.PostgresRepository, ctx context.Context, tbl, qType string) (catID int, questionID models.PracticeQuestionID) {
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
		Type:        qType,
		Requirement: "gate fixture",
		Points:      1,
	}
	if qType == "DML" {
		q.DMLKind = "DELETE"
		q.CorrectSQL = fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl)
		q.SeedSQL = fmt.Sprintf("INSERT INTO %s (ssn, name) VALUES ('999999999', 'Seeded')", tbl)
	} else {
		q.CorrectSQL = fmt.Sprintf("SELECT ssn, name FROM %s ORDER BY ssn", tbl)
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

// TestPracticeGate_ConcurrentDMLSubmitsDoNotOverlap is the core ARCH-059
// assertion: two students submitting DML to one category are serialised.
func TestPracticeGate_ConcurrentDMLSubmitsDoNotOverlap(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_dml_overlap", "DML")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	fake := &fakeGateValidator{delay: 150 * time.Millisecond}
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, fake, auth))

	s1 := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	s2 := mkCtrlUser(t, repo, models.RoleStudent, "pw2")
	tok1 := login(t, r, s1.Username, "pw1")
	tok2 := login(t, r, s2.Username, "pw2")

	body := submitBody(t, qid, "DELETE FROM gate_dml_overlap WHERE ssn = '999999999'")

	var wg sync.WaitGroup
	for _, tok := range []string{tok1, tok2} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			w := doJSON(r, http.MethodPost, "/api/v1/practice/submit", token, body)
			if w.Code != http.StatusOK {
				t.Errorf("submit: want 200, got %d (%s)", w.Code, w.Body.String())
			}
		}(tok)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&fake.calls); got != 2 {
		t.Fatalf("validator called %d times, want 2", got)
	}
	if got := atomic.LoadInt32(&fake.maxSeen); got != 1 {
		t.Fatalf("ARCH-059: %d concurrent practice DML evaluations on one schema, want 1", got)
	}
}

// TestPracticeGate_SelectSubmitsAreNotGated: gating SELECT would serialise the
// common case for no benefit. They must overlap.
func TestPracticeGate_SelectSubmitsAreNotGated(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_sel_overlap", "QUERY")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	fake := &fakeGateValidator{delay: 150 * time.Millisecond}
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, fake, auth))

	s1 := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	s2 := mkCtrlUser(t, repo, models.RoleStudent, "pw2")
	tok1 := login(t, r, s1.Username, "pw1")
	tok2 := login(t, r, s2.Username, "pw2")

	body := submitBody(t, qid, "SELECT ssn, name FROM gate_sel_overlap ORDER BY ssn")

	var wg sync.WaitGroup
	for _, tok := range []string{tok1, tok2} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			doJSON(r, http.MethodPost, "/api/v1/practice/submit", token, body)
		}(tok)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&fake.maxSeen); got != 2 {
		t.Fatalf("practice SELECT was serialised (max concurrent = %d, want 2)", got)
	}
}

// TestPracticeGate_BusyCategoryShedsWithoutRecordingAttempt is the assertion
// most likely to regress silently. A submission shed while waiting for the gate
// never reached the evaluator, so it must NOT be persisted — practice otherwise
// persists every attempt regardless of outcome (practice_controller.go:205-224),
// and writing is_correct=false for a request the server refused would corrupt
// the student's history and their score.
func TestPracticeGate_BusyCategoryShedsWithoutRecordingAttempt(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_shed", "DML")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	// Shorten the wait budget so the second submitter is shed promptly instead
	// of queueing for the production 15 s.
	origBudget := practiceGateWaitBudget
	practiceGateWaitBudget = 100 * time.Millisecond
	defer func() { practiceGateWaitBudget = origBudget }()

	// The holder stays inside the validator far longer than the shortened budget.
	fake := &fakeGateValidator{delay: 2 * time.Second}
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, fake, auth))

	holder := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	shedStudent := mkCtrlUser(t, repo, models.RoleStudent, "pw2")
	holderTok := login(t, r, holder.Username, "pw1")
	shedTok := login(t, r, shedStudent.Username, "pw2")

	body := submitBody(t, qid, "DELETE FROM gate_shed WHERE ssn = '999999999'")

	before, err := repo.ListPracticeAttempts(ctx, int(shedStudent.ID))
	if err != nil {
		t.Fatalf("ListPracticeAttempts (before): %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		doJSON(r, http.MethodPost, "/api/v1/practice/submit", holderTok, body)
	}()

	// Give the holder time to be inside the validator holding the gate.
	time.Sleep(200 * time.Millisecond)

	w := doJSON(r, http.MethodPost, "/api/v1/practice/submit", shedTok, body)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("submit into a busy category: want 503, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "practice_busy") {
		t.Fatalf("503 body must carry error=practice_busy, got %s", w.Body.String())
	}

	wg.Wait()

	after, err := repo.ListPracticeAttempts(ctx, int(shedStudent.ID))
	if err != nil {
		t.Fatalf("ListPracticeAttempts (after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("a shed submission was recorded: attempts went %d -> %d; "+
			"evaluation never ran, so there is nothing to persist",
			len(before), len(after))
	}
}

// TestPracticeGate_EvaluationContextHasNoWaitDeadline pins ARCH-060: the wait
// budget must never become the evaluation budget.
func TestPracticeGate_EvaluationContextHasNoWaitDeadline(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_ctx_split", "DML")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	fake := &fakeGateValidator{}
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, fake, auth))

	s := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	tok := login(t, r, s.Username, "pw1")

	body := submitBody(t, qid, "DELETE FROM gate_ctx_split WHERE ssn = '999999999'")
	if w := doJSON(r, http.MethodPost, "/api/v1/practice/submit", tok, body); w.Code != http.StatusOK {
		t.Fatalf("submit: want 200, got %d (%s)", w.Code, w.Body.String())
	}

	if atomic.LoadInt32(&fake.sawDeadline) != 0 {
		t.Fatal("ARCH-060: the gate wait context leaked into the validator; " +
			"queue wait would consume the evaluation window")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate -p 1 -v
```

Expected: `TestPracticeGate_ConcurrentDMLSubmitsDoNotOverlap` FAILs with `2 concurrent practice DML evaluations on one schema, want 1`, and `TestPracticeGate_BusyCategoryShedsWithoutRecordingAttempt` FAILs with `want 503, got 200`. `TestPracticeGate_SelectSubmitsAreNotGated` and `TestPracticeGate_EvaluationContextHasNoWaitDeadline` should already pass — that is correct and expected; they are the guards against over-correcting in Step 3.

- [ ] **Step 3: Add the gate helper**

In `backend/internal/controllers/practice_controller.go`, immediately after `NewPracticeController`, add:

```go
// acquirePracticeDMLGate serialises practice DML per category schema.
//
// On success it returns a release func and true. On failure it has ALREADY
// written the response and the caller must return immediately.
//
// The wait budget is a context of its own, separate from ctx (ARCH-060): ctx —
// not waitCtx — is what goes to the validator, so time spent queueing can never
// be deducted from the evaluation window.
func (ctrl *PracticeController) acquirePracticeDMLGate(c *gin.Context, ctx context.Context, schema string) (func(), bool) {
	waitCtx, cancel := context.WithTimeout(ctx, practiceGateWaitBudget)
	defer cancel()

	release, err := ctrl.dmlGate.Acquire(waitCtx, schema)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The client hung up while queued. Nothing ran and nobody is
			// listening; writing a body here would be noise.
			c.Abort()
			return nil, false
		}
		// Deliberately no attempt row: evaluation never ran, so there is nothing
		// to record. Persisting is_correct=false here would corrupt the
		// student's history for a request the server shed.
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "practice_busy",
			"message": "Another submission for this category is being graded. Please try again.",
		})
		return nil, false
	}
	return release, true
}
```

- [ ] **Step 4: Gate the submit call site**

In `Submit`, replace:

```go
	q := question.AsQuestion()
	result, err := ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)
```

with:

```go
	q := question.AsQuestion()

	// ARCH-059: practice DML is serialised per category schema. Practice has no
	// SubmissionQueue, and the per-schema advisory lock is taken SHARED, so
	// without this two submissions whose seeds touch the same tables in opposite
	// order deadlock and one student is told their question is broken. SELECT is
	// never gated. The gate is released before the attempt is persisted — there
	// is no reason to hold a category-wide gate across an unrelated INSERT.
	var result models.ValidationResult
	if q.Type == "DML" {
		release, ok := ctrl.acquirePracticeDMLGate(c, ctx, schema)
		if !ok {
			return
		}
		// Released via defer inside this closure so a panicking validator cannot
		// wedge the category, while still keeping the critical section tight.
		func() {
			defer release()
			result, err = ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)
		}()
	} else {
		result, err = ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd backend && go build ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate -p 1 -v
```

Expected: all four `TestPracticeGate_*` PASS. In particular `maxSeen == 1` for DML and `maxSeen == 2` for SELECT — if SELECT also reports 1, the `q.Type == "DML"` branch is wrong and practice SELECT has been serialised.

Note the shed test mutates the package-level `practiceGateWaitBudget` and restores it with `defer`. No test in this package calls `t.Parallel()`, so this is safe; if one ever does, that test must not touch the gate.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/controllers/practice_controller.go backend/internal/controllers/practice_gate_test.go
git commit -m "feat(practice): serialise DML submissions per category schema (ARCH-059/060)"
```

---

### Task 4: Gate the preview path

Without this, preview and submit still race: `evaluateDMLPreview` runs DML against the same `practice_cat_N` and takes its advisory lock on the same schema (`postgres_evaluation.go:498`).

**Files:**
- Modify: `backend/internal/controllers/practice_controller.go` (`Preview`, ~line 266)
- Test: `backend/internal/controllers/practice_gate_test.go` (append)

**Interfaces:**
- Consumes: `acquirePracticeDMLGate` from Task 3, unchanged.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/controllers/practice_gate_test.go`:

```go
// TestPracticeGate_SubmitAndPreviewExcludeEachOther: preview runs DML against
// the same practice_cat_N as submit, so keying both on `schema` is what closes
// the preview-vs-submit race. If preview used previewSchema as its key instead,
// these two would overlap.
func TestPracticeGate_SubmitAndPreviewExcludeEachOther(t *testing.T) {
	repo := openCtrlTestRepo(t)
	ctx := context.Background()

	catID, qid := mkGateFixture(t, repo, ctx, "gate_mixed_overlap", "DML")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	fake := &fakeGateValidator{delay: 150 * time.Millisecond}
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildPracticeGateRouter(auth, repo, NewPracticeController(repo, fake, auth))

	s1 := mkCtrlUser(t, repo, models.RoleStudent, "pw1")
	s2 := mkCtrlUser(t, repo, models.RoleStudent, "pw2")
	tok1 := login(t, r, s1.Username, "pw1")
	tok2 := login(t, r, s2.Username, "pw2")

	body := submitBody(t, qid, "DELETE FROM gate_mixed_overlap WHERE ssn = '999999999'")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		doJSON(r, http.MethodPost, "/api/v1/practice/submit", tok1, body)
	}()
	go func() {
		defer wg.Done()
		doJSON(r, http.MethodPost, "/api/v1/practice/preview", tok2, body)
	}()
	wg.Wait()

	if got := atomic.LoadInt32(&fake.calls); got != 2 {
		t.Fatalf("validator called %d times, want 2", got)
	}
	if got := atomic.LoadInt32(&fake.maxSeen); got != 1 {
		t.Fatalf("submit and preview overlapped on one schema (max concurrent = %d, want 1)", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate_SubmitAndPreviewExcludeEachOther -p 1 -v
```

Expected: FAIL — `submit and preview overlapped on one schema (max concurrent = 2, want 1)`.

- [ ] **Step 3: Gate the preview call site**

In `Preview`, replace:

```go
	q := question.AsQuestion()
	vResult, rows, columns, rowCount, truncated, err := ctrl.validator.ValidatePreview(ctx, int(student.ID), *q, req.SQLText, schema, previewSchema)
	if err != nil {
```

with:

```go
	q := question.AsQuestion()

	// ARCH-059: keyed on `schema`, NOT previewSchema. evaluateDMLPreview runs the
	// student's DML against `schema` and takes its advisory lock there
	// (postgres_evaluation.go:498), so using the same key as Submit is what makes
	// preview and submit mutually exclusive.
	var (
		vResult   models.ValidationResult
		rows      []map[string]interface{}
		columns   []string
		rowCount  int
		truncated bool
	)
	runPreview := func() {
		vResult, rows, columns, rowCount, truncated, err = ctrl.validator.ValidatePreview(ctx, int(student.ID), *q, req.SQLText, schema, previewSchema)
	}
	if q.Type == "DML" {
		release, ok := ctrl.acquirePracticeDMLGate(c, ctx, schema)
		if !ok {
			return
		}
		func() {
			defer release()
			runPreview()
		}()
	} else {
		runPreview()
	}
	if err != nil {
```

Note `err` is assigned with `=`, not `:=` — it is already in scope from `question, err := ctrl.repo.GetPracticeQuestion(...)` earlier in `Preview`. Redeclaring it will not compile.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd backend && go build ./... && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate -p 1 -v
```

Expected: all five `TestPracticeGate_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/controllers/practice_controller.go backend/internal/controllers/practice_gate_test.go
git commit -m "feat(practice): gate DML preview on the grading schema (ARCH-059)"
```

---

### Task 5: Turn the deadlock probe into a regression guard

`TestPracticeConcurrentDML_CaseB_ProductionPath` currently fails on purpose — its `t.Errorf` **is** the finding. Now that the gate exists it must assert the opposite, and it must keep failing loudly if the gate is ever removed.

**Files:**
- Modify: `backend/internal/repository/postgres_practice_concurrent_dml_test.go`
- Modify: `backend/internal/repository/postgres_student_pool.go:70-72` (comment only)

**Interfaces:**
- Consumes: the gated handlers from Tasks 3 and 4.
- Produces: nothing.

- [ ] **Step 1: Understand what this probe can and cannot assert**

`repo.Evaluate` is the repository entry point — it sits **below** the controller, so the gate does not apply to it. `_CaseB_Mechanism` and `_CaseB_ProductionPath` therefore still deadlock, and that is correct: they document that the engine itself is unprotected and that the gate above it is load-bearing.

Do not delete them and do not "fix" them by adding a gate to the repository layer. Restate what they prove instead.

- [ ] **Step 2: Restate the production-path finding as a documented invariant**

In `backend/internal/repository/postgres_practice_concurrent_dml_test.go`, replace the `t.Errorf("CONFIRMED — feedback.md OPEN-1: ...")` call with:

```go
	// The evaluator itself is unprotected by design — the gate lives one layer
	// up, in PracticeController (ARCH-059). This subtest therefore still
	// reproduces the deadlock, and that is the point: it documents exactly what
	// the controller-level gate is protecting, so removing the gate cannot
	// quietly become safe.
	//
	// The user-visible consequence is asserted where the gate actually applies:
	// controllers.TestPracticeGate_ConcurrentDMLSubmitsDoNotOverlap and
	// controllers.TestPracticeGate_SubmitAndPreviewExcludeEachOther.
	t.Logf("unguarded evaluator: student(s) %v were shed with %q — expected at this "+
		"layer; the gate that prevents it lives in PracticeController (ARCH-059)",
		shed, out[shed[0]].res.Feedback)
```

- [ ] **Step 3: Update the file's header comment**

In the same file, replace the two lines reading:

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
// It now serves as the standing proof that the evaluator is unprotected on its
// own. The fix is a per-schema gate in PracticeController, one layer above
// repo.Evaluate, so these subtests still deadlock by design — they are what
// makes removing that gate visibly unsafe.
```

- [ ] **Step 4: Update the pool-sizing comment**

In `backend/internal/repository/postgres_student_pool.go`, the comment at lines 70-72 justifies the 4-connection `exam_student` pool by citing the `SubmissionQueue` alone. Extend it so the justification covers practice too:

```go
	// Sized small deliberately: graded DML is serialised before it reaches this
	// pool — exam DML by the single-worker SubmissionQueue (services/
	// submission_queue.go), practice DML by the per-schema SchemaGate in
	// PracticeController (ARCH-059). The practice half was missing until
	// 2026-08-09, which let concurrent practice submissions deadlock; see
	// postgres_practice_concurrent_dml_test.go.
```

Keep whatever wording surrounds it intact — only the justification changes.

- [ ] **Step 5: Run the full backend suite**

```bash
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./... -p 1
```

Expected: all packages PASS with no `FAIL` lines. This is the first point in the plan where the whole suite must be green — `_CaseB_ProductionPath` included.

- [ ] **Step 6: Run the race detector over the new concurrency code**

```bash
cd backend && go test ./internal/services/ -run TestSchemaGate -race -count=3
cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/controllers/ -run TestPracticeGate -race -p 1
```

Expected: PASS, no `DATA RACE` reports.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/repository/postgres_practice_concurrent_dml_test.go backend/internal/repository/postgres_student_pool.go
git commit -m "test(practice): restate the deadlock probe as the gate's justification"
```

---

## Verification checklist

Run before considering the plan complete:

- [ ] `cd backend && go build ./...` — silent
- [ ] `cd backend && go vet ./...` — silent
- [ ] `cd backend && TEST_DATABASE_URL=... go test ./... -p 1` — no FAIL
- [ ] `cd backend && go test ./internal/services/ -run TestSchemaGate -race -count=3` — no DATA RACE
- [ ] `git diff main --stat -- backend/internal/controllers/quiz_controller.go backend/internal/services/submission_queue.go` — **empty**; the exam path must be untouched
- [ ] `git diff main -- backend/ | grep -iE '^\+.*(CREATE TABLE|ALTER TABLE|migrat)'` — **empty**; no database changes
- [ ] `grep -rn "15 \* time.Second" backend/internal/controllers/` — appears only in the `practiceGateWaitBudget` declaration

## Not in scope

Recorded so a reviewer does not mistake them for oversights:

- **No practice sandbox clone.** The gate closes concurrency hazards only. Practice DML still runs against `practice_cat_N`, the lecturer's real imported data, with the savepoint as sole barrier. Separate decision, separate spec.
- **Multi-instance safety.** The gate is process-local, matching what `SubmissionQueue` already assumes. Two backend instances would each keep their own.
- **Pool resizing.** `exam_student` stays at 4 connections; only its comment changes.
- **`feedback.md` OPEN-2 … OPEN-7.** Including OPEN-7 (tests re-running `CreateRestrictedUser` invalidate pools built once behind a `sync.Once`), which was found while building the probe and is a suite-wide fragility deserving its own fix.
