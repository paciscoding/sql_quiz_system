# Project Roadmap & TODO

## DONE Phase 1: Core Validation Engine
- [x] Implement SQL Syntax Error handling (returns zero score and error feedback).
- [x] Develop Query Validation (Select) logic:
    - [x] Result set matching.
    - [x] Optional: Command ordering check (string sequence).
    - [x] Optional: Attribute name strictness.
    - [x] Optional: Record order strictness.
- [x] Develop DML Validation (Insert/Update/Delete) logic:
    - [x] "Backup -> Execute Teacher -> Diff" workflow.
    - [x] "Execute Student -> Diff" workflow.
    - [x] Row-by-row structural comparison of diffs.
    - [x] Idempotent query detection (zero score logic).
- [x] Set up PostgreSQL Repository and API Controller.

## DONE Phase 2: Infrastructure & Security
- [x] Setup Docker Compose for Go Gin + PostgreSQL.
- [x] Implement DB Sandbox:
    - [x] Logic to generate temporary schemas/database instances per student.
    - [x] Restricted database user permissions (CRUD only).
- [x] Implement Request Queueing:
    - [x] Postgres session-level advisory locks (`pg_advisory_lock`) for cross-process serialisation.
- [x] Enable CORS for frontend-backend communication.

## DONE Phase 3: Lecturer Management
- [x] API for lecturers to submit "Correct Answers".
- [x] API for lecturers to define custom DB structures (via SQL Import).

## DONE Phase 4: Frontend (Vue 3)
- [x] Basic exam interface for students.
- [x] Simple dashboard for lecturers to manage questions.
- [x] Real-time feedback/score display.

## DONE Phase 5: Hierarchical User Management
- [x] Implement `system_users` table and role system.
- [x] Implement JWT-based Authentication.
- [x] Implement Account Generation API (Admin/Lecturer flow).
- [x] Implement Bulk Account Generation (Multiple users at once).
- [x] Frontend: Login Page.
- [x] Frontend: Admin Dashboard for account generation.

## DONE Maintenance & DevOps
- [x] Secure `docker-compose.yml` with `.env` file.
- [x] Containerize frontend using Nginx multi-stage build.
- [x] Upgrade backend to Go 1.26.2.

## Architecture Refactor (2026-05-02, from CRITIQUE.md "Architecture & Design")
- [x] Move system tables (`system_users`, `examinations`, `questions`, `exam_students`) into a dedicated `app` schema and drop the hardcoded exclusion list in `GetPublicTables`.
- [x] Make `InitializeSandbox` idempotent — only clone tables that are missing from the student schema.
- [x] Switch sandbox cloning from `CREATE TABLE … AS SELECT` to `CREATE TABLE … (LIKE public.x INCLUDING ALL)` + `INSERT … SELECT` to preserve PKs / NOT NULL / defaults / indexes.
- [x] Add `ResetTable(schema, target)` and call it before each DML evaluation so per-question state doesn't leak across submissions.
- [x] Replace `fmt.Sprintf("%v", results)` comparison with a structurally-deterministic JSON-based diff.
- [x] Honour `StrictMode` in `compareRows` and DML validation.

### Deferred Architecture (still in CRITIQUE.md)
- *Previously-deferred "wrap DML eval in transaction with savepoints" and "replace `restoreTable` TRUNCATE/INSERT with transactional rollback" have been promoted into the Phase Lifecycle Redesign section below.*
- *Previously-deferred "AST-based ordering check" is **resolved** as of 2026-05-06: the keyword-sequence claim has been removed from spec docs and `strict_ordering` now means row-order match. See `## Strict-Mode Rename` below for the code follow-up.*

## Backend Build Gaps (2026-05-06, blocking)
`go build ./...` currently fails with two errors that are **not** covered by any other section. Working through the rest of `TODO.md` does not unblock the build — these must land first.

- [x] Define `(*PostgresRepository).WithTransaction(ctx context.Context, fn func(pgx.Tx) error) error` on `backend/internal/repository/postgres.go`. It should: acquire a connection from the pool, `Begin` a tx, call `fn(tx)`, `Commit` on nil error, otherwise `Rollback` and return the error. Used by `CreateQuestion` (line 497, Phase 0 capture), `ArchiveExamination` (line 659, Phase 3 sandbox drop), `AddStudentsToExam` (line 694, bulk insert).
- [x] Move the local `type Submission struct {...}` from `backend/internal/repository/postgres.go:889` into `backend/internal/models/submission.go` as `models.Submission`, preserving fields (`ExamID int`, `StudentID int`, `QuestionID int`, `SQLText string`, `IsCorrect bool`, `Score float64`, `ValidationDetails []byte`). Delete the local copy. Unblocks the existing `CreateSubmission(ctx, s models.Submission)` signature at line 880.

