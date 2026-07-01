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

## Schema Layout
The database is split across several logical schemas, each with a distinct role and lifetime:

| Schema                                | Purpose                                                                                          |
| ------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `app`                                 | System tables (see below). Always present.                                                       |
| `public`                              | Curriculum tables imported by the lecturer. Treated as a clean template; **frozen** for the duration of any active examination. SELECT (Query) submissions read directly from here under the `exam_student_readonly` role. |
| `sandbox_exam_{examID}`               | Shared per-exam sandbox cloned from `public.*` once at activation. Used by **DML** submissions only; pure-SELECT exams skip cloning entirely. All students of the same exam read and write the same physical tables here, serialised by the global in-process FIFO submission queue (single worker, capacity 256, 30 s per-job timeout); each submission's writes are reverted by `ROLLBACK TO SAVEPOINT`. Dropped on deactivation. |
| `curriculum_exam_{examID}`            | Per-exam curriculum schema (optional). A lecturer imports a database into it via `POST /api/v1/lecturer/exams/:id/curriculum/import`; the exam's Phase-0 Correct Result capture, Phase-2 SELECT read-path, and Phase-1 sandbox clone all source from here. Falls back to the shared `public` schema when `app.examinations.curriculum_schema = 'public'` (the default), preserving the original single-template behaviour. Dropped when the examination is hard-deleted. See `practice_cat_{categoryID}` for the precedent pattern. |
| `practice_cat_{categoryID}`           | Per-practice-category database. Each `app.practice_categories` row owns one schema into which a lecturer imports a `.sql` (POST `/api/v1/lecturer/practice/categories/:id/import`). Practice questions belong to a category (FK `practice_questions.category_id`) and both their SELECT and DML submissions run against the category's own schema, isolated from `public`. DML uses savepoint-rollback; SELECT reads the category schema under `exam_student_readonly`. This is what keeps practice databases separate from the exam curriculum. |
| `sandbox_practice`                    | **Deprecated** (retained one release for rollback; scheduled for removal). Former persistent shared practice sandbox cloned from `public.*`. Superseded by per-category `practice_cat_{id}` schemas; no longer used by the submission path. |

System tables in `app`:

| Table                     | Purpose                                                                                                                |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `app.system_users`        | Identity table for admins, lecturers, students. Single table with a `role` discriminator.                              |
| `app.examinations`        | Examination definitions. Lifecycle flags: `is_active`, `activated_at`, `archived_at`.                                  |
| `app.questions`           | Question definitions **plus the cached Correct Result** (`expected_result`, `expected_columns`, capture status).        |
| `app.exam_students`       | Junction table for student enrollment (records `assigned_by`, `assigned_at`).                                          |
| `app.submissions`         | Per-attempt durable record of every student submission and its score.                                                  |
| `app.exam_sandboxes`      | Tracks which `sandbox_exam_{examID}` schemas exist (one row per exam), with `created_at` / `dropped_at` for cleanup bookkeeping. |

The full ER definition lives in `docs/database_schema.mmd`. Render with `mmdc -i database_schema.mmd -o database_schema.png`.

![Database Schema](docs/database_schema.png)

