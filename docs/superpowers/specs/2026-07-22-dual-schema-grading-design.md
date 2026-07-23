# Dual-Schema Grading — Design Spec

**Date:** 2026-07-22
**Status:** Approved for implementation
**Scope:** Practice + Exam, QUERY + DML, this pipeline run.

## 1. Purpose

Prevent students from brute-forcing a correct result without understanding the
instruction. Today, a question's **preview** and its **grading** run against the
*same* dataset, so a student can iterate their query until the preview "looks
right" and submit — never learning the actual command the requirement asked for.

This feature lets a lecturer attach a **second, decoy schema** to a question's
owner (practice category / exam). When a question is toggled dual-schema:

- The **preview** the student sees runs on the **decoy schema A** (same tables +
  columns, different row data).
- The **grading** runs on the **real schema B** (the existing schema, where the
  correct answer was already captured) and is never shown to the student.

Because A holds different data, the preview reveals nothing about whether the
answer matches B — the student must actually understand the query.

## 2. Decisions (locked)

1. **Two schemas, same structure, different data.** Lecturer uploads both. Same
   table names + column definitions, different rows. The student's single query
   is valid on both.
2. **Per-question toggle** (`dual_schema` boolean). The two schemas are
   provisioned once at the owner level (category / exam); each question opts in.
3. **Both practice and exam** in this run.
4. **Warn-only** for the DML correct-answer table check (non-blocking notice).

## 3. Core insight (keeps the blast radius small)

Grading **already** runs on the real schema (`practice_cat_<id>` for practice,
exam `curriculum_schema` for QUERY, `sandbox_exam_<id>` for DML) and the correct
answer was captured there. That real schema **is** grading schema **B**.

- **Grading path is unchanged** — scoring, capture location, comparison logic all
  stay as-is.
- **Only the preview path changes**: when `dual_schema=true` and a decoy schema
  exists, the preview resolves to **A** instead of **B**.

The remaining work is: provision A, add the per-question flag, plug data leaks,
and build the authoring UI.

## 4. Data model

New columns (idempotent `ADD COLUMN IF NOT EXISTS` in `extraMigrations` **and** in
the fresh-install `CREATE TABLE` bodies in
`backend/internal/repository/postgres_schema.go` `InitializeSystemTables`,
`:925-1104`):

| Table | New column | Meaning |
|---|---|---|
| `app.practice_categories` (`:1044`) | `preview_schema_name TEXT NOT NULL DEFAULT ''` | decoy schema A; `''` = none |
| `app.examinations` | `preview_curriculum_schema TEXT NOT NULL DEFAULT ''` | decoy curriculum A; `''` = none |
| `app.practice_questions` (`:1002`) | `dual_schema BOOLEAN NOT NULL DEFAULT false` | previews-on-A / grades-on-B |
| `app.questions` (`:953`) | `dual_schema BOOLEAN NOT NULL DEFAULT false` | same, for exam |

**Migrations run at backend boot only.** They are `IF NOT EXISTS` / `DEFAULT`, so
existing rows are untouched and existing behavior is preserved. Do **not** apply
migrations against a running container by hand — the operator restarts via
`docker compose down && up`, which re-runs boot migrations.

Schema-name helpers (add to `postgres_schema.go`, alongside the existing
`PracticeCategorySchemaName :742`, `ExamCurriculumSchemaName :750`,
`ExamSandboxName :733`):

- `PracticeCategoryPreviewSchemaName(id)` → `practice_cat_<id>_preview`
- `ExamPreviewCurriculumSchemaName(id)` → `curriculum_exam_<id>_preview`
- `ExamPreviewSandboxName(id)` → `sandbox_exam_<id>_preview`

All names ≤63 chars; sanitize with `pgx.Identifier{...}.Sanitize()` as everywhere
else.

## 5. Provisioning the decoy schema A

- **Practice:** new function `ImportSchemaIntoCategoryPreview` mirroring
  `ImportSchemaIntoCategory` (`postgres_schema.go:322`) but targeting
  `practice_cat_<id>_preview`; same `validateNoSchemaQualifiedNames` denylist,
  same `ensureCategorySchema` grants, same exclusive rebuild lock. Sets
  `practice_categories.preview_schema_name`. New endpoint
  `POST /lecturer/practice/categories/:id/import-preview[?confirm_replace=true]`.
- **Exam:** `ImportSchemaIntoExamCurriculumPreview` mirroring
  `ImportSchemaIntoExamCurriculum` (`postgres_schema.go:430`) → imports into
  `curriculum_exam_<id>_preview`, sets `examinations.preview_curriculum_schema`.