## Strict-Mode Rename (2026-05-06, code follow-up)
Spec docs have been collapsed to two flags (`strict_ordering`, `strict_attributes`); see `CHANGELOG.md § [2026-05-06]`. The code still carries the old three-flag shape — fold it down so the docs and code agree.

- [x] `backend/internal/models/submission.go:21–25` — drop `RecordOrder bool` from `StrictMode`. Keep `Ordering` and `Attributes`. The `ordering` JSON tag now carries row-order semantics.
- [x] `backend/internal/services/validation_service.go` — delete `(*ValidationService).checkSQLOrdering` (line 84) and its call site at lines 37–39 (the `if question.StrictMode.Ordering { ... checkSQLOrdering ... }` block). Inside `compareResults` (line 193) the row-order branch currently keys off `mode.RecordOrder` (line 198); rewire it to read `mode.Ordering`.
- [x] `frontend/src/components/LecturerView.vue:32` — drop `record_order: false` from the form default. Lines 305 and 307 — relabel the checkbox bound to `strict_mode.ordering` from "Command Ordering" to "Row Ordering"; delete the "Record Order" checkbox bound to `strict_mode.record_order`.
- [x] DB migration in `InitializeSystemTables`'s `extraMigrations` slice: `ALTER TABLE app.questions DROP COLUMN IF EXISTS strict_record_order` (idempotent). Drop the column from the `INSERT … VALUES` and `ON CONFLICT DO UPDATE SET` lists in `CreateQuestion`'s SQL, and from the `SELECT` list in `GetQuestion`.

## Phase Lifecycle Redesign (2026-05-04, in flight)
Reference: `ARCHITECTURE.md § Phase Lifecycle`, `PROJECT.md § Lowered Operation Strategy`, `docs/database_schema.mmd`. This is the **active sprint** for the in-flight redesign; complete top-to-bottom.

### DONE Schema migrations (idempotent DDL in `InitializeSystemTables`)
- [x] Add columns to `app.questions`: `expected_result jsonb`, `expected_columns jsonb`, `expected_result_status varchar(16) DEFAULT 'pending'`, `expected_result_captured_at timestamptz`, `capture_error text`, `created_by int`, `created_at timestamptz`, `updated_at timestamptz`.
- [x] Add columns to `app.examinations`: `activated_at timestamptz`, `archived_at timestamptz`.
- [x] Add columns to `app.exam_students`: `assigned_by int`, `assigned_at timestamptz`.
- [x] Create `app.submissions` with index `(exam_id, student_id, question_id, submitted_at DESC)`.
- [x] Create `app.student_sandboxes` with composite PK `(student_id, exam_id)`.
- [x] All DDL must use `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE … ADD COLUMN IF NOT EXISTS` so startup is idempotent.

### DONE Phase 0 — Correct Result Capture
- [x] In `UpsertQuestion`, run teacher SQL on a `public` clone inside a transaction, capture result-set (SELECT) or `target_table` post-image (DML), roll back. Persist to cache columns. On failure: persist `expected_result_status = 'failed'` + `capture_error` and return both in the response (do **not** roll back the question upsert).
- [x] Enforce a configurable result-row cap (default 10,000); over-cap captures fail with `capture_error = "result-set too large"`.

### DONE Phase 1 — Activation gate
- [x] `ActivateExamination` rejects with 409 + structured body listing failing questions if any has `expected_result_status != 'captured'`.
- [x] While `is_active = true`: `UpsertQuestion`, `AssignStudents` for that exam return 409. `ImportSchema` returns 409 if any exam is active.

