# ARCHITECTURE.md - SQL Quiz System

This document is the primary technical reference and architecture specification. The companion source-of-truth docs are `PROJECT.md` (requirements), `API_SPEC.md` (HTTP contract), `DEVELOPMENT.md` (local setup), and `MANUAL.md` (user workflows).

## Project Overview
A PostgreSQL examination system for undergraduate computer science students (90–150 concurrent users). The system evaluates Query (`SELECT`) and DML (`INSERT` / `UPDATE` / `DELETE`) submissions against a cached "Correct Result" captured once per question.

## Technical Stack
- **Frontend:** Vue 3, Vite
- **Backend:** Go 1.26 (Gin framework), `pgx/v5` + `pgxpool`
- **Database:** PostgreSQL 15+
- **Infrastructure:** Docker, Nginx (Reverse Proxy)
- **Package Manager:** Bun
- **API Testing:** Bruno

## Codebase Map
62 non-test Go files (plus 85 `_test.go`) and a Vue SPA with 44 Vitest files. Layering is strict: `controllers → services → repository`, with `models` shared by all three and no upward imports.

### Backend (`backend/`)
```
cmd/main.go                 boot order + the whole route table (see below)
internal/publicid/          Sqids codec; opaque ids derived from ID_SALT
internal/models/            wire + DB structs, no behaviour
internal/repository/        every SQL statement in the system
internal/services/          grading, auth, email, queue, SSE, import/export
internal/controllers/       Gin handlers, HTTP status mapping, auth/role middleware
internal/middleware/        exam & practice freeze gates
internal/testsupport/       shared DB fixture for integration tests
loadtest/                   standalone drivers: seed, driver, gradeseed, gradedriver
```

**`internal/repository/`** — split by concern rather than by table:

| File | Responsibility |
| --- | --- |
| `postgres.go` | pool construction, `ExaminationEffectiveState` |
| `postgres_core.go` | generic query/exec/transaction helpers, row collection |
| `postgres_schema.go` | `InitializeSystemTables` (all idempotent DDL + migrations), `CreateRestrictedUser`, schema-name helpers, curriculum/category import, sandbox clone |
| `postgres_student_pool.go` | the four restricted LOGIN roles and their pools (`poolForRole`) |
| `postgres_evaluation.go` | `Evaluate` / `EvaluatePreview` and the four query/DML × graded/preview paths |
| `dml_autoseed.go` | seed-plan derivation, the SQL scanner it needs, `ValidateSeedSQL`, `applyQuestionSeed` |
| `postgres_questions.go` / `postgres_practice.go` | question CRUD **and** Phase-0 Correct Result capture |
| `postgres_exams.go` | exam lifecycle, activation/archival, roster |
| `postgres_exam_categories.go` | exam categories + their schemas, sandboxes and decoys |
| `postgres_preview_schema.go` | all decoy-schema provisioning and grants |
| `postgres_rebuild_lock.go` | shared/exclusive per-schema advisory locks |
| `postgres_target_table.go` | `pg_class` resolution of the lecturer's `target_table` |
| `postgres_sql_normalize.go` | smart-quote repair |
| `postgres_student_exec.go` | student statement execution + error-message shaping |
| `postgres_dml_table_check.go` | authoring-time warning when `correct_sql` and `target_table` disagree |
| `postgres_users.go`, `postgres_password_bulk.go` | identity, password events, OTP state, bulk reset |
| `postgres_results.go`, `postgres_export.go`, `postgres_images.go`, `postgres_state.go` | scores, export windows, intro images, freeze state |

**`internal/services/`** — `validation_service.go` (status decision + QUERY comparison), `dml_compare.go` (multiset diff + diff formatting), `submission_queue.go` (single-worker FIFO), `auth_service.go`, `password_crypto.go` / `password_gen.go`, `email.go` / `email_smtp.go`, `sse_hub.go`, `roster_parser.go` / `student_import_service.go`, `submission_export.go`.

### Frontend (`frontend/src/`)
```
components/            views + shared primitives (FieldError, EmptyState, Skeleton,
                       Spinner, ConfirmDialogHost, BackLink, StudentMultiSelect,
                       SqlEditor, ResultTable, ClockSidebar, IntroImagesEditor,
                       SchemaIntro*/SchemaImageWindow, Forbidden/NotFound views)
components/lecturer/   the exam authoring workspace (see below)
composables/           useFormValidation, useSingleFlight, useAutosave,
                       useKeyboardShortcut, useFocusTrap, useExamStream,
                       useAuthoringLayout
services/              auth, session, exam, practice, schemaImages,
                       confirm_service, error_handler, notification_service
utils/                 format_date, format_sql, csv, schemaWindowState
constants/             ui_copy.ts
__tests__/             Vitest + happy-dom, mirroring the tree above
```

### Boot sequence (`cmd/main.go`)
Order is load-bearing; each step fails fast rather than degrading.
1. `DATABASE_URL` required.
2. `publicid.Init(ID_SALT)` — `log.Fatal` on empty/invalid salt (never fall back to the predictable alphabet).
3. Repository + pool.
4. `NewSubmissionQueue(256, 30s)`. A drain with a 35 s budget is registered as a `defer` in `main()`, but **it does not run in practice**: `main.go` installs no signal handler and ends in a blocking `r.Run(...)`, so on SIGTERM/SIGINT (any `docker stop` or redeploy) the process exits without running deferred functions. Treat the queue as non-durable — up to 256 buffered submissions plus the in-flight one are lost on restart, and since `app.submissions` is written only *after* evaluation returns, a dropped job leaves no record at all.
5. `CreateRestrictedUser` — the four sandbox roles.
6. `InitStudentPools` — the four LOGIN roles + pools. Must follow (5); `log.Fatal` on failure, so there is no path where student SQL falls back to the privileged connection.
7. `InitializeSystemTables` — idempotent DDL, column adds, and the Go-side migrations (`migrateUniqueEmailIndex`, `migrateQuestionsIdentity`, `migratePracticeCategories`, `migrateExamCategories`, `migrateDMLKind`, orphan `student_*` schema cleanup).
8. SSE hub, validation/import services, `JWT_SECRET` (≥ 32 chars, else fatal).
9. Email: real SMTP sender iff `EMAIL_SENDER_ADDRESS` is set — otherwise the no-op stub, and **the submission-log exporter is not started and `/export/*` answers 503**.
10. Admin seed (`ADMIN_USER`), idempotent; the generated password is emailed and printed once.
11. Controllers, CORS, `/health`, then the `/api/v1` route tree.

Two runtime gates worth knowing: `PASSWORD_RESET_EMAIL_ENABLED` (default **off**) decides whether the public `/forgot-password` pair is mounted at all — when off the gate answers a constant 404 pointing at the administrator, and the admin reset path is unaffected; and `ALLOWED_ORIGINS`, whose mere presence widens CORS to `*`.

## Schema Layout
The database is split across several logical schemas, each with a distinct role and lifetime:

