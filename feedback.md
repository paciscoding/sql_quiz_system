# Exam-vs-Practice Inspection — Open Findings

Audit date: 2026-08-08. Scope: verify the documented claims that the exam path
behaves like practice, that DML is queued, that practice is blocked while an
exam runs, and that savepoint handling is identical.

Documentation was corrected in the same pass (`README.md:11`, `MANUAL.md:245`,
`API_SPEC.md` → Practice, `ARCHITECTURE.md:78` and the Practice Evaluation
Concurrency section). **No code was changed in the audit pass.**

**Status 2026-08-10.** OPEN-1 is now CLOSED by branch `fix/practice-dml-schema-gate`
(ARCH-058 … ARCH-061), which also rewrote those same docs a second time — the
audit-pass corrections above described practice as deliberately unserialised,
which the fix made false. OPEN-8 is CLOSED on the same branch. OPEN-2 … OPEN-7
and OPEN-9 are still open.

## Before deploying to the server

`main` is at `8544293`. Full detail lives in `CHANGELOG.md`'s two "Before
deploying" / "Before you deploy" sections — this is the short version for
whoever is standing at the server about to pull the image.

1. **Run both audits against the real prod DB first, not just this note.**
   Both were only proven against the local docker-compose stack, which has
   zero lecturer content — that proved the feature *unused* there, not the
   change *safe* on real data.
   ```
   psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql
   psql "$DATABASE_URL" -f scripts/audit_seed_sql_qualified_names.sql
   ```
2. **Read the non-zero rows before acting.** A reported name/row is something
   that *could* be reached, not a confirmed broken question — confirm by hand.
   Full per-count meaning (save-time vs. grade-time vs. preview-time) is in
   `CHANGELOG.md`'s "Before deploying" section under the `fix/dml-public-fallback`
   entry.
3. **Back up the DB before pulling.** This changes grading behaviour on
   already-captured questions; the backup is the rollback path if the audits
   missed something on real data.
4. **Confirm the image is actually built from `main` past `8544293`** — check
   the git SHA the image was built from, not just the tag name.
5. **After deploy: work the audit's non-zero list.** There is **no automatic
   re-capture** — a question flagged by either audit keeps failing (at save
   time, grade time, or both) until a lecturer opens and re-saves it. The
   audit output is a worklist, not something the deploy resolves on its own.

Unrelated to this change but boot-fatal, worth checking while you're in prod
`.env` anyway: `ID_SALT` must be set and non-empty or the backend
`log.Fatal`s on startup.

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

Note a sandbox is **not** the fix. A sandbox clone bounds damage *scope*; it does
nothing about concurrency. What protected exam DML at audit time was the
single-worker `SubmissionQueue`, which practice never touches — so the audit
concluded the answer was queue-shaped. The shipped fix is neither: see below.

**Fixed 2026-08-09** — `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`
(ARCH-058 … ARCH-061). An in-process per-schema gate inside
`Evaluate`/`EvaluatePreview`, keyed on the schema those functions resolve as the
execution target, so the guarantee belongs to the engine rather than to whichever
controller remembered to ask for it. `TestPracticeConcurrentDML_CaseB_ProductionPath`
now asserts both concurrent submissions grade `success`.

No backfill. Any practice attempt already recorded as incorrect because its seed
deadlocked is indistinguishable in `practice_attempts` from a genuinely wrong
answer, so past practice scores cannot be repaired retroactively.

The whole-branch review then found the same hazard on the **lecturer** paths —
`captureExpectedResult` and the test-run previews replay the same seeds against
the same schema, and originally took no gate. Worse consequence than the student
case: if the lecturer loses the race, `expected_result_status='failed'` persists
and every student hits 409 `question_not_ready` on that question until someone
re-saves it. Gated in the same branch.

**Residual, not closed** — all recorded in the design's *Known limitations*, not
repeated here: a long lecturer import now sheds waiters with a 503 that
misattributes the cause; the gate is process-local, so a second backend instance
would not see it.

The cross-schema seed residual has since **split in two**, and the halves are
not equally settled:

- *Schema-qualified targets: CLOSED 2026-08-10* on branch
  `fix/seed-schema-qualification` (`ee330ed`, hardened by `e28a249`, `21d4b69`,
  `ea3b6a0`, `867731e`). `validateSeedSQL` refuses a seed that names another
  schema, on save and again at grade time, reusing the existing
  `schemaQualifiedNameRe` denylist plus a literal-blanking scanner. The earlier
  wording above — "`validateSeedSQL` permits schema-qualified INSERT targets" —
  is no longer true and the estimate that closing it needed a table-set-keyed
  gate was wrong; an existing validator covered it. Three bounds remain, of
  which only one is a real hole: `INSERT INTO "public"."employee"`
  (double-quoted schema name) is still accepted and would execute. The other
  two — a non-ASCII dollar-quote tag, and `E'…\'…'` backslash escapes — degrade
  to a lecturer-visible error rather than a silent write.