### DONE Phase 2 — Savepoint-based evaluation
- [x] Replace `ResetTable`-driven flow with: `BEGIN; SAVEPOINT student_eval; SET LOCAL search_path = sandbox, public; SET LOCAL ROLE exam_student; …student SQL…; capture post-state; ROLLBACK TO SAVEPOINT student_eval; INSERT INTO app.submissions; COMMIT`.
- [x] All Phase 2 steps must share one pool connection (the same one holding the advisory lock).
- [x] Comparison reads from `app.questions.expected_result` only — never re-execute teacher SQL.
- [x] Switch sandbox naming from `student_{studentID}` to `student_{studentID}_exam_{examID}`.
- [x] Track sandbox creation in `app.student_sandboxes` on first submission.

### DONE Phase 3 — Deactivation / cleanup
- [x] Add `PUT /api/v1/lecturer/exams/:id/deactivate` endpoint.
- [x] On deactivate, set `is_active = false`, `archived_at = now()`, then enumerate `app.student_sandboxes WHERE exam_id = ? AND dropped_at IS NULL`, `DROP SCHEMA … CASCADE`, set `dropped_at`.

## DONE Phase Lifecycle Redesign (2026-05-04)
Reference: `ARCHITECTURE.md § Phase Lifecycle`, `PROJECT.md § Lowered Operation Strategy`, `docs/database_schema.mmd`.

### DONE Schema migrations
- [x] Add columns to `app.questions`: `expected_result`, `expected_columns`, etc.
- [x] Create `app.submissions` and `app.student_sandboxes`.

### DONE Phase 0–3 Implementation
- [x] Phase 0: Correct Result Capture.
- [x] Phase 1: Activation gate.
- [x] Phase 2: Savepoint-based evaluation.
- [x] Phase 3: Deactivation / cleanup.
- [x] Submission persistence and audit endpoint (`GET /api/v1/lecturer/exams/:id/submissions`).

## DONE Critical Security Improvements
- [x] **Identity Integrity:** Derive student identity strictly from JWT context.
- [x] **SQL Injection Prevention:** Sanitize and quote schema names.
- [x] **Auth Hardening:** Secure JWT secrets, alg checks, login rate limiting.
- [x] **Access Control:** Restrict `ImportSchema` to Admin role.
- [x] **Admin Security:** Configurable admin credentials via environment variables.

## DONE Technical Debt & Refactoring
- [x] Backend: Context plumbing, pool tuning, SERIAL desync fix, transaction helpers.
- [x] Frontend: TypeScript interfaces, Vue Router, CodeMirror, Multi-select, Toast notifications.
- [x] DevOps: Healthchecks, non-root users, hardened Nginx.

### DevOps & Infrastructure
- [x] **Reliability:**
    - [x] Add Postgres healthcheck and `service_healthy` condition in Compose.
    - [x] Add `HEALTHCHECK` instructions to Dockerfiles.
    - [x] Backend: Run as a non-root user in the production stage.
- [x] **Nginx Optimization:**
    - [x] Forward real client IPs (`X-Real-IP`, `X-Forwarded-For`).
    - [x] Add caching, gzip, and security headers (CSP, HSTS).
- [ ] **CI/CD:** Set up GitHub Actions for automated testing.

## Testing Strategy
- [x] Strengthen "Correct DML" validation tests with distinct teacher/student states.
- [ ] Implement unit tests for Auth, Repository, Controllers, and Lock Manager.
- [ ] Add integration tests using a real PostgreSQL instance (e.g., via Testcontainers).

## DONE Lecturer View Smoke Test (2026-05-07, fixed 2026-05-07)
End-to-end probe of the lecturer flow against the running stack at `localhost`. Findings were confirmed by hitting `/api/v1/*` directly with a real JWT and inspecting the DB; lightpanda's own SPA `fetch` is broken in its sandbox so the form-driven login is unreliable there (real browsers work).