| Schema                                | Purpose                                                                                          |
| ------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `app`                                 | System tables (see below). Always present.                                                       |
| `public`                              | Curriculum tables imported by the lecturer. Treated as a clean template; **frozen** for the duration of any active examination. SELECT (Query) submissions read directly from here under the `exam_student_readonly` role. |
| `sandbox_exam_{examID}`               | Shared per-exam sandbox cloned from `public.*` once at activation. Used by **DML** submissions only; pure-SELECT exams skip cloning entirely. All students of the same exam read and write the same physical tables here, serialised by the global in-process FIFO submission queue (single worker, capacity 256, 30 s per-job timeout); each submission's writes are reverted by `ROLLBACK TO SAVEPOINT`. Dropped on deactivation. |
| `curriculum_exam_{examID}`            | Per-exam curriculum schema (optional). A lecturer imports a database into it via `POST /api/v1/lecturer/exams/:id/curriculum/import`; the exam's Phase-0 Correct Result capture, Phase-2 SELECT read-path, and Phase-1 sandbox clone all source from here. Falls back to the shared `public` schema when `app.examinations.curriculum_schema = 'public'` (the default), preserving the original single-template behaviour. Dropped when the examination is hard-deleted. See `practice_cat_{categoryID}` for the precedent pattern. |
| `exam_cat_{categoryID}` / `sandbox_exam_cat_{categoryID}` | Per-exam-category database (exam categories, mirroring `practice_cat_{categoryID}` below). Each `app.exam_categories` row owns a reference schema `exam_cat_{categoryID}` — imported via `POST /api/v1/lecturer/exams/:id/categories/:categoryId/import`, granted read-only to `exam_student_readonly` exactly like `curriculum_exam_{examID}` — plus a lazily-cloned DML sandbox `sandbox_exam_cat_{categoryID}`, exactly like `sandbox_exam_{examID}`. A question resolves its grading schema from its own category, falling back to the exam's `curriculum_exam_{examID}` (or `public`) when uncategorised. A category auto-adopted from a pre-existing exam by the boot migration instead shares the exam's own schema (often literally `public`) until a lecturer imports a database directly into it, at which point it flips to owning `exam_cat_{categoryID}`. Both names (plus their `_preview` decoy pair) are dropped on category delete; untouched by exam archive. |
| `practice_cat_{categoryID}`           | Per-practice-category database. Each `app.practice_categories` row owns one schema into which a lecturer imports a `.sql` (POST `/api/v1/lecturer/practice/categories/:id/import`). Practice questions belong to a category (FK `practice_questions.category_id`) and both their SELECT and DML submissions run against the category's own schema, isolated from `public`. DML uses savepoint-rollback; SELECT reads the category schema under `exam_student_readonly`. This is what keeps practice databases separate from the exam curriculum. |
| `*_preview` (`practice_cat_{id}_preview`, `curriculum_exam_{id}_preview`, `sandbox_exam_{id}_preview`, `exam_cat_{id}_preview`, `sandbox_exam_cat_{id}_preview`) | **Decoy schemas for dual-schema grading** (ARCH-001/002/020). Optional, lecturer-imported alongside the real database: same structure, different data. A question with `dual_schema = true` runs the *student preview* here and is *graded* on the real schema. Provisioned only by `postgres_preview_schema.go` / `postgres_exam_categories.go`, and granted **only** to the two preview-only roles (`exam_student_preview`, `exam_student_preview_readonly`) — `exam_student` / `exam_student_readonly` are explicitly REVOKEd on every call, so a schema-qualified read of the real grading schema from a preview fails at the engine. Never captured, never graded. |
| `sandbox_practice`                    | **Removed.** Former persistent shared practice sandbox cloned from `public.*`. No repository function creates, resets or resolves it any more (`ResetPracticeSandbox` is gone); only test fixtures still name the string. Superseded by per-category `practice_cat_{id}` schemas. |

System tables in `app`:

| Table                     | Purpose                                                                                                                |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `app.system_users`        | Identity table for admins, lecturers, students. Single table with a `role` discriminator. Carries optional `email` and a `token_version` counter bumped on every password change to evict stale JWTs. |
| `app.examinations`        | Examination definitions. Lifecycle flags: `is_active`, `activated_at`, `archived_at`; scheduling window `scheduled_start_at` / `scheduled_end_at`; `curriculum_schema`, `introduction`, `block_practice_during_exam`. |
| `app.questions`           | Question definitions **plus the cached Correct Result** (`expected_result`, `expected_columns`, capture status). Carries a nullable `category_id` FK to `app.exam_categories` (`ON DELETE SET NULL`). |
| `app.exam_categories`     | Organisational unit for exam questions, mirroring `app.practice_categories`. Each row owns a schema (see `exam_cat_{categoryID}` above) and an optional decoy for dual-schema grading; a question with no `category_id` falls back to the exam's own curriculum schema. `name` is UNIQUE per exam, not globally; `schema_name` has **no** unique constraint — an idempotent boot migration gives every pre-existing exam a `General` category whose `schema_name` is that exam's own `curriculum_schema`, so several `General` rows legitimately share `'public'` without creating, copying, renaming or dropping a single PostgreSQL schema. |
| `app.exam_students`       | Junction table for student enrollment (records `assigned_by`, `assigned_at`).                                          |
| `app.submissions`         | Per-attempt durable record of every student submission and its score.                                                  |
| `app.exam_sandboxes`      | Tracks which `sandbox_exam_{examID}` schemas exist (one row per exam), with `created_at` / `dropped_at` for cleanup bookkeeping. |
| `app.examination_intro_images` | PNG/JPEG intro images attached to an exam's introduction (bytes + `mime_type`, `sort_order`, `byte_size`).         |
| `app.password_events`     | Audit trail of password mutations (`self_change`, `create`, `admin_reset`, `forgot_reset`), optionally storing the encrypted password when `PASSWORD_LOG_KEY` is set. |
| `app.schema_import_audit` | One row per schema/curriculum import (global, per-exam, or per-category) recording the importing lecturer.              |
| `app.practice_questions`  | Practice question bank (separate from `app.questions`), each with a NOT-NULL `category_id` FK and its own cached Correct Result. |
| `app.practice_categories` | Practice categories; each owns a `practice_cat_{id}` schema. Includes a protected `Uncategorized` fallback.            |
| `app.practice_attempts`   | Per-attempt history for practice submissions (practice questions allow unlimited retries).                             |
| `app.practice_category_intro_images` | Intro images for a practice category (same shape as the exam variant).                                       |
| `app.export_state`        | Singleton (`CHECK (id = 1)`) driving the submission-log emailer: `enabled` plus the `last_export_at` watermark that bounds the next export window. |

`app.practice_sandbox_state` is **not** created by `InitializeSystemTables` and no code reads it — it belonged to the retired shared `sandbox_practice` model.

The full ER definition lives in `docs/database_schema.mmd`. Render with `mmdc -i database_schema.mmd -o database_schema.png`.

![Database Schema](docs/database_schema.png)

## Phase Lifecycle
Examination evaluation is split into four phases. The optimizations (cached Correct Result, lazy sandbox, no per-submission teacher SQL) all rely on these phase boundaries holding.

### Effective state & scheduling (no background scheduler)
Gating keys off a computed **effective state**, not the raw `is_active` boolean. An exam carries optional `scheduled_start_at` / `scheduled_end_at` (RFC3339 UTC), and the single helper `ExaminationEffectiveState(exam)` derives one of four phases over the half-open window `[start, end)`:

