# CHANGELOG.md

## [2026-08-10] DML search_path drops the public fallback (branch `fix/dml-public-fallback`)

**Behaviour change for lecturers, not an internal-only fix: a DML question whose SQL relies on an unqualified table name that only exists in `public` may now fail — at save/capture time if re-saved, or at grade time if not.** Read "Before deploying" below before rolling this out to a database that holds real lecturer content.

### Fixed
- DML capture and grading no longer resolve unqualified table names through
  `public`. A lecturer's SQL that named a table absent from its own category
  schema previously fell through to a table shared by every category — a
  silent cross-category read or write, and a lock the per-schema DML gate
  (ARCH-058…061) cannot see, so two categories grading concurrently could
  deadlock (`SQLSTATE 40P01`) and the failure was reported to the student
  instead of the lecturer. `, public` is dropped from `search_path` at all
  five sites that must agree on name resolution: both evaluation sites
  (`postgres_evaluation.go:308` `evaluateDML`, `:604` `evaluateDMLPreview`),
  both capture sites (`postgres_questions.go:129` exam capture,
  `postgres_practice.go:499` practice capture), and the concurrency test's
  hand-replicated eval transaction (`postgres_practice_concurrent_dml_test.go`)
  — moved together in one commit so a question can no longer capture green
  against `public` and then fail at grade time. `pg_temp` stays named last at
  the two evaluation sites (ARCH-023); the two capture sites have no
  `pg_temp` and gained none. `postgres_core.go`'s `queryWithTx`/
  `queryWithTxCols` deliberately keep their `, public` element: the only DML
  path reaching them is the post-image `SELECT` on `target_table`, which
  `ResolveTargetTable` has already resolved against the question's own
  schema *before* capture runs, so it never needed the fallback in the first
  place. (Commits `4570931`, `e5a9462` — audit script; `9113c24`, `df57eef`,
  `2fd72cf` — the five-site change and its tests.)

### Added
- `scripts/audit_public_fallback_questions.sql` — a **read-only** catalog
  report (`BEGIN; SET TRANSACTION READ ONLY`) that lists, per *execution
  schema*, every **name** in `public` that is absent from that schema — i.e.
  the names the dropped fallback could still have resolved there. It is a
  deliberate **over-approximation of names, not a per-question verdict**: a
  reported name may be referenced by no question at all, and the report never
  says which question is broken. A non-empty report is a worklist to check by
  hand. Questions whose `target_table` itself lives only in `public` are
  **deliberately excluded** — `ResolveTargetTable` already refuses them at
  save time regardless of `search_path`, so they need no deploy gate
  (`scripts/audit_dml_vacuous_questions.sql` sections 2a/2b report those).

  **The claim an empty report makes is bounded, and the script's header states
  the bound**: for the execution schemas it enumerates and the object classes
  it enumerates, an empty report means no name of those classes in `public`
  was reachable through the fallback there. It is *not* a claim that no
  question can break for any other reason. Covered object classes:
  **relations** (`pg_class` relkinds `r p v m f S`), **routines**
  (`pg_proc` — functions, procedures, aggregates, window functions; compared
  by NAME, not by argument signature) and **types** (`pg_type` — standalone
  composites, domains, enums, ranges, multiranges). Explicitly **not** covered:
  operators, operator classes/families, collations, conversions, text-search
  configurations/dictionaries/parsers/templates and statistics objects, which
  `search_path` also resolves but which nothing in this codebase creates; and
  the QUERY paths, which do not use the five DML `search_path` sites.

  It audits the schemas DML *actually executes in*, which differ between
  capture, grading and preview: practice capture and grading both use
  `practice_categories.schema_name`; exam **capture** uses
  `exam_categories.schema_name` (categorised) or
  `COALESCE(NULLIF(curriculum_schema,''),'public')` (uncategorised); exam
  **grading** uses the sandbox — `sandbox_exam_cat_<category_id>` when the
  category owns its schema, otherwise `sandbox_exam_<exam_id>` — and *never*
  `curriculum_schema`; **DML preview** of a `dual_schema` question runs in a
  *decoy* schema that is none of those — `practice_categories.preview_schema_name`,
  `sandbox_exam_cat_<category_id>_preview`, or `sandbox_exam_<exam_id>_preview`
  (`postgres_evaluation.go:596-604`, `quiz_controller.go:370,382`,
  `practice_controller.go:291-294`). The sandboxes matter on their own because
  they are partial clones: `GetTablesInSchema` reads `pg_catalog.pg_tables`, so
  only ordinary and partitioned tables are cloned — **views, materialised
  views, foreign tables, sequences, functions and types are never copied into
  a sandbox**, and were reachable only through the fallback. The *preview*
  decoy sandboxes are built by the same tables-only path
  (`InitializeExamPreviewSandbox`), so they are missing exactly the same
  classes; a `dual_schema` exam DML question reading a `public` view can break
  every student's **preview** while grading still works, which is why preview
  is its own section rather than being assumed identical to grading. There is
  no `<> 'public'` exclusion on the exam side, so legacy exams still on the
  default `curriculum_schema='public'` are audited rather than skipped.
  Sections: 1 practice, 2 exam capture, 3 exam grading, 4 DML preview decoys,
  5 `public` routines absent from any execution schema, 6 `public` types
  absent from any execution schema, 7 execution schemas that do not exist yet
  (typically a sandbox for an exam never activated, or a decoy nobody has
  previewed into — reachability there is *unknown*, not zero), 8 verdict
  (`practice_dml_total`, `exam_dml_total`, `practice_categories_affected`,
  `exam_capture_targets_affected`, `exam_grading_targets_affected`,
  `preview_targets_affected`, `routine_targets_affected`,
  `type_targets_affected`, `exec_schemas_missing`), each count derived from the
  same CTE and the same section predicate as the section it summarises, so the
  two cannot drift. Run it with
  `psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql`.