### Backend bugs
- [x] **`UpsertQuestion` now sets `created_by` from the JWT.** `LecturerController.UpsertQuestion` reads `userVal` from the Gin context, casts to `models.User`, and sets `q.CreatedBy = lecturer.ID` before calling `repo.CreateQuestion`. Verified with `aj_a` (id 3) — `app.questions.created_by` is populated correctly.
- [x] **Freeze conflicts return HTTP 409.** Repository now wraps freeze errors with `repository.ErrExamActive` (and `ErrCaptureNotReady` / `ErrEmptyExam` for the activation gate); a new `statusForRepoErr` helper in the controller maps these sentinels to `http.StatusConflict`. Applied to `UpsertQuestion`, `AssignStudents`, `ImportSchema`, and `ActivateExamination`. Verified: each now returns 409 with the sentinel text.
- [x] **Activation gate rejects empty exams.** `SetExaminationActive` first counts all questions for the exam and returns `ErrEmptyExam` when 0, then runs the original capture-status check (now wrapping with `ErrCaptureNotReady`). Verified: empty exam activates → 409 with `examination has no questions: define at least one question before activating`.
- [x] **Orphaned legacy sandbox schemas are cleaned up at startup.** `InitializeSystemTables` enumerates `information_schema.schemata WHERE schema_name LIKE 'student\_%'` and runs `DROP SCHEMA … CASCADE` for each before retiring `app.student_sandboxes`. Verified: prior `student_680531006` schema is gone after backend restart.
- [x] **Phase 0 capture-failure handling fixed.** `CreateQuestion` now returns sentinel errors (`errCaptureRollback` for success, `errCaptureFailed` for runtime errors) instead of `fmt.Errorf("rollback_capture")` + `nil`. The `nil` path was triggering `WithTransaction.Commit` on an aborted Postgres tx, surfacing as `commit unexpectedly resulted in rollback` (HTTP 500) when teacher SQL had a syntax error. Now bad teacher SQL saves the question row with `expected_result_status='failed'` and the PG error in `capture_error`, returning HTTP 200. Discovered during smoke-test verification of the freeze-status fix above.

### Frontend (LecturerView.vue) regressions
- [x] **Strict-mode UI cleaned up.** "Command Ordering" checkbox relabelled to **"Row Ordering"** with hint text `(rows must match the teacher's order; off ⇒ set-equality)`. The phantom "Record Order" checkbox was deleted — it was bound to `strict_mode.record_order`, which is no longer in the form's reactive object. The strict-mode block is now visible for both QUERY and DML (relevant to both: ordering = row order; attributes = column names) instead of being gated on `type === 'QUERY'`.
- [x] **Capture-status badge is rendered.** `LecturerController.UpsertQuestion` now re-reads the saved question and includes `expected_result_status`, `expected_result_captured_at`, `capture_error` in the response. `LecturerView.vue` shows a green badge on `captured`, red on `failed` (with the PG error), amber on `pending`. Badge styles added to scoped CSS.
- [x] **Deactivation uses the dedicated `/deactivate` endpoint.** `toggleExamActive(id, false)` now PUTs `/api/v1/lecturer/exams/:id/deactivate` (no body); `active=true` still goes to `/activate`.
- [x] **Question form UX hazards.**
  - `question.id` is now nullable and reset after each save via `blankQuestion()`. Save handler refuses to submit if id is empty/non-positive. Reduces silent `ON CONFLICT DO UPDATE` collisions.
  - `question.exam_id` is auto-filled (via a `watch`) from the lecturer's `selectedExamId` choice in the "Assign Students" panel, and preserved across saves so consecutive question entries don't need re-typing. Save handler refuses to submit if `exam_id` is empty.
  - "Target Table" input is now `v-if="question.type === 'DML'"`.

### Frontend build infra (incidentally unblocked while deploying the fixes)
- [x] **TypeScript bumped from `~5.4.2` to `~5.8.0`.** The pre-existing `frontend/tsconfig.{app,node}.json` use `erasableSyntaxOnly` (introduced in TS 5.8) and `target: es2023` / `lib: ES2023`, none of which TS 5.4 supported. The `bun run build` step was failing in the Dockerfile's `vue-tsc -b` phase. Both tsconfigs also got an explicit `incremental: true` to satisfy `tsBuildInfoFile`. One unrelated dead variable in `StudentView.vue:26` (`const user = JSON.parse(...)` never referenced) deleted; the project now builds clean via `bun run build` and the docker `frontend` image rebuilds successfully.

### Login flow note (not a bug, lightpanda quirk)
- The login form in lightpanda's headless browser shows `fetch error` even though `/api/v1/login` returns 200 + JWT via curl. This is a known lightpanda limitation — its embedded fetch implementation can't reach `/api/v1` from a same-origin JS context in some cases. Real browsers work. **No action needed**, recorded so the next person doesn't chase a phantom.

