# Serialising Practice DML Per Category

**Date:** 2026-08-09
**Status:** Approved, ready for implementation — evidence gate **PASSED** 2026-08-09 (see *Testing*)
**ARCH range:** ARCH-058 … ARCH-061
**Origin:** `feedback.md` OPEN-1, from the 2026-08-08 exam-vs-practice audit

## Problem

Exam and practice share one evaluation engine. Both reach
`services/validation_service.go:92` → `repository/postgres_evaluation.go:25`
`Evaluate`. Savepoint handling, role switching, `search_path` (`pg_temp` last),
seed replay and the 30 s ceiling are identical by construction.

They differ in how work is *scheduled*, and only one side is scheduled at all.

| Path | Line | Serialised? |
|---|---|---|
| Exam submit | `quiz_controller.go:256` | yes — `submissionQueue.Run` at `:261` |
| Exam preview | `quiz_controller.go:391` | yes — `submissionQueue.Run` at `:396` |
| Practice submit | `practice_controller.go:205` | **no** |
| Practice preview | `practice_controller.go:268` | **no** |

`SubmissionQueue` runs exactly one worker goroutine
(`services/submission_queue.go`: `q.wg.Add(1); go q.worker()`), executing jobs
inline in a `for range`. At most one exam DML transaction exists system-wide at
any instant.

Practice has no such bound. Gin serves practice submissions concurrently, and
`acquireSchemaEvalLock` (`postgres_rebuild_lock.go:26`) takes the per-schema
advisory lock **shared** — deliberately, so parallel evaluations do not exclude
each other. Only the exclusive rebuild lock waits for them.

### What concurrent practice DML actually does

Every practice DML evaluation runs this shape against `practice_cat_N`:

```
BEGIN
pg_advisory_xact_lock_shared(key(practice_cat_N))   -- shared: no mutual exclusion
SAVEPOINT student_eval
SET LOCAL search_path TO practice_cat_N, public, pg_temp
SET LOCAL ROLE exam_student
<seed replay inserts>          -- ARCH-041, inside this transaction
<student DML>
ROLLBACK TO SAVEPOINT student_eval ; RESET ROLE ; COMMIT
```

Seed replay happens *inside* the transaction, after `SAVEPOINT`
(`postgres_evaluation.go:217`ff). Keys it inserts stay locked for the
transaction's entire life — across the student's SQL, the rollback, and the
commit.

**Case A — blocking. Near-certain.** Two students on the same question. A
inserts the fixed seed key (ARCH-045, e.g. `'999999999'`); B's identical insert
blocks on the uncommitted unique key until A's transaction *ends*. B holds one
of the four `exam_student` pool connections while waiting
(`postgres_student_pool.go:70-72` sizes that pool to 4 explicitly *because*
"graded DML is serialised by the single-worker SubmissionQueue" — an assumption
practice violates). Correctness survives: A's rollback removes the key. Cost is
latency and pool pressure.

**Case B — deadlock. Conditional, and currently unproven.** Requires opposite
lock-acquisition order, which requires two *different* questions in one
category:

```
Q1 seed: INSERT students(999999999)  → INSERT enrollments(888888)
Q2 seed: INSERT enrollments(888888)  → INSERT students(999999999)
```

A holds the `students` key and wants `enrollments`; B holds `enrollments` and
wants `students`. Cycle → `40P01`. Seeds are generated per question, so ordering
is uncoordinated across questions — plausible, not guaranteed.

**Reproduced 2026-08-09** in `postgres_practice_concurrent_dml_test.go`:
`actor 1: seed stmt 2: ERROR: deadlock detected (SQLSTATE 40P01)`. On the
production path one of two students submitting the model answer verbatim is
told the question "could not be prepared for grading … contact your
instructor". The serial control over the identical fixture grades both
`success`, so concurrency is the cause and not the fixture.

### What this is not

The audit originally listed "give practice its own sandbox clone" as a candidate
fix. It is not one. The sandbox addresses **damage scope** — if savepoint
rollback is ever bypassed, wreckage lands on a throwaway schema instead of real
data. It does nothing about concurrency. The queue is what prevents Case A and
Case B on the exam path, not the clone.

## Goal

Give practice DML the same *scheduling* guarantee exam DML has — at most one DML
evaluation per category schema at a time — without moving the exam path, without
adding database state, and without serialising all practice globally.

## Non-goals

- **No practice sandbox clone.** Damage scope stays open; practice DML still
  runs against `practice_cat_N`, the lecturer's real imported data, with the
  savepoint as sole barrier. Separate decision, separate spec.
- **No change to `SubmissionQueue`** — not its wiring, capacity, timeout, or
  worker count.
- **No change to the exam evaluation path.** Zero lines.
- **No new Postgres objects.** No table, column, migration, DDL, or persisted
  state. The gate is Go memory only.
- **Not multi-instance safe.** Process-local, matching the existing
  `SubmissionQueue` assumption. Stated as a limitation, not solved here.
- Practice SELECT stays unserialised.
- The remaining `feedback.md` findings (OPEN-2 … OPEN-6) are out of scope.