- **Exam preview sandbox:** `InitializeExamPreviewSandbox` mirroring
  `InitializeExamSandbox` (`postgres_schema.go:764`) but cloning from the preview
  curriculum into `sandbox_exam_<id>_preview`. Built lazily on the first dual-DML
  preview (mirror `EnsureExamSandboxReady`, `postgres_exams.go:92`).
- **Structure-parity warning (non-blocking):** on decoy import, compare A's table
  and column set to B's; if they differ (missing table, differing columns), return
  a warning in the response — a student query could otherwise fail on preview.
- The correct answer is **still captured only on B** (`captureExpectedResult`
  `postgres_practice.go:380`, `CreateQuestion` capture `postgres_questions.go:33`
  unchanged). A is a display-only sandbox — never captured, never graded.

## 6. Execution matrix

| Case | Preview (student sees) | Grade (verdict) |
|---|---|---|
| dual **OFF**, any | schema B (as today) | schema B (as today) |
| dual **ON**, QUERY | student SQL on **A**, `exam_student_readonly`, rollback, 200-cap → A rows | student SQL on **B**, compare to captured `expected_result` → verdict |
| dual **ON**, DML | student DML on **A**, SAVEPOINT rollback, `SELECT * FROM target_table` on A → A post-image | student DML on **B**, compare to captured post-image → verdict |

**Only hot-path change:** the *preview* schema resolver returns A when
`dual_schema=true` and the owner has a preview schema; otherwise B.

### 6.1 Dual toggled ON but no decoy schema uploaded (decided)

**Behaviour: fall back to single-schema B, and notify the lecturer.** The student
is never blocked and grading is unaffected — the question simply behaves exactly
as it does today until the decoy is uploaded.

This must be validated in **both layers**:

- **Backend (authoritative):** whenever a question with `dual_schema=true` belongs
  to an owner whose preview schema is empty, the save/response path returns a
  warning (e.g. `dual_schema_warning`: "Dual schema is enabled but no decoy schema
  has been uploaded for this category/exam — previews will use the grading schema
  until you upload one."). The resolver logs the fallback server-side.
- **Frontend:** the authoring UI validates the same condition at toggle time and
  on load — turning `dual_schema` on while the owner has no decoy schema shows an
  inline warning next to the toggle, plus the backend warning surfaced on save.

Accepted trade-off: there is a leak window while a question is dual-but-undecoyed
(the preview runs on B). It is made loud rather than silent; the lecturer is told
in two places how to close it.

Touch points:

- Practice preview schema resolution: `resolveCategorySchema`
  (`practice_controller.go:369`) → add a preview-aware variant that returns the
  decoy schema for dual questions.
- Exam preview schema resolution: `PreviewAnswer` (`quiz_controller.go:275`) →
  resolve `curriculum_exam_<id>_preview` (QUERY) / `sandbox_exam_<id>_preview`
  (DML) for dual questions.
- Repository preview execs `evaluateQueryPreview` (`postgres_evaluation.go:232`)
  and `evaluateDMLPreview` (`:288`) take the resolved preview schema; the DML
  eval lock is keyed on that schema.
- `models`: `PracticeQuestion` / `Question` gain `DualSchema bool`;
  `PracticeCategory` gains `PreviewSchemaName string`. `AsQuestion()`
  (`models/practice.go:64`) copies `DualSchema`. `GetQuestion`
  (`postgres_questions.go:271`) / practice getters SELECT the new columns.

## 7. Leak-proofing (the whole point — a dual question must never emit B's data)

1. **Submit returns no rows today** (verdict + score only) — keep it that way for
   both practice (`ValidationResult`) and exam.
2. **DML diff feedback** (`formatDMLDiff`, `dml_compare.go:80`) dumps B's expected
   rows into `Feedback`. **Suppress the row-level diff for dual questions**:
   `decideDMLStatus` (`validation_service.go:138`) emits a generic
   "your result does not match the expected outcome" message with no rows when the
   question is dual. Non-dual keeps the current detailed diff.
3. **Exam Question Brief** ships QUERY `expected_result` / `expected_columns` to
   the student (`StudentView.vue:495-502`, populated by `GetExamQuestions`
   `quiz_controller.go:139-145`). **Strip `expected_result` + `expected_columns`
   for dual questions regardless of type** in the student payload. (Practice
   already strips all three — `practice_controller.go:42-46, 65-67` — so practice
   needs no change here.)

## 8. Authoring UI

- **Practice** (`PracticeAuthoring.vue`): a per-question **"Dual schema (hidden
  grading)"** checkbox → `form.dual_schema`; a **decoy-schema import** control at
  category level (parallel to the existing category import, `:424-461`); a
  **"Preview as student"** button that runs on A via a `schema` param on the
  lecturer-preview endpoint. Surface the regex table warning and the
  structure-parity warning.