## DONE [2026-05-16] Bulk Roster Import & Student Exam Results
- [x] `POST /api/v1/lecturer/students/import` with CSV and XLSX support (ParseRoster + StudentImportService + UpsertStudentByNumber savepointed tx).
- [x] `app.system_users` extended with `student_number`, `student_id_ext`, `sec_lec`, `sec_lab`, `first_name`, `last_name` (partial UNIQUE indexes).
- [x] `GET /api/v1/quiz/results` and `GET /api/v1/quiz/exams/:id/results` student-scoped endpoints.
- [x] `BulkImportStudents.vue` + `StudentResults.vue` frontend components and `/student/results` route.

## DONE Sandbox Redesign: Shared per-exam DML + read-only Query (2026-05-07)
Reversed the per-student-per-exam clone model. See `CHANGELOG.md [2026-05-07]` and `company_elmasri_postgres.sql` header for the user-facing summary.

### DONE Schema / role migrations (idempotent DDL in `InitializeSystemTables`)
- [x] Role `exam_student_readonly` is created in `CreateRestrictedUser` with `USAGE` on `public` + `SELECT` on its tables + `ALTER DEFAULT PRIVILEGES … GRANT SELECT`, guarded by `DO $$ BEGIN IF NOT EXISTS …`.
- [x] `app.exam_sandboxes (exam_id PK, schema_name, created_at, dropped_at)` is created in `InitializeSystemTables`. Migration drops the legacy `app.student_sandboxes`.

### DONE `postgres.go` changes
- [x] Removed `InitializeSandbox(schema string)` and `ResetTable`. Added `InitializeExamSandbox(ctx, examID int)` and `ExamSandboxName(examID)` helper. Called from `SetExaminationActive` only when `examHasDMLQuestions(examID)` returns true.
- [x] `ArchiveExamination` reads `app.exam_sandboxes` (single row), runs one `DROP SCHEMA sandbox_exam_{examID} CASCADE`, sets `dropped_at`.
- [x] `student_{studentID}_exam_{examID}` schema naming removed everywhere; `Evaluate` now uses `ExamSandboxName(*question.ExamID)` for DML and `public` (under readonly role) for SELECT.

### DONE Controller / service changes
- [x] `QuizController.SubmitAnswer` branches on `question.Type`:
  - SELECT: no lock; `BEGIN; SET LOCAL ROLE exam_student_readonly; SET LOCAL search_path = public;` → run → ROLLBACK; compare; INSERT submission.
  - DML: `LockManager.ExecuteDMLLocked(ctx, examID, target_table, …)` wraps the eval; inside, `BEGIN; SAVEPOINT student_eval; SET LOCAL ROLE exam_student; SET LOCAL search_path = sandbox_exam_X, public;` → run → capture target_table post-image → ROLLBACK TO SAVEPOINT → COMMIT; compare; INSERT submission.
- [x] `LockManager.ExecuteLocked`/`studentAdvisoryKey` removed. New `ExecuteDMLLocked(ctx, examID, target_table, fn)` keys on `FNV1a("sql_quiz/dml/" || examID || ":" || target_table)`.

### DONE Spec-doc updates
- [x] `ARCHITECTURE.md § Schema Layout`, `§ Phase Lifecycle`, `§ DML Validation`, `§ Architecture Q&A` rewritten around the hybrid model.
- [x] `AGENTS.md § Already in code` rewritten; `§ Technical Mandates` updated to reflect DML-needs-lock / SELECT-skips-lock invariants.
- [x] `PROJECT.md § Lowered Operation Strategy` rewritten.
- [x] `README.md § Key Features` rewritten.
- [x] `MANUAL.md` lecturer flow + Technical Architecture Notes rewritten.
- [x] `DEVELOPMENT.md § Database Layout` updated.
- [x] `API_SPEC.md` `/quiz/submit` and `/lecturer/exams/:id/deactivate` rewritten.
- [x] `docs/database_schema.mmd` — `APP_EXAM_SANDBOXES` replaces `APP_STUDENT_SANDBOXES`; renders cleanly via `mmdc`.
- [x] `CHANGELOG.md [2026-05-07] - Sandbox Redesign: Shared per-exam DML + read-only Query`.