## Design decisions

### ARCH-058 — `SchemaGate`, a process-local per-schema serialiser

New file `internal/services/schema_gate.go`:

```go
// SchemaGate serialises work per schema name, in-process.
// A buffered channel — not sync.Mutex — because waiting must be
// ctx-cancellable and time-bounded; sync.Mutex.Lock is neither.
type SchemaGate struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

func NewSchemaGate() *SchemaGate

// Acquire blocks until the schema is free or ctx ends. On success it returns a
// release func that is safe to call more than once. It never returns a nil
// release together with a nil error.
func (g *SchemaGate) Acquire(ctx context.Context, schema string) (release func(), err error)
```

Each key maps to a `chan struct{}` of capacity 1. Acquiring is a `select` on
sending into that channel versus `<-ctx.Done()`; releasing receives from it. The
`release` closure wraps its receive in `sync.Once` so a double call cannot
free another caller's slot.

An empty schema string returns a no-op release and a nil error, mirroring
`acquireSchemaEvalLock`'s treatment of the same case.

Map entries are created lazily and never evicted. The population is bounded by
the number of category schemas; a deleted category leaks one channel and one map
entry. Not worth an eviction path.

### ARCH-059 — Only practice DML takes the gate

`PracticeController` owns one `*services.SchemaGate`, constructed inside
`NewPracticeController`. The constructor signature does not change, so
`cmd/main.go` is untouched and no new code reaches the exam path.

Two call sites, gated only when `q.Type == "DML"`:

| Site | Call | Key |
|---|---|---|
| `practice_controller.go:205` | `validator.Validate` | `schema` |
| `practice_controller.go:268` | `validator.ValidatePreview` | `schema` |

Both key on `schema` — the `practice_cat_N` returned by
`resolveCategorySchema` — and **not** on `previewSchema`. `evaluateDMLPreview`
takes its advisory lock on `schema` (`postgres_evaluation.go:498`), and the DML
it runs executes there. Using one key for both sites is what makes preview and
submit mutually exclusive, closing the preview-vs-submit race as well.

The critical section covers the validator call **only**. It is released before
`CreatePracticeAttempt`: there is no reason to hold a category-wide gate across
an unrelated INSERT.

Release is deferred inside a closure, so a panic in the validator cannot strand
the gate while still keeping the section tight:

```go
func() {
	defer release()
	result, err = ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)
}()
```

Exam schemas would be uncontended anyway — both exam DML entry points pass
through the single-worker queue — but the exam path is where a mistake is most
expensive, so it gets no new code regardless.

### ARCH-060 — The wait budget is a separate context from the evaluation context

```go
waitCtx, cancel := context.WithTimeout(ctx, practiceGateWaitBudget) // 15 * time.Second
defer cancel()
release, err := ctrl.dmlGate.Acquire(waitCtx, schema)
// ...
result, err := ctrl.validator.Validate(ctx, ...)   // ctx — never waitCtx
```

Passing `waitCtx` into `Validate` would let queue-wait consume the execution
window. That is exactly the defect the audit recorded on the exam side, where
the 30 s job budget starts at *enqueue* and pre-empts `evaluateDML`'s own timer
(`postgres_evaluation.go:191`). This design must not reproduce it, and a test
pins the distinction.

`practiceGateWaitBudget = 15 * time.Second`, a named constant. Rationale:
`evaluateDML` is capped at 30 s, so a genuinely stuck grader sheds waiters in
half that rather than hanging them; a class of 30 at a realistic ~300 ms each
drains in ~9 s, comfortably inside budget.

Failure handling at both call sites:

| Condition | Response | Attempt row |
|---|---|---|
| `context.DeadlineExceeded` waiting | 503 `practice_busy`, retriable | **none** |
| `context.Canceled` (client gone) | abort, no body | none |
| Validator error after acquiring | 500, unchanged | yes — existing behaviour |
| Panic in validator | gate released, panic propagates to gin recovery | none |

The 503 case deliberately departs from practice's existing "persist the attempt
regardless of outcome" rule: evaluation never ran, so there is no attempt to
record. Persisting a spurious `is_correct=false` row for a request that was shed
would corrupt the student's history.

The 503 body follows the `question_not_ready` shape already used at
`practice_controller.go:191`:

```json
{"error": "practice_busy", "message": "Another submission for this category is being graded. Please try again."}
```

**No deadlock is constructible on the gate itself.** Exactly one gate is held
per request and never two at once, so there is no lock-ordering cycle.

### ARCH-061 — A narrow validator seam on the practice controller

`PracticeController.validator` is currently the concrete
`*services.ValidationService` (`practice_controller.go:87`), which leaves no way
to observe call overlap without a live database.

Introduce a minimal interface in `package controllers`, satisfied by the
existing concrete type:

```go
type practiceValidator interface {
	Validate(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error)
	ValidatePreview(ctx context.Context, studentID int, q models.Question, sql, schema, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error)
}
```

`NewPracticeController` accepts the interface; `cmd/main.go` keeps passing
`*services.ValidationService` unchanged. `QuizController` is not touched — the
seam is practice-only, matching the scope of this spec.

