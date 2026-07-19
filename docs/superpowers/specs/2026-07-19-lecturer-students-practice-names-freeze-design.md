# Design: Lecturer/Student Parity, Practice Names, and Exam-Activation Freeze

Date: 2026-07-19
Status: Implemented — validated (21/21 PASS) and regression-tested (backend 179/0 with DB, frontend 347/0). This document is written post-implementation and describes what shipped, not a plan.

## 1. Goal

Six requirements, tracked as `pipeline-state.json` project `lecturer-students-practice-names-freeze`, `ARCH-001..012`:

- **R1a** — Activating an exam should automatically freeze student practice, with the lecturer still able to toggle it back off during the active window.
- **R2** — Lecturers should be able to create students the same way admins do (single-add), without gaining update/delete or staff-account-creation power.
- **R3** — Practice score exports and the lecturer-facing results view should show category and question *names*, not just opaque ids.
- **R4** — The student practice view should show a human-readable practice name instead of the opaque Sqids token.
- **R5** — Confirm (or fix) that practices are shared across lecturers rather than owned by their creator.
- **R6** — This document.

All items are implemented on branch `https-selfsigned-stopgap` (this project did not touch TLS; it landed on top of that branch). No code in this project changed authentication, session handling, or the opaque-id/Sqids scheme shipped in the prior `opaque-sqids-resource-ids` project — typed ids stay typed, tokens stay tokens, `ID_SALT` requirement is unaffected.

## 2. R1a — Auto-freeze practice on exam activation

**Decision (ARCH-001):** every activation of an exam — not only the first — unconditionally sets `block_practice_during_exam = TRUE`. Deactivation does not touch the flag.

**Backend.** `backend/internal/repository/postgres_exams.go`, function `SetExaminationActive`: the existing `UPDATE app.examinations SET is_active = TRUE, activated_at = NOW(), archived_at = NULL, scheduled_start_at = $2, scheduled_end_at = $3 WHERE id = $1` gained `block_practice_during_exam = TRUE` in the same `SET` clause — one atomic statement, not a second write. `ArchiveExamination` (the deactivate path) was deliberately left untouched, so deactivating an exam does not clear the flag; `IsPracticeFrozen` (`postgres_state.go`) only counts `is_active = TRUE` exams, so a deactivated exam's stale `TRUE` flag is inert until the exam is reactivated, at which point it is re-set to `TRUE` anyway. No new cache-invalidation call was needed: the existing `middleware.InvalidateExamFreezeCache()` call in the `ActivateExamination` controller already covers the newly-flipped column, and the same call already fires on `DeactivateExamination` too.

`LecturerExaminationResponse` in `backend/internal/controllers/lecturer_controller.go` gained `BlockPracticeDuringExam bool` with json tag `block_practice_during_exam`, mapped in `ListExaminations` from the already-scanned `models.Examination` field, so the frontend can render current state without a second request.

**Frontend.** `frontend/src/components/lecturer/types.ts`'s `Examination` interface gained `block_practice_during_exam: boolean`. `frontend/src/components/lecturer/ExamCard.vue`, only inside the `exam.is_active` branch (never on scheduled/inactive cards), gained: a small "Frozen"/"Live" badge, and a checkbox labeled "Freeze student practice during this exam" seeded from `exam.block_practice_during_exam`. Toggling it `PUT`s `/api/v1/lecturer/exams/${exam.id}/practice-freeze` with body `{ block_practice: <boolean> }` — the pre-existing `SetPracticeFreeze` endpoint; **no new endpoint was added**, only a UI for one that already existed but had no caller. The update is non-optimistic (the local ref is only set after `resp.ok`) and emits `changed` to trigger a parent refetch.

**Explicitly declined scope (user decision, recorded in `agent_notes.orchestrator.requirements.R1a`):** the middleware's fail-open behavior when the freeze check itself errors, and any new tests targeting that fail-open middleware path, were **intentionally not changed** as part of this project. Only the auto-enable-on-activation semantics and the missing toggle UI were in scope.

## 3. R2 — Lecturer student-creation parity

**Starting state (verified, not assumed):** the backend already allowed lecturers to create students — `POST /students` and `POST /lecturer/students/import` were both gated `RoleMiddleware(RoleAdmin, RoleLecturer)`, and bulk import was already lecturer-reachable via `ExamRosterPanel.vue` → `BulkImportStudents.vue`. The only real gap was that the *single-add* form existed only inline in `AdminView.vue`, with no lecturer-reachable equivalent. Separately, `POST /users` (staff-account creation, i.e. creating admins/lecturers) was also lecturer-callable, which was not intended.

**Decision (ARCH-003, ARCH-004):**