### Verification (after deploy / integration testing)
- [ ] Two students enrolled in the same exam, both submitting the same DML question: only one is inside the savepoint window at a time (verify via `pg_locks`); both see correct scores.
- [ ] Student submits a SELECT whose body is `WITH x AS (UPDATE employee SET salary=0 RETURNING *) SELECT * FROM x` — fails with permission error from `exam_student_readonly` role, NOT a successful zero-out of the salary column. Score = 0 with `validation_details` capturing the role-permission error.
- [ ] On `DeactivateExamination`, `sandbox_exam_{examID}` is dropped exactly once; `app.exam_sandboxes.dropped_at` is set.
- [ ] Storage check: 150 students enrolled, 6 tables, exam active → exactly 6 tables in `sandbox_exam_X`, not 900.

## DONE Frontend UI/UX — P0 / P1 / P2 rounds (2026-05-22 → 2026-05-23)
Three sequential pipeline rounds against `frontend/src/`. Full per-round detail in `CHANGELOG.md`; `pipeline-state.json` carries ARCH-001..032 + TODO-001..089 with completed/failed history.

### DONE P0 — blocking issues
- [x] Reactive auth store (`services/auth.ts`) — `App.vue` and router consume `useAuth().isLoggedIn` instead of synchronous `localStorage` reads.
- [x] `StudentMultiSelect.vue` (role=listbox, keyboard nav, search, Select All/None) replaces comma-separated student ID textarea in `LecturerView > Assign Students`.
- [x] `NotFoundView` (`path: '/:pathMatch(.*)*'`) and `ForbiddenView` (route name `forbidden`) — kept reachable for logged-in users via Rule 1 name exception.
- [x] Mobile responsiveness < 768 px: viewport meta, fluid widths, stacking nav.
- [x] Router rewritten as a single `beforeEach` with four explicit rules + `RouteMeta` augmentation.

### DONE P1 — major usability issues
- [x] `useFormValidation` composable + `<FieldError>` (role=alert) integrated into LoginView, LecturerView, AdminView, PracticeAuthoring, BulkImportStudents.
- [x] `useSingleFlight` wraps every mutation handler (StudentView, LecturerView, AdminView, PracticeAuthoring).
- [x] `<BackLink>`, `<EmptyState>` (role=status), `<Skeleton>` (line/block/table-row variants, `prefers-reduced-motion`), `.sticky-table-head` CSS.
- [x] `<Spinner>` component on primary submit buttons.
- [x] `<ConfirmDialogHost>` (Teleport, role=alertdialog) + `useConfirm()` service replace every `window.confirm()` callsite.
- [x] `services/error_handler.ts` (`handleApiError`, `toApiError`) — every mutation/fetch catch routed through it; `LoginView` keeps its inline form-level error per ARCH-018.
- [x] `useAutosave` (debounced localStorage with `restoreDraft`, `clearDraft`, `flush`) — integrated into `PracticeAuthoring`; `LecturerView` ships per-exam-key variant.
- [x] Router ADMIN wildcard (`meta.adminBlocked` opts out; `/practice` opts out to protect analytics).
- [x] `frontend/vite.config.ts` switched to `defineConfig` from `vitest/config` (unblocks `vue-tsc -b`).

### DONE P2 — polish
- [x] `useKeyboardShortcut` composable (whenTyping, scope) — Ctrl/Cmd+Enter submit in StudentView/PracticeView/LecturerView; Escape in ConfirmDialogHost + hamburger drawer.
- [x] SqlEditor: `Mod+/` line-comment toggle + Format SQL button (powered by `sql-formatter` 15.x, language=postgresql, keywordCase=upper). Autocomplete deferred.
- [x] Quiz progress strip in StudentView, seeded from `/api/v1/quiz/results/:examId` on exam change.
- [x] `ResultTable` overflow detection via `ResizeObserver` + `.scroll-hint` badge.
- [x] `utils/format_date.ts` (`formatDate`, `formatRelative`) with `Intl.DateTimeFormat('en-US', …)`; consumers in StudentResults + PracticeView; all `.toLocaleString()` callsites removed.
- [x] `useFocusTrap` composable — wired into `ConfirmDialogHost` and hamburger drawer.
- [x] Autofocus on AdminView create-user form first input.
- [x] WCAG AA contrast bump: `--color-text-muted` to `#4b5563` (light) / `#d1d5db` (dark).
- [x] LecturerView question table sort (ID/Type/Points) + filter input.
- [x] App.vue mobile hamburger drawer (matchMedia-gated focus trap, Escape + route-change auto-close, active-route bottom border) + `role="main"`.
- [x] `<form novalidate>` on validated forms; defensive `aria-label`s.
- [x] `constants/ui_copy.ts` (`UI_COPY`) — governed button copy consumed by App.vue + 4 views.