### Tests
- `TestPracticePreview_NoPublicFallback_RejectsUnqualifiedPublicOnlyTable`
  (`backend/internal/repository/postgres_public_fallback_test.go`) pins the
  preview site (`postgres_evaluation.go:604`), which was the one production
  `search_path` site with nothing holding it: restoring `, public` there alone
  previously left the whole suite green, so a partial revert could reopen the
  hazard silently. Like its grading counterpart it asserts on the feedback
  text containing `does not exist`, not merely on an error status —
  `evaluateDMLPreview` returns an error status for *any* failing student
  statement, so a status-only assertion would pass for the wrong reason.

### Before deploying
**Run the audit script against your own database first**, not just trust this note:
`psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql`.
The report covers **capture, grading and preview**, which execute in three
different schemas — in particular exam grading runs in `sandbox_exam_cat_<cat>` /
`sandbox_exam_<exam>`, never in `curriculum_schema`, DML preview of a
`dual_schema` question runs in a decoy schema that is neither, and every
sandbox is only a partial clone (no views, matviews, foreign tables,
sequences, functions or types). Read each count as follows:

- Non-zero `practice_categories_affected` — **a grade-time count, not only a
  save-time one.** Practice capture *and* practice grading run in the same
  schema, so a name reachable there breaks both: the lecturer's re-save, and
  every student's submission at grade time, including the `seed_sql` /
  auto-seed replay that runs under the same `search_path` before the student's
  statement.
- Non-zero `exam_capture_targets_affected` — save-time only: a lecturer
  re-saving a question in that schema may now see capture fail. Existing
  captures are unaffected, because exam grading does not run in the capture
  schema.
- Non-zero `exam_grading_targets_affected` — **grade time for exams**: those
  questions already captured green and can break for every student. This is
  the count that catches a legacy exam whose DML read a `public` view or
  sequence that activation never cloned into the sandbox.
- Non-zero `preview_targets_affected` — student **preview** of `dual_schema`
  DML questions breaks in the decoy schema. Grading is unaffected in this case
  (it runs elsewhere), so it is lower severity than the two grade-time counts —
  but it is a per-student, every-attempt failure, not a lecturer-only one.
- Non-zero `routine_targets_affected` / `type_targets_affected` — a function,
  procedure, aggregate or type in `public` was reachable from an execution
  schema that does not define it. `ensureTablesInSchema` clones tables only, so
  a lecturer-imported `CREATE FUNCTION` in `public` never reaches a sandbox and
  resolved solely through the fallback. Read the row's own `section` column to
  know whether that target is a capture, grading or preview schema — i.e.
  whether the failure lands at save time, grade time or preview time.
  Extension-provided routines in `public` (uuid-ossp, pgcrypto, …) are listed
  too, and are genuinely at risk for the same reason.
- Non-zero `exec_schemas_missing` — the audit could **not** answer for those
  targets, because the schema (usually a sandbox for an exam that has never
  been activated, or a decoy nobody has previewed into) does not exist yet.
  Treat them as potentially affected and re-run the audit after
  activating/previewing, rather than reading a zero elsewhere as all-clear.

A reported name is a name that *could* be reached, not a broken question:
confirm by hand whether the listed DML questions actually reference it.
Affected questions fail at grade time after this deploys until a lecturer
re-saves them (re-saving re-runs capture under the corrected resolution,
which either succeeds against the question's own schema or surfaces the
problem to the lecturer instead of the student). **There is no automatic
re-capture** — that is a deliberate decision, not an oversight: auto
re-capture would run arbitrary lecturer SQL unattended at deploy time. The
audit output is a worklist a human must work through, not something the
deploy can resolve on its own.

**The audit run during development reported all counts zero — this
is NOT evidence the change is safe.** It was run against the local
`docker-compose` stack, which holds only the `app` and `public` schemas:
no practice categories and no exams exist there at all. The gate proved
the feature *unused* on that database, not the change *safe*. Re-run it
on every database that holds real lecturer content before trusting a zero
there.

## [2026-08-10] Seed SQL may no longer name another schema (branch `fix/seed-schema-qualification`)

**Behaviour change for lecturers, not an internal-only fix: a seed that saved yesterday may be refused today — on save AND at grade time.** Read the "Before you deploy" note below before rolling this out to a database that holds real lecturer seeds.

