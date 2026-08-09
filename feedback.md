# Exam-vs-Practice Inspection — Open Findings

Audit date: 2026-08-08. Scope: verify the documented claims that the exam path
behaves like practice, that DML is queued, that practice is blocked while an
exam runs, and that savepoint handling is identical.

Documentation was corrected in the same pass (`README.md:11`, `MANUAL.md:245`,
`API_SPEC.md` → Practice, `ARCHITECTURE.md:78` and the Practice Evaluation
Concurrency section). **No code was changed.** Everything below is still open.

## Verdict on the four claims

| Claim | Verdict | Note |
|---|---|---|
| Exam executes SQL the same way as practice | **TRUE** | one shared engine, not two |
| INSERT/UPDATE/DELETE go through a queue | **FALSE as documented** | exam-only; practice bypasses |
| Practice unavailable while an exam is started, returns on stop | **TRUE in effect** | but via a per-exam toggle, not "exam is active" |
| Savepoint handling identical to practice | **TRUE** | one implementation, `postgres_evaluation.go:183` |

Both paths converge on `services/validation_service.go:92` →
`repository/postgres_evaluation.go:25` `Evaluate`. Only two `Evaluate` call
sites exist in the backend. Login role, `SET LOCAL ROLE`, `search_path`
(`pg_temp` last), 30 s timeout, seed replay, post-image capture and
single-statement enforcement are therefore identical by construction. The
divergences are all in *what is handed to that engine and how it is scheduled*.

---

## OPEN-1 — Practice DML is unserialized against the live reference schema

Severity: **high** (data-integrity + availability) — **deadlock confirmed by
test 2026-08-09; CLOSED the same day by ARCH-058…061**

Exam DML grades against a clone (`sandbox_exam_<id>`, `postgres_schema.go:755`).
Practice has no sandbox at all — `sandbox_practice` / `ResetPracticeSandbox`
were removed as dead code. Practice DML runs directly against
`practice_cat_<id>`, the lecturer's real imported database, which
`postgres_schema.go:1291-1296` grants `exam_student` full INSERT/UPDATE/DELETE
on.

```
cmd/main.go:44             NewSubmissionQueue(256, 30*time.Second)
cmd/main.go:148            → injected into QuizController ONLY
quiz_controller.go:261,396 the only two enqueue sites, gated `if question.Type == "DML"`
practice_controller.go:91  NewPracticeController(repo, validator, auth) — no queue param exists
practice_controller.go:205 ctrl.validator.Validate(...) called directly
```

Two consequences:

1. The savepoint at `postgres_evaluation.go:221` is the *only* barrier between a
   practice student's DML and permanent corruption of the reference DB. On the
   exam path it is a second line of defence behind the clone.
2. `postgres_student_pool.go:70-72` sizes the `exam_student` pool to 4
   connections *because* "graded DML is serialised by the single-worker
   SubmissionQueue" — an assumption practice violates. Concurrent practice
   submitters using the standard fixed-PK seed pattern (ARCH-045, e.g.
   `INSERT ... VALUES ('999999999', ...)`) block on the unique index for each
   other's full transaction; deadlock is possible on multi-table seeds.

**REPRODUCED 2026-08-09** — `postgres_practice_concurrent_dml_test.go`.

Two practice questions in one category whose `seed_sql` blocks insert into the
same two tables in opposite order deadlock under concurrent submission:

```
actor 1: seed stmt 2: ERROR: deadlock detected (SQLSTATE 40P01)
student 1: status="error" feedback="This question could not be prepared for
           grading due to a configuration problem. Please contact your
           instructor so the question can be re-saved."
```

Both students submitted the **model answer verbatim**. The serial control
(same fixture, one after the other) grades both `success`, so concurrency is
the cause, not the fixture. `evaluateDML` converts the seed failure into a
graded `"error"` with a **nil Go error** (`postgres_evaluation.go:258-289`), so
a server-side concurrency defect reaches the student as a broken-question
message and the lecturer as a bogus bug report.

Note the fix is the **queue, not a sandbox**. A sandbox clone bounds damage
*scope*; it does nothing about concurrency. What protects exam DML here is the
single-worker `SubmissionQueue`, which practice never touches.