### Vitest coverage
- 22 test files, **198/198 passing** at end of P2 round.

## DONE Requirements batch (2026-05-25, round `reqbatch-2026-05-25`)
Source: deleted `NEW_ADDITION_REQUIREMENT.md` (10 items). Full design ARCH-001..011 / TODO-001..027 in `.pipelinestates/pipeline-state.json`; narrative in `CHANGELOG.md § [2026-05-25]`. 27/27 TODOs landed, frontend 209/209 + backend hub/effective-state suites green.
- [x] Timestamp-driven **lazy** activation windows: `scheduled_start_at`/`scheduled_end_at` + single `ExaminationEffectiveState` helper (`[start,end)`), lazy sandbox prep, no background scheduler.
- [x] SSE live exam-start: `sse_hub.go` + `GET /quiz/exams/:id/stream` (token-query auth, outside bearer group) + `useExamStream.ts` (15 s fallback poll) + nginx SSE passthrough. WebSocket/Nginx-LB rejected.
- [x] Student SELECT→WAITING→ACTIVE flow; enrolled-exams endpoint for pre-activation selection.
- [x] Lecturer question-ID dropdown (existing vs new) + override confirm before upsert.
- [x] Scheduled activate/deactivate UI (RFC3339 UTC) + effective-state badges.
- [x] Exam roster: assigned count + per-row un-assign (guarded while active).
- [x] "New Practice Question" button + editing indicator.
- [x] Lecturer schema import enabled (RoleLecturer) + `app.schema_import_audit` + UI confirm (user-approved security re-opening).
- [x] Score clarity: `/lecturer/exams/max-scores` + earned/max + % per exam.
- [x] Renamed exam enrollment to "Exam Roster"/"Enroll Students" (internal tab key kept `'assignments'`).

### Follow-ups from this round (open)
- [x] **PRE-EXISTING race (pre-dates this batch):** data race in `backend/internal/services/submission_queue.go` (`TestSubmissionQueue_ShutdownWaitsForInFlight` under `-race`). **Fixed 2026-07-11:** (1) worker now runs `fn` inline via `runJob` instead of a detached goroutine, so the worker's WaitGroup registration genuinely covers in-flight work (no orphaned goroutine on ctx timeout); (2) added `sync.RWMutex` + `closed` flag — `Run` sends under `RLock`, `Shutdown` sets `closed`+`close(jobCh)` under exclusive `Lock`, so the send can never race the channel close (replaced the `recover()`-on-send hack). Full `internal/services` suite green under `-race`.
- [ ] `SetExaminationActive` `end <= start` → return a 400 sentinel instead of a bare error (currently maps to HTTP 500).
- [x] Re-sync spec docs for the new surface. **Done 2026-07-11:** `API_SPEC.md` fully rewritten against `main.go` routes (~70 endpoints incl. SSE stream, scheduled activate/deactivate payloads, roster GET/remove/assign, max-scores, consolidated scores, email/password flows, intro-images, per-exam curriculum import, full practice-categories surface; corrected drifted paths like `/user/password`, `/admin/users/*`, `practice_question_id`). `ARCHITECTURE.md`: added effective-state/scheduling + lazy-sandbox + SSE Hub sections, full `app.*` table inventory (14 tables), Identity/Auth/Password section, and corrected the non-existent `export-scores` endpoint to client-side CSV from `/lecturer/scores`. `MANUAL.md`: per-exam curriculum import, instant-vs-scheduled activation, student waiting page, forgot-password flow, admin password actions, practice categories, corrected reset-sandbox section. `PROJECT.md`: scheduling note + SSE + email-auth + practice-categories feature bullets. `docs/database_schema.mmd`: added `app.password_events` entity + `email`/`token_version`/`last_forgot_reset_at` on `app.system_users` (all 14 `app.*` tables now present); re-rendered `database_schema.png` via `mmdc`.
- [ ] Optional: per-question score drill-down for the lecturer Results tab (deferred from ARCH-011).
- [ ] Cleanliness: fold `ListExaminations`'s inline `ExaminationResponse` build into the `examToResponse` helper to prevent field drift.

