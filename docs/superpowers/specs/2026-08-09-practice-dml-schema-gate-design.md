# Serialising Practice DML Per Category

**Date:** 2026-08-09
**Status:** Approved, ready for implementation — evidence gate **PASSED** 2026-08-09 (see *Testing*)
**ARCH range:** ARCH-058 … ARCH-061
**Origin:** `feedback.md` OPEN-1, from the 2026-08-08 exam-vs-practice audit

**Revision 2 — 2026-08-09.** The gate moves from `PracticeController` into the
evaluation engine (`PostgresRepository.Evaluate` / `EvaluatePreview`). Revision 1
placed it in the controller, which left `repo.Evaluate` — the single point every
DML evaluation passes through — unprotected, and mis-keyed the preview path.
See *Why the engine and not the controller*.

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

Make the evaluation engine itself enforce what the exam queue only enforces by
accident: **at most one DML evaluation per execution schema at a time,
process-wide** — without adding database state and without serialising SELECT or
serialising practice globally.

## Why the engine and not the controller

Revision 1 gated `PracticeController`. Three problems killed it.

**1. The invariant belongs to the engine.** `repo.Evaluate` is the single
convergence point for every DML evaluation in the system
(`validation_service.go:92`, `:123` are its only callers). A guard placed above
it protects the two call sites that remembered to ask. A guard placed *in* it
protects the property — a third caller, a future controller, a test, or a script
cannot forget.

**2. It mis-keyed preview.** `EvaluatePreview` resolves the DML execution schema
as `previewSchema` when a decoy exists and `sandboxSchema` only otherwise
(`postgres_evaluation.go:389-397`). Revision 1 keyed preview on `schema`, so on
every dual-schema category it would have gated a schema the DML never touched
and left the one it did touch open.

**3. It missed a live contention pair — within a single practice category.**
Practice submit gates on `practice_cat_N`; practice preview executes against
`practice_cat_N_preview` when a decoy exists. Both put `public` in
`search_path`, so a seed touching an unqualified table that resolves to `public`
is contended between them while the two keys differ. Only a key computed where
the schema is resolved makes that visible at all.

**Correction, 2026-08-10.** An earlier draft of this section claimed practice
preview and *exam* preview could both resolve `question.CategoryPreviewSchema`
and contend. They cannot. `quiz_controller.go:361` sits in the **QUERY** branch,
and QUERY is never gated; exam DML preview resolves
`ExamCategoryPreviewSandboxName` → `sandbox_exam_cat_N_preview` or
`ExamPreviewSandboxName` → `sandbox_exam_N_preview`, and graded exam DML resolves
`sandbox_exam_N`. The practice and exam namespaces are disjoint, so **exam and
practice can never share a gate key.** Reasons 1 and 2 above are unaffected and
still carry the decision; the consequences of this correction are recorded under
*Known limitations*.

The cost is that the exam path is no longer untouched. That is accepted
deliberately — see *Known limitations*.

## Non-goals

- **No practice sandbox clone.** Damage scope stays open; practice DML still
  runs against `practice_cat_N`, the lecturer's real imported data, with the
  savepoint as sole barrier. Separate decision, separate spec.
- **No change to `SubmissionQueue`** — not its wiring, capacity, timeout, or
  worker count. Exam *scheduling* is unchanged; exam DML still reaches the engine
  one job at a time.
- **No new Postgres objects.** No table, column, migration, DDL, or persisted
  state. The gate is Go memory only.
- **Not multi-instance safe.** Process-local, matching the existing
  `SubmissionQueue` assumption. Stated as a limitation, not solved here.
- **SELECT is never gated**, on either path. `Evaluate` branches to
  `evaluateQuery` before the gate is reached.
- The remaining `feedback.md` findings (OPEN-2 … OPEN-7) are out of scope.

## Design decisions

### ARCH-058 — `SchemaGate`, a process-local per-schema serialiser

New package `internal/schemagate`, file `internal/schemagate/schema_gate.go`.