**Fixed 2026-08-09** — `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`
(ARCH-058 … ARCH-061). An in-process per-schema gate inside
`Evaluate`/`EvaluatePreview`, keyed on the schema those functions resolve as the
execution target, so the guarantee belongs to the engine rather than to whichever
controller remembered to ask for it. `TestPracticeConcurrentDML_CaseB_ProductionPath`
now asserts both concurrent submissions grade `success`.

No backfill. Any practice attempt already recorded as incorrect because its seed
deadlocked is indistinguishable in `practice_attempts` from a genuinely wrong
answer, so past practice scores cannot be repaired retroactively.

Practice *serial* isolation was already well covered
(`postgres_seedsql_isolation_test.go:119`,
`dml_autoseed_schema_isolation_test.go:77`). Exam rollback has exactly one
assertion (`postgres_exam_categories_test.go:835-839`).

## OPEN-2 — No signal handling; queue drain is dead code

Severity: **medium** (silent data loss on every deploy)

`cmd/main.go:45-51` registers a 35 s `submissionQueue.Shutdown` drain as a
`defer` in `main()`. But `main.go` installs no signal handler (`grep -n
'signal\.\|SIGTERM\|http.Server'` → nothing) and ends in a blocking
`r.Run(":"+port)` at `:385`. On SIGTERM/SIGINT — i.e. every `docker stop` or
redeploy — Go exits without running deferred functions, so the drain never
happens.

Up to 256 buffered submissions plus one in-flight evaluation are lost. Because
`app.submissions` is written only *after* evaluation returns
(`quiz_controller.go:278-292`), a dropped job leaves **no record whatsoever** —
the student's answer vanishes silently.

Fix: `signal.Notify` + `http.Server.Shutdown` around the gin engine, drain
before exit.

## OPEN-3 — Exam SubmitAnswer has no readiness gate

Severity: **medium** (wrong grades, blames students)

Practice refuses submissions for a question whose model answer never captured:

```go
// practice_controller.go:191
if question.ExpectedResultStatus != "captured" { → 409 question_not_ready }
```

`quiz_controller.go` never reads `ExpectedResultStatus`. An exam question with a
failed capture is graded against a nil `expected_result`, marking every
submission incorrect. Same class of defect as the historical
`target_table` case bug: a lecturer-side error surfaces as student failure.

## OPEN-4 — Exam sandboxes are never re-synced

Severity: **medium**

`EnsureExamSandboxReady` (`postgres_exams.go:98-116`) returns early if any
sandbox row exists, and `ensureTablesInSchema` (`postgres_schema.go:701-703`)
skips tables that already exist. So the clone is built once and never refreshed.

This matters because exam capture and exam grading run against *different*
physical schemas: capture uses category/curriculum/`public`
(`postgres_questions.go:61-85`), grading uses `sandbox_exam*`. Their agreement
depends entirely on the clone being current. Reference data edited after first
activation will not propagate, and the mismatch shows up as incorrect grades.

## OPEN-5 — Practice freeze: scope and semantics

Severity: **low-medium** (behaves acceptably, but not as the toggle implies)

The gate is server-side and real — `middleware/exam_freeze.go:79`
`PracticeFreezeMiddleware`, applied at `cmd/main.go:328` to the whole
`/api/v1/practice` student group, backed by `postgres_state.go:24`. A student
curling `/api/v1/practice/submit` gets 423. Not UI-only.

Four properties worth deciding on:

- **Activation force-writes the flag.** `postgres_exams.go:68` sets
  `block_practice_during_exam = TRUE` unconditionally, overriding the column's
  `FALSE` default (`postgres_schema.go:827`). Deactivation clears `is_active`
  but leaves the toggle set, so a lecturer's deliberate "off" is silently
  reverted by the next activation.
- **Global scope.** `EXISTS(is_active AND block_practice_during_exam)` has no
  join to class, enrolment, or category. One frozen exam blocks practice for
  every student in every category, enrolled or not.
- **Keyed on `is_active`, not `ActiveNow`.** A future-dated exam freezes
  practice the moment it is activated, before `scheduled_start_at`.
- **Fails open.** `exam_freeze.go:94-101` — if the DB probe errors, practice is
  allowed. The unused `ExamFreezeMiddleware` fails *closed* (`:57`). Opposite
  postures in one file.

Nothing is ever hidden: the nav link (`App.vue:138`) and route stay, the page
loads, the request 423s and `PracticeView.vue:251` renders a banner. Unfreeze
takes effect server-side on the next request
(`InvalidateExamFreezeCache`, `lecturer_controller.go:218`) but is not pushed —
the student must click Retry or reload.