- *Unqualified targets: CLOSED 2026-08-10* on branch `fix/dml-public-fallback`
  (`9113c24`, tests hardened by `df57eef`, `2fd72cf`; the audit script itself
  hardened by three further rounds — `33cd4ad` (exam grading/capture schema
  coverage), `71e154a` (preview decoys, `pg_proc`/`pg_type`), `8544293` (bounded
  claim corrections) — merged to `main` as `8544293`). `, public` is dropped
  from the DML `search_path` at both evaluation sites
  (`postgres_evaluation.go:308` `evaluateDML`, `:604` `evaluateDMLPreview`)
  and both capture sites (`postgres_questions.go:129`,
  `postgres_practice.go:499`), moved together in one commit — exactly the
  "in one move" bound this bullet used to describe, so a question can no
  longer capture green against `public` and then fail at grade time. The
  submit-vs-preview gap this bullet used to flag closes as a side effect:
  `practice_cat_N` and `practice_cat_N_preview` can no longer reach a shared
  `public` table through `search_path` at all, so there is nothing left for
  the two to disagree about.

  **Audit before trusting this on a database with real lecturer content.**
  `scripts/audit_public_fallback_questions.sql` (read-only) lists, per
  *execution schema*, every **name** in `public` that is absent from that
  schema — the names the fallback could still have resolved. It is an
  over-approximation of NAMES, **not a per-question verdict**: a reported name
  may be referenced by no question, and the report never names a broken
  question. `target_table` cases are deliberately excluded, because
  `ResolveTargetTable` already refuses them at save time regardless of
  `search_path` (`audit_dml_vacuous_questions.sql` sections 2a/2b covers
  those). It audits capture, grading and preview separately, because they run
  in three different schemas: exam grading runs in `sandbox_exam_cat_<cat>` /
  `sandbox_exam_<exam>`, never in `curriculum_schema`; DML preview of a
  `dual_schema` question runs in a *decoy* schema that is neither
  (`practice_categories.preview_schema_name`,
  `sandbox_exam_cat_<cat>_preview`, `sandbox_exam_<exam>_preview`). Only the
  two **exam** decoys are built by the same tables-only clone path; the
  **practice** decoy is not cloned at all — `ImportPreviewSchemaIntoCategory`
  (`postgres_preview_schema.go:282-296`) runs the lecturer's SQL directly
  into it under `SET LOCAL search_path`, so it can carry its own views and
  functions. Either way a `public` view or function can break every
  student's preview while grading still works. Legacy exams on the default
  `curriculum_schema='public'` are audited, not excluded, and a schema that
  does not exist yet is reported in its own "unknown, not zero" section rather
  than silently counted either way.

  **The empty-report claim is bounded and the script header states the
  bound.** Covered object classes: relations (`pg_class` relkinds `r p v m f
  S`), routines (`pg_proc`, compared by name not signature — `search_path`
  resolves function names exactly as it resolves relation names, and
  `ImportSchemaIntoCategory` has no DDL allowlist, so a lecturer curriculum
  really can leave a `CREATE FUNCTION` in `public` that no sandbox ever
  clones) and types (`pg_type`: standalone composites, domains, enums, ranges,
  multiranges). NOT covered, and named as such in the header: operators,
  operator classes/families, collations, conversions, text-search
  configurations/dictionaries/parsers/templates and statistics objects —
  also `search_path`-resolved, but nothing in this
  codebase creates them; and the QUERY paths, which do not use the five DML
  `search_path` sites. An empty report therefore proves "no name of those
  classes in those schemas", not "nothing can break".

  The pre-ship run reported all
  counts zero, but against the local docker-compose stack, which has no
  practice categories and no exams — that proved the feature unused there,
  not the change safe. Re-run it wherever real lecturer content lives. **No
  automatic re-capture**: a question captured before this change keeps its
  stored capture and fails at grade time until a lecturer re-saves it —
  deliberate, since auto re-capture would run lecturer SQL unattended at
  deploy time.

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

## OPEN-8 — Tests hardcode email addresses and never clean up — **CLOSED 2026-08-10**

Severity: **low** (test-only, but it fails work that did not cause it)

`TestVerifyForgotPassword_HTTP` (`internal/controllers/forgot_password_http_test.go`)
creates a user with the hardcoded address `verify@example.com` and deletes
nothing afterwards. On the shared `TEST_DATABASE_URL` the row survives the run,
so the next invocation collides on the unique email and the test fails.

`TestParity_SingleVsBulk` does the same with `parity@example.com`, so this is a
pattern rather than one careless fixture. Both were hit during the same
afternoon's work; assume others exist and `grep` for hardcoded `@example.com`
addresses before trusting a suite run.