**It cannot live in `internal/services`.** `services` imports `repository`
(`validation_service.go`), so `repository` importing `services` would close an
import cycle. A leaf package with no project dependencies is importable by both
and keeps its tests genuinely DB-free, outside the `repository` package's test
harness.

```go
package schemagate

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

**The zero value is usable.** `gateFor` allocates the map on first use under the
mutex, so `SchemaGate` works as a plain struct field with no constructor call.
That is what lets `PostgresRepository` hold it as a value and leave
`NewPostgresRepository` (`postgres.go:185`) completely unedited — a repository
built any other way still gets a working gate rather than a nil-pointer panic.
`NewSchemaGate` remains for standalone use.

Map entries are created lazily and never evicted. The population is bounded by
the number of category schemas; a deleted category leaks one channel and one map
entry. Not worth an eviction path.

### ARCH-059 — The engine takes the gate, keyed on the resolved execution schema

`PostgresRepository` gains one `*schemagate.SchemaGate`, built in
`NewPostgresRepository` (`postgres.go:152`). Two acquire sites, both on the DML
branch only, both keyed on **the schema the code has already resolved as the
execution target** — never on a schema recomputed by the caller:

| Site | Key | Resolves to |
|---|---|---|
| `Evaluate` (`postgres_evaluation.go:25`) | `sandboxSchema` | `practice_cat_N` / `sandbox_exam_N` |
| `EvaluatePreview` (`:365`) | the local `schema` after `previewSchema` fallback (`:389-397`) | decoy schema, else the grading schema |

**Added 2026-08-10, after the whole-branch review.** The student paths are not
the only ones that replay seeds against a graded schema. The lecturer-side
capture and test-run paths — `captureExpectedResult` and
`PreviewPracticeCorrectSQL` in `postgres_practice.go`, and their two exam
equivalents in `postgres_questions.go` — run `applyQuestionSeed` plus the
lecturer's DML against the same `practice_cat_N` a student is graded on, and
originally took no gate at all. They now do.

That omission mattered more than the student-vs-student case it mirrors. A
lecturer saving Q2 while a student submits Q1 produces the same cycle, and if
the *lecturer* loses, `captureExpectedResult` persists
`expected_result_status='failed'` with a deadlock `capture_error` — so every
student then hits 409 `question_not_ready` on that question until someone
re-saves it. A transient race became a durable outage for one question.

It also refutes, in the tree as it stood, this design's own argument for engine
placement: "a third caller, a future controller, a test, or a script cannot
forget." Four such callers already existed. The gate belongs in the engine, but
the engine's entry points were not the only ones that needed it.

Keying at the resolution point is the whole reason the gate moved down. The
caller does not have to know which of two schemas the preview will actually
execute against — the engine already computed it one line earlier.

SELECT is untouched: `Evaluate` returns via `evaluateQuery` before the gate, and
`EvaluatePreview` via `evaluateQueryPreview`.

The critical section wraps the `evaluateDML*` call only. Release is `defer`red
so a panic inside evaluation cannot strand a schema:

```go
release, err := r.acquireDMLGate(ctx, sandboxSchema)
if err != nil {
	return models.ValidationResult{}, err
}
defer release()
return r.evaluateDML(ctx, question, studentSQL, sandboxSchema)
```

**No deadlock is constructible on the gate itself.** Exactly one gate is held
per evaluation and the four `evaluate*` internals have no other callers
(verified: `Evaluate` and `EvaluatePreview` are the only ones), so no evaluation
nests inside another and there is no lock-ordering cycle. The gate is also taken
strictly *before* `BEGIN`, so a holder never waits on a database lock while
another goroutine waits on the Go gate behind it. Schema rebuild
(`acquireSchemaRebuildLockTx`) does **not** take the gate, so it cannot
participate in a cycle either.

### ARCH-060 — The wait budget is a separate context, and failure is a sentinel

```go
// schemaGateWaitBudget = 15 * time.Second
waitCtx, cancel := context.WithTimeout(ctx, schemaGateWaitBudget)
defer cancel()
release, err := r.dmlGate.Acquire(waitCtx, schema)
```

`waitCtx` bounds the wait and nothing else — `ctx`, not `waitCtx`, is what the
`evaluateDML*` call receives, so queue-wait can never be deducted from the
execution window. That is precisely the defect the audit recorded on the exam
side, where the 30 s job budget starts at *enqueue* and pre-empts
`evaluateDML`'s own timer (`postgres_evaluation.go:191`). A test pins it.

15 s rationale: `evaluateDML` is capped at 30 s, so a genuinely wedged grader
sheds waiters in half that rather than hanging them; a class of 30 at a realistic
~300 ms each drains in ~9 s, inside budget.

**Failure must be distinguishable from a timeout**, because the exam controller
already maps `context.DeadlineExceeded` to 504 (`quiz_controller.go:270`).
Returning a bare `ctx.Err()` would make a busy schema look like a graded request
that timed out. So `acquireDMLGate` separates the two causes:

```go
var ErrSchemaBusy = errors.New("schema is busy: another DML evaluation is in progress")