- Extract the inline "Add Student" form out of `AdminView.vue` into `frontend/src/components/AddStudentForm.vue`, a standalone component with no required props that emits `created` on success. It preserves, verbatim, the nine `data-testid`s the existing admin test suite depends on: `add-student-form`, `student-number`, `student-id-ext`, `student-sec-lec`, `student-sec-lab`, `student-first-name`, `student-last-name`, `student-email-input`, `student-password-reveal`.
- `AdminView.vue` now renders `<AddStudentForm @created="fetchUsers" />` in place of the old inline form/state (`newStudent`, `newStudentForm`, `studentReveal`, `addStudentAction` all removed).
- `frontend/src/components/lecturer/ExamRosterPanel.vue` renders the same component in a new `<details class="add-student-panel">` block placed beside the existing `<details class="import-roster-panel">` (`BulkImportStudents`), wired `@created="fetchStudents"` so a newly-added student is immediately available in the roster-assignment `StudentMultiSelect` without a page reload.
- `backend/cmd/main.go`: `staff.POST("/users", ...)` (`CreateAccount`, staff-account creation) was narrowed from `RoleMiddleware(RoleAdmin, RoleLecturer)` to `RoleMiddleware(RoleAdmin)`. `staff.POST("/students"` and `staff.POST("/lecturer/students/import"` were left untouched at `RoleMiddleware(RoleAdmin, RoleLecturer)`. `PUT`/`DELETE /users/:id` (update/delete) remain admin-only and were not touched — lecturers still cannot update or delete any user, including students.

Net effect: lecturers gained a discoverable single-add path identical in behavior to the admin one (same service call, same validation, same resulting row), gained nothing on update/delete, and lost the ability to call `POST /users` (which they should never have had).

## 4. R3 — Practice score names

Two independent pieces, R3a (export) and R3b (lecturer view), file-disjoint by design (`ARCH-012` groups B and A/F).

### 4a. R3a — email CSV export gains name columns (ARCH-005)

`backend/internal/services/submission_export.go`, `buildPracticeAttemptsCSVGz`: the header gained two columns, `category_name` and `question_requirement`, inserted immediately after `practice_question_id` and before `is_correct`. `practice_question_id` itself stays a raw `int` (`strconv.Itoa`) — this is additive, not a replacement, consistent with the prior project's decision that CSV export id columns stay raw int.

Supporting changes: `backend/internal/models/export.go`'s `PracticeAttemptExportRow` gained `CategoryName string` and `QuestionRequirement string`. `backend/internal/repository/postgres_export.go`'s `ExportPracticeAttempts` gained `LEFT JOIN app.practice_questions pq ON pq.id = a.practice_question_id` and `LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id`, selecting `COALESCE(pc.name, '')` and `COALESCE(pq.requirement, '')` right after `practice_question_id` and scanning them into the two new struct fields at the matching position. Both joins are `LEFT` so an attempt whose question or category was since deleted still exports, with empty name columns rather than a dropped row.

Deliberately, this path reads `pq.requirement`, not `pq.title` — `ARCH-005`'s rationale is explicit that R3a must have **zero dependency** on R4's title migration (`practice_questions.title`), so the two efforts (R3a export names, R4 practice-question titles) could be implemented and tested independently and in parallel.

### 4b. R3b — lecturer practice-results endpoint and breakdown view (ARCH-006, ARCH-007)

New endpoint, frozen as a contract before frontend work started so the frontend group could build against it in parallel:

```
GET /api/v1/lecturer/practice/results
```

Gated by the existing `/lecturer/practice` group's `RoleMiddleware(RoleAdmin, RoleLecturer)` in `backend/cmd/main.go` — no pagination, no request body/params. Returns one row per (student, question) pair that has **at least one attempt**; students or questions with zero attempts are simply absent (class-size data, not expected to need pagination).

`backend/internal/models/scoring.go` — new type `LecturerPracticeResultRow`, frozen at 16 fields:

```go
type LecturerPracticeResultRow struct {
    StudentID     UserID             `json:"student_id"`
    StudentNumber string             `json:"student_number"`
    Username      string             `json:"username"`
    FirstName     string             `json:"first_name"`
    LastName      string             `json:"last_name"`
    SecLec        string             `json:"sec_lec"`
    SecLab        string             `json:"sec_lab"`
    QuestionID    PracticeQuestionID `json:"question_id"`
    CategoryID    CategoryID         `json:"category_id"`
    CategoryName  string             `json:"category_name"`
    QuestionTitle string             `json:"question_title"`
    Requirement   string             `json:"requirement"`
    Points        float64            `json:"points"`
    IsSolved      bool               `json:"is_solved"`
    AttemptCount  int                `json:"attempt_count"`
    EarnedPoints  float64            `json:"earned_points"`
}
```

