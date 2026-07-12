# API_SPEC.md

The HTTP contract between frontend and backend, synced to the implemented routes in `backend/cmd/main.go`. Phase-lifecycle behaviour is described in `ARCHITECTURE.md § Phase Lifecycle`.

## Base URL
`http://localhost/api/v1` (proxied via Nginx on port 80).

## Authentication
Most endpoints require a JWT in the header: `Authorization: Bearer <jwt>`.

The JWT carries `user_id, username, role, token_version, exp` (24 h TTL). `token_version` is bumped by any password change/reset, which **invalidates every previously-issued token** for that user. The SSE stream endpoint takes the token as a query param instead of a header (see § Live Exam Stream).

Middleware order on protected routes: `AuthMiddleware` (401) → `RoleMiddleware` (403).

Status codes used throughout:
- `200` success · `201` created · `204` no content.
- `400` malformed request body / bad path param.
- `401` missing / invalid / version-mismatched token, or bad credentials.
- `403` authenticated but role or enrollment denies access.
- `404` resource missing.
- `409` operation rejected by a phase gate (e.g. editing a question while its exam is active).
- `413` upload too large · `415` unsupported media type · `423` practice locked during exam.
- `500` unexpected backend error · `503` submission queue saturated / shutting down · `504` evaluation timed out.

No request struct uses `binding:"required"` except where explicitly noted (`ImportSchema.sql`); presence checks are otherwise manual, so `ShouldBindJSON` fails with 400 mainly on malformed JSON.

---

## Route Index