Found 2026-08-09 during the ARCH-058 work, where it failed a package run twice
and had to be reproduced with the branch's changes stashed before it could be
attributed. That is the real cost: a test that fails for reasons unrelated to
the change under test makes every green run an argument rather than a fact.

Two fixes worth considering, neither done here: give the fixture a unique
address per run the way `mkCtrlUser` already does, or delete the row in a
`t.Cleanup`. Same family as OPEN-7 — suite-wide state that no single test owns.

**Fixed 2026-08-10.** The first option, because the codebase had already chosen
it: `mkUserWithEmail` in the services package appends a `+<UnixNano>` tag to the
local part for exactly this reason. Its controllers-package twin
`mkCtrlUserWithEmail` had been written without that block, and
`TestParity_SingleVsBulk` bypasses both helpers (it builds a
`StudentUpsertInput` directly), so it now derives the address from the
already-unique `sn`. Both paths still hand ONE address to the single and bulk
halves, which is what the parity assertion needs.

The "assume others exist" warning above was checked, not left standing: every
`@example.com` in a `_test.go` was reviewed, and the remaining ones are either
non-DB (SMTP header fixtures, parser input) or already routed through the
uniquifying helper. Verified by reverting the fix and reproducing both failures
on demand (`SQLSTATE 23505` / `email already in use`), then running the full
suite twice in a row against the same dirty database — clean both times. That
second run is the point: a green suite is now a fact rather than an argument.

## OPEN-9 — The exam 503 arms are unreachable and untested

Severity: **low** (dead defensive code, but the reasoning behind it was wrong once)

`quiz_controller.go` maps `repository.ErrSchemaBusy` to 503 in both
`SubmitAnswer` and `PreviewAnswer`. Neither arm can execute today: exam DML
resolves `sandbox_exam_N` / `sandbox_exam_cat_N_preview` and practice resolves
`practice_cat_N*`, so the two can never share a gate key, and exam DML reaches
the engine one job at a time anyway.

They ship with **no test**. `QuizController` takes the concrete
`*services.ValidationService` with no interface seam, so testing them means
adding an exam-side equivalent of ARCH-061's `practiceValidator` — refactoring
the exam path in order to test code that cannot run. That trade was declined.

Recorded because the design originally justified these arms with a contention
scenario that does not exist (it claimed practice and exam preview could share
`question.CategoryPreviewSchema`; that resolution sits in the QUERY branch, and
QUERY is never gated). The arms are right — a 500 or a 504 would be the wrong
answer — but the premise was wrong, and a future reader should not rebuild on
it. If exam and practice schemas are ever unified, add the seam and the test
together.

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
- Practice still persists the attempt *before* checking the eval error, writing
  `is_correct=false` / `validation_details=NULL` on infrastructure failures.
  Exam checks the error first. **Partly addressed 2026-08-10:** the schema-gate
  work added an `ErrSchemaBusy` → 503 check *above* the persistence, so a
  gate-shed submission records nothing — but that is one sentinel, not a general
  rule. Every other infrastructure failure still writes a bogus attempt row, and
  there is still no 504 mapping.
- `scripts/audit_seed_sql_qualified_names.sql` (added 2026-08-10, alongside the
  existing `audit_*.sql` family) is the pre-deploy report for the
  schema-qualified seed refusal: read-only, lists every persisted
  `app.practice_questions.seed_sql` / `app.questions.seed_sql` that names a
  schema. **Run it against any database that actually holds lecturer seeds
  before deploying that refusal.** The database audited on 2026-08-10 returned
  `practice_questions_affected=0` and `exam_questions_affected=0`, but also
  `practice_seeds_total=0` / `exam_seeds_total=0` — the local docker-compose
  stack has no seeds at all, so the audit proved the feature unused there, not
  the change safe. The refusal fires on save *and* at grade time, so a seed that
  saved months ago can start failing.
- That script's one known false positive is deliberate and documented in its own
  header: it scans raw `seed_sql`, whereas `validateSeedSQL` blanks string
  literals before matching, so a schema name appearing only inside a quoted
  value is listed but will not actually refuse. Read each row before acting.
- **No detective control at activation.** A persisted schema-qualified seed
  keeps `expected_result_status='captured'`, so the exam-activation pre-flight
  (`postgres_exams.go:59`, the `ErrCaptureNotReady` return inside
  `SetExaminationActive`) passes, the exam arms, and every student on that
  question silently receives the configuration-error result until a human
  notices. The audit script is the only control and an operator has to remember
  to run it. Cheap follow-up, not done: call `ValidateSeedSQL` over each
  question's stored seed in that same pre-flight, turning a per-student grade
  failure into one lecturer-facing activation error while it is still fixable.

### Running the backend tests

Backend tests are DB-global — use `go test ./... -p 1`.
