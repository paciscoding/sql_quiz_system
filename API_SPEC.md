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
| Users | POST | `/users/password` | any auth |
| Users | GET | `/users/students` | Admin/Lecturer |
| Users | POST | `/users` | Admin/Lecturer |
| Users | POST | `/students` | Admin/Lecturer |
| Admin | GET | `/users/all` | Admin |
| Admin | PUT | `/users/:id` | Admin |
| Admin | DELETE | `/users/:id` | Admin |
| Admin | POST | `/users/:id/reset-password` | Admin |
| Admin | POST | `/users/:id/resend-credential` | Admin |
| Admin | GET | `/users/:id/password-events` | Admin |
| Admin | POST | `/users/bulk-reset-password` | Admin |
| Admin | POST | `/students/import-credentials` | Admin |
| Admin | GET | `/students/import-credentials/:job_id/status` | Admin |
| Admin | GET | `/export/settings` | Admin |
| Admin | PUT | `/export/settings` | Admin |
| Admin | POST | `/export/run-now` | Admin |
| Quiz | GET | `/quiz/exams/:id/stream` | any auth (query token) |
| Quiz | POST | `/quiz/submit` | Student |
| Quiz | POST | `/quiz/preview` | Student |
| Quiz | GET | `/quiz/exams` | Student |
| Quiz | GET | `/quiz/exams/:id/questions` | Student |
| Quiz | GET | `/quiz/exams/:id/results` | Student |
| Quiz | GET | `/quiz/exams/:id/introduction` | Student |
| Quiz | GET | `/quiz/exams/:id/intro-images` | Student/Admin/Lecturer |
| Quiz | GET | `/quiz/exams/:id/intro-images/:imageId` | Student/Admin/Lecturer |
| Quiz | GET | `/quiz/exams/:id/categories` | Student/Admin/Lecturer |
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
| Lecturer | POST | `/lecturer/exams/:id/curriculum/import-preview` | Admin/Lecturer |
| Lecturer | GET | `/lecturer/exams/:id/categories` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams/:id/categories` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/categories/:categoryId` | Admin/Lecturer |
| Lecturer | DELETE | `/lecturer/exams/:id/categories/:categoryId` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams/:id/categories/:categoryId/import` | Admin/Lecturer |
| Lecturer | POST | `/lecturer/exams/:id/categories/:categoryId/import-preview` | Admin/Lecturer |
| Lecturer | PUT | `/lecturer/exams/:id/categories/:categoryId/introduction` | Admin/Lecturer |
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
| Practice (lecturer) | GET | `/lecturer/practice/results` | Admin/Lecturer |
| Practice (lecturer) | GET | `/lecturer/practice/categories` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/categories` | Admin/Lecturer |
| Practice (lecturer) | PUT | `/lecturer/practice/categories/:id` | Admin/Lecturer |
| Practice (lecturer) | DELETE | `/lecturer/practice/categories/:id` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/categories/:id/import` | Admin/Lecturer |
| Practice (lecturer) | POST | `/lecturer/practice/categories/:id/import-preview` | Admin/Lecturer |
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

> **Both forgot-password routes are feature-gated and default to OFF.** The routes stay registered, but a middleware gate lets them through only when `PASSWORD_RESET_EMAIL_ENABLED` is truthy. Otherwise it aborts with a constant **404** pointing the user at the administrator — before any lookup, email, or timing-observable work. Flipping the env var is the only thing needed to bring the flow back. The admin reset path (`POST /users/:id/reset-password`) needs no email and is **not** gated.

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
- `POST /users/password`
- **Payload:** `{"old_password": "...", "new_password": "..."}`
- **200:** `{"token": "..."}` — a fresh JWT (bumped `token_version`) so the current device stays logged in; **all other devices are evicted**.
- **401:** `{"error": "invalid old password"}` (other service faults also surface as 401 here).
- **Notable:** records a `self_change` password event when `PASSWORD_LOG_KEY` is configured.

### List Students (Admin / Lecturer)
- `GET /users/students`
- **200:** array of STUDENT `User` objects, each with the six nullable profile fields (`student_number`, `student_id_ext`, `sec_lec`, `sec_lab`, `first_name`, `last_name`), omitted when null.

### Create Staff User (Admin / Lecturer)
- `POST /users`
- **Payload:** `{"username": "...", "role": "LECTURER"|"ADMIN", "email": "..."?}` — any supplied `password` is **ignored** (server-generated).
- **201:** `{"id": "<opaque user token>", "generated_password": "..."}` — shown once.
- **400:** `{"error": "STUDENT accounts must be created via /api/v1/students"}` when `role=STUDENT`.
- **403:** role-hierarchy violations (`lecturers cannot create admin accounts`, etc.).
- **409:** `{"error": "email already in use"}`.
- **Notable:** password bcrypt-hashed; `create` event recorded; credential emailed when an address is present.

### Create / Upsert Single Student (Admin / Lecturer)
- `POST /students`
- **Payload:** `{"student_number": "...", "student_id_ext": "...", "sec_lec"?, "sec_lab"?, "first_name"?, "last_name"?, "email"?}` — `student_number` and `student_id_ext` (the login username) are required.
- **201:** `{"id": "<opaque user token>", "created": bool}`, plus `"generated_password"` **only when `created == true`**.
- **400:** `{"error": "student_number is required"}` / `{"error": "student_id is required"}`.
- **409:** `{"error": "email already in use"}`.
- **Notable:** conflict on an existing student **preserves the existing password** — re-adding never resets it and returns no generated password. Shares the roster-import upsert path.

---

## Admin-only User Management

### List All Users
- `GET /users/all` → **200:** array of every `User`.

### Update User
- `PUT /users/:id`
- **Payload:** full `User` object (path `id` wins; password not settable here).
- **200:** `{"message": "User updated successfully"}`. **400:** `{"error": "invalid id id"}` (malformed/mistyped opaque token — see `idParam`).

### Delete User
- `DELETE /users/:id` → **200:** `{"message": "User deleted successfully"}`.

### Reset Password
Single-user counterpart of **Bulk Reset Passwords** below. Unlike the bulk path, this one *can* email the new password — but only on explicit opt-in.
- `POST /users/:id/reset-password`
- **Payload:** `{"email_password": bool}` — **optional**; an absent or malformed body is tolerated and defaults to `false`.
- **200:** `{"generated_password": "...", "email_requested": bool}` — the password is shown once.
- **Notable:** updates the hash, **bumps `token_version`** (evicts every session), and records an `admin_reset` event. The password is emailed **only** when `email_password` is `true` *and* the target has an address on file; otherwise the recipient is passed empty and nothing is sent. Emailing a plaintext password is a deliberate opt-in tradeoff, so it defaults off.

### Bulk Reset Passwords
Resets a batch of **STUDENT** accounts in one call — random generated passwords, never emailed, handed back to the admin as plaintext.
- `POST /users/bulk-reset-password`
- **Payload:** `{"mode": "ids"|"all_students"|"exam_roster", "user_ids": ["<opaque user token>", ...], "exam_id": "<opaque exam token>", "confirm": "RESET", "after_user_id": "<opaque user token>", "batch_size": 100}`.
  - `mode` is an explicit enum — never inferred from which optional field happens to be present.
  - `user_ids` required and non-empty **iff** `mode == "ids"` (max 1000 per request); must be **absent** for the other two modes.
  - `exam_id` required **iff** `mode == "exam_roster"`; must be absent otherwise.
  - `confirm` must be the literal string `"RESET"` on **every** request, including continuation batches — the server never trusts the UI's own confirm gate for this.
  - `after_user_id` — opaque continuation cursor from a previous response's `next_after_user_id`; absent starts from the beginning.
  - `batch_size` — optional; default `100`, clamped to `[1, 200]` (below 1 → 1, above 200 → 200).
- **Behaviour:** every check below runs before any write, in this order: confirmation literal → mode enum → mode/field agreement → `user_ids` cap → `batch_size` clamp → opaque-token decode. For `mode == "ids"` only, the **whole request** is then rejected with 400 if even one supplied id does not resolve to an existing **STUDENT** row — no ADMIN or LECTURER account, including the actor's own, can ever be reset through this path. Each resolved target gets an independently generated random password (`GeneratePassword`, the same generator the single-user reset uses — never the student number, never a shared value) and its own bcrypt hash, applied in its **own** transaction (password hash + `token_version` bump + optional `password_events` row). The batch is **not** transactional across students: a failure on one target is recorded in `failures` and the loop continues, so students already reset stay reset.
- **200:** `{"mode": "...", "processed": int, "succeeded": int, "failed": int, "remaining": int, "next_after_user_id": "<opaque>"|null, "key_configured": bool, "results": [{"user_id", "username", "student_number", "first_name", "last_name", "sec_lec", "sec_lab", "password"}], "failures": [{"user_id", "username", "error"}]}` with `Cache-Control: no-store`. `remaining` is the count of matching targets not yet processed for this mode/filter. A partial failure is still **200** (not a server error); a failing target's password and `token_version` are left unchanged. The client loops, passing the previous response's `next_after_user_id` back in as the next request's `after_user_id`, until `next_after_user_id` is `null`.
- **400:** `{"error": "confirmation_required"}` (missing/wrong `confirm`) / `{"error": "unknown mode"}` / a mode/field mismatch (e.g. `exam_id` set for `mode=ids`) / `{"error": "too_many_ids"}` (`user_ids` over 1000) / `{"error": "non_student_target", "user_ids": [...]}` (`mode=ids` naming a non-student) / an invalid opaque token in `user_ids`/`exam_id`/`after_user_id`.
- **401:** unauthenticated. **403:** LECTURER or STUDENT token (this route is Admin-only, not Admin/Lecturer like the roster-reading routes).
- **Notable:** **no email of any kind is ever sent on this path** — it deliberately bypasses the single-user reset path's `logPasswordEvent` helper (which would otherwise fire one admin notice per student when `ADMIN_EMAIL` is configured). Exactly **one** structured audit log line is written per request — actor username + id, mode, `exam_id`, `requested`/`succeeded`/`failed` counts, and the internal ids of the affected set — and it never contains a password, a hash, or ciphertext. **There is no rate limiter on this endpoint**; the audit line is its only detective control (see `TODO.md`).

### Resend Credential
- `POST /users/:id/resend-credential`
- **200:** `{"emailed_to": "..."}` — re-emails the *current* credential; does **not** regenerate the password or evict sessions.
- **400:** no email on file / `PASSWORD_LOG_KEY` not configured / no password on record / decrypt failure / SMTP send failure (all ResendCredential faults map to 400).
- **Notable:** recovers the plaintext from the newest `password_events` row (requires `PASSWORD_LOG_KEY`).

### Password Events (audit)
- `GET /users/:id/password-events`
- **200:** `{"key_configured": bool, "events": [{"source", "changed_by_username", "created_at", "new_password?"}]}` — newest first; `new_password` present only when the key is configured and decrypt succeeds. The admin's view access is itself logged (metadata only).

---

## Admin-only Submission-log Export
Controls for the background exporter that emails an admin a delta gzip-CSV of exam + practice submissions on an interval; see `MANUAL.md § Submission-log Export`. These three routes are always registered, but return **503** (`{"error": "email/export not configured"}`) when SMTP (`EMAIL_SENDER_ADDRESS`) is not configured, since the exporter never starts in that case.

### Get Export Settings
- `GET /export/settings`
- **200:** `{"enabled": bool, "last_export_at": "<RFC3339 UTC>", "interval": "24h", "recipient": "..."}`.

### Update Export Settings
- `PUT /export/settings`
- **Payload:** `{"enabled": bool}` (required).
- **200:** `{"enabled": bool}`.
- **400:** `{"error": "body must be {\"enabled\": bool}"}`.

### Run Export Now
- `POST /export/run-now`
- **Behaviour:** triggers an immediate export cycle synchronously on the request context (verification aid; the scheduled path runs on the long-lived server context).
- **200:** `{"enabled": bool, "window_start", "window_end", "submission_count": int, "practice_count": int, "sent": bool}`.
- **500:** `{"error": string, "result": {ExportResult}}` — `result` nests the same object shape as the 200 body (fields may be partially populated depending on where in the cycle the failure occurred, e.g. `sent` will be `false`); the watermark only advances on `sent: true`.

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
- **Errors:** 400 (malformed body / question not assigned to an exam), 403 (exam not active / not enrolled), 500 (fetch/validate/persist failure), 503 (queue full or shutting down; **or** the per-schema DML gate shed this evaluation — `{"error": "schema_busy", "message": "Another submission is being graded against this data. Please try again."}`), 504 (evaluation deadline exceeded).

### Preview Answer (Student)
- `POST /quiz/preview`
- **Payload:** `{"question_number": int, "sql_text": "..."}`
- **200:** `{"status": "preview"|"syntax_error", "feedback": "...", "rows": [...], "columns": [...], "row_count": int, "truncated": bool}` — `rows`/`columns` never null.
- **Notable:** same queue branching and `active`+enrollment gates as submit. **Correctness is neutralised** — a `correct`/`incorrect` outcome is rewritten to `"preview"` with feedback cleared, so students can't infer right/wrong mid-run. Syntax errors are preserved. **Not persisted.**
- **Errors:** 400, 403, 404 (question not found), 500, 503 (queue full/shutting down, or the DML gate's `schema_busy` — same body as `/quiz/submit` above), 504.

### Get Available Exams (Student)
- `GET /quiz/exams` — optional query `include_inactive=true` returns all enrolled exams; otherwise only currently-relevant ones.
- **200:** array of `ExaminationResponse`: `{"id", "title", "is_active", "created_by", "created_at", "activated_at?", "archived_at?", "scheduled_start_at?", "scheduled_end_at?", "effective_state", "introduction"}`.

### Get Exam Questions (Student)
- `GET /quiz/exams/:id/questions`
- **Behaviour:** enrollment + effectively-`active` required. Lazily prepares the DML sandbox (`EnsureExamSandboxReady`) and, for a category that owns its own schema, that category's own DML sandbox (`EnsureExamCategorySchemasReady`). Broadcasts an `active` status frame over the SSE Hub.
- **200:** array of `Question`. `correct_sql` is always cleared; for DML questions `expected_result`/`expected_columns` are nulled (QUERY questions keep them as the target to reproduce). **Additive, backward-compatible fields (exam categories):** each element gains `category_id` and `category_name` (both omitted, not null, when the question has no category) — never `schema_name`/`preview_schema_name`, which stay grading-internal. **Ordering:** the array is ordered categorised-questions-first, `category name ASC, id ASC`, then uncategorised questions by `id ASC`, so a client can group by category with a single pass.
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

### Get Exam Categories (Student / Admin / Lecturer)
Read-only "category header" view — what the flat question array in **Get Exam Questions** cannot carry: a category with zero questions, its introduction text, and its own table structure.
- `GET /quiz/exams/:id/categories`
- **Behaviour:** enrollment-gated exactly like **Get Exam Introduction**; staff (Admin/Lecturer) skip the enrollment check. **Not** active-gated, so it can back the intro page before the exam starts. Ordered `name ASC, id ASC` — the same order categorised questions in **Get Exam Questions** are grouped by.
- **200:** `{"categories": [{"id", "name", "introduction", "question_count", "imported": bool, "tables": [{"name", "columns": [{"name", "data_type", "nullable", "is_primary_key", "is_foreign_key", "references_table?", "references_column?"}]}]}]}` — the same per-table introspection shape as **Get Exam Introduction**. `imported` reflects whether the category's schema currently has any tables. **Never** carries `schema_name` / `preview_schema_name` — those stay grading-internal.
- **403:** not enrolled (student only). **404:** exam not found.

### Get My Results Summary (Student)
- `GET /quiz/results`
- **200:** array of `{"exam_id", "exam_title", "is_active", "question_count", "answered_count", "correct_count", "total_score", "max_score"}` — one per enrolled exam, including zero-submission exams.

---

## Protected Endpoints — Lecturer

### Upsert Question
- `POST /lecturer/question`
- **Payload:** `models.Question` — `{"id", "exam_id", "category_id?", "type": "QUERY"|"DML", "dml_kind?", "target_table", "correct_sql", "seed_sql?", "points", "dual_schema", "strict_mode": {"ordering": bool, "attributes": bool, "column_order": bool}}`. `created_by` is forced from the JWT.
  - `strict_mode.ordering` — rows must match the teacher's order; off ⇒ set-equality.
  - `strict_mode.attributes` — output column names must match `expected_columns`.
  - `strict_mode.column_order` — output columns must match `expected_columns` index-by-index; independent gate, default `false`. Compared case-insensitively — order is graded, casing is not.
  - `dual_schema` (default `false`) — student **preview** runs against the decoy schema while **grading** stays on the real schema. Requires a decoy to have been imported for the resolved category/curriculum.
  - `dml_kind` — `INSERT` | `UPDATE` | `DELETE`; must be empty for QUERY. Presentation/validation only; grading branches on `type`. `correct_sql` is the authority: **400** when the declared kind disagrees with its leading keyword, and **400** when it is omitted while that keyword *is* derivable. A CTE-led statement (`WITH … DELETE …`) yields no derivable keyword, so it may be saved with an empty `dml_kind` — unverifiable is not the same as wrong.
  - `seed_sql` — optional **INSERT-only** block applied inside the rolled-back savepoint before the model answer at capture, and before the student's answer at grading and preview. **400** for a non-INSERT leading keyword or unbalanced quotes, **400** when it references another schema by name — `public`, `app`, the PostgreSQL system schemas, and the `practice_cat_*` / `curriculum_exam_*` / `exam_cat_*` / `sandbox_*` families including their `_preview` variants — because a schema-qualified write to a shared table can deadlock against another category being graded at the same time (the double-quoted form `"public"."employee"` is a known gap and is **not** refused; a schema name inside a string literal is data and is likewise not refused), **400** when supplied for a QUERY question, and **400** when supplied for `dml_kind = "INSERT"` (a 0-row INSERT is an `INSERT … SELECT` over an empty source, which seeding cannot fix).
  - `auto_seed` is **never accepted from a client** — it is derived by the backend and stored, and is only ever emitted on lecturer read paths.
  - **Binding note:** `dml_kind` and `seed_sql` are `json:"-"` on `models.Question`, so the handler binds the body twice and recovers exactly those two fields by name. That is why `auto_seed`, which has no such second bind, remains un-injectable.
- **Behaviour:** saves the row, then runs the teacher SQL against the exam's curriculum clone inside a transaction, captures the result, rolls back, writes the cache. **Capture failure does not roll back the upsert.** For DML, a model answer that affects **0 rows** is refused (`expected_result_status = "failed"`): with `seed_sql` present the error says the seed inserted but the answer still matched nothing; without it the backend first tries to derive a one-row seed from the answer's `WHERE` equality terms, and reports the refusal reason when it cannot. A 0-row `INSERT` is never rescued.
- **200:** `{"message": "Question saved successfully", "id", "expected_result_status", "expected_result_captured_at", "capture_error", "table_warning", "dual_schema_warning", "mixed_dual_warning"}` (everything after `id` added on read-back success). `expected_result_status` is `captured` | `failed` | `pending`. The three warnings are advisory strings — empty when they do not apply, and **never** blocking:
  - `table_warning` — the DML `correct_sql` appears to write a different table than `target_table`.
  - `dual_schema_warning` — `dual_schema` is on but no decoy has been imported for the resolved category/exam, so previews fall back to the grading schema.
  - `mixed_dual_warning` — the exam holds both dual and non-dual questions, so a student can open a non-dual question's preview and read the grading schema, bypassing the decoy.
- **409:** `{"error": "exam is active; ..."}` when the exam is active.

### List Questions for an Exam
- `GET /lecturer/exams/:id/questions` → **200:** array of full question objects, serialised as `models.LecturerQuestionView` — the **only** shape that carries `auto_seed` and `seed_sql`. Both fields are `json:"-"` on the question model itself, so no student-facing response can leak them by omission and no request body can inject them. Also carries `expected_rows_affected` (diagnostics only; never read during grading).

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

### Import & Rotate Credentials (multipart, Admin-only)
Admin-only counterpart to **Import Student Roster** above: rotates the password of **every** row in the file, new and returning students alike, and emails each credential from a background worker. `POST /lecturer/students/import` itself is completely unchanged by this endpoint — its Admin/Lecturer permissions and its "existing student keeps their password" behaviour still apply there.
- `POST /students/import-credentials`
- **Content-Type:** `multipart/form-data`; same fields and columns as `/lecturer/students/import` (`file`, `.csv`/`.xlsx`), plus `confirm` — must be the literal string `ROTATE` on every request; the server re-checks it independently of the UI, the same rule as **Bulk Reset Passwords**' `RESET`.
- **Behaviour:** every row gets a freshly generated password (never the student number), **bumping `token_version`** so any existing session for that student is evicted — do not run this while an exam is in progress. Rows with no email address are still imported; their `email_status` is `"no_email"` and the plaintext password is only ever recoverable from this response. One `app.password_events` row is written per rotated account (`create` source for new rows, `admin_reset` for returning ones) via `AuthService.LogBulkCredentials`, which deliberately bypasses the single-user reset path's admin-notice email so a large import does not fan out one `ADMIN_EMAIL` notice per row. Rows with an email address are queued to a background worker that sends them **serially, one at a time**; the saved `email_status` stays `"queued"` forever regardless of whether the send later succeeds — there is no durable delivery record and no retry.
- **200:** `{"total", "created", "updated", "skipped", "errors": [{"row", "student_number", "message"}], "accounts": [{"row", "student_number", "student_id_ext", "email", "password", "outcome": "created"|"updated", "email_status": "queued"|"no_email"}], "job_id": "..."}`. `job_id` is `""` when no row in the file carried an email address.
- **400:** `{"error": "confirmation_required"}` when `confirm` is absent or not exactly `ROTATE` / missing required header / parse error. **500:** import service not configured.
- **Notable:** **no rate limiter on this endpoint** — the same accepted residual as `POST /users/bulk-reset-password`. Emailing plaintext passwords in bulk, and returning every one of them in the HTTP response body, is the deliberate point of this endpoint.

### Import & Rotate Credentials — Job Status (Admin-only)
- `GET /students/import-credentials/:job_id/status`
- **200:** `{"job_id", "total", "sent", "failed", "done", "failures": ["<student_id_ext>", ...]}` — progress of the background email worker started by the import above.
- **404:** `{"error": "job not found"}` — an unknown id, an id belonging to a **different** admin (deliberately indistinguishable, so the endpoint cannot be used to probe another admin's activity), or any id at all after a server restart. Progress tracking is **in-memory and single-process**, retained only 30 minutes after the job finishes; a `404` here is an ordinary expected outcome, not an error condition.

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

### Exam Categories
Organises an exam's questions into per-category databases, mirroring the practice-category surface but scoped under the owning exam (`ExamCategory`: `{"id", "exam_id", "name", "schema_name", "preview_schema_name", "has_preview_schema", "introduction", "question_count", "created_by", "created_at", "updated_at", "owns_schema"}`). `owns_schema` is `false` for a category auto-adopted from a pre-existing exam by the boot migration — it still shares the exam's own schema (often literally `public`) until a lecturer imports a database directly into it. Every route below checks `category.exam_id == :id` from the path — a category token from a different exam 404s, exactly like an unknown id.
- `GET /lecturer/exams/:id/categories` → **200:** array of `ExamCategory`. **404:** exam not found.
- `POST /lecturer/exams/:id/categories` — **Payload:** `{"name": "..."}` (required, trimmed). **201:** the created `ExamCategory` (its `exam_cat_{id}` schema is provisioned and granted to `exam_student_readonly` before the response returns). **400:** empty name. **404:** exam not found. **409:** name already used within this exam.
- `PUT /lecturer/exams/:id/categories/:categoryId` — **Payload:** `{"name": "..."}` (required, trimmed) — renames only; `schema_name` is never touched. **200:** `{"message": "Category renamed"}`. **400:** empty name. **404:** category not found (incl. cross-exam token). **409:** name already used within this exam.
- `DELETE /lecturer/exams/:id/categories/:categoryId` → **200:** `{"message": "Category deleted"}`, dropping the category's four canonical schemas (`exam_cat_{id}`, `exam_cat_{id}_preview`, and both `sandbox_exam_cat_{id}` variants) wherever they exist. **404:** category not found. **409:** `{"error": "...", "attached_question_count": N}` whenever **any** question still references the category. **There is no `force` (or any other) query parameter on this route** — unlike practice-category delete, an exam category can never be force-deleted out from under its questions; the lecturer must reassign or delete the attached questions first. (Detaching would silently re-point a question at the exam-level curriculum schema, so it would grade against different data than it was authored and captured against.)
- `POST /lecturer/exams/:id/categories/:categoryId/import?confirm_replace=true`
  - **Payload:** `{"sql": "<unqualified SQL>"}` — schema-qualified names rejected with 400.
  - **Behaviour:** same shape as **Import Per-Exam Curriculum**, scoped to the category's own `exam_cat_{id}` schema. On success, stamps `schema_name = exam_cat_{id}` (this is what flips a migrated `General` category from sharing the exam's schema to owning its own) and drops the category's now-stale DML sandbox so the next activation re-clones it.
  - **200:** `{"message": "Database imported into category"}`.
  - **400:** schema-qualified identifier / empty sql. **404:** category not found. **409:** exam active, or category schema non-empty without `confirm_replace`.
- `POST /lecturer/exams/:id/categories/:categoryId/import-preview?confirm_replace=true` — same shape, against the category's decoy schema (`exam_cat_{id}_preview`), stamping `preview_schema_name`. **200:** `{"message": "Decoy database imported into category", "parity_warning": "..."}` — `parity_warning` is `""` when the decoy structurally matches the category's real schema, or names the offending table when it does not; either way the import **succeeds**. Same 400/404/409 cases as the import above.
- `PUT /lecturer/exams/:id/categories/:categoryId/introduction` — **Payload:** `{"introduction": "..."}` (max 20 000 chars). **200:** `{"message": "Category introduction updated"}`. **400:** over the length cap. **404:** category not found. **Notable:** unlike exam- and practice-level introductions, an exam category has **no** intro-images sub-feature — introduction text only.

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

Student practice endpoints return **423 Locked** (`{"error": "practice_unavailable_during_exam"}`) only when an active exam has its `block_practice_during_exam` toggle set. Lecturer practice endpoints are never frozen.

**In practice this means every activated exam blocks practice.** The column defaults to `FALSE`, but activation (`PUT /lecturer/exams/:id/activate`) writes `block_practice_during_exam = TRUE` unconditionally, so the toggle is only ever off if a lecturer turns it off *after* activating that exam via `PUT /lecturer/exams/:id/practice-freeze`. Deactivation clears `is_active` but leaves the toggle set, and re-activating rewrites it to `TRUE` — a lecturer's "off" choice does not survive the next activation.

Two further properties of the check worth knowing when writing clients:

- **Global scope.** The gate is `EXISTS(is_active AND block_practice_during_exam)` with no join to class, enrolment, or category — one frozen exam blocks practice for *every* student in *every* category, enrolled or not.
- **Keyed on `is_active`, not the schedule window.** A future-dated exam freezes practice the moment it is activated, before `scheduled_start_at`.

Unfreezing takes effect server-side on the next request (the 5 s cache is invalidated on activate/deactivate/toggle), but nothing is pushed to the client — a student sitting on a 423 must retry or reload.

### Practice (Student)

- `GET /practice/questions` → **200:** array of practice questions with `correct_sql`/`expected_result`/`expected_columns` **stripped**.
- `GET /practice/questions/:id` → **200:** single practice question (sensitive fields stripped). **404:** not found.
- `POST /practice/submit` — **Payload:** `{"practice_question_id": int, "sql_text": "..."}` (`student_id` from JWT). Resolves the question's category schema (must be imported), validates, and persists a `PracticeAttempt` regardless of outcome. **200:** validation `result` (`is_correct`, `validation_details`, ...). **400:** category not configured / category DB not configured / category schema empty. **404:** question not found. **500:** persist/validation failure. **503:** the per-schema DML gate shed this evaluation because another submission (student or lecturer capture/preview) is being graded against the same category schema — `{"error": "practice_busy", "message": "Another submission for this category is being graded. Please try again."}`. Nothing is persisted for a 503; the client should retry.
- `POST /practice/preview` — same payload. **200:** `{"status", "feedback", "rows": [...], "columns": [...], "row_count", "truncated"}`. Not persisted. **503:** same DML-gate condition and body as `/practice/submit` above.
- `GET /practice/attempts` → **200:** array of the caller's attempts.
- `GET /practice/summary` → **200:** the student's practice summary (answered / solved / points).
- `GET /practice/categories/:id/introduction` → **200:** `{"category_id", "category_name", "introduction", "imported": bool, "tables": [...]}` (`imported:false`, `tables:[]` when the schema has no tables). **404:** category not found.
- `GET /practice/categories/:id/intro-images` → **200:** `{"images": [...]}`.
- `GET /practice/categories/:id/intro-images/:imageId` → **200:** raw image bytes with ETag caching; **304** on `If-None-Match` match.

### Practice (Lecturer)

- `POST /lecturer/practice/question` — **Payload:** `models.PracticeQuestion` with **top-level** strict flags (`strict_ordering`, `strict_attrs`, `strict_column_order`, all default `false`), `dual_schema`, optional `title`, optional `dml_kind` / `seed_sql`, and a required `category_id > 0`; `id=0` creates. `created_by` from JWT. `dml_kind` / `seed_sql` / `auto_seed` follow **exactly** the rules documented under *Upsert Question* above, including the same 400s and the same lecturer-only serialisation (`models.LecturerPracticeQuestionView`). **200:** `{"message": "Practice question saved", "id", "expected_result_status", "expected_result_captured_at", "capture_error"}`. **404:** category not found.
- `GET /lecturer/practice/questions` → **200:** full array (sensitive fields intact).
- `DELETE /lecturer/practice/question/:id` → **200:** `{"message": "Practice question deleted"}`.
- `POST /lecturer/practice/preview` — **Payload:** `{"type", "target_table", "correct_sql"}` (no `exam_id`). **200 (ok):** `{"status": "ok", "rows", "columns"}`; **200 (engine error):** `{"status": "error", "error"}`.
- `GET /lecturer/practice/categories` → **200:** array of categories.
- `POST /lecturer/practice/categories` — **Payload:** `{"name": "..."}` → **201:** created category.
- `PUT /lecturer/practice/categories/:id` — **Payload:** `{"name": "..."}` → **200:** `{"message": "Category renamed"}`. **404:** not found.
- `DELETE /lecturer/practice/categories/:id?force=true` → **200:** `{"message": "Category deleted"}`. **409:** category has questions (without `force`) or is the protected `Uncategorized` fallback.
- `POST /lecturer/practice/categories/:id/import?confirm_replace=true` — **Payload:** `{"sql": "..."}` (unqualified; `created_by` audited). **200:** `{"message": "Database imported into category"}`. **409:** schema non-empty without `confirm_replace`. **400:** schema-qualified name.
- `PUT /lecturer/practice/categories/:id/introduction` — **Payload:** `{"introduction": "..."}` (max 20 000) → **200:** `{"message": "Category introduction updated"}`.
- `POST /lecturer/practice/categories/:id/intro-images` — multipart `image`, PNG/JPEG, 5 MB/file, 25 MB aggregate. **201:** metadata. **413**/**415** on cap/type.
- `DELETE /lecturer/practice/categories/:id/intro-images/:imageId` → **204**.
- `PUT /lecturer/practice/categories/:id/intro-images/order` — **Payload:** `{"order": [{"id", "sort_order"}, ...]}` → **200:** `{"message": "Order updated"}`.