## DONE Auth + roster fixes (2026-07-13)
- [x] **Admin reset-password email toggle.** `AuthService.ResetPassword` gained a trailing `emailPassword bool`; it previously hardcoded `SendCredential("")` so the new password was NEVER emailed (only returned to the admin UI). Now emails the plaintext password to the target ONLY when `emailPassword` is true AND the target has an address on file — default OFF (plaintext-in-email is a deliberate opt-in). Controller `POST /users/:id/reset-password` binds an optional `{"email_password":bool}` body (absent ⇒ false) and echoes `email_requested`. Regression test `TestResetPassword_EmailsPasswordWhenToggled` (spy sender, both toggle states). Files: `auth_service.go`, `user_controller.go`, `password_flows_integration_test.go`, `password_http_integration_test.go`.
- [x] **student_number de-uniqued (was silently collapsing students).** `student_number` is duplicate-able by design, but the schema enforced a UNIQUE index AND the roster upsert keyed `ON CONFLICT (student_number)` → importing two students with the same non-null student_number made the second OVERWRITE the first (silent identity loss). Fixed: idempotent migration drops the UNIQUE index only if still unique and recreates it NON-unique; `UpsertStudentByNumber` now keys `ON CONFLICT (student_id_ext)` (the login, already unique) and treats `student_number` as a mutable attribute. Read-only audit `scripts/audit_student_number_duplicates.sql`. Regression tests in `postgres_student_import_test.go` (dup number keeps both rows / same student_id_ext updates in place / index is non-unique). ⚠️ Pre-fix prod DBs may have ALREADY collapsed duplicate-student_number students — unrecoverable from data alone; run the audit. Files: `postgres_schema.go`, `postgres_users.go`, + 2 new.
- Verified live end-to-end (fresh Postgres + AUTH SMTP sink, real HTTP): reset toggle off→no email / on→emailed to target; duplicate `SN-DUP` import → both rows persist (ids 78,79); re-import same student_id → updated in place, row count stable. Full `repository`+`services`+`controllers` suites green against real DB.

## Practice feature follow-ups
- [ ] **Bulk import:** Import practice questions from CSV (question text, type, target table, correct SQL, points) via a single lecturer API call.
- [ ] **Per-question attempt limits:** Allow lecturers to cap how many attempts a student can make on each practice question (e.g., max 3 tries).
- [ ] **Leaderboard / attempt statistics:** Aggregate per-student scores across practice questions; expose a leaderboard endpoint for both lecturers and students.
- [x] **Race-safe sandbox reset:** `ResetPracticeSandbox` now holds `pg_advisory_lock(FNV1a("sql_quiz/practice:reset"))` EXCLUSIVELY on a dedicated session connection for the whole drop+re-clone; each practice evaluation takes the same key SHARED (`pg_advisory_xact_lock_shared`) for the life of its tx (only when the schema is `sandbox_practice` — exam/public evals skip it and pay nothing). Shared holders run in parallel; the reset waits for every in-flight eval to finish and blocks new ones, so no student SQL can run against a half-rebuilt schema. Note: the queue (`SubmissionQueue`) does NOT cover this — practice submits bypass the queue and reset is not a submission. Regression tests: `postgres_practice_reset_lock_test.go` (key-stability unit test + real-Postgres block-then-proceed integration test); full `repository`+`services` suites green under `-race`.

## Exam categories + bulk password reset — open follow-ups (2026-07-28)
Source: ARCH-026..036 (per-question exam categories with a byte-identical-migration guarantee, plus the admin bulk password-reset endpoint). Full design/TODO history in `pipeline-state.json`.
- [ ] **No rate limiter on the bulk password-reset endpoint (ARCH-036, resolution of OQ-5).** `POST /api/v1/users/bulk-reset-password` has no rate limiting of any kind. The only rate limiter that exists anywhere in this codebase is the per-username login-attempt throttle in `AuthService.Login` (5 attempts / 10 minutes, in-memory) guarding the public `/login` endpoint — it does not apply here, since this route is reached with an already-valid Admin JWT. A compromised or careless admin session can therefore call it repeatedly with no server-side brake; the one structured audit log line it writes per request (actor, mode, `exam_id`, requested/succeeded/failed counts, affected user ids) is currently the **only** detective control. Shipped as-designed (ARCH-032 accepted residual (a)) — revisit if this becomes a real operational concern.