## OPEN-6 — Freeze middleware test coverage is on the dead path

Severity: **low**

`ExamFreezeMiddleware` (`exam_freeze.go:39`) is wired to nothing, yet owns all 8
tests in `exam_freeze_test.go`. `PracticeFreezeMiddleware` — the one actually in
the request path — has **zero** middleware-level tests. Repo-layer coverage only
(`postgres_autofreeze_test.go`).

## OPEN-7 — Stale student pools silently weaken the test suite

Severity: **medium** (tests that appear to pass may be proving nothing)

Found 2026-08-09 while building the OPEN-1 probe, which first reported a **false
negative**: run inside the full package it printed "CASE B NOT REPRODUCED — do
not ship the schema gate", on evidence that was actually a connection failure.

`openTestRepo` builds the student pools once behind a `sync.Once`. But four
files re-run `CreateRestrictedUser`, which **regenerates the student role
passwords**:

```
postgres_exam_categories_test.go:566
postgres_exam_categories_gap_test.go:105
postgres_preview_role_test.go:76,79,119,242
postgres_restricted_pool_test.go:467
```

Every test that calls `poolForRole` after one of those runs acquires against a
stale password and fails:

```
failed SASL auth: FATAL: password authentication failed for user "quiz_exec_rw" (SQLSTATE 28P01)
```

The danger is not the failure — it is what a test *does* with it. Any test that
treats "the evaluator returned an error" as a negative result will silently
report the wrong conclusion, and the more confident the test's failure message,
the more damage it does. Some files already work around this by calling
`InitStudentPools` themselves; there is no suite-wide guarantee.

Two things to decide: whether `CreateRestrictedUser` should re-init pools it
invalidates, and whether eval-path tests should be required to distinguish
infrastructure failure from a real negative. The OPEN-1 probe does the latter
(`t.Fatalf("INCONCLUSIVE…")`), and that pattern is worth generalising.

## OPEN-8 — A password test hardcodes an email and never cleans up

Severity: **low** (test-only, but it fails work that did not cause it)

`TestVerifyForgotPassword_HTTP` (`internal/controllers/forgot_password_http_test.go`)
creates a user with the hardcoded address `verify@example.com` and deletes
nothing afterwards. On the shared `TEST_DATABASE_URL` the row survives the run,
so the next invocation collides on the unique email and the test fails.

Found 2026-08-09 during the ARCH-058 work, where it failed a package run twice
and had to be reproduced with the branch's changes stashed before it could be
attributed. That is the real cost: a test that fails for reasons unrelated to
the change under test makes every green run an argument rather than a fact.

Two fixes worth considering, neither done here: give the fixture a unique
address per run the way `mkCtrlUser` already does, or delete the row in a
`t.Cleanup`. Same family as OPEN-7 — suite-wide state that no single test owns.

---

### Smaller notes

- Exam DML's 30 s budget starts at *enqueue*, so queue wait eats the execution
  window; `evaluateDML` then starts its own fresh 30 s timer
  (`postgres_evaluation.go:191`), which the outer timeout always pre-empts.
- Queue overflow is a non-blocking send with `default`
  (`submission_queue.go:107-113`) → immediate `ErrQueueFull` → HTTP 503. It
  never blocks-and-waits; a saturated queue *rejects* exam answers.
- `evaluateDMLPreview` inverts the `SET LOCAL ROLE` / `SET LOCAL search_path`
  order relative to `evaluateDML` (`:510`/`:519` vs `:234`/`:237`) and swallows
  recovery errors with `_, _ =` where `evaluateDML` hard-fails.
  `evaluateQueryPreview` omits the `acquireSchemaEvalLock` that `evaluateQuery`
  takes at `:136`.
- Practice preview discloses the real `correct`/`incorrect` status
  (`practice_controller.go:290-293`); exam preview rewrites it to `"preview"`
  (`quiz_controller.go:423-428`). Documented as intentional.
- Practice persists the attempt *before* checking the eval error
  (`practice_controller.go:205-224`), writing `is_correct=false` /
  `validation_details=NULL` on infrastructure failures, and has no 503/504
  mapping. Exam checks the error first (`quiz_controller.go:265-292`).

### Running the backend tests

Backend tests are DB-global — use `go test ./... -p 1`.