if err != nil {
	if ctx.Err() != nil {
		return nil, ctx.Err()   // the CALLER's deadline/cancel — a real timeout
	}
	return nil, fmt.Errorf("%w: %s", ErrSchemaBusy, schema)  // our wait budget only
}
```

`ErrSchemaBusy` deliberately does **not** wrap `context.DeadlineExceeded`.

`ValidationService` returns executor errors unwrapped
(`validation_service.go:93-95`, `:124-126`), so `errors.Is` reaches both
controllers intact.

### ARCH-061 — Controllers map `ErrSchemaBusy` to 503; practice records nothing

**Practice** (`practice_controller.go`) persists the attempt *before* it checks
the evaluation error (`:205-224`), so without a change a shed request would
write `is_correct=false` for SQL that never ran. The busy check therefore goes
**before** `CreatePracticeAttempt`:

```json
{"error": "practice_busy", "message": "Another submission for this category is being graded. Please try again."}
```

matching the `question_not_ready` body shape at `practice_controller.go:191`.

**Exam** (`quiz_controller.go:265`) already has an error ladder — `ErrQueueFull`
→ 503, `DeadlineExceeded` → 504, else 500. `ErrSchemaBusy` joins the 503 arm,
above the `DeadlineExceeded` arm. This is the one exam-path change in the design;
it is three lines and it prevents a transient busy schema from being reported as
a 500. Exam persists after the error check already, so nothing else moves.

**The validator seam stays**, and its justification is now stronger. Introduce in
`package controllers`, satisfied by `*services.ValidationService`:

```go
type practiceValidator interface {
	Validate(ctx context.Context, studentID int, q models.Question, sql, schema string) (models.ValidationResult, error)
	ValidatePreview(ctx context.Context, studentID int, q models.Question, sql, schema, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error)
}
```

`NewPracticeController` accepts the interface; `cmd/main.go:153` is unchanged.
A fake that simply **returns `ErrSchemaBusy`** makes the 503-and-no-attempt-row
assertion deterministic — no timing, no contention to arrange. Under revision 1
this seam had to fake *timing*; now it fakes an *error*, which is strictly more
reliable. Overlap itself is asserted where the gate lives, in the repository
package.

## Known limitations

- **Process-local.** Two backend instances against one database would each keep
  their own gate and could run two concurrent practice DML evaluations on the
  same schema. The advisory lock stays shared, so nothing at the database level
  catches it. This matches what `SubmissionQueue` already assumes; if the
  deployment ever scales out, both need revisiting together.
- **Damage scope untouched.** The gate closes concurrency hazards only. A
  bypassed savepoint still corrupts the lecturer's real data.
- **Cross-schema seeds — the schema-*qualified* half is CLOSED.** Shipped on
  branch `fix/seed-schema-qualification` (`ee330ed`, hardened by `e28a249`,
  `21d4b69`, `ea3b6a0`, `867731e`). `validateSeedSQL` (`dml_autoseed.go:1039`)
  now refuses a seed that names another schema, so `INSERT INTO
  public.employee …` can no longer take locks the gate cannot see. The refusal
  runs on the save paths (`postgres_questions.go:30`,
  `postgres_practice.go:375` and `:710`) and again on the replay path
  (`applyQuestionSeed`), so it is enforced at grade time too, not only in the
  authoring form.

  **The remedy this section originally proposed was wrong; do not re-cost the
  work from it.** It said closing this "would mean either restricting
  `validateSeedSQL` to the question's own schema or keying the gate on the set
  of tables a seed touches — both larger changes than this spec". Neither was
  needed for this half. An existing validator already covered it: the check
  reuses `schemaQualifiedNameRe` (`postgres_schema.go:258`), the same denylist
  that keeps a category import inside its own schema, plus one purpose-built
  character scanner, `stripSQLLiterals` (`dml_autoseed.go:892`), which blanks
  string-literal bodies before the scan so a schema name inside a `VALUES`
  literal stays data. No new gate key and no second rule to keep in agreement
  with the first.

  Three bounds remain, and they are **not equally severe** — do not read them
  as one list of equivalent nits:

  - **A double-quoted schema name is not refused, and it would genuinely
    execute a cross-schema write.** `INSERT INTO "public"."employee" …` passes:
    `schemaQualifiedNameRe` needs the schema word followed by optional
    whitespace and then a dot, and a closing `"` is neither. This is the one
    bound that is a real hole rather than a degraded failure. It was left
    because widening that regex would change behaviour for its other caller,
    `ImportSchemaIntoCategory`, which has accepted the same bypass since
    ARCH-027. Closing it belongs with the unqualified work below, not here.
  - **A non-ASCII dollar-quote tag** (`$é$…$é$`) is not recognised as a
    delimiter (`isDollarTagStartByte` is ASCII-only), so such a body stays
    visible to the denylist and its apostrophes can mis-pair and blank a
    qualified target. This degrades rather than escapes:
    `splitTopLevelSemicolons` is `$`-blind in exactly the same way, so the seed
    comes back as ONE statement holding every command, and the pool's extended
    protocol accepts at most one command per Parse — a lecturer-facing exec
    error, not a silent write.
  - **`E'it\'s'` backslash escapes** desync all four scanners identically, so
    such a seed is refused upstream by `checkSeedQuotesBalanced` as "an
    unterminated quoted value". That is an over-strict refusal — a bound on
    what a lecturer may write, not an escape from the check. It is pre-existing
    in the seed path, not introduced here.

  The bounds are recorded in full on `stripSQLLiterals` and `dollarQuoteTagAt`
  in `dml_autoseed.go`; that comment is the authority, this is the summary.