`StudentID`, `QuestionID`, and `CategoryID` are the typed id wrapper types from the opaque-Sqids-ids project, so they serialize to opaque string tokens automatically on the wire — no manual encoding needed, and no risk of a raw int leaking through this DTO.

`backend/internal/repository/postgres_results.go`, `GetLecturerPracticeResults(ctx)`: `INNER JOIN`s `app.practice_attempts` to `app.system_users` and `app.practice_questions`, `LEFT JOIN`s `app.practice_categories` (so a question in a since-deleted category still appears, with an empty category name), grouped by student + question. `is_solved = bool_or(pa.is_correct)`, `attempt_count = count(pa.id)`, `earned_points = CASE WHEN bool_or(pa.is_correct) THEN pq.points ELSE 0 END`. Selects `COALESCE(pq.title, '')` for `question_title` — this is the R4 title column, making R3b's read path (unlike R3a's) dependent on R4's migration having landed, which it has (`postgres_schema.go`, see §5). `ORDER BY student_number, username, category name, question id` for deterministic table/CSV rendering.

`backend/internal/controllers/lecturer_controller.go`, `GetPracticeResults` handler: calls the repo function, `500` with `{"error": ...}` on failure, `200` with the row slice as JSON on success — same pattern as the existing `GetConsolidatedScores` handler in the same file.

**Frontend.** `frontend/src/components/lecturer/LecturerResults.vue` gained a "Practice Breakdown" section: fetches `GET /lecturer/practice/results` on mount (same authenticated-fetch pattern as the existing score fetch), renders the 16-field rows in a table using the file's existing `preview-table`/`sticky-table-head` styling, and adds an "Export Practice Breakdown (CSV)" button that builds `practice_breakdown.csv` client-side via the file's existing `toCsv`/`downloadCsv` helpers with exactly this header, in this order:

```
StudentNumber, Username, FirstName, LastName, SecLec, SecLab, Category, Question, Points, Solved, Attempts, Earned
```

— names, never the opaque id tokens. The pre-existing `exportPracticeCSV` (the coarser, `PracticeTotal`-only export) and `exportExamCSV` were left unmodified and continue to work unchanged; this is a new, additional export, not a replacement.

## 5. R4 — practice question titles

**Decision (ARCH-008, ARCH-009):** add a nullable `title` column as the practice question's display name; fall back to a category-relative ordinal when unset; never render the opaque token as visible text again.

**Schema.** `backend/internal/repository/postgres_schema.go`: `title TEXT` added to the `CREATE TABLE IF NOT EXISTS app.practice_questions (...)` DDL, and `"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS title TEXT"` appended to the `extraMigrations` slice — the same idempotent-migration pattern already used by every other post-hoc column on this table. This means the column self-installs on next backend startup against an existing database; **no manual DB step is required** (see §7, Ops notes).

`backend/internal/models/practice.go`: `PracticeQuestion` gained `Title string` with json tag `title`. Empty string means "no title set" — a plain zero-value, not a special sentinel.

`backend/internal/repository/postgres_practice.go`: `CreatePracticeQuestion`'s `INSERT` and `UpdatePracticeQuestion`'s `UPDATE` both bind `q.Title`; `ListPracticeQuestions` and `GetPracticeQuestion` both select `COALESCE(pq.title, '')` and scan it into `&q.Title`.

**Authoring.** `frontend/src/components/PracticeAuthoring.vue` gained an optional "Title" input (`v-model="form.title"`) in the create/edit form — the existing whole-form `upsertQuestion(form.value)` POST already carries it, no separate payload wiring needed — and a `Title` column in the question list table (`q.title || '—'`).

**Student display.** `frontend/src/components/PracticeView.vue` previously rendered the opaque Sqids token directly: `<span class="q-id">#{{ q.id }}</span>`. This is now `{{ questionLabel(q) }}`, where `questionLabel` returns `q.title` when non-empty, else `'#' + <1-based index within the question's category group>` — the ordinal is computed from an uncapped, otherwise-identical grouping (`questionsByCategoryFull`) so the first five entries of every category match the pre-existing capped `groupedQuestions` exactly, meaning **the visible ordinals for already-displayed questions do not shift** as a side effect of this change. The opaque token no longer appears anywhere in the rendered question list.

Attempt history, which previously showed no question identity at all, now labels each attempt via a client-side `Record` built by flattening `questionsByCategoryFull` and applying the same title-or-ordinal rule, keyed by `a.practice_question_id`. This required no backend or `services/practice.ts` change to `listMyAttempts`/`PracticeAttempt` — the map is built entirely from the already-fetched question list. `frontend/src/services/practice.ts`'s `PracticeQuestion` interface gained `title?: string` (optional, since existing rows predate the column).