## Phase Lifecycle
Examination evaluation is split into four phases, gated on `app.examinations.is_active`. The optimizations (cached Correct Result, lazy sandbox, no per-submission teacher SQL) all rely on these phase boundaries holding.

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
1. **Pre-flight** — `ActivateExamination` rejects activation unless every question for the exam has `expected_result_status = 'captured'`. The 409 response body lists each failing question with its `capture_error`.
2. **Sandbox preparation** — If the exam contains at least one DML question, `InitializeExamSandbox(examID)` runs once: `CREATE SCHEMA IF NOT EXISTS sandbox_exam_{examID}`, grant `exam_student` CRUD on it, then for each `public.*` table `CREATE TABLE sandbox_exam_{examID}.x (LIKE public.x INCLUDING ALL); INSERT INTO … SELECT * FROM public.x`. The clone happens at activation, not lazily on first submission. A row is upserted into `app.exam_sandboxes`. Pure-SELECT exams skip this step entirely.
3. **Freeze** — On success, `is_active = true`, `activated_at = now()`. While active:
   - `UpsertQuestion` for any question of this exam → 409.
   - `ImportSchema` (global, targeting `public.*`) → 409 (would invalidate every active exam's cache and any active sandbox clone).
   - `ImportExamCurriculum` (`POST /lecturer/exams/:id/curriculum/import`) → 409 **for this exam only** — the per-exam curriculum import is gated per-exam (blocked only while that specific exam is active, not globally). A lecturer may import curriculum for a different inactive exam while this one runs.
   - `AssignStudents` for this exam → 409.
   - Only `DeactivateExamination` is permitted on the exam itself.

### Phase 2 — Active Exam (submission event loop, `is_active = true`)
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
5. Compare row-by-row against `app.questions.expected_result` (captured in Phase 0 from teacher SQL on a `public` clone).
6. **Idempotent submissions** (post-state byte-identical to pre-state) score zero.
7. `ROLLBACK TO SAVEPOINT student_eval; COMMIT` — sandbox state is restored for the next job in the queue.

The legacy "Backup → Teacher → Diff → Restore" loop and the `ResetTable(schema, target)` workaround are **removed**: the savepoint+rollback inside the global FIFO queue is the only mechanism that resets state between submissions.

---

## Architecture Q&A (Design Decisions)

### 1. Security & Sandboxing
*   **Q: How does the system prevent destructive SQL (e.g., `DROP TABLE`, `TRUNCATE`) on the DML path?**
    *   **A:** Three layers of defense. (a) `exam_student` PG role has only CRUD privileges on the exam sandbox schema; DDL is denied. (b) Phase 2 runs inside a savepoint that is unconditionally rolled back, so even if a destructive statement somehow ran, it would be reverted. (c) `search_path` is scoped to `sandbox_exam_{examID}` + `public`, so unqualified writes can't reach `app.*`.
*   **Q: How is the SELECT path kept safe given that data-modifying CTEs are legal Postgres?**
    *   **A:** The SELECT path runs under `exam_student_readonly`, which holds only `USAGE` on `public` and `SELECT` on its tables. The underlying `INSERT` / `UPDATE` / `DELETE` inside any data-modifying CTE fails on permissions; the parser doesn't need to inspect the SQL. No clone is required because nothing can mutate.
*   **Q: Are the restricted roles created at startup?**
    *   **A:** **Yes.** `CreateRestrictedUser` runs from `main.go` before `InitializeSystemTables`. Two roles: `exam_student` (CRUD on sandbox schemas) and `exam_student_readonly` (SELECT-only on `public.*`). Both are guarded with `IF NOT EXISTS`-style `DO` blocks so re-runs are no-ops.

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
    *   **A:** At Phase 1 activation, by `InitializeExamSandbox(examID)`, but only if the exam contains at least one DML question. Pure-SELECT exams never create a sandbox. The call is idempotent: re-activation only fills in tables that are missing.
*   **Q: How are tables cloned from `public`?**
    *   **A:** `CREATE TABLE sandbox_exam_{examID}.x (LIKE public.x INCLUDING ALL); INSERT INTO sandbox_exam_{examID}.x SELECT * FROM public.x`. Preserves primary keys, NOT NULL, defaults, and indexes. (Foreign keys are not propagated by `LIKE` — Postgres limitation. Sandbox tables therefore have no FK constraints, which is acceptable because student DML runs inside an unconditional savepoint rollback.)
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
Because each category owns a schema separate from `public` and the exam sandboxes, practice is **live during exams by default** — there is no shared-state split-brain to guard against. Practice student endpoints are gated by `PracticeFreezeMiddleware`, which blocks practice (**423 Locked**, `{"error": "practice_unavailable_during_exam"}`) only when an active examination has its `block_practice_during_exam` toggle set (the optional "focus" rule, flipped via PUT `/api/v1/lecturer/exams/:id/practice-freeze`). The check is cached in-process for 5 seconds and invalidated on activate/deactivate/toggle. With every exam's toggle off (the default) practice stays available throughout exams. Lecturer-facing practice endpoints are never frozen. (`ExamFreezeMiddleware`, which froze on *any* active exam, is retained but no longer wired to the practice group.)

### Advisory Lock Partitioning
Practice DML submissions follow the same serialisation pattern as exam submissions but use a separate namespace for advisory locks to avoid collisions. The lock key is derived from `FNV1a("practice:" + target_table)`. This partitioning allows practice questions targeting different tables to be evaluated in parallel, while ensuring that concurrent submissions to the same table are serialised.

### Savepoint-Rollback Semantics
Each DML practice submission is executed within a transaction using the following sequence:
1. `BEGIN;`
2. `SAVEPOINT student_eval;`
3. `SET LOCAL search_path = practice_cat_{categoryID}, public;`
4. `SET LOCAL ROLE exam_student;`
5. Execute student SQL.
6. `SELECT * FROM target_table;` (capture post-state for validation).
7. `ROLLBACK TO SAVEPOINT student_eval;`
8. `COMMIT;`

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
Lecturers can download a comprehensive CSV report (`GET /api/v1/lecturer/export-scores`) containing the performance of all students across all assessments.
- **Dynamic Columns**: The CSV header is generated dynamically based on the set of all examinations created.
- **Columns**: `StudentNumber`, `FirstName`, `LastName`, `SecLec`, `SecLab`, `[Exam 1 Title]`, `[Exam 2 Title]`, ..., `PracticeTotal`, `GrandTotal`.
- **Aggregates**:
  - **Exam Column**: Sum of `score` for the student's latest submissions in that exam.
  - **PracticeTotal**: Sum of `points` for all practice questions solved by the student.
  - **GrandTotal**: Sum of all exam scores and the practice total.

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
- **`__tests__/`** — Vitest + happy-dom test files mirroring the directories above (29 files, 299 cases).

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