- **Cross-schema seeds — the *unqualified* half is still OPEN, and it is the
  more likely one in practice.** A lecturer writes `INSERT INTO employee …`,
  not `INSERT INTO public.employee …`. If that bare name does not exist in the
  question's own schema it falls through `search_path` to `public`, takes locks
  on a shared table, and produces `40P01` in exactly the original shape — the
  gate keys on the execution schema and cannot see it. The refusal above does
  not touch this case and must never be described as if it did.

  Closing it means removing `public` from the DML `search_path` at
  `postgres_evaluation.go:301` and `postgres_evaluation.go:597`, and at the two
  capture sites that must move with them — `postgres_questions.go:129` and
  `postgres_practice.go:461`. All four have to change together: leave the
  capture sites on the wider path and a question captures green against
  `public` and then fails at grade time, which is the failure mode this whole
  design exists to stop. Deliberately out of scope for the
  `fix/seed-schema-qualification` branch.

  This is not only a cross-*category* hazard. Within one practice category,
  submit gates on `practice_cat_N` while preview gates on
  `practice_cat_N_preview`, so a seed touching an unqualified table that
  resolves to `public` is ungated between those two.
- **Serialisation is now strict.** DML per execution schema goes from
  concurrent-with-lock-contention to strictly one-at-a-time. Under a full class
  submitting simultaneously this changes latency in a direction that has not
  been measured. Case A's baseline measurement (below) exists to quantify it.