| Group | Method | Path | Role |
|---|---|---|---|
| Auth | POST | `/login` | public |
| Auth | POST | `/forgot-password` | public |
| Auth | POST | `/forgot-password/verify` | public |
| Users | POST | `/user/password` | any auth |
| Users | GET | `/user/students` | Admin/Lecturer |
| Users | POST | `/users` | Admin/Lecturer |
| Users | POST | `/students` | Admin/Lecturer |
| Admin | GET | `/admin/users/all` | Admin |
| Admin | PUT | `/admin/users/:id` | Admin |
| Admin | DELETE | `/admin/users/:id` | Admin |
| Admin | POST | `/admin/users/:id/reset-password` | Admin |
| Admin | POST | `/admin/users/:id/resend-credential` | Admin |
| Admin | GET | `/admin/users/:id/password-events` | Admin |
| Quiz | GET | `/quiz/exams/:id/stream` | any auth (query token) |
| Quiz | POST | `/quiz/submit` | Student |
| Quiz | POST | `/quiz/preview` | Student |
| Quiz | GET | `/quiz/exams` | Student |
| Quiz | GET | `/quiz/exams/:id/questions` | Student |
| Quiz | GET | `/quiz/exams/:id/results` | Student |
| Quiz | GET | `/quiz/exams/:id/introduction` | Student |
| Quiz | GET | `/quiz/exams/:id/intro-images` | Student/Admin/Lecturer |
| Quiz | GET | `/quiz/exams/:id/intro-images/:imageId` | Student/Admin/Lecturer |
| Quiz | GET | `/quiz/results` | Student |
| Lecturer | POST | `/lecturer/question` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/exams/max-scores` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/exams/:id/questions` | Admin/Lecturer |
| Lecturer | DELETE | `/lecturer/questions/:id` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/preview` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/import-schema` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/students/import` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/exams` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/exams/:id/submissions` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id` | Admin/Lecturer |
| Lecturer | DELETE | `/lecturer/exams/:id` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/activate` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/deactivate` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/practice-freeze` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/introduction` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams/:id/intro-images` | Admin/Lecturer |
| Lecturer | DELETE | `/lecturer/exams/:id/intro-images/:imageId` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/intro-images/order` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams/:id/curriculum/import` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/exams/:id/students` | Admin/Lecturer |
| Lecturer | DELETE | `/lecturer/exams/:id/students/:studentId` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams/:id/students` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/scores` | Admin/Lecturer |
| Practice (student) | GET | `/practice/questions` | Student |
| Practice (student) | GET | `/practice/questions/:id` | Student |
| Practice (student) | POST | `/practice/submit` | Student |
| Practice (student) | POST | `/practice/preview` | Student |
| Practice (student) | GET | `/practice/attempts` | Student |
| Practice (student) | GET | `/practice/summary` | Student |
| Practice (student) | GET | `/practice/categories/:id/introduction` | Student |
| Practice (student) | GET | `/practice/categories/:id/intro-images` | Student |
| Practice (student) | GET | `/practice/categories/:id/intro-images/:imageId` | Student |
| Practice (lecturer) | POST | `/lecturer/practice/question` | Admin/Lecturer |
| Practice (lecturer) | GET | `/lecturer/practice/questions` | Admin/Lecturer |
| Practice (lecturer) | DELETE | `/lecturer/practice/question/:id` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/preview` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/reset-sandbox` | Admin/Lecturer |
| Practice (lecturer) | GET | `/lecturer/practice/categories` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/categories` | Admin/Lecturer |
| Practice (lecturer) | PUT | `/lecturer/practice/categories/:id` | Admin/Lecturer |
| Practice (lecturer) | DELETE | `/lecturer/practice/categories/:id` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/categories/:id/import` | Admin/Lecturer |
| Practice (lecturer) | PUT | `/lecturer/practice/categories/:id/introduction` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/categories/:id/intro-images` | Admin/Lecturer |
| Practice (lecturer) | DELETE | `/lecturer/practice/categories/:id/intro-images/:imageId` | Admin/Lecturer |
| Practice (lecturer) | PUT | `/lecturer/practice/categories/:id/intro-images/order` | Admin/Lecturer |

---

## Public Endpoints — Auth

### Login
- `POST /login`
- **Payload:** `{"username": "...", "password": "..."}`
- **200:** `{"token": "...", "user": {"id", "username", "role", "created_by", "created_at", "email?", "student_number?", "student_id_ext?", "sec_lec?", "sec_lab?", "first_name?", "last_name?"}}` (password never emitted).
- **401:** `{"error": "invalid username or password"}`, or `{"error": "too many login attempts. please try again in 10 minutes"}`.
- **Notable:** In-memory sliding-window rate limit — **5 attempts / 10 min per username**, cleared on success; not persisted across restarts.

### Forgot Password (step 1 — request code)
- `POST /forgot-password`
- **Payload:** `{"username": "..."}`
- **200 (always):** `{"message": "If an account with that username exists and has an email on file, a verification code has been sent."}` — enumeration-safe on both result and timing; even malformed bodies return this.
- **Notable:** Emails a 6-digit OTP (10-min TTL, max 5 verify attempts). Only a SHA-256 hash is stored. Throttled to one honoured request / 5 min per account. SMTP send is deferred to a background goroutine.

### Forgot Password (step 2 — verify code)
- `POST /forgot-password/verify`
- **Payload:** `{"username": "...", "otp": "123456"}`
- **200:** `{"message": "Verification successful. A new password has been sent to your email."}`
- **400:** `{"error": "invalid or expired code"}` — single sentinel for every failure (malformed body, unknown user, no email, missing/expired/exhausted/wrong code).
- **Notable:** On success, a fresh password is generated, hashed, emailed; `token_version` is bumped (all sessions evicted); a `forgot_reset` audit event is written (only when `PASSWORD_LOG_KEY` is set).

---

## Protected Endpoints — Users

### Change Password (any authenticated role)
- `POST /user/password`
- **Payload:** `{"old_password": "...", "new_password": "..."}`
- **200:** `{"token": "..."}` — a fresh JWT (bumped `token_version`) so the current device stays logged in; **all other devices are evicted**.
- **401:** `{"error": "invalid old password"}` (other service faults also surface as 401 here).
- **Notable:** records a `self_change` password event when `PASSWORD_LOG_KEY` is configured.

### List Students (Admin / Lecturer)
- `GET /user/students`
- **200:** array of STUDENT `User` objects, each with the six nullable profile fields (`student_number`, `student_id_ext`, `sec_lec`, `sec_lab`, `first_name`, `last_name`), omitted when null.

### Create Staff User (Admin / Lecturer)
- `POST /users`
- **Payload:** `{"username": "...", "role": "LECTURER"|"ADMIN", "email": "..."?}` — any supplied `password` is **ignored** (server-generated).
- **201:** `{"id": int, "generated_password": "..."}` — shown once.
- **400:** `{"error": "STUDENT accounts must be created via /api/v1/students"}` when `role=STUDENT`.
- **403:** role-hierarchy violations (`lecturers cannot create admin accounts`, etc.).
- **409:** `{"error": "email already in use"}`.
- **Notable:** password bcrypt-hashed; `create` event recorded; credential emailed when an address is present.

### Create / Upsert Single Student (Admin / Lecturer)
- `POST /students`
- **Payload:** `{"student_number": "...", "student_id_ext": "...", "sec_lec"?, "sec_lab"?, "first_name"?, "last_name"?, "email"?}` — `student_number` and `student_id_ext` (the login username) are required.
- **201:** `{"id": int, "created": bool}`, plus `"generated_password"` **only when `created == true`**.
- **400:** `{"error": "student_number is required"}` / `{"error": "student_id is required"}`.
- **409:** `{"error": "email already in use"}`.
- **Notable:** conflict on an existing student **preserves the existing password** — re-adding never resets it and returns no generated password. Shares the roster-import upsert path.

---

## Admin-only User Management

### List All Users
- `GET /admin/users/all` → **200:** array of every `User`.

### Update User
- `PUT /admin/users/:id`
- **Payload:** full `User` object (path `id` wins; password not settable here).
- **200:** `{"message": "User updated successfully"}`. **400:** `{"error": "Invalid user ID"}`.

### Delete User
- `DELETE /admin/users/:id` → **200:** `{"message": "User deleted successfully"}`.

### Reset Password
- `POST /admin/users/:id/reset-password`
- **200:** `{"generated_password": "..."}` — shown once.
- **Notable:** updates hash, **bumps `token_version`** (evicts sessions), records an `admin_reset` event. No email is sent (recipient passed empty).

### Resend Credential
- `POST /admin/users/:id/resend-credential`
- **200:** `{"emailed_to": "..."}` — re-emails the *current* credential; does **not** regenerate the password or evict sessions.
- **400:** no email on file / `PASSWORD_LOG_KEY` not configured / no password on record / decrypt failure / SMTP send failure (all ResendCredential faults map to 400).
- **Notable:** recovers the plaintext from the newest `password_events` row (requires `PASSWORD_LOG_KEY`).

### Password Events (audit)
- `GET /admin/users/:id/password-events`
- **200:** `{"key_configured": bool, "events": [{"source", "changed_by_username", "created_at", "new_password?"}]}` — newest first; `new_password` present only when the key is configured and decrypt succeeds. The admin's view access is itself logged (metadata only).

---

## Protected Endpoints — Student Quiz

### Live Exam Stream (SSE)
- `GET /quiz/exams/:id/stream?access_token=<jwt>`
- **Auth:** JWT via **query param** (not a header — `EventSource` can't set headers); same `token_version` invalidation as Bearer. Any authenticated user; enrollment enforced.
- **200:** `Content-Type: text/event-stream` (`Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`). Long-lived.
- **Event frames:**
  ```
  event: exam-status
  id: <unix-millis>
  data: {"exam_id": <int>, "effective_state": "inactive|scheduled|active|ended", "server_time": "<RFC3339 UTC>"}
  ```
  One immediate snapshot on connect; further frames pushed by the in-process `Hub` on activation/status broadcasts. A `: ping` comment is sent every 20 s as keep-alive. Hub sends are non-blocking (a full 4-slot buffer drops that event).
- **Fallback:** none server-side; on disconnect the client falls back to polling `GET /quiz/exams`.
- **Errors:** 400 (bad id), 401 (missing/invalid token), 404 (exam not found), 403 (not enrolled).

### Submit Answer (Student)
Validates a submission and persists it to `app.submissions`.
- `POST /quiz/submit`
- **Payload:** `{"question_number": int, "sql_text": "..."}` — `student_id` is derived from the JWT; any client value is ignored.
- **Behaviour:** the owning exam must be effectively `active`. Branch on `question.type`:
  - **QUERY (SELECT):** lock-free against frozen `public.*` (or the exam's curriculum schema) under role `exam_student_readonly`.
  - **DML:** routed through the global single-worker FIFO `SubmissionQueue`; savepoint-rollback against `sandbox_exam_{examID}` under role `exam_student`.
- **200:** `{"status": "correct"|"incorrect"|"syntax_error", "score": float, "feedback": "...", "is_correct": bool, "validation_details": <raw JSON>}`.
- **Errors:** 400 (malformed body / question not assigned to an exam), 403 (exam not active / not enrolled), 500 (fetch/validate/persist failure), 503 (queue full or shutting down), 504 (evaluation deadline exceeded).

### Preview Answer (Student)
- `POST /quiz/preview`
- **Payload:** `{"question_number": int, "sql_text": "..."}`
- **200:** `{"status": "preview"|"syntax_error", "feedback": "...", "rows": [...], "columns": [...], "row_count": int, "truncated": bool}` — `rows`/`columns` never null.
- **Notable:** same queue branching and `active`+enrollment gates as submit. **Correctness is neutralised** — a `correct`/`incorrect` outcome is rewritten to `"preview"` with feedback cleared, so students can't infer right/wrong mid-run. Syntax errors are preserved. **Not persisted.**
- **Errors:** 400, 403, 404 (question not found), 500, 503, 504.

### Get Available Exams (Student)
- `GET /quiz/exams` — optional query `include_inactive=true` returns all enrolled exams; otherwise only currently-relevant ones.
- **200:** array of `ExaminationResponse`: `{"id", "title", "is_active", "created_by", "created_at", "activated_at?", "archived_at?", "scheduled_start_at?", "scheduled_end_at?", "effective_state", "introduction"}`.

### Get Exam Questions (Student)
- `GET /quiz/exams/:id/questions`
- **Behaviour:** enrollment + effectively-`active` required. Lazily prepares the DML sandbox (`EnsureExamSandboxReady`) and broadcasts an `active` status frame over the SSE Hub.
- **200:** array of `Question`. `correct_sql` is always cleared; for DML questions `expected_result`/`expected_columns` are nulled (QUERY questions keep them as the target to reproduce).
- **Errors:** 400, 403 (not enrolled / exam not found / not active), 500.

### Get Exam Results (Student)
- `GET /quiz/exams/:id/results`
- **200:** array of `{"question_id", "requirement", "type", "points", "sql_text", "is_correct", "score", "submitted_at", "validation_details"}` — latest-per-question. No active-state gate (results always visible).
- **403:** not enrolled.

### Get Exam Introduction (Student)
- `GET /quiz/exams/:id/introduction`
- **200:** `{"exam_id", "title", "introduction", "tables": [{"name", "columns": [{"name", "data_type", "nullable", "is_primary_key", "is_foreign_key", "references_table?", "references_column?"}]}]}` — full curriculum-schema introspection. No active-state gate; enrollment required.

### Exam Intro Images (Student / Admin / Lecturer)
- `GET /quiz/exams/:id/intro-images` → **200:** `{"images": [{"id", "owner_id", "sort_order", "mime_type", "byte_size", "created_at", "updated_at"}]}` (metadata only). Students must be enrolled; staff skip the enrollment check.
- `GET /quiz/exams/:id/intro-images/:imageId` → **200:** raw image bytes with the stored `Content-Type`, `ETag` (updated-at unix seconds), `Cache-Control: private, max-age=86400, must-revalidate`; **304** when `If-None-Match` matches.

### Get My Results Summary (Student)
- `GET /quiz/results`
- **200:** array of `{"exam_id", "exam_title", "is_active", "question_count", "answered_count", "correct_count", "total_score", "max_score"}` — one per enrolled exam, including zero-submission exams.

---

## Protected Endpoints — Lecturer

### Upsert Question
- `POST /lecturer/question`
- **Payload:** `models.Question` — `{"id", "exam_id", "type": "QUERY"|"DML", "target_table", "correct_sql", "points", "strict_mode": {"ordering": bool, "attributes": bool, "column_order": bool}}`. `created_by` is forced from the JWT.
  - `strict_mode.ordering` — rows must match the teacher's order; off ⇒ set-equality.
  - `strict_mode.attributes` — output column names must match `expected_columns`.
  - `strict_mode.column_order` — output columns must match `expected_columns` index-by-index; independent gate, default `false`.
- **Behaviour:** saves the row, then runs the teacher SQL against the exam's curriculum clone inside a transaction, captures the result, rolls back, writes the cache. **Capture failure does not roll back the upsert.**
- **200:** `{"message": "Question saved successfully", "id", "expected_result_status", "expected_result_captured_at", "capture_error"}` (last four added on read-back success). `expected_result_status` is `captured` | `failed` | `pending`.
- **409:** `{"error": "exam is active; ..."}` when the exam is active.

### List Questions for an Exam
- `GET /lecturer/exams/:id/questions` → **200:** array of full question objects.

### Delete Question
- `DELETE /lecturer/questions/:id` → **200:** `{"message": "Question deleted successfully"}`.

### Preview Correct SQL
- `POST /lecturer/preview`
- **Payload:** `{"type": "QUERY"|"DML", "target_table": "...", "correct_sql": "...", "exam_id": int?}` — `exam_id > 0` runs against that exam's curriculum schema; absent/0 ⇒ `public`.
- **200 (ok):** `{"status": "ok", "rows": [...], "columns": [...]}`.
- **200 (engine error):** `{"status": "error", "error": "..."}` — SQL errors surfaced inline, not as an HTTP error.
- **400:** bad JSON / type not QUERY|DML.

### Import Schema (global `public.*`)
Legacy shared-template import; retained for scripting, no longer surfaced in the UI (use per-exam curriculum import instead).
- `POST /lecturer/import-schema`
- **Payload:** `{"sql": "..."}` (required). `created_by` recorded to `app.schema_import_audit`.
- **200:** `{"message": "Database schema imported successfully"}`.
- **409:** rejected while any exam is active (`statusForRepoErr`).

### Import Student Roster (multipart)
- `POST /lecturer/students/import`
- **Content-Type:** `multipart/form-data`; field `file` (`.csv` or `.xlsx`, format by extension).
- **Required columns** (case-insensitive, order-independent): `Student number`, `SECLEC`, `SECLAB`, `Student_id`, `First Name`, `Last Name`.
- **Behaviour:** upserts by `student_number`; new students get `username = student_number` and initial `password = student_number` (bcrypt). Re-import updates profile/section fields but **preserves `password_hash`**. Post-commit best-effort `create` audit + credential emails for newly-created rows.
- **200:** `{"total", "created", "updated", "skipped", "errors": [{"row", "student_number", "message"}]}`.
- **400:** `{"error": "file is required"}` / missing required header / parse error. **500:** import service not configured.

### List / Create Examinations
- `GET /lecturer/exams` → **200:** array of `LecturerExaminationResponse`: `{"id", "title", "is_active", "created_by", "created_at", "activated_at?", "archived_at?", "scheduled_start_at?", "scheduled_end_at?", "effective_state", "introduction", "curriculum_schema", "question_count", "total_points", "uncaptured_count"}`. `uncaptured_count > 0` means the exam cannot yet be activated.
- `POST /lecturer/exams` — **Payload:** `models.Examination` (server forces `is_active=false`, `created_by`=JWT). **201:** `{"message": "Examination created successfully", "id": int}`.

### Get Exam Max Scores
- `GET /lecturer/exams/max-scores` → **200:** `{"max_scores": {<exam title>: <points>}}`. Feeds the Results-tab denominators.

### List Submissions (audit)
- `GET /lecturer/exams/:id/submissions` → **200:** array of `app.submissions` rows. Returned regardless of active state.

### Rename Examination
- `PUT /lecturer/exams/:id`
- **Payload:** `{"title": "..."}` (required, trimmed). Allowed active or inactive.
- **200:** `{"message": "Exam renamed"}`. **404:** `{"error": "exam not found"}`.

### Delete Examination (cascade)
- `DELETE /lecturer/exams/:id`
- **Behaviour:** hard-deletes submissions, roster, intro-image records, questions (incl. caches), drops `sandbox_exam_{id}` if present, then the exam — in one transaction.
- **200:** `{"message": "Exam deleted"}`. **409:** `{"error": "deactivate the exam before deleting"}` while effectively active. **404:** exam not found.

### Activate Examination
- `PUT /lecturer/exams/:id/activate`
- **Payload:** `{"active": true, "mode": "instant"|"scheduled", "scheduled_start_at": RFC3339?, "scheduled_end_at": RFC3339?}`.
  - `active` must be `true` (else 400 — use the deactivate endpoint).
  - `mode="scheduled"` requires `scheduled_start_at` (400 if missing); `scheduled_end_at` optional.
  - `mode="instant"`/empty ⇒ both nil ⇒ repo uses `now()` as the start.
- **Pre-flight:** every question must have `expected_result_status = 'captured'` and the exam must have ≥1 question.
- **200:** `{"message": "Examination status updated"}`. Broadcasts an `active` SSE frame and invalidates the practice-freeze cache.
- **409:** capture-gate failure — `ErrCaptureNotReady` (questions not captured) or `ErrEmptyExam` (no questions).

### Deactivate Examination
- `PUT /lecturer/exams/:id/deactivate`
- **Payload (optional):** `{"mode": "scheduled", "scheduled_end_at": RFC3339}` sets a future end and keeps the sandbox; empty body / other ⇒ instant archive (`is_active=false`, `archived_at=now()`, drop `sandbox_exam_{id}` if present).
- **200:** `{"message": "Examination deactivated and sandboxes cleaned up"}`.

### Set Practice Freeze
- `PUT /lecturer/exams/:id/practice-freeze`
- **Payload:** `{"block_practice": bool}` → **200:** `{"message": "Practice freeze updated", "block_practice": bool}`. Invalidates the freeze cache.

### Set Exam Introduction
- `PUT /lecturer/exams/:id/introduction`
- **Payload:** `{"introduction": "..."}` (max 20 000 chars) → **200:** `{"message": "Exam introduction updated"}`. **400:** over the length cap.

### Exam Intro Images (lecturer management)
- `POST /lecturer/exams/:id/intro-images` — multipart field `image`; **PNG/JPEG only** (magic-byte sniff), **5 MB/file**, **25 MB aggregate/exam**. **201:** image metadata row. **413:** over size cap. **415:** wrong type.
- `DELETE /lecturer/exams/:id/intro-images/:imageId` → **204**. **404:** image not found.
- `PUT /lecturer/exams/:id/intro-images/order` — **Payload:** `{"order": [{"id", "sort_order"}, ...]}` → **200:** `{"message": "Order updated"}`.

### Import Per-Exam Curriculum
Imports a SQL script into the exam's own `curriculum_exam_{id}` schema.
- `POST /lecturer/exams/:id/curriculum/import?confirm_replace=true`
- **Payload:** `{"sql": "<unqualified SQL>"}` — schema-qualified names (`public.x`, etc.) rejected with 400. `created_by` recorded to `app.schema_import_audit`.
- **Behaviour:** drops+recreates `curriculum_exam_{id}`, sets `app.examinations.curriculum_schema`. Phase-0 capture, Phase-2 SELECT reads, and Phase-1 sandbox clone all source from it.
- **200:** `{"message": "Curriculum imported into exam"}`.
- **400:** schema-qualified identifier / empty sql. **404:** exam not found. **409:** exam active, or schema non-empty without `confirm_replace`.

### Exam Roster
- `GET /lecturer/exams/:id/students` → **200:** `{"count": int, "students": [...]}`.
- `DELETE /lecturer/exams/:id/students/:studentId` → **200:** `{"message": "Student removed from roster"}` (409 while active).
- `POST /lecturer/exams/:id/students` — **Payload:** `{"student_ids": [1,2,3]}` → **200:** `{"message": "Students assigned to examination"}`. Inserts into `app.exam_students` with `assigned_by`/`assigned_at`. **409** while active.

### Consolidated Scores
- `GET /lecturer/scores` → **200:** consolidated per-student score payload (each entry carries `exam_scores` keyed by exam title, practice total, grand total). The **CSV export is built client-side** in `LecturerResults.vue` (two separate downloads: `exam_scores.csv`, `practice_scores.csv`); there is no server-side CSV endpoint.

---

## Notes for Implementers
- All `app.questions` mutations recompute the cache synchronously inside `UpsertQuestion`; `ImportSchema` is refused while any exam is active so curriculum drift can't invalidate live caches.
- 409 bodies carry structured errors (see activate / curriculum import) so the frontend renders actionable messages.
- `student_sql` in `app.submissions` is stored verbatim — treat as untrusted on read; escape before rendering.

---

## Practice

Student practice endpoints return **423 Locked** (`{"error": "practice_unavailable_during_exam"}`) only when an active exam has its `block_practice_during_exam` toggle set. With the toggle off (default) practice stays live during exams. Lecturer practice endpoints are never frozen.

### Practice (Student)

- `GET /practice/questions` → **200:** array of practice questions with `correct_sql`/`expected_result`/`expected_columns` **stripped**.
- `GET /practice/questions/:id` → **200:** single practice question (sensitive fields stripped). **404:** not found.
- `POST /practice/submit` — **Payload:** `{"practice_question_id": int, "sql_text": "..."}` (`student_id` from JWT). Resolves the question's category schema (must be imported), validates, and persists a `PracticeAttempt` regardless of outcome. **200:** validation `result` (`is_correct`, `validation_details`, ...). **400:** category not configured / category DB not configured / category schema empty. **404:** question not found. **500:** persist/validation failure.
- `POST /practice/preview` — same payload. **200:** `{"status", "feedback", "rows": [...], "columns": [...], "row_count", "truncated"}`. Not persisted.
- `GET /practice/attempts` → **200:** array of the caller's attempts.
- `GET /practice/summary` → **200:** the student's practice summary (answered / solved / points).
- `GET /practice/categories/:id/introduction` → **200:** `{"category_id", "category_name", "introduction", "imported": bool, "tables": [...]}` (`imported:false`, `tables:[]` when the schema has no tables). **404:** category not found.
- `GET /practice/categories/:id/intro-images` → **200:** `{"images": [...]}`.
- `GET /practice/categories/:id/intro-images/:imageId` → **200:** raw image bytes with ETag caching; **304** on `If-None-Match` match.

### Practice (Lecturer)

- `POST /lecturer/practice/question` — **Payload:** `models.PracticeQuestion` with **top-level** strict flags (`strict_ordering`, `strict_attrs`, `strict_column_order`, all default `false`) and a required `category_id > 0`; `id=0` creates. `created_by` from JWT. **200:** `{"message": "Practice question saved", "id", "expected_result_status", "expected_result_captured_at", "capture_error"}`. **404:** category not found.
- `GET /lecturer/practice/questions` → **200:** full array (sensitive fields intact).
- `DELETE /lecturer/practice/question/:id` → **200:** `{"message": "Practice question deleted"}`.
- `POST /lecturer/practice/preview` — **Payload:** `{"type", "target_table", "correct_sql"}` (no `exam_id`). **200 (ok):** `{"status": "ok", "rows", "columns"}`; **200 (engine error):** `{"status": "error", "error"}`.
- `POST /lecturer/practice/reset-sandbox` → **200:** `{"reset_at": <timestamp>}`. Rebuilds practice schemas.
- `GET /lecturer/practice/categories` → **200:** array of categories.
- `POST /lecturer/practice/categories` — **Payload:** `{"name": "..."}` → **201:** created category.
- `PUT /lecturer/practice/categories/:id` — **Payload:** `{"name": "..."}` → **200:** `{"message": "Category renamed"}`. **404:** not found.
- `DELETE /lecturer/practice/categories/:id?force=true` → **200:** `{"message": "Category deleted"}`. **409:** category has questions (without `force`) or is the protected `Uncategorized` fallback.
- `POST /lecturer/practice/categories/:id/import?confirm_replace=true` — **Payload:** `{"sql": "..."}` (unqualified; `created_by` audited). **200:** `{"message": "Database imported into category"}`. **409:** schema non-empty without `confirm_replace`. **400:** schema-qualified name.
- `PUT /lecturer/practice/categories/:id/introduction` — **Payload:** `{"introduction": "..."}` (max 20 000) → **200:** `{"message": "Category introduction updated"}`.
- `POST /lecturer/practice/categories/:id/intro-images` — multipart `image`, PNG/JPEG, 5 MB/file, 25 MB aggregate. **201:** metadata. **413**/**415** on cap/type.
- `DELETE /lecturer/practice/categories/:id/intro-images/:imageId` → **204**.
- `PUT /lecturer/practice/categories/:id/intro-images/order` — **Payload:** `{"order": [{"id", "sort_order"}, ...]}` → **200:** `{"message": "Order updated"}`.