This exists to make the gating *branch* testable without a database: that SELECT
is not gated, and that a shed request writes no attempt row. Both are behaviours
that would otherwise regress silently.

## Known limitations

- **Process-local.** Two backend instances against one database would each keep
  their own gate and could run two concurrent practice DML evaluations on the
  same schema. The advisory lock stays shared, so nothing at the database level
  catches it. This matches what `SubmissionQueue` already assumes; if the
  deployment ever scales out, both need revisiting together.
- **Damage scope untouched.** The gate closes concurrency hazards only. A
  bypassed savepoint still corrupts the lecturer's real data.
- **Serialisation is now strict.** Practice DML per category goes from
  concurrent-with-lock-contention to strictly one-at-a-time. Under a full class
  submitting simultaneously this changes latency in a direction that has not
  been measured. Case A's baseline measurement (below) exists to quantify it.
- **Stale student pools across the test suite (pre-existing, found while
  building the Case B probe).** `postgres_exam_categories_test.go:566`,
  `postgres_exam_categories_gap_test.go:105`, `postgres_preview_role_test.go`
  and `postgres_restricted_pool_test.go:467` each re-run
  `CreateRestrictedUser`, regenerating the student role passwords, while
  `openTestRepo` constructs the pools once behind a `sync.Once`. Every later
  test that calls `poolForRole` therefore acquires against a stale password and
  fails `28P01` — silently changing what those tests actually prove. Individual
  files work around it by calling `InitStudentPools` themselves. Out of scope
  here; worth its own fix.
- **Pool sizing is unchanged.** `exam_student` stays at 4 connections. The gate
  moves waiting *ahead* of `pool.Acquire`, so waiters no longer occupy a
  connection while queued, but the pool comment at
  `postgres_student_pool.go:70-72` still cites the queue as its justification
  and should be updated to cite the gate for practice.

## Testing

Run everything with `go test ./... -p 1` — these tests are DB-global.

### Evidence gate — run before implementing

Case B was written and run **first, against unmodified code**, in
`internal/repository/postgres_practice_concurrent_dml_test.go`. Three subtests:

| Subtest | Result |
|---|---|
| `_CaseB_Mechanism` — raw SQL in evaluateDML's transaction shape | PASS — `SQLSTATE 40P01` on actor 1 |
| `_CaseB_SerialControl` — identical fixture, run one at a time | PASS — both graded `success` |
| `_CaseB_ProductionPath` — two concurrent `repo.Evaluate` | FAIL **by design** — one student shed |

**Gate passed: `40P01` fires.** ARCH-058 … ARCH-061 proceed, and
`_CaseB_ProductionPath`'s `t.Errorf` becomes the regression guard — it must be
replaced by a pass assertion when the gate lands, not deleted.

Two properties of this file that must survive the fix:

- **It fails inconclusive, never negative.** An actor that dies before running
  its seed statements (connection, auth) calls `t.Fatalf("INCONCLUSIVE…")`. An
  infrastructure failure must never print "not reproduced" and argue for
  retiring a live finding.
- **It refreshes the student pools in its fixture.** Several tests in this
  package re-run `CreateRestrictedUser`, which regenerates the student role
  passwords, while `openTestRepo` builds pools once behind a `sync.Once`. Any
  test using `poolForRole` after them inherits a stale password and fails
  `28P01`. `mkCaseBFixture` calls the idempotent `InitStudentPools` to stay
  order-independent. **This is a pre-existing suite-wide fragility, not
  specific to this file** — see *Known limitations*.

### Layer 1 — `SchemaGate` unit tests (no DB, no gin)

- N goroutines on one key under `-race`: maximum observed concurrency is 1
- distinct keys never block each other
- `DeadlineExceeded` while waiting returns an error and leaks nothing — a
  subsequent `Acquire` on the same key succeeds
- `context.Canceled` is distinguishable from `context.DeadlineExceeded`
- calling `release` twice is safe
- a panic inside the critical section leaves the gate reusable
- empty schema string yields a usable no-op release

### Layer 2 — controller tests (fake validator via ARCH-061)

- two concurrent `Submit`, same category, DML → validator calls do not overlap
- `Submit` versus `Preview`, same category → mutually exclusive
- SELECT question → **not** gated; two concurrent calls do overlap
- different categories → overlap
- wait budget exhausted → 503 `practice_busy` **and no `practice_attempts` row**
- the validator receives the request context, not `waitCtx` — pins ARCH-060

### Layer 3 — integration (`TEST_DATABASE_URL`, real Postgres)

- **Case A**: two concurrent submits, same question, fixed-PK seed → both
  succeed, no unique violation reaches the student. Run against unmodified code
  first and record wall-clock per submission, so the strict-serialisation cost
  noted under *Known limitations* is measured against a real baseline rather
  than assumed
- **Case B**: two questions, opposite-order multi-table seeds → no `40P01`
- integrity: `practice_cat_N` contents identical before and after N concurrent
  runs
- regression: `postgres_seedsql_isolation_test.go` and
  `dml_autoseed_schema_isolation_test.go` stay green