## 6. R5 — shared practices (documented, no code change)

**Decision (ARCH-010):** no code change. Verified in code that practice authoring and listing are gated only by `RoleMiddleware(RoleAdmin, RoleLecturer)` — `created_by` is an authorship stamp, not an ownership filter. `ListPracticeQuestions` and `ListPracticeCategories` are unfiltered by author; `PracticeAuthoring.vue` shows every question/category regardless of who created it; any lecturer can edit or delete any other lecturer's practice content. Students cannot reach any of this — they are confined to the separate student practice group, gated by `RoleMiddleware(RoleStudent)` plus the practice-freeze middleware from §2. This is the intended shared-catalog behavior (all lecturers co-author one shared bank of practice content, rather than each maintaining a private set), and R5 is satisfied by confirming and recording it rather than by changing anything.

## 7. Verification

**Validator (step 6):** 21/21 completed TODOs (TODO-001..021) PASS, 0 FAIL, ground-truth-verified against `done_when` and `ARCH-001..010` acceptance criteria (not implementor self-reports) — full detail in `pipeline-state.json` `agent_notes.validator`.

**Regression (step 7) — final numbers:**

- Backend, with a real ephemeral Postgres: `go test -mod=vendor ./... -v -count=1` → **179 PASS, 0 FAIL, 0 SKIP**. `go build -mod=vendor ./...` and `go vet -mod=vendor ./...` both exit 0 with no output.
- Backend, without a database (`TEST_DATABASE_URL` unset): 118 PASS, 0 FAIL, 61 SKIP — every DB-gated suite, including all newly-added test files, skips cleanly rather than failing.
- Frontend: `npm run build` (`vue-tsc -b && vite build`) exits 0, no type errors. `npm test -- --run` (vitest) → **35 test files, 347 tests, all PASS, 0 FAIL, 0 SKIP**.

**How to re-run.** The DB-gated backend suites are controlled by the `TEST_DATABASE_URL` environment variable — unset, they self-skip; set, they run against whatever Postgres that URL points at. The pattern used for this project's verification run was an ephemeral, throwaway container, never the live database:

```bash
docker run -d --name sql-quiz-test-db-regr2 \
  -p 127.0.0.1:0:5432 \
  -e POSTGRES_USER=testuser -e POSTGRES_PASSWORD=testpass -e POSTGRES_DB=testdb \
  postgres:15-alpine
# resolve the random host port docker assigned, then:
TEST_DATABASE_URL=postgres://testuser:testpass@127.0.0.1:<port>/testdb?sslmode=disable \
  go test -mod=vendor ./... -v -count=1
docker rm -f sql-quiz-test-db-regr2
```

The live `sql-quiz-db` container is never started, stopped, or touched by this pattern. New backend test files added for this project: `postgres_autofreeze_test.go`, `postgres_practice_title_test.go`, `postgres_lecturer_practice_results_test.go`, `postgres_export_practice_names_test.go`, `submission_export_csv_test.go` (pure unit, no DB), `lecturer_practice_results_role_http_test.go`, `create_account_role_http_test.go`. New frontend test files: `ExamRosterPanel_AddStudentForm.test.ts`, `PracticeView_questionLabel.test.ts`.

## 8. Ops notes

- **No manual database step is required for this deploy.** `practice_questions.title` and `examinations.block_practice_during_exam` (the latter pre-existing, only its *write path* changed) both come from the standing idempotent-migration mechanism in `postgres_schema.go` (`extraMigrations`, `ADD COLUMN IF NOT EXISTS`), which runs automatically on backend startup. A fresh database gets the column from the `CREATE TABLE` DDL directly; an existing database gets it from the migration; both converge to the same schema, and re-running the migration against a database that already has the column is a no-op.
- **Relation to the Sqids/`ID_SALT` work.** This project's new endpoint (`GET /lecturer/practice/results`) and DTO (`LecturerPracticeResultRow`) use the typed id wrapper types (`UserID`, `PracticeQuestionID`, `CategoryID`) introduced by the prior `opaque-sqids-resource-ids` project, so student/question/category ids serialize as opaque tokens on this new route exactly like every other route — no new encoding logic was written, and no route in this project bypasses the codec. Per that project's `ARCH-006`, carried forward unchanged here: **CSV export id columns stay raw int** (`practice_question_id` in both the email-export CSV and the practice-breakdown CSV), with name columns added *additively* alongside them rather than replacing them. Exam-question ids (distinct from practice-question ids) also stay plain readable ints, unaffected by any change in this project. `ID_SALT` remains a required, fail-fast startup environment variable; nothing here changes that.