| `effective_state` | Meaning |
| ----------------- | ------- |
| `inactive` | Never activated (`is_active = false`, no window). |
| `scheduled` | Activated, but `now < scheduled_start_at`. Students see a **waiting page**. |
| `active` | Within `[start, end)` — or an instant activation with no `start`. Submissions accepted. |
| `ended` | `now >= scheduled_end_at`. Submissions rejected; results still readable. |

There is **no cron/background scheduler**: the state is computed on read, and the DML sandbox is prepared **lazily** the first time an enrolled student pulls the questions of an exam that has become effectively `active` (`GetExamQuestions` → `EnsureExamSandboxReady`). `ActivateExamination` accepts `mode: "instant"` (start = `now()`) or `mode: "scheduled"` (explicit `scheduled_start_at`, optional end); `DeactivateExamination` can likewise set a future `scheduled_end_at` (keeping the sandbox) or archive immediately.

### Live exam-start (SSE)
`GET /api/v1/quiz/exams/:id/stream` is a Server-Sent Events endpoint (JWT via `?access_token=` since `EventSource` can't set headers). An in-process `Hub` (`sse_hub.go`) pushes `exam-status` frames — `{exam_id, effective_state, server_time}` — on connect and whenever an exam's state is broadcast (activation, or the lazy `active` transition in `GetExamQuestions`). A `: ping` keep-alive is emitted every 20 s; Hub sends are non-blocking (a full 4-slot per-subscriber buffer drops that frame). The frontend `useExamStream.ts` composable consumes it and moves a student from the waiting page to the live exam the instant it flips, with a ~15 s poll of `GET /quiz/exams` as fallback if the stream drops. WebSocket and an Nginx-LB approach were both rejected in favour of this single-process Hub.

### Phase 0 — Lecturer Prep (`is_active = false`, submissions rejected)
1. **Curriculum Import** — `ImportSchema` populates `public.*`. Allowed only when no examination is currently active (otherwise 409).
2. **Examination Definition** — A row is created in `app.examinations` (inactive by default).
3. **Question Definition** — Each question is upserted into `app.questions` (teacher SQL, type, target table, strict-mode flags).
4. **Correct Result Capture** — On every successful `UpsertQuestion`, the system runs the teacher's SQL inside a transaction on a `public` clone, captures the expected output, and rolls back. Captured results live on `app.questions`:
   - **SELECT (`Query`)**: `expected_result` = ordered JSON array of row objects; `expected_columns` = ordered array of column names.
   - **DML (`DML`)**: `expected_result` = post-image of `target_table` after teacher SQL; `expected_columns` = target-table column order.
   - **On success**: `expected_result_status = 'captured'`, `expected_result_captured_at = now()`, `capture_error = NULL`.
   - **On failure** (parse / runtime / unknown target_table / oversized result): `expected_result_status = 'failed'`, `capture_error` populated with the PG error or limit message. The question row is still saved so the lecturer can fix and retry.
5. **Student Assignment** — Students added to `app.exam_students`. Allowed only when `is_active = false`.

### Phase 1 — Activation (transition event)
1. **Pre-flight** — `ActivateExamination` rejects activation when the exam has **no questions at all** (`ErrEmptyExam`) or when any question has `expected_result_status != 'captured'` (`ErrCaptureNotReady`). The 409 response body lists each failing question with its `capture_error`.
2. **Sandbox preparation (window-gated, otherwise lazy)** — `SetExaminationActive` clones only when the scheduling window is *already open* (`ExaminationEffectiveState(...).ActiveNow`). For a future-dated `scheduled` activation nothing is cloned yet; the clone then happens **lazily** on the first `GetExamQuestions` after the exam becomes effectively `active` (`EnsureExamSandboxReady`). When it does run and the exam has at least one DML question, `InitializeExamSandbox(examID)` does `CREATE SCHEMA IF NOT EXISTS sandbox_exam_{examID}`, grants `exam_student` CRUD, then for each source-schema table `CREATE TABLE … (LIKE … INCLUDING ALL); INSERT INTO … SELECT * FROM …`, and upserts `app.exam_sandboxes`. `EnsureExamCategorySchemasReady(examID)` does the same for every category that owns its schema (`sandbox_exam_cat_{categoryID}`). Pure-SELECT exams skip cloning entirely.
3. **Freeze** — On success, `is_active = true`, `activated_at = now()`, and — note — `block_practice_during_exam` is **forced to `TRUE` by activation**, regardless of the column's `FALSE` default. So practice is blocked for students during an exam unless a lecturer explicitly flips it back off via `PUT /lecturer/exams/:id/practice-freeze`. While active:
   - `UpsertQuestion` for any question of this exam → 409.
   - `ImportSchema` (global, targeting `public.*`) → 409 (would invalidate every active exam's cache and any active sandbox clone).
   - `ImportExamCurriculum` (`POST /lecturer/exams/:id/curriculum/import`) → 409 **for this exam only** — the per-exam curriculum import is gated per-exam (blocked only while that specific exam is active, not globally). A lecturer may import curriculum for a different inactive exam while this one runs.
   - `AssignStudents` for this exam → 409.
   - Only `DeactivateExamination` is permitted on the exam itself.

### Phase 2 — Active Exam (submission event loop, effective state `active`)
Phase 2 has two paths, chosen by `question.question_type`. Both share `app.questions.expected_result` as the only source of truth — neither re-executes teacher SQL.

#### SELECT (Query) path — lock-free, no clone
1. Acquire a pool connection.
2. `BEGIN`; `SET LOCAL search_path = public`; `SET LOCAL ROLE exam_student_readonly`.
3. Execute the student's SQL; collect the result-set.
4. `ROLLBACK` (the role can't write, so commit/rollback is moot — rolling back guarantees no session state leaks).
5. Compare against `expected_result` using the question's strict-mode flags.
6. `INSERT INTO app.submissions …` (separate statement on the same connection).
7. Release the connection.

The `exam_student_readonly` role has only `USAGE` on `public` and `SELECT` on its tables. This is the safety boundary: data-modifying CTEs (`WITH … AS (UPDATE … RETURNING *) SELECT * FROM x`) fail on the underlying write, not on AST inspection. No lock is needed because reads can't contend.

#### DML path — shared sandbox + global FIFO submission queue
1. Submit the evaluation closure to the global FIFO submission queue (single worker, channel depth 256, 30 s per-job timeout). The queue admits one DML evaluation closure at a time.
2. (On a pool connection acquired inside the evaluation closure) `BEGIN`; `SAVEPOINT student_eval`; `SET LOCAL search_path = sandbox_exam_{examID}, public`; `SET LOCAL ROLE exam_student`.
3. Execute the student's SQL.
4. `SELECT * FROM target_table` (still inside the savepoint) and serialise to canonical JSON.
5. `ROLLBACK TO SAVEPOINT student_eval` — sandbox state is restored for the next job in the queue.
6. `COMMIT` and release the eval connection.
7. Compare the captured post-state against `app.questions.expected_result` using the question's strict-mode flags.
8. `INSERT INTO app.submissions …` (separate transaction; not under the queue, since it only writes to `app.*`).

The savepoint+rollback pattern guarantees the shared sandbox returns to its activation-time state between submissions; no `ResetTable` call or per-student clone is required.

### Phase 3 — Deactivation / Archive (transition event)
1. `DeactivateExamination` flips `is_active = false`, sets `archived_at = now()`. New submissions are rejected.
2. Look up `app.exam_sandboxes WHERE exam_id = ? AND dropped_at IS NULL`. If a row exists:
   - `DROP SCHEMA sandbox_exam_{examID} CASCADE`.
   - `UPDATE app.exam_sandboxes SET dropped_at = now()`.
3. Pure-SELECT exams have no `app.exam_sandboxes` row and skip the drop entirely.
4. `app.submissions` rows are retained for read-only access by lecturers.

## Core Requirements & Logic

### 1. Query Validation Logic
Phase 2 compares the student's result-set against `app.questions.expected_result` (captured in Phase 0). Layered checks:
1. **Result Set Match** — full-score path. Comparison is over canonical JSON (sorted keys; row order honored only if `strict_ordering = true`).
2. **Attribute Strictness** (lecturer-toggleable, `strict_attributes`) — column names compared against `expected_columns`.
3. **Column Order** (lecturer-toggleable, `strict_column_order`) — when on, the student's output columns must appear in the same left-to-right order as `expected_columns`, compared index-by-index. This is an **independent** pass/fail gate: it is evaluated separately from `strict_ordering` and `strict_attributes`, and does not imply either. Default `false`; existing questions are unaffected.
4. **Syntax Errors** — captured from PG, returned to the student verbatim, score = 0.

*Row-order strictness is controlled by the `strict_ordering` flag — when off, comparison is set-equality (multiset of rows); when on, the student's rows must appear in the same order as the teacher's.*

**`expected_columns` capture order.** To make the Column Order check meaningful, `expected_columns` is populated from the PostgreSQL field-descriptor array returned by the driver (true SQL left-to-right column order), not from Go map iteration. The student's output column order is captured the same way in the evaluation path and compared index-by-index against `expected_columns`.

**DB columns.** Both `app.questions` and `app.practice_questions` carry `strict_column_order BOOLEAN NOT NULL DEFAULT FALSE`. For exam questions the flag is nested in the `strict_mode` JSON object on the wire (`"column_order": bool`); for practice questions it is a top-level field (`"strict_column_order": bool`).

### 2. DML Validation (Insert / Update / Delete)
Phase 2 evaluates DML inside the shared per-exam sandbox, serialised by the global in-process FIFO submission queue:
1. Submit the evaluation closure to the global FIFO submission queue (single worker, capacity 256, 30 s per-job timeout).
2. On a pool connection: `BEGIN; SAVEPOINT student_eval; SET LOCAL ROLE exam_student; SET LOCAL search_path = sandbox_exam_{examID}, public`.
3. Run student SQL.
4. `SELECT * FROM target_table` to capture post-state.
5. Compare the two post-images as a **multiset of rows under key-aware equality** (`decideDMLStatus` → `diffRowMultiset`) against `app.questions.expected_result`. DML forces all three strictness dimensions regardless of the stored flags: ordering is never graded, the `column_order` gate never applies, and attribute comparison is always key-aware.
6. `ROLLBACK TO SAVEPOINT student_eval; RESET ROLE; COMMIT` — sandbox state is restored for the next job in the queue.

Feedback on a wrong DML answer is the row-level `missing` / `extra` diff (`formatDMLDiff`, sample rows truncated) — **except** for a `dual_schema` question, where the teacher post-image lives on the hidden grading schema and the diff would leak it; those get a generic "does not match" message (ARCH-007).

**Vacuous DML is blocked at capture, not at grading.** A model answer that affects 0 rows cannot distinguish a correct answer from a statement that does nothing, so Phase 0 refuses it rather than letting Phase 2 mark every no-op correct. See *DML seeding* below for the two rescues.

The legacy "Backup → Teacher → Diff → Restore" loop and the `ResetTable(schema, target)` workaround are **removed**: the savepoint+rollback inside the global FIFO queue is the only mechanism that resets state between submissions.

### 3. DML seeding — lecturer `seed_sql` and derived `auto_seed`
An `UPDATE`/`DELETE` model answer only affects rows if a matching row exists in the reference database. Two mechanisms supply one; they are **mutually exclusive** (ARCH-045: a non-empty `seed_sql` wins and `auto_seed` is left NULL).

- **Lecturer-authored `seed_sql`** (ARCH-043/045/046) — an **INSERT-only** block the lecturer writes alongside the answer. `ValidateSeedSQL` rejects anything whose leading keyword is not `INSERT`, rejects unbalanced quotes, and rejects a seed that references another schema by name — the `schemaQualifiedNameRe` denylist (`public`, `app`, the PostgreSQL system schemas, and the `practice_cat_*` / `curriculum_exam_*` / `exam_cat_*` / `sandbox_*` families with their `_preview` variants), scanned after string literals are blanked so a schema name inside a `VALUES` literal stays data. Replay holds DML on every real schema, so a qualified write takes locks the ARCH-059 schema gate cannot see; the double-quoted form `"public"."employee"` is a known, documented gap and is **not** refused. Comments are stripped with the same scanner the derivation uses. It runs **before** the model answer at capture time (inside a nested `SAVEPOINT lecturer_seed`, so a failed seed records a capture error instead of poisoning the capture transaction) and again before the student's answer at grading and preview time — one state observed by all three.
- **Derived `auto_seed`** (ARCH-037/038/039/040) — only on the 0-row branch, and only when no `seed_sql` was given. `deriveAutoSeed` parses the model answer's `WHERE` clause into a set of top-level `col = literal` equality terms, reconciles each against the target table's real columns, and refuses (wrapping `ErrAutoSeedUnsupported`) on anything it cannot represent exactly — parenthesised or non-equality predicates, `OR`, a `NOT NULL` column with no default left unpinned, and so on. The result is an `AutoSeedPlan` (`{"table", "columns":[{"name","value"}]}`) whose values are **always Go strings**, bound as text parameters so large integers and `NUMERIC` survive the JSON round-trip; Postgres coerces them via the INSERT assignment cast. The plan is persisted so grading replays exactly what capture validated. A 0-row `INSERT` is never rescued (`isAutoSeedIneligibleInsert`) — it is an `INSERT … SELECT` over an empty source, which seeding cannot fix.

Every seed replay at grading time runs **after** `SET LOCAL ROLE exam_student` — the LOGIN roles are `NOINHERIT`, so before the switch the connection holds nothing and the INSERT would fail. A replay failure (the category database was reset or replaced since capture) is surfaced as a graded `status = "error"` with `AutoSeedFailureFeedback`, never as an HTTP 500.

**`dml_kind`** (`INSERT` / `UPDATE` / `DELETE`, NULL for QUERY) drives the authoring dropdown without widening the `question_type` CHECK — grading still branches on `Type` alone. It must agree with the leading keyword of `correct_sql` whenever that keyword is derivable (ARCH-057); `migrateDMLKind` backfills it in Go (a Postgres regex cannot see past a leading `--` or `/* */` comment) and leaves it NULL rather than guessing when the statement leads with none of the three.

**Both `auto_seed` and `seed_sql` are `json:"-"` on the model itself** (ARCH-052/053) — they can be neither bound from a request body nor marshalled into any response. The single deliberate exception is `models.LecturerQuestionView` / `LecturerPracticeQuestionView`, which re-tag them for lecturer authoring reads. Nothing student-facing may use those types.

### 4. Column-name case folding (ARCH-043/044)
Grading compares column names **case-insensitively** (`foldColumnName` = `strings.ToLower`): `SELECT ename` and `SELECT ENAME` grade alike, and the `column_order` gate compares element-wise under the same folding (`equalStringSlicesFold`) — order is graded, casing is not.

When a single row's *own* column names collide under folding (`SELECT a AS "x", b AS "X"`, or a target table created with quoted `"Total"`/`"total"`), there is no non-arbitrary winner and Go's map iteration is randomised, so that row pair falls back to exact-key comparison (`rowsEqualExactKeys`) instead. Failing closed there was a real regression: the collision fires on the *teacher's* captured row too, which zeroed every student on such a question — including one submitting SQL byte-identical to the model answer.

### 5. Dual-schema (decoy) preview
A question with `dual_schema = true` runs the student's **preview** against a decoy schema (same structure, different data) while **grading stays on the real schema**. The isolation is enforced by roles, not by SQL inspection: decoys are granted only to `exam_student_preview` / `exam_student_preview_readonly`, and `grantPreviewSchemaPermissions` REVOKEs `exam_student` / `exam_student_readonly` on every call — which also self-heals decoys created before ARCH-020. A schema-qualified read of the real grading schema from inside a preview therefore fails at the engine. Two preview roles rather than one, so a QUERY preview still cannot run a data-modifying CTE against the shared decoy. Decoy imports report a `parity_warning` when the decoy's structure diverges from the real schema (`compareSchemaStructures`), but still succeed.

### 6. Statement preparation
Two normalisations run before any student or lecturer SQL executes, on both the graded and preview paths:
- **Smart quotes** (`normalizeSmartQuotes`) — typographic quotes from a pasted document are rewritten to ASCII, so a paste fails on its actual mistake rather than on a spurious `column … does not exist`.
- **`target_table` resolution** (`ResolveTargetTable`) — the lecturer's raw table name is resolved against `pg_class` **at save time**, so a casing mismatch surfaces in the authoring UI instead of silently saving green and failing at grade time.

### 7. Schema-rebuild safety
Every evaluation path (`evaluateQuery`, `evaluateDML`, `evaluateDMLPreview`) takes `pg_advisory_xact_lock_shared` keyed on the schema it runs against (`schemaRebuildLockKey`, `postgres_rebuild_lock.go`) for the life of its transaction. Category import-with-replace and category delete take the same key **exclusively** before dropping a schema. Shared holders never block each other, so two categories — or an exam sandbox and a category — never contend; the only thing serialised is a rebuild against in-flight evaluations.

---

## Identity, Auth & Password Management
- **JWT with token versioning.** Login returns a 24 h JWT carrying `user_id, username, role, token_version`. `AuthMiddleware` rejects any token whose `token_version` is behind the user's current counter, so a password change/reset **evicts every previously-issued session**. Login is rate-limited in-memory (5 attempts / 10 min per username, cleared on success).
- **Account creation.** Staff accounts (`POST /users`, LECTURER/ADMIN) get a **server-generated** password shown once; STUDENT accounts are created via `POST /students` or the roster import (`username`/initial `password` = student number). Role hierarchy is enforced (lecturers can't mint admins).
- **Self-service change** (`POST /user/password`) returns a fresh JWT so the current device stays logged in while others are evicted.
- **Forgot-password (two-step, enumeration-safe).** `POST /forgot-password` always returns the same generic 200 (uniform timing; SMTP send deferred to a goroutine) and emails a 6-digit OTP (10-min TTL, hashed at rest, ≤5 verify attempts, one honoured request / 5 min). `POST /forgot-password/verify` issues a new password on success and bumps `token_version`.
- **Admin actions.** `reset-password` (regenerate + evict sessions), `resend-credential` (re-email the current password, recovered from `app.password_events` — requires `PASSWORD_LOG_KEY`, no regenerate/evict), and `password-events` (audit view; the view itself is logged).
- **Email delivery.** Gmail SMTP over STARTTLS (port 587); header values are CR/LF-sanitised against injection. All sends are best-effort and are **skipped silently when no address is on file** (only `resend-credential` surfaces a send failure, as 400).

## Architecture Q&A (Design Decisions)

### 1. Security & Sandboxing
*   **Q: How does the system prevent destructive SQL (e.g., `DROP TABLE`, `TRUNCATE`) on the DML path?**
    *   **A:** Three layers of defense. (a) `exam_student` PG role has only CRUD privileges on the exam sandbox schema; DDL is denied. (b) Phase 2 runs inside a savepoint that is unconditionally rolled back, so even if a destructive statement somehow ran, it would be reverted. (c) `search_path` is scoped to `sandbox_exam_{examID}` + `public`, so unqualified writes can't reach `app.*`.
*   **Q: How is the SELECT path kept safe given that data-modifying CTEs are legal Postgres?**
    *   **A:** The SELECT path runs under `exam_student_readonly`, which holds only `USAGE` on `public` and `SELECT` on its tables. The underlying `INSERT` / `UPDATE` / `DELETE` inside any data-modifying CTE fails on permissions; the parser doesn't need to inspect the SQL. No clone is required because nothing can mutate.
*   **Q: Are the restricted roles created at startup?**
    *   **A:** **Yes, in two layers.** `CreateRestrictedUser` runs from `main.go` before `InitializeSystemTables` and creates the four **sandbox** (NOLOGIN) roles — `exam_student` (CRUD on sandbox schemas), `exam_student_readonly` (SELECT-only on reference schemas), and the decoy-only pair `exam_student_preview` / `exam_student_preview_readonly`. `InitStudentPools` then runs and provisions the four **LOGIN** roles below. Both are idempotent `DO`-block guarded, so re-boots are no-ops.
*   **Q: Why four separate LOGIN roles and separate pools (ARCH-022)?**
    *   **A:** Because `SET LOCAL ROLE` is not a boundary on a superuser connection. The application pool authenticates as whatever `DATABASE_URL` names — in production the Postgres superuser — and a single student statement on that connection can `RESET ROLE` back to full privilege (reading `app.system_users` password hashes and every `correct_sql`, or reaching `COPY … TO PROGRAM`). Student SQL therefore never touches the app pool. Instead there are four non-superuser LOGIN roles, each a member of **exactly one** sandbox role, each with its own `pgxpool`:

        | LOGIN role | sole membership | used by |
        | --- | --- | --- |
        | `quiz_exec_ro` | `exam_student_readonly` | graded QUERY, read-only QUERY preview |
        | `quiz_exec_rw` | `exam_student` | graded DML, DML preview |
        | `quiz_exec_prev_ro` | `exam_student_preview_readonly` | decoy QUERY preview |
        | `quiz_exec_prev_rw` | `exam_student_preview` | decoy DML preview |

        One membership each is what makes a cross-class `SET ROLE` (preview → real grading role, or readonly → read-write) fail with *permission denied to set role*. The roles are **NOINHERIT**, so an idle pooled connection carries zero privilege and a forgotten `SET LOCAL ROLE` fails loudly instead of running privileged. `poolForRole` returns a hard error for an unknown or unprovisioned role and **never** falls back to the application pool. Pool sizes: 8 / 4 / 4 / 4, worst case 40 backends against `max_connections = 100`.
*   **Q: Where does the password for those LOGIN roles come from?**
    *   **A:** Generated fresh at every boot (`crypto/rand`, hex-encoded), never logged and never written to disk. One password shared by all four is safe because the boundary is *which role you authenticate as*, not the secret — and the `db` compose service publishes no port, so 5432 is reachable only on the compose network. Hex is deliberate: `CREATE`/`ALTER ROLE` cannot take a bind parameter, and a hex alphabet contains no quote, backslash or dollar sign to escape. **Open constraint:** boot provisioning needs the app connection to hold `CREATEROLE`, which `scripts/deprivilege_app_db_user.sql` deliberately removes. Applying that script without first moving the four roles out of band would make startup `log.Fatal`.
*   **Q: Can a student shadow the DML target table with a temp table (ARCH-023)?**
    *   **A:** No. `pg_temp` is named **last** in the evaluator's `SET LOCAL search_path TO <schema>, public, pg_temp`. Postgres searches `pg_temp` before everything else *unless* it is listed explicitly, in which case it is searched in the position given — so naming it last stops a `CREATE TEMP TABLE <target_table> AS SELECT …` from shadowing the real table for the post-image capture. Covered by `TestPgTempForgery_TargetTableShadowing_Rejected`.
*   **Q: Are database ids exposed to clients?**
    *   **A:** No. `internal/publicid` (Sqids, alphabet shuffled from `ID_SALT`) encodes per-entity opaque tokens for users, exams, categories, exam categories, practice questions, images, submissions and attempts; `internal/models/ids.go` wires the codec into JSON marshal/unmarshal so handlers keep working with plain `int`. `main.go` calls `publicid.Init` first and `log.Fatal`s on an empty/invalid salt rather than silently using the predictable alphabet. **Exam question ids stay plain integers** (`Question.ID int`) — that namespace is deliberately readable.

### 2. Concurrency & Isolation
*   **Q: How are DML submissions serialised across students?**
    *   **A:** A single in-process FIFO submission queue (goroutine worker, channel depth 256, 30 s per-job timeout) admits one DML evaluation closure at a time. SELECT submissions remain lock-free.
*   **Q: Are SELECT submissions serialised?**
    *   **A:** No. Reads under `exam_student_readonly` against frozen `public.*` can't contend; there is no lock and no queue.
*   **Q: How does freeze-on-activate work?**
    *   **A:** `UpsertQuestion`, `ImportSchema`, and `AssignStudents` check `app.examinations.is_active` for the relevant exam (or, for `ImportSchema`, any exam) and return 409 if any block is active. This is what makes Phase 0's cache safe to consume in Phase 2 without re-validation, and what keeps the DML sandbox a faithful copy of `public.*` for the duration of the active window.

### 3. Sandbox Lifecycle
*   **Q: What is the DML sandbox schema name?**
    *   **A:** `sandbox_exam_{examID}`. One schema per exam, shared by every enrolled student. Tracked in `app.exam_sandboxes` (one row per exam).
*   **Q: When is the sandbox created?**
    *   **A:** By `InitializeExamSandbox(examID)`, only if the exam contains at least one DML question — and only once the exam is *effectively* active. An instant activation clones during `SetExaminationActive`; a future-dated scheduled activation clones nothing then, and the clone happens lazily on the first `GetExamQuestions` after the window opens (`EnsureExamSandboxReady`). Pure-SELECT exams never create a sandbox. The call is idempotent: re-activation only fills in tables that are missing.
*   **Q: How are tables cloned?**
    *   **A:** From the exam's resolved source schema — the category's schema when the question's category owns one, else `curriculum_exam_{examID}`, else `public`. `CREATE TABLE sandbox_exam_{examID}.x (LIKE <source>.x INCLUDING ALL); INSERT INTO sandbox_exam_{examID}.x SELECT * FROM <source>.x`. Preserves primary keys, NOT NULL, defaults, and indexes. (Foreign keys are not propagated by `LIKE` — Postgres limitation. Sandbox tables therefore have no FK constraints, which is acceptable because student DML runs inside an unconditional savepoint rollback.)
*   **Q: What keeps DML state from leaking between submissions, given that the sandbox is shared?**
    *   **A:** Two mechanisms. (1) The global FIFO submission queue admits one DML evaluation at a time. (2) Inside each evaluation, `BEGIN; SAVEPOINT student_eval; … ROLLBACK TO SAVEPOINT student_eval; COMMIT` reverts the student's writes before the next job runs. The next job sees the sandbox in its activation-time state.
*   **Q: When is the sandbox dropped?**
    *   **A:** On Phase 3 (exam deactivation). Phase 3 reads `app.exam_sandboxes WHERE exam_id = ? AND dropped_at IS NULL`, runs `DROP SCHEMA sandbox_exam_{examID} CASCADE`, and sets `dropped_at`. Pure-SELECT exams have no row to find and no schema to drop.

### 4. Data Integrity & Diffing
*   **Q: What defines a "Diff" for DML?**
    *   **A:** Row-by-row comparison of `target_table`'s post-state (captured inside the savepoint) against `app.questions.expected_result` (captured in Phase 0). Both are normalized to canonical JSON (sorted keys; row order honored only when `strict_ordering = true`).
*   **Q: How are idempotent queries handled?**
    *   **A:** Zero score (per requirement).
*   **Q: How big can `expected_result` get?**
    *   **A:** Bounded by the curriculum's largest table or a configured row cap. Stored as `jsonb`. Phase 0 capture refuses to commit if the row count exceeds a configurable limit (default: 10,000) and writes `capture_error = "result-set too large"` so the issue surfaces at Phase 0 rather than Phase 2.

### 5. Implementation Details
*   **Database Structure:** Lecturers import a custom SQL schema (e.g., Elmasri Company) per exam from the exam's authoring workspace, which populates that exam's own `curriculum_exam_{id}` (API 9.5) and is gated only on *that* exam being active. The legacy global import that populates the shared `public.*` (`ImportSchema`, gated on *no* examination being active) is retained for scripting / backward-compatibility but is no longer surfaced in the dashboard. Either way, system tables in `app.*` are never touched.
*   **Lecturer Answer Submission:** `UpsertQuestion` triggers Correct Result capture inline. The response body returns `expected_result_status` and `capture_error`. Capture failures **do not** roll back the upsert — the row is saved with `expected_result_status = 'failed'` so the lecturer can fix and retry. Activation will refuse to start as long as any question is in `failed` or `pending` state.
*   **Roster Import Pipeline:** The lecturer-facing bulk import endpoint (`POST /api/v1/lecturer/students/import`) accepts a multipart file upload (`.csv` via `encoding/csv`; `.xlsx` via `github.com/xuri/excelize/v2`; format detected by filename extension). `ParseRoster` reads the file with a case-insensitive header alias map and returns a slice of `ParsedStudentRow`. `StudentImportService.Import` opens a single database transaction and iterates the slice: for each row it establishes a `SAVEPOINT`, calls `UpsertStudentByNumber` (SQL `INSERT … ON CONFLICT (student_number) … DO UPDATE` — `password_hash` is intentionally excluded from the `UPDATE` clause so re-importing the roster does not reset a student's changed password), then releases the savepoint on success or rolls it back on failure and appends to the per-row `errors[]` array. The outer transaction commits once all rows are processed; the response always includes `{total, created, updated, skipped, errors}`.
*   **Schema Migrations:** New columns and tables are added via idempotent `CREATE TABLE IF NOT EXISTS` and `ALTER TABLE … ADD COLUMN IF NOT EXISTS` from `InitializeSystemTables` at backend startup. No external migration tool is required, but every DDL statement in `InitializeSystemTables` must be idempotent.

## Practice Mode

### Independent Question Bank
The practice module maintains its own question bank in `app.practice_questions`, separate from the examination question bank (`app.questions`). Student attempt history for practice questions is stored in `app.practice_attempts`.

### Categories & Per-Category Databases
Practice questions are organized into **categories** (`app.practice_categories`); each question references one via the NOT-NULL FK `practice_questions.category_id`. Every category **owns its own schema** (`practice_cat_{id}`) into which a lecturer imports a database; all of that category's submissions (SELECT and DML) execute against that schema, fully isolated from the exam curriculum in `public`. Exam questions have **no** category and are unaffected. Category management (CRUD + import) lives under `/api/v1/lecturer/practice/categories`. Deleting a category that still has questions is blocked (409) unless `?force=true`; the `Uncategorized` fallback category cannot be deleted. Imports reject schema-qualified names (e.g. `public.x`) and require `?confirm_replace=true` to overwrite a non-empty category schema.

### Practice During Exams (Per-Exam Toggle)
Because each category owns a schema separate from `public` and the exam sandboxes, practice *can* stay live during exams — there is no shared-state split-brain to guard against. Practice student endpoints are gated by `PracticeFreezeMiddleware`, which blocks practice (**423 Locked**, `{"error": "practice_unavailable_during_exam"}`) when an active examination has its `block_practice_during_exam` toggle set (flipped via PUT `/api/v1/lecturer/exams/:id/practice-freeze`). The check is cached in-process for 5 seconds and invalidated on activate/deactivate/toggle.

**The effective default is blocked, not live.** The column defaults to `FALSE`, but `SetExaminationActive` writes `block_practice_during_exam = TRUE` as part of activation, so every newly activated exam freezes practice until a lecturer turns the toggle off for that exam. Lecturer-facing practice endpoints are never frozen. (`ExamFreezeMiddleware`, which froze on *any* active exam, is retained but no longer wired to the practice group.)

### Practice Evaluation Concurrency
Practice DML (and SELECT) submissions do **not** pass through the `SubmissionQueue` — that queue is exam-only, wired in `quiz_controller.go` (single worker, capacity 256, 30 s per-job timeout). Practice evaluations run directly on pool connections. Correctness now comes from three mechanisms, one of which (ARCH-058..061) was added after concurrent practice DML was found to deadlock under the two below alone:
- **A per-execution-schema serialisation gate**: `acquireDMLGate` (`postgres_evaluation.go`), backed by an in-process `SchemaGate` (`internal/schemagate`), admits at most one DML evaluation per execution schema at a time — process-wide, and identically for practice and exam DML. It is taken at the top of `Evaluate` and `EvaluatePreview` (keyed on the schema each actually executes against) before any transaction begins, and at the lecturer capture/preview sites (`captureExpectedResult`, `PreviewPracticeCorrectSQL`, `CreateQuestion`, `PreviewCorrectSQLInSchema`) that replay DML against the same schema. A caller that cannot acquire it within `schemaGateWaitBudget` is shed with `ErrSchemaBusy`, which both controllers map to HTTP 503 rather than grading (or persisting) anything. **SELECT is never gated** — it takes the advisory lock shared and is parallel-safe by construction. The gate is process-local: two backend instances against one database each keep their own.
- **Savepoint-rollback isolation**: every practice DML submission executes inside its own transaction with `SAVEPOINT student_eval` immediately rolled back, so the category schema is unchanged after every attempt.
- **A shared per-schema advisory lock**: every evaluation path (`evaluateQuery`, `evaluateDML`, `evaluateDMLPreview`) takes `pg_advisory_xact_lock_shared` keyed on the schema it runs against (`schemaRebuildLockKey`, see `postgres_rebuild_lock.go`) for the life of its transaction. Its only purpose is to guard against a schema being dropped and rebuilt mid-evaluation — category import-with-replace and category delete take the same key EXCLUSIVELY (`pg_advisory_xact_lock`) before dropping a schema, so a rebuild blocks until every in-flight evaluation against that schema finishes, and new evaluations wait until the rebuild completes. (The old `ResetPracticeSandbox` / `sandbox_practice` reset path has been removed; there is no practice sandbox clone — practice DML runs against the category's own `practice_cat_{id}` schema, i.e. the lecturer's imported reference database, with savepoint-rollback as the barrier against persisting a student's writes.) Shared holders never block each other, so two categories (or an exam sandbox and a category) never contend on this lock alone — it is the schema gate above, not this lock, that now serialises concurrent DML against the *same* table.

### Savepoint-Rollback Semantics
Each DML practice submission is executed within a transaction using the following sequence:
1. `BEGIN;`
2. Take the shared per-schema advisory lock (`acquireSchemaEvalLock`).
3. `SAVEPOINT student_eval;`
4. `SET LOCAL search_path = practice_cat_{categoryID}, public;`
5. `SET LOCAL ROLE exam_student;`
6. Execute student SQL.
7. `SELECT * FROM target_table;` (capture post-state for validation).
8. `RESET ROLE; ROLLBACK TO SAVEPOINT student_eval;`
9. `COMMIT;` (a commit failure here is a no-op for grading — the savepoint rollback already emptied the transaction).

This pattern ensures the category's `practice_cat_{id}` schema remains in its original state after every attempt, providing a hermetic environment for each student. SELECT (Query) practice submissions read the same category schema under the `exam_student_readonly` role (no public fallback), so they too are isolated from the exam curriculum.

## Scoring & Reporting

### Practice Scoring
Since students can attempt practice questions multiple times, "score" for a specific practice question is defined as the question's `points` if at least one attempt by the student is marked `is_correct`.
- **Student View**: A consolidated summary shows:
  - Questions Answered (at least one attempt).
  - Questions Solved (at least one correct attempt).
  - Total Points Earned / Max Possible Points.
- **Backend**: Aggregated via `app.practice_attempts` joined with `app.practice_questions`.

### Exam Completion Summary
Upon submitting the final question of an active examination, the student is presented with an immediate summary of their performance for that exam:
- **Questions Answered**: count of distinct `question_id` in `app.submissions` for this student and exam.
- **Correct Answers**: count where `is_correct = true`.
- **Exam Score**: sum of `score` from latest submissions per question.
- **Max Score**: sum of `points` for all questions in the exam.

### Lecturer Score Export
The backend exposes `GET /api/v1/lecturer/scores` (`GetConsolidatedScores`), returning per-student rows with `exam_scores` keyed by exam title plus a practice total; `GET /api/v1/lecturer/exams/max-scores` supplies the denominators. **CSV generation is client-side** — `LecturerResults.vue` builds two separate downloads (`exam_scores.csv` and `practice_scores.csv`) from that JSON. There is **no** server-side `export-scores` endpoint.
- **Dynamic Columns**: The exam CSV header is generated from the set of exam titles present in the consolidated payload.
- **Aggregates**:
  - **Exam Column**: Sum of `score` for the student's latest submissions in that exam.
  - **PracticeTotal**: Sum of `points` for all practice questions solved by the student.

### Submission-log export (out-of-band emailer)
`SubmissionExportService` ships the raw submission log off-box on an interval so a lost database is not a lost exam. It is a plain ticker goroutine started from `main.go` — there is no scheduler and no queue.

Each cycle reads the `app.export_state` singleton, takes the window `(last_export_at, now]`, and pulls `app.submissions` + `app.practice_attempts` for it. An empty window advances the watermark and sends nothing. Otherwise the two result sets are written as gzip CSV attachments (`submissions_YYYYMMDD.csv.gz`, `practice_attempts_YYYYMMDD.csv.gz`) and emailed to `EXPORT_ADMIN_EMAIL` (falling back to `ADMIN_EMAIL`).

The watermark is the correctness property: it is advanced **only after a successful send**, so a failed SMTP cycle re-sends the same window next tick rather than dropping it. A missing recipient likewise holds the watermark. Cycle errors are logged and swallowed so a transient failure cannot kill the loop.

Admin controls: `GET`/`PUT /export/settings` (read state, flip `enabled`) and `POST /export/run-now`. All three answer **503** when SMTP is unconfigured, because the whole service is only constructed when `EMAIL_SENDER_ADDRESS` is set.

---

## Frontend Architecture (Vue 3 + Vite)

The SPA is organised around small, single-responsibility primitives. Three rounds of UI/UX work (2026-05-22 → 2026-05-23) consolidated the patterns below; see `CHANGELOG.md` for the per-round detail.

### Module layout (`frontend/src/`)
- **`components/`** — Vue components (views + shared building blocks like `<FieldError>`, `<EmptyState>`, `<Skeleton>`, `<Spinner>`, `<ConfirmDialogHost>`, `<BackLink>`, `<StudentMultiSelect>`, `<ForbiddenView>`, `<NotFoundView>`).
- **`composables/`** — reusable behaviour: `useFormValidation`, `useSingleFlight`, `useAutosave`, `useKeyboardShortcut`, `useFocusTrap`.
- **`services/`** — module-singleton state: `auth`, `confirm_service`, `error_handler`, `notification_service`, plus API clients (`practice.ts`, etc.).
- **`utils/`** — pure helpers: `format_date` (Intl.DateTimeFormat + relative time), `format_sql` (sql-formatter wrapper).
- **`constants/`** — `ui_copy.ts` (`UI_COPY`) governs button labels.
- **`components/lecturer/`** — the lecturer examination authoring UI, decomposed into single-responsibility panels (see *Lecturer examination workspace* below).
- **`__tests__/`** — Vitest + happy-dom test files mirroring the directories above (44 files).

See *Codebase Map* above for the full directory listing.

### Lecturer examination workspace (master → detail)
The lecturer authoring UI (`/lecturer`) is a master/detail flow under `components/lecturer/`, split out of a former 1894-line monolith into focused files:
- **`LecturerView.vue`** — slim shell; sole owner of the exam list (`exams` + `fetchExams()`) and `selectedExamId`. Renders `ExamListPanel` when nothing is selected, else `ExamWorkspace`. Children emit `changed`; the shell re-runs `fetchExams()` so list-visible readiness counts stay fresh.
- **`ExamListPanel.vue` / `ExamCard.vue`** — the list: a create-exam form plus one self-contained card per exam carrying inline rename, the readiness summary, and **activation / scheduling / deactivation** (exam-"event" creation lives at the list level, not inside the detail). Cards emit `open` / `changed` / `deleted`.
- **`ExamWorkspace.vue`** — the detail view, mounted with `:key="exam.id"` so panels fully remount per exam; a "← Back" control plus a **Schema / Questions / Roster** sub-tab bar (tab state is in-memory, not persisted).
- **`ExamSchemaPanel.vue`** — imports into the exam's **own** `curriculum_exam_{id}` via `importExamCurriculum()` (API 9.5) with a 409 → confirm-replace retry. The legacy shared-`public` import (API 9) is no longer surfaced in the UI.
- **`ExamQuestionsPanel.vue`** — question CRUD, preview (sends `exam_id` so it resolves the exam's curriculum), Phase-0 capture status, strict-mode flags, the description + intro-image editor, and requirement autosave (localStorage key scoped per exam).
- **`ExamRosterPanel.vue`** — enrollment (bulk import + multi-select) and the roster table. Destructive actions (delete exam, remove student) are gated on `effective_state === 'active'`, protecting a scheduled-active exam.
- **`types.ts`** — shared interfaces (`Examination`, `Student`, `RosterStudent`, `CaptureStatus`, `PreviewResult`) imported by every panel to avoid drift.

Results are **not** a tab here — they live on a dedicated, role-gated page (see *Auth + routing*).

### Auth + routing
- Auth state lives in `services/auth.ts > useAuth()` (`user`, `isLoggedIn`, `login`, `logout`, `hydrateFromStorage`). Components never read `localStorage` directly.
- `router.ts > beforeEach` is a single four-rule guard:
  1. Public-route redirect for logged-in users (exemptions: `to.query.force === '1'`, `to.name === 'forbidden'`, `to.name === 'not-found'`).
  2. Auth gate (`meta.requiresAuth`).
  3. Role check (`meta.allowedRoles`) with an **ADMIN wildcard short-circuit** — ADMIN passes any route whose `meta.adminBlocked !== true`. `/practice` opts out because student submissions there are recorded under the submitting user_id and would pollute analytics.
  4. Fall-through.
- A 404 catch-all (`path: '/:pathMatch(.*)*'`) and `/403` page exist and are kept reachable for logged-in users.
- The lecturer **Results** page (`/lecturer/results` → lazy `components/lecturer/LecturerResults.vue`) is gated `meta.allowedRoles: ['ADMIN', 'LECTURER']` (ADMIN via the wildcard; no `adminBlocked`). It owns its own data lifecycle — consolidated scores, per-exam drill-down, and CSV export — independent of the `/lecturer` authoring shell. A STUDENT hitting this path is redirected to `/403`.

### Forms and errors
- Validated forms carry `novalidate`; `useFormValidation` owns error display via `<FieldError>` (`role="alert"`) with `aria-invalid`/`aria-describedby` bound on each input.
- Mutation/fetch catch blocks route through `services/error_handler.ts > handleApiError`; `!response.ok` branches throw via `toApiError`. `LoginView` keeps its inline form-level `error` ref as the documented exception.
- Confirmations go through `services/confirm_service.ts > useConfirm()` (promise-based, brutalist `<ConfirmDialogHost>` with focus trap and Escape/Enter handling). Native `window.confirm()` is not used.

### A11y and responsive
- WCAG AA: `--color-text-muted` is `#4b5563` (light) / `#d1d5db` (dark).
- Mobile breakpoint at 767.98 px: hamburger drawer in `App.vue` with `matchMedia`-gated focus trap, Escape and route-change auto-close, and active-route bottom border.
- `prefers-reduced-motion` honored on all animated elements (`.skeleton` shimmer, etc.).

### Testing
- `vite.config.ts` imports `defineConfig` from `vitest/config` so `vue-tsc -b` accepts the `test` block. The build pipeline is `vue-tsc -b && vite build`.
- Run frontend tests with `cd frontend && bun run test` (or `npx vitest run`).

---

## Additional Information

*(This header is intentionally left blank per project instructions)*