### Changed
- **A DML question's seed SQL is refused if it names another schema** (`validateSeedSQL`, `backend/internal/repository/dml_autoseed.go`). `INSERT INTO public.employee (…) VALUES (…)` in a seed block is now rejected with a 400 naming the offending schema and telling the lecturer to use an unqualified table name that exists in the question's own schema. Why: seed replay runs inside the grading transaction as a role holding DML on *every* real schema, so a schema-qualified target takes locks the ARCH-059 per-schema gate cannot see — the gate keys on the execution schema alone. Two categories graded at the same time whose seeds write the same shared table in opposite order then deadlock (`SQLSTATE 40P01`), and the *student* is blamed for the lecturer's SQL.
- The check reuses the existing `schemaQualifiedNameRe` denylist from `postgres_schema.go` (the same rule that keeps a category import inside its own schema), plus a new character scanner `stripSQLLiterals` that blanks string-literal bodies first — so a schema name inside a `VALUES` literal stays data and is not refused.
- Enforcement is on **both** the save paths (`postgres_questions.go`, `postgres_practice.go`) and the replay path, so an already-persisted seed that names a schema stops grading, not just stops saving. This is the reason for the deploy note below.

### Added
- `scripts/audit_seed_sql_qualified_names.sql` — a **read-only** report (`SET default_transaction_read_only = on`, `BEGIN`/`ROLLBACK`) listing every already-persisted practice and exam seed whose SQL names a schema, plus totals. Run it with `psql "$DATABASE_URL" -f scripts/audit_seed_sql_qualified_names.sql`. It scans raw `seed_sql` while the Go check strips literals first, so it over-reports: a seed matching only inside a quoted value is listed but will not actually be refused. Read each row before acting on it.

### Before you deploy
**Run the audit script against your own database first.** The pre-ship audit reported `practice_questions_affected=0` and `exam_questions_affected=0` — but also `practice_seeds_total=0` and `exam_seeds_total=0`: it was run against the local `docker-compose` stack, which has no lecturer seeds at all. The change is therefore **not** proven against real seed data; the audit passed because the feature was unused there. On a database that holds real seeds, every row the script lists needs a lecturer to edit that seed before this version goes live, or those questions stop grading.

### Known limitations (recorded in full in `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`, *Known limitations*)
- **The refusal is not complete, and must not be described as if it were.** `INSERT INTO "public"."employee" …` — a **double-quoted** schema name — is *not* refused and would genuinely execute a cross-schema write: the shared regex needs the schema word followed by optional whitespace and a dot, and a closing `"` is neither. Not closed here because widening that regex would change behaviour for its other caller, `ImportSchemaIntoCategory`, which has accepted the same bypass since ARCH-027.
- Two further bounds degrade to a lecturer-visible error rather than a silent write: a non-ASCII dollar-quote *tag* (`$é$…$é$`) is not recognised as a delimiter and the seed fails at exec time on the one-command-per-Parse limit; and `E'it\'s'` backslash escapes are refused upstream as "an unterminated quoted value" (pre-existing over-strictness in the seed path).
- **This closes the schema-qualified half only. The unqualified case is still open, and is the more likely one in practice** — a lecturer writes `INSERT INTO employee …`, not `INSERT INTO public.employee …`. If that bare name does not exist in the question's own schema it falls through `search_path` to `public` and deadlocks in the original shape. Closing it means removing `public` from the DML `search_path` at `postgres_evaluation.go:301` and `:597` together with the two capture sites `postgres_questions.go:129` and `postgres_practice.go:461` — deliberately out of scope for this branch.

## [2026-05-25] Requirements batch — scheduled exams, SSE live-start, exam roster, lecturer schema (round `reqbatch-2026-05-25`)

Source: the now-deleted `NEW_ADDITION_REQUIREMENT.md` (10 items). Designed as ARCH-001..011 / TODO-001..027 in `.pipelinestates/pipeline-state.json` (this round; prior P0/P1/P2 history archived to `.pipelinestates/pipeline-state.json.bak.before-reqbatch-2026-05-25`). Four user decisions (2026-05-25): SSE realtime over Nginx-LB/WebSocket; lecturers may import schema; timestamp-driven **lazy** scheduling (no background scheduler); scheduled-start fires via fallback-poll (no `time.AfterFunc`); a passed end-time stops submissions but keeps the exam armed (no auto-archive).