- **Exam** (`lecturer/ExamQuestionsPanel.vue`): per-question `dual_schema` in the
  question config; decoy-curriculum import; "Preview as student"; warnings.
- **Lecturer sees both:** the existing "Preview Correct SQL Result" runs on B; the
  new "Preview as student" runs on A. The lecturer-preview endpoints
  (`/lecturer/practice/preview`, `/lecturer/preview`) gain an optional
  `schema: "grading" | "preview"` param (default `grading`, current behavior).
- **Student views need no logic change** — the preview panel already renders
  whatever rows the backend returns (which become A's rows when dual). The
  Question Brief already only renders `expected_result` if present, which the
  backend now strips for dual questions.

## 9. DML correct-answer table check (warn-only)

On DML question save (practice + exam), regex-scan `correct_sql` (after
`stripSQLNoise`, `postgres_schema.go:296`) for a word-boundary,
case-insensitive occurrence of the declared `target_table`. If absent, return a
**non-blocking warning** in the save response (e.g. `table_warning` string),
surfaced in the authoring UI next to the capture status. Never blocks save or
capture. Independent of dual-schema.

## 10. API deltas

- `POST /lecturer/practice/question` / `POST /lecturer/question` request: add
  `dual_schema`. Response: add optional `table_warning`.
- `POST /lecturer/practice/preview` / `POST /lecturer/preview` request: add
  optional `schema: "grading"|"preview"`.
- New: `POST /lecturer/practice/categories/:id/import-preview`,
  `POST /lecturer/exams/:id/import-curriculum-preview` (name to match existing
  curriculum-import route). Response includes a `parity_warning` when A ≠ B
  structure.
- Student submit/preview payloads unchanged on the wire; server-side schema
  resolution differs for dual questions.

## 11. Back-compat / migration

- Every new column defaults to off/empty → **existing questions behave exactly as
  today** (single schema = B).
- No backfill, no re-capture, no re-score. Dual is strictly opt-in per question.
- Migrations idempotent; applied at boot on `docker compose down && up`.

## 12. Out of scope (this run)

- Auto-generating decoy data (lecturer uploads it).
- Recomputing past scores.
- Any change to non-dual grading semantics.

## 13. Testing / smoke

- Unit: preview-schema resolver returns A only when dual + A present, else B;
  DML feedback suppression for dual; exam `expected_result` strip for dual; regex
  table check positive/negative.
- Integration (TEST_DATABASE_URL-gated): dual QUERY — student sees A rows, verdict
  computed on B; dual DML — A post-image previewed, graded on B; decoy-import
  parity warning.
- **Smoke test** (mandatory before wrap): run the repo smoke test after the
  pipeline completes; verify a non-dual question still previews+grades exactly as
  before (regression guard), then a dual question previews on A / grades on B.

## 14. Affected files (for task-router partitioning)

Backend:
- `backend/internal/repository/postgres_schema.go` (DDL, naming, decoy import, preview sandbox init)
- `backend/internal/repository/postgres_evaluation.go` (preview schema param)
- `backend/internal/repository/postgres_practice.go` (getters, resolveCategorySchema preview variant)
- `backend/internal/repository/postgres_questions.go` (getters select dual_schema)
- `backend/internal/services/validation_service.go` + `dml_compare.go` (dual feedback suppression)
- `backend/internal/models/practice.go`, `submission.go` (DualSchema, PreviewSchemaName, AsQuestion)
- `backend/internal/controllers/practice_controller.go` (preview resolver, import-preview, regex warn, schema param)
- `backend/internal/controllers/quiz_controller.go` (preview resolver, dual expected_result strip, import-preview)
- `backend/cmd/main.go` (routes)

Frontend:
- `frontend/src/components/PracticeAuthoring.vue`
- `frontend/src/components/lecturer/ExamQuestionsPanel.vue`
- `frontend/src/api/practice.ts`, `frontend/src/api/exam.ts`, `frontend/src/components/lecturer/types.ts`
- (Student views `PracticeView.vue` / `StudentView.vue`: no logic change expected.)