- **The exam path is no longer untouched, but its gate is uncontended by
  construction.** Exam DML reaches the engine one job at a time (single-worker
  queue), so exam evaluations never contend with each other; and per the
  correction above, exam and practice schema namespaces are disjoint, so they
  cannot contend with each other either. Every exam acquire is therefore an
  uncontended channel send — tens of nanoseconds.
  - Consequence: the `ErrSchemaBusy` → 503 arms in `quiz_controller.go` are
    **defence-in-depth, not a reachable path today**, and they have no test. That
    is deliberate. `QuizController` takes the concrete `*services.ValidationService`
    with no interface seam, so testing them would mean introducing an exam-side
    equivalent of ARCH-061's `practiceValidator` — a refactor of the path this
    design set out not to disturb, in order to test code that cannot currently
    execute. If exam and practice schemas are ever unified, add the seam and the
    test together.
  - **The wait could consume an exam job's budget** if it ever did contend:
    `context.WithTimeout(ctx, …)` keeps whichever deadline is earlier, so wait
    time would come out of the exam job's 30 s. Unavoidable at this layer — the
    engine cannot manufacture budget the caller did not give it.
  - The gate is FIFO, which the above relies on without it being obvious: Go
    hands a released buffered-channel slot to the head of the blocked-sender
    queue, so a stream of submitters cannot starve a waiter.
- **A long lecturer import turns waiting students into misleading 503s.**
  `ImportSchemaIntoCategory` and `DeletePracticeCategory` take the exclusive
  rebuild advisory lock. Under the gate, the one evaluation holding the gate
  blocks on `acquireSchemaEvalLock` inside its transaction, and everyone behind
  it is shed after `schemaGateWaitBudget` with "Another submission for this
  category is being graded" — which is not what is happening. Before this change
  they all waited on the advisory lock and eventually succeeded. Retriable, so
  not severe, but the message misattributes the cause. Fixing it means
  distinguishing "shed behind an evaluation" from "shed behind a rebuild", which
  the gate cannot see.
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

**Gate passed: `40P01` fires.** ARCH-058 … ARCH-061 proceed.

Because the fix now lives *below* `repo.Evaluate`, `_CaseB_ProductionPath`
becomes the direct regression test for it: its `t.Errorf` is replaced by an
assertion that **both** concurrent submissions are graded normally. It is the
one test that fails if the gate is removed, and it needs no new fixture to say
so. `_CaseB_Mechanism` issues raw SQL in `evaluateDML`'s transaction shape,
bypassing `Evaluate` entirely, so it still deadlocks — correctly, since it
documents the hazard rather than the guard.

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

### Layer 2 — engine tests (`repository` package, real Postgres)

The gate now lives here, so the mutual-exclusion assertions live here too:

- `_CaseB_ProductionPath` — two concurrent `repo.Evaluate` on one practice
  schema → both graded normally, no `40P01`
- concurrent `Evaluate` (submit) versus `EvaluatePreview` on the same resolved
  schema → do not overlap
- `EvaluatePreview` with a non-empty `previewSchema` keys on the **decoy**, not
  on `sandboxSchema` — the bug revision 1 would have shipped
- two QUERY evaluations on one schema **do** overlap — SELECT is not gated
- distinct schemas overlap — the gate is per-schema, not global
- the evaluator receives `ctx`, not `waitCtx` — pins ARCH-060

### Layer 3 — controller tests (fake validator via ARCH-061)

- practice `Submit`, validator returns `ErrSchemaBusy` → 503 `practice_busy`
  **and no `practice_attempts` row**
- practice `Preview`, same → 503, same body
- exam `SubmitAnswer`, same → 503 (not 500, not 504)

### Layer 4 — integration (`TEST_DATABASE_URL`, real Postgres)

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