### Added
- **Timestamp-driven lazy activation windows** (ARCH-001) — `app.examinations` gains nullable `scheduled_start_at` / `scheduled_end_at` (idempotent DDL in `InitializeSystemTables`). One authoritative helper `ExaminationEffectiveState(exam) -> {phase: inactive|active|ended, ActiveNow}` (`backend/internal/repository/postgres.go`) with half-open `[start, end)` semantics is consulted by the activation gate, submission gate, questions gate, and the SSE push. "Instant" = start now / no end. Sandbox is cloned **lazily** on first access inside the window (`EnsureExamSandboxReady`), so pure-SELECT and not-yet-started exams never clone. Activation pre-flight (every question `expected_result_status='captured'`) still runs the moment `is_active` flips true, even for a future-start schedule.
- **SSE live exam-start** (ARCH-002/003/009) — new in-process pub/sub `backend/internal/services/sse_hub.go` (`Hub`, keyed by examID) and `GET /api/v1/quiz/exams/:id/stream` (registered **outside** the bearer-auth group; authed via `?access_token=` since `EventSource` can't set headers). Emits a snapshot of current effective state on connect (no activation/connection race), ~20 s keepalive comments, and a `start` event on `Hub.Broadcast`. Frontend composable `frontend/src/composables/useExamStream.ts` consumes it with a 15 s fallback poll (the poll is what crosses a scheduled-start boundary and triggers lazy activation + broadcast). `frontend/nginx.conf` gains a dedicated SSE location (`proxy_buffering off`, `X-Accel-Buffering: no`, long read timeout) ahead of `/api/`. **WebSocket and Nginx-LB rejected**: a one-way start signal needs neither bidirectional framing nor more boxes to spread polling that SSE removes outright.
- **Student 3-state flow** (ARCH-003) — `StudentView.vue` rewritten to SELECT → WAITING (live "exam not yet active" page) → ACTIVE; the old immediate-questions path is gone. New `GET /api/v1/quiz/exams` enrolled-exams variant surfaces not-yet-active exams for pre-activation selection.
- **Question-ID dropdown + override guard** (ARCH-004) — `LecturerView.vue` replaces the raw numeric question-ID input with a dropdown (existing question vs "create new"); choosing an existing id routes the save through `useConfirm()` **before** the upsert.
- **Scheduled activation/deactivation UI** (ARCH-005) — datetime-local pickers (converted to RFC3339 UTC via `new Date(localStr).toISOString()`), four activation modes, and effective-state badges. `PUT /lecturer/exams/:id/activate|deactivate` payloads extended (`{active, mode, scheduled_start_at?, scheduled_end_at?}`); `{active:true}` stays backward-compatible = instant.
- **Exam roster monitoring + un-assign** (ARCH-006) — `GET /lecturer/exams/:id/roster` (assigned students + count) and `DELETE` un-assign (`RemoveStudentFromExam`, guarded so it's rejected while the exam is effectively active); roster table with per-row Remove in `LecturerView.vue`.
- **"New Practice Question" button** (ARCH-008) — explicit create-new action + editing indicator in `PracticeAuthoring.vue`.
- **Lecturer schema import with audit** (ARCH-010) — `app.schema_import_audit` table; `ExecuteSchemaScript(ctx, sql, importedBy)` writes an audit row with the importer's user id; UI confirm dialog.
- **Score clarity** (ARCH-011) — `GET /lecturer/exams/max-scores` (registered before `/:id` routes to avoid Gin param shadowing); Results tab now shows earned/max + percentage per exam.

### Changed
- **Exam enrollment renamed** to "Exam Roster" / "Enroll Students" (`LecturerView.vue`, `ui_copy.ts`) to stop colliding with practice-assignment naming. The internal `activeTab` key value is kept as `'assignments'` to avoid a stale-localStorage blank-tab regression.
- **Import-schema route widened** to `models.RoleLecturer` (`backend/cmd/main.go`), a deliberate, user-approved re-opening of a previously Admin-only surface — mitigated by the audit table + UI confirm.
- `GetExamByID` / `GetAllExaminations` / `GetStudentExams` SELECTs widened to carry `scheduled_*` / `activated_at` / `archived_at`; controller signatures `NewQuizController(...hub, authService)`, `NewLecturerController(repo, hub)` extended.

### Fixed (during pipeline review — Codex CLI rate-limited until 2026-05-29, applied inline)
- Two truncated `fmt.Sprintf` format strings that broke `go build` (`buildExamStatusEvent`; lazy-broadcast in `GetExamQuestions`, which now reuses the shared formatter).
- **SSE hub send-on-closed-channel race** (`sse_hub.go`): `Broadcast` now holds the read lock across its non-blocking send loop and `Unsubscribe`/`Close` close channels under the write lock with an idempotent membership guard. Doubled `event:`/`id:` header in the stream consumer removed.

### Tests
- Frontend: **209/209** vitest (23 files). New: `__tests__/composables/useExamStream.test.ts` (11).
- Backend: `services/sse_hub_test.go` (7, incl. a 50-goroutine concurrency case clean under `-race`) and `repository/effective_state_test.go` (8, table-driven phase matrix).

### Known issues / follow-ups (NOT in this round's scope)
- **PRE-EXISTING data race** in `backend/internal/services/submission_queue.go:97/113` (`TestSubmissionQueue_ShutdownWaitsForInFlight` under `-race`). The file is git-unmodified this round and unrelated to these requirements; all services tests pass without `-race`. Surfaced for a future fix.
- `SetExaminationActive`'s `end <= start` returns a bare error → HTTP 500 instead of 400; wants a sentinel.
- `GetExamMaxScores` keys by exam title (non-unique titles would collide) — matches the ARCH-011 spec, acceptable for current usage.
- Spec docs (`API_SPEC.md`, `ARCHITECTURE.md`, `MANUAL.md`, `PROJECT.md`) not yet re-synced for the new endpoints/flows — see TODO.md.

## [2026-05-23] Frontend UI/UX — P2 polish

### Added
- **Keyboard shortcuts** — Ctrl/Cmd+Enter submits the active SQL editor in `StudentView`, `PracticeView`, and `LecturerView` (scoped to editor / question-form wrapper refs so it does not fire from unrelated inputs). Escape closes confirm dialogs and the mobile drawer. New composable `frontend/src/composables/useKeyboardShortcut.ts` with `whenTyping` and `scope` options.
- **SQL editor productivity** — `Mod+/` toggles line comments (CodeMirror `toggleLineComment`); a **Format SQL** button (powered by `sql-formatter` 15.x, language=`postgresql`, keywordCase=`upper`) is wired into all three editor surfaces. Autocomplete deferred.
- **Quiz progress strip** — `StudentView` shows answered / correct / total + percentage. `progressMap` is seeded from `GET /api/v1/quiz/results/:examId` on exam change and updated live on each submission.
- **Result-table overflow affordance** — `ResultTable.vue` toggles `data-overflow="true"` via `ResizeObserver` and renders a `.scroll-hint` badge when the table is wider than its container.
- **Relative-time tooltips** — `StudentResults.vue` and `PracticeView.vue` timestamps show the absolute date and an inline relative span (`(2 hours ago)`); the `title` attribute carries the ISO-8601 string for hover. New utility `frontend/src/utils/format_date.ts` (`formatDate`, `formatRelative`) using `Intl.DateTimeFormat('en-US', …)`.
- **Focus trap** — `frontend/src/composables/useFocusTrap.ts` (Tab/Shift+Tab cycle, focus restore on deactivation). Wired into `ConfirmDialogHost` and the mobile hamburger drawer.
- **Mobile hamburger nav** — `App.vue` ships a hamburger button + slide-in drawer below the 767.98 px breakpoint with `role="main"` on content, `matchMedia`-gated focus trap, Escape and route-change auto-close, and active-route bottom border.
- **Sortable + filterable question table** — `LecturerView.vue` ID/Type/Points headers toggle ascending/descending; a filter input narrows `displayedQuestions` in real time.
- **Governed copy** — `frontend/src/constants/ui_copy.ts` (`UI_COPY`, `UICopyKey`) centralises button labels (Login / Logout / Submit / Cancel / etc.) consumed by `App.vue`, `LoginView`, `StudentView`, `PracticeView`, `LecturerView`.

### Changed
- **WCAG AA contrast bump** — `--color-text-muted` raised from `#6b7280` to `#4b5563` (light, ~7.4:1 on white, AAA) and `#d1d5db` (dark, ~12:1 on black, AAA) in `frontend/src/style.css`.
- **`<form novalidate>` on validated forms** — `LoginView`, `AdminView`, `LecturerView` opt out of native HTML5 validation so the `useFormValidation` composable owns error display.
- **`aria-label` defensively added** to inputs whose only label is a placeholder.

### Removed
- All `.toLocaleString()` and `.toLocaleDateString()` callsites swept from `frontend/src/` in favor of `formatDate`/`formatRelative` for consistent en-US output regardless of browser locale.

### Tests
- 22 vitest files, **198/198 passing** (+ 52 new in this round). New suites: `keyboard_shortcut_scope`, `utils/ui_copy`, `components/timestamp_tooltip`, `components/result_table_overflow`, `components/lecturer_sort_filter`, `components/student_view_progress`, `components/confirm_dialog_focus`, `components/login_novalidate_aria`, `components/App.test.ts`.

## [2026-05-22] Frontend UI/UX — P1 major-issue round

### Added
- **Inline form validation** — `frontend/src/composables/useFormValidation.ts` (`required`, `minLength`, `maxLength`, `validateAll`, per-field `markTouched`) integrated into `LoginView`, `LecturerView` question form, `AdminView` create-user form, `PracticeAuthoring` form, and `BulkImportStudents` file picker. Errors render via the new `<FieldError>` (`role="alert"`) with `aria-invalid` and `aria-describedby` bound on each input.
- **Double-submit guard** — `frontend/src/composables/useSingleFlight.ts` wraps every mutation handler in `StudentView`, `LecturerView`, `AdminView`, `PracticeAuthoring` (synchronous `inFlight` flag; re-entry returns `undefined`).
- **Back navigation** — new `<BackLink>` component used on `PracticeAuthoring`, `StudentResults`, etc.
- **Empty / loading states** — new `<EmptyState>` (`role="status"`, dashed border) and `<Skeleton>` (line / block / table-row variants, `prefers-reduced-motion` honored) replace bare "Loading…" text across `LecturerView` and `AdminView`.
- **Sticky table heads** — `.sticky-table-head` CSS pattern (configurable max-height via `--sticky-max-h`) applied to `LecturerView` questions table, `StudentResults` detail table, and `ResultTable`.
- **Spinner component** — `<Spinner>` (sm/md/lg, inline/invert) shipped and wired into primary submit buttons.
- **Custom confirm dialog** — `<ConfirmDialogHost>` (`Teleport`, `role="alertdialog"`, Escape/Enter handling) + `useConfirm()` service replace every native `window.confirm()` callsite in `LecturerView` and `AdminView`. Confirms now match the brutalist monotone theme.
- **Centralized error handling** — `frontend/src/services/error_handler.ts` (`handleApiError`, `toApiError`). All mutation/fetch catch blocks in seven components routed through `handleApiError`; `!response.ok` branches converted to `throw await toApiError(res, fallback)`. `LoginView` keeps its inline form-level `error` ref (validated as ARCH-018 exception).
- **Draft autosave** — `frontend/src/composables/useAutosave.ts` (debounced localStorage with `minLength`, `restoreDraft`, `clearDraft`, `flush`) integrated into `PracticeAuthoring`. `LecturerView` ships a hand-rolled per-exam-key variant of the same pattern.

### Changed
- **Router ADMIN wildcard** — `router.ts > beforeEach > Rule 3` now short-circuits to allow ADMIN through any route whose `meta.adminBlocked !== true`. `/practice` opts out (admin submissions would pollute analytics); `/lecturer` and `/lecturer/practice` simplified to `allowedRoles: ['LECTURER']` because the wildcard covers ADMIN.
- `frontend/vite.config.ts` switched to `defineConfig` from `vitest/config` so `vue-tsc -b` accepts the `test` block.

### Tests
- **118/118 vitest passing** at end of round (+ 86 new cases): `useSingleFlight`, `useFormValidation`, `useAutosave`, `confirm_service`, `error_handler`, `router_admin_wildcard`.

## [2026-05-22] Frontend UI/UX — P0 blocking-issue round

### Added
- **Reactive auth store** — `frontend/src/services/auth.ts` (`useAuth`: `user`, `isLoggedIn`, `login`, `logout`, `hydrateFromStorage`) so `App.vue` and the router consume a reactive source of truth instead of synchronously reading `localStorage` on every render.
- **Student multi-select** — `frontend/src/components/StudentMultiSelect.vue` (`role="listbox"`, keyboard navigation, search filter, Select All / None) replaces the comma-separated ID textarea in `LecturerView > Assign Students`. Populated from `GET /api/v1/users/students`.
- **404 + 403 pages** — `NotFoundView.vue` (catch-all `path: '/:pathMatch(.*)*'`) and `ForbiddenView.vue` (route name `forbidden`, RBAC redirects land here). Both kept reachable for logged-in users by exempting their route names from the Rule 1 auth-redirect.
- **Mobile responsiveness** — viewport meta tag added; fluid widths and stacking nav below the 768 px breakpoint.

### Changed
- **Router rewritten** as a single `beforeEach` with four explicit rules: (1) public-route redirect for logged-in users (with `to.query.force === '1'` / `to.name === 'forbidden'` / `to.name === 'not-found'` opt-outs), (2) auth gate, (3) role check, (4) fall-through. The `RouteMeta` augmentation defines `requiresAuth`, `allowedRoles`, and (added in P1) `adminBlocked`.
- `App.vue` no longer reads `localStorage` directly; it consumes `useAuth().isLoggedIn` and `useAuth().user`.

### Tests
- 32 new vitest cases: `auth.test.ts`, `router.test.ts`, `StudentMultiSelect.test.ts`.

## [2026-05-16] Bulk Roster Import & Student Exam Results
### Added
- Lecturer bulk roster import: `POST /api/v1/lecturer/students/import` accepts `.csv` and `.xlsx` files; per-row savepointed upsert keyed on `student_number`; default credentials `username=student_number` / `password=student_number` (bcrypt).
- Student exam-results endpoints: `GET /api/v1/quiz/results` (per-exam summary) and `GET /api/v1/quiz/exams/:id/results` (per-question detail, `DISTINCT ON` latest submission).
- `BulkImportStudents.vue` panel in `LecturerView`; `StudentResults.vue` at `/student/results`.
### Changed
- `app.system_users` gains six nullable student profile columns: `student_number`, `student_id_ext`, `sec_lec`, `sec_lab`, `first_name`, `last_name` (partial `UNIQUE` indexes on each non-null value).
- Re-import preserves existing student `password_hash`; only name and section columns are updated on conflict.

## [2026-05-15] Practice question feature
### Added
- Independent practice question bank (app.practice_questions, app.practice_attempts) separate from exam questions.
- System-wide exam freeze (HTTP 423) on all five student practice endpoints via ExamFreezeMiddleware with 5 s in-process TTL cache.
- Shared sandbox_practice schema with savepoint-rollback DML evaluation (same pattern as exam sandbox).
- pg_advisory_lock namespace practice: + target_table for per-table DML serialisation without colliding with exam locks.
- Ten new API endpoints (5 student, 5 lecturer) under /api/v1/practice/ and /api/v1/lecturer/practice/.

## [2026-04-20] - Initial Documentation & Design
### Added
- Created `GEMINI.md` as the technical source of truth.
- Created `TODO.md` for task tracking.
- Created `DEVELOPMENT.md` for local setup and coding standards.
- Created `API_SPEC.md` for frontend-backend contract.
- Established `docs/` directory with architectural and logic flow diagrams.
- Finalized architectural decisions on schema isolation and submission queueing.

## [2026-04-22] - Core Backend Implementation
### Added
- Initialized Go backend with Gin.
- Implemented `ValidationService` for Query and DML validation.
- Implemented `PostgresRepository` with `pgx`.
- Implemented `QuizController` and basic submission logic.

## [2026-04-24] - Frontend Development & Styling
### Added
- Created Vue 3 frontend project with Vite.
- Implemented `StudentView` with SQL editor and feedback.
- Implemented `LecturerView` for question management.
- Integrated backend API calls.
- Styled UI with scoped Vanilla CSS.

## [2026-04-26] - Auth & RBAC
### Added
- Implemented `system_users` table and JWT-based authentication.
- Added role-based middleware (Admin, Lecturer, Student).
- Integrated bcrypt for password hashing.
- Added hierarchical account creation.

## [2026-04-28] - Examination & Enrollment Features
### Added
- Implemented `examinations` and `exam_students` models.
- Added endpoints for creating, activating, and assigning students to exams.
- Updated `QuizController` to filter questions by exam enrollment.
- Added bulk student enrollment support.

## [2026-04-30] - Schema Isolation & Refinement
### Added
- Implemented `ImportSchema` for custom exam environments.
- Developed dynamic table cloning for per-student sandboxes.
- Implemented `StrictMode` logic for fine-grained grading.
- Containerized the entire stack with Docker Compose.

## [2026-05-02] - Documentation Audit & Security Fixes
### Fixed
- Corrected typos and grammar across all documentation.
- Updated `API_SPEC.md` to match current implementation.
- Refactored `PROJECT.md` to reflect optimized "Lowered Operation" strategy.
- Implemented user password change endpoint.

## [2026-05-02] - Architecture Refactor (Sandbox & Locking)
### Changed
- Moved system tables (`system_users`, `examinations`, `questions`, `exam_students`) from `public` into a dedicated `app` schema. `public` is now reserved for the lecturer's curriculum and can be cloned wholesale into student sandboxes.
- `InitializeSandbox` is now idempotent: it inspects `pg_catalog.pg_tables` and only clones tables that are missing from the student schema. Repeat submissions no longer pay for a full re-clone.
- Sandbox cloning switched from `CREATE TABLE … AS SELECT` to `CREATE TABLE … (LIKE public.x INCLUDING ALL)` + `INSERT … SELECT`, preserving primary keys, NOT NULL, defaults, and indexes.
- Replaced the in-memory `sync.Map`-based `LockManager` with Postgres session-level advisory locks (`pg_advisory_lock`, keyed by FNV hash of the student ID). Per-student serialisation now survives horizontal scaling and no longer leaks process memory.
### Added
- `repository.ResetTable(schema, target)` — rebuilds a single sandbox table from its `public` counterpart. Called from `QuizController.SubmitAnswer` before each DML evaluation so state changes don't leak across submissions.
- `repository.PostgresRepository.Pool()` accessor so the advisory-lock `LockManager` can acquire its own connection from the shared pool.
### Removed
- Hardcoded system-table exclusion list in `GetPublicTables`. After the schema move it is no longer needed.

## [2026-05-04] - Phase Lifecycle Redesign (Spec Docs)
*Spec-doc-only; no code changes yet. Implementation task list lives in `TODO.md § Phase Lifecycle Redesign`.*

### Added (specification only — implementation pending)
- Four-phase evaluation lifecycle: Phase 0 (lecturer prep + Correct Result Capture), Phase 1 (activation pre-flight + freeze), Phase 2 (savepoint-based per-submission eval), Phase 3 (deactivation + sandbox cleanup). Documented in `PROJECT.md § Lowered Operation Strategy`, `ARCHITECTURE.md § Phase Lifecycle`, and `MANUAL.md`.
- New design entities in `app.*`: `app.submissions` (durable per-attempt audit), `app.student_sandboxes` (sandbox bookkeeping with `created_at`/`dropped_at`).
- New design columns on `app.questions`: `expected_result jsonb`, `expected_columns jsonb`, `expected_result_status`, `expected_result_captured_at`, `capture_error`, `created_by`, `created_at`, `updated_at`.
- New design lifecycle columns on `app.examinations`: `activated_at`, `archived_at`.
- New design API endpoint: `PUT /api/v1/lecturer/exams/:id/deactivate` (Phase 3 trigger). Optional: `GET /api/v1/lecturer/exams/:id/submissions` (audit).
- New sandbox naming convention: `student_{studentID}_exam_{examID}` (supersedes `student_{studentID}`).

### Changed
- `PROJECT.md § Lowered Operation Strategy` rewritten as a four phase-annotated lifecycle.
- `ARCHITECTURE.md` Schema Layout, DML Validation, and Sandbox Lifecycle Q&A rewritten around the cached Correct Result and savepoint-based evaluation. New Phase Lifecycle section added.
- `AGENTS.md` Implementation Status split into "already in code" vs. "redesign in flight" with explicit work list and technical mandates.
- `API_SPEC.md` updated: capture-status response on `UpsertQuestion`, structured 409 on `ActivateExamination`, freeze semantics on `ImportSchema` / `AssignStudents`, new `Deactivate` endpoint, optional submissions endpoint.
- `DEVELOPMENT.md` Database Layout Quick Reference updated for new tables, columns, and sandbox naming.
- `MANUAL.md` lecturer workflow now includes Correct Result capture, activation pre-flight, and deactivation/cleanup steps.
- `README.md` Key Features updated for cached Correct Result, freeze-on-activate, hermetic savepoint evaluation, and durable submission audit.
- `docs/database_schema.mmd` redrawn with the new entities and columns; `%%` comments and `PK_FK` syntax stripped to satisfy the strict erDiagram parser in `mmdc` v11.
- `TODO.md` adds an active "Phase Lifecycle Redesign" sprint. Previously-deferred "savepoint-based DML eval" and "transactional rollback for restoreTable" promoted into it.

### Note
The legacy `ResetTable(schema, target)` per-DML-table reset flow is **superseded** by the Phase 2 savepoint+rollback model. It may be retained transitionally during code migration but should not be referenced by new code.
## [2026-05-06] - Frontend UX Overhaul & Security Hardening

### Added
- **Vue Router:** Proper routing and navigation guards for protected views.
- **CodeMirror 6:** Dedicated SQL editor with syntax highlighting for students and lecturers.
- **Multi-select Enrollment:** Replace manual ID input with a reactive student selection list.
- **Global Notifications:** Centralized toast alert system for user feedback.
- **Submission Audit:** New `GET /api/v1/lecturer/exams/:id/submissions` endpoint for lecturers.
- **Configurable Admin:** Environment variables `ADMIN_USER` and `ADMIN_EMAIL` for secure seeding — the admin password is server-generated and emailed to `ADMIN_EMAIL` (printed to the log if unset), never set by hand.

### Changed
- **Strict-Mode Refactor:** Simplified to `strict_ordering` and `strict_attributes`; removed `strict_record_order`.
- **Security:** Restricted `ImportSchema` to Admin role only.
- **Architecture:** Complete plumbing of `context.Context` through all backend layers.
- **Stability:** Fixed SERIAL desync, transaction helpers, and type mismatches.

## [2026-05-06] - Phase Lifecycle Implementation
...
Code (`models.StrictMode.RecordOrder`, `validation_service.checkSQLOrdering`, frontend "Command Ordering"/"Record Order" checkboxes, DB column `strict_record_order`) still uses the old shape. The follow-up rename is tracked in `TODO.md` and is sequenced after the build-fix items.

## [2026-05-07] - Sandbox Redesign: Shared per-exam DML + read-only Query

### Changed
- **Sandbox model rewritten.** Replaced per-student-per-exam clones with a hybrid model:
  - **DML submissions** share one per-exam sandbox `sandbox_exam_{examID}.*`, cloned from `public.*` once at Phase 1 activation (only when the exam contains DML questions). Students enter a queue keyed on `pg_advisory_lock(FNV1a("sql_quiz/dml/" || examID || ":" || target_table))`; questions targeting different tables proceed in parallel. Per-submission state is reverted via `BEGIN; SAVEPOINT student_eval; … ROLLBACK TO SAVEPOINT student_eval; COMMIT`.
  - **SELECT (Query) submissions** read frozen `public.*` directly under role `exam_student_readonly` (only `USAGE` + `SELECT`). No clone, no lock, no sandbox. Data-modifying CTEs are rejected at the engine level on permissions.
- **Storage:** at 150 students × 6 curriculum tables, the sandbox footprint shrinks from 900 tables to 6 (or 0 for pure-SELECT exams).
- **Concurrency:** SELECT submissions are now fully parallel; DML throughput is bounded only by per-target contention.
- **Bookkeeping:** `app.student_sandboxes` retired in favor of `app.exam_sandboxes (exam_id PK, schema_name, created_at, dropped_at)`. The legacy table is dropped via idempotent migration.
- **Phase 3 cleanup:** Single `DROP SCHEMA sandbox_exam_{examID} CASCADE` instead of looping over per-student schemas. Pure-SELECT exams skip the drop entirely.

### Added
- `(*PostgresRepository).InitializeExamSandbox(ctx, examID)` and `ExamSandboxName(examID)` helper.
- `(*LockManager).ExecuteDMLLocked(ctx, examID, targetTable, fn)` with `dmlAdvisoryKey` derivation.
- Idempotent role bootstrap for `exam_student_readonly` in `CreateRestrictedUser`.

### Removed
- `(*PostgresRepository).InitializeSandbox` and `ResetTable` (per-student, no longer needed).
- `(*LockManager).ExecuteLocked` and `studentAdvisoryKey` (per-student locking superseded by per-target DML lock; SELECT skips locking entirely).
- All references to `student_{studentID}_exam_{examID}` schema naming and `app.student_sandboxes` from spec docs and code.

### Touched
- Code: `backend/internal/repository/postgres.go`, `backend/internal/services/lock_manager.go`, `backend/internal/controllers/quiz_controller.go`.
- Docs: `ARCHITECTURE.md`, `AGENTS.md`, `PROJECT.md`, `README.md`, `MANUAL.md`, `DEVELOPMENT.md`, `docs/database_schema.mmd`, `company_elmasri_postgres.sql` header.

## [2026-05-12] - DML Serialization: Replace Advisory Lock with In-Process FIFO Queue

### Changed
- **Submission queue:** Replaced per-(examID, target_table) Postgres session advisory locks with a single global in-process FIFO submission queue (one worker goroutine, channel capacity 256, 30 s per-job timeout). DML submissions are serialized through this queue; SELECT submissions remain lock-free under `exam_student_readonly`.
- **evaluateQuery timeout:** Added a 30 s `context.WithTimeout` at the start of `evaluateQuery` so a pathological SELECT cannot pin a pool connection indefinitely.
- **Error mapping:** `ErrQueueFull` and `ErrQueueShutdown` map to HTTP 503; context deadline exceeded maps to HTTP 504.

### Removed
- `backend/internal/services/lock_manager.go` (`LockManager`, `ExecuteDMLLocked`, `dmlAdvisoryKey`).

### Added
- `backend/internal/services/submission_queue.go` (`SubmissionQueue`, `NewSubmissionQueue`, `Run`, `Shutdown`, `ErrQueueFull`, `ErrQueueShutdown`).
- `backend/internal/services/submission_queue_test.go` (five unit tests).
