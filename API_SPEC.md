# API_SPEC.md

The HTTP contract between frontend and backend. The phase-lifecycle behavior described under each endpoint is the **target state** per `ARCHITECTURE.md § Phase Lifecycle`; endpoints marked *(redesign)* are not yet implemented as specified.

## Base URL
`http://localhost:80/api/v1` (proxied via Nginx).

## Authentication
Most endpoints require a JWT in the header:
`Authorization: Bearer <jwt>`

Status codes used throughout:
- `200` success.
- `400` malformed request body.
- `401` missing / invalid token.
- `403` authenticated but role/enrollment denies access.
- `404` resource missing.
- `409` operation rejected by phase gate (e.g., trying to upsert a question while its exam is active).
- `500` unexpected backend error.

---

## Public Endpoints

### 1. Login
- **URL:** `/login`
- **Method:** `POST`
- **Payload:** `{"username": "...", "password": "..."}`
- **Response 200:** `{"token": "...", "user": {"id": 1, "username": "admin", "role": "ADMIN", ...}}`

---

## Protected Endpoints — Users

### 2. Change Password (all roles)
- **URL:** `/users/password`
- **Method:** `POST`
- **Payload:** `{"old_password": "...", "new_password": "..."}`

### 3. Create User (Admin / Lecturer)
- **URL:** `/users`
- **Method:** `POST`
- **Payload:** `{"username": "...", "password": "...", "role": "STUDENT"}`

### 4. Bulk Create Users (Admin / Lecturer)
- **URL:** `/users/bulk`
- **Method:** `POST`
- **Payload:** `{"users": [{"username": "...", "password": "...", "role": "STUDENT"}, ...]}`

### 4.5. List Students (Admin / Lecturer)
- **URL:** `/users/students`
- **Method:** `GET`
- **Auth:** ADMIN | LECTURER
- **Response 200:** Array of student user objects. Each object includes the standard fields plus six nullable profile fields: `student_number`, `student_id_ext`, `sec_lec`, `sec_lab`, `first_name`, `last_name`. These fields are omitted from JSON when null for non-student rows.

### 4.6. List All Users (Admin only)
- **URL:** `/users/all`
- **Method:** `GET`
- **Auth:** ADMIN
- **Response 200:** Array of all user objects in the system.

### 4.7. Update User (Admin only)
- **URL:** `/users/:id`
- **Method:** `PUT`
- **Auth:** ADMIN
- **Payload:** User object with fields to update (username, role, first_name, last_name, etc.)
- **Response 200:** `{"message": "User updated successfully"}`

### 4.8. Delete User (Admin only)
- **URL:** `/users/:id`
- **Method:** `DELETE`
- **Auth:** ADMIN
- **Response 200:** `{"message": "User deleted successfully"}`

---

## Protected Endpoints — Student Quiz

### 5. Submit Answer (Student only)
Validates a SQL submission and persists the attempt to `app.submissions`.

- **URL:** `/quiz/submit`
- **Method:** `POST`
- **Payload:** `{"question_number": 1, "sql_text": "..."}`
  - Note: `student_id` is **derived from the JWT**. Any client-supplied value is ignored. (Was a security fix from the 2026-04 audit.)
- **Behavior (Phase 2):**
  1. Check that the question's exam is active and the student is enrolled (else 403).
  2. Branch on `question.question_type`:
     - **SELECT:** Acquire pool conn → `BEGIN; SET LOCAL ROLE exam_student_readonly; SET LOCAL search_path = public;` → run student SQL → collect rows → `ROLLBACK`. A 30 s `context.WithTimeout` inside `evaluateQuery` bounds pathological queries. No queue, no lock — SELECT runs in parallel across the pool.
     - **DML:** Submit the evaluation closure to the global in-process FIFO `SubmissionQueue` (single worker goroutine, channel capacity 256, 30 s per-job timeout). Inside the worker: `BEGIN; SAVEPOINT student_eval; SET LOCAL ROLE exam_student; SET LOCAL search_path = sandbox_exam_{examID}, public;` → run student SQL → `SELECT * FROM target_table` post-image → `ROLLBACK TO SAVEPOINT student_eval; COMMIT`.
  3. Compare captured post-state against `app.questions.expected_result`.
  4. `INSERT INTO app.submissions …`.
- **Response 200:** `{"status": "correct" | "incorrect" | "syntax_error", "score": 10, "feedback": "...", "is_correct": true, "validation_details": [...]}`. `status` distinguishes the three outcomes; `feedback` carries the engine error for `syntax_error` and a generic "result is wrong" for `incorrect`.
- **Response 503:** `{"error": "submission queue is full"}` or `{"error": "submission queue is shutting down"}` when the DML queue is saturated or the backend is shutting down.
- **Response 504:** `{"error": "submission timed out"}` when the per-job 30 s deadline fires.

### 6. Get Available Exams (Student only)
- **URL:** `/quiz/exams`
- **Method:** `GET`
- **Response 200:** `[{"id": 1, "title": "Midterm", "is_active": true, ...}, ...]` — only exams the student is enrolled in **and** that have `is_active = true`.

### 7. Get Exam Questions (Student only)
- **URL:** `/quiz/exams/:id/questions`
- **Method:** `GET`
- **Behavior:** 403 unless the student is enrolled and the exam `is_active`.
- **Response 200:** Array of questions. **`correct_sql`, `expected_result`, and `expected_columns` MUST NOT be included** in the response.

---

## Protected Endpoints — Lecturer

### 8. Upsert Question (Admin / Lecturer)
Triggers **Phase 0 Correct Result Capture** *(redesign)*.

- **URL:** `/lecturer/question`
- **Method:** `POST`
- **Payload:**
```json
{
  "id": 1,
  "exam_id": 1,
  "type": "QUERY",
  "target_table": "employee",
  "correct_sql": "SELECT ...",
  "points": 10,
  "strict_mode": {
    "ordering": true,
    "attributes": true
  }
}
```
- **Phase gate:** 409 if `app.examinations.is_active = true` for the question's exam.
- **Behavior:** Save the question row, then synchronously run the teacher SQL on a `public` clone inside a transaction, capture the result, roll back, write the cache fields. Capture failures **do not roll back the upsert**.
- **Response 200 (captured):**
```json
{
  "id": 1,
  "expected_result_status": "captured",
  "expected_result_captured_at": "2026-05-04T12:34:56Z",
  "row_count": 42
}
```
- **Response 200 (capture failed — row still saved):**
```json
{
  "id": 1,
  "expected_result_status": "failed",
  "capture_error": "syntax error at or near \"SELET\""
}
```
- **Response 409 (exam is active):** `{"error": "exam is active; deactivate before editing questions"}`

### 8.5. List Questions for an Exam (Admin / Lecturer)
- **URL:** `/lecturer/exams/:id/questions`
- **Method:** `GET`
- **Auth:** ADMIN | LECTURER
- **Response 200:** Array of full question objects for the exam.

### 8.6. Delete Question (Admin / Lecturer)
- **URL:** `/lecturer/questions/:id`
- **Method:** `DELETE`
- **Auth:** ADMIN | LECTURER
- **Response 200:** `{"message": "Question deleted successfully"}`

### 9. Import Schema (Admin / Lecturer)
Populates `public.*` from a SQL script.

- **URL:** `/lecturer/import-schema`
- **Method:** `POST`
- **Payload:** `{"sql": "CREATE TABLE ..."}`
- **Phase gate** *(redesign)*: 409 if **any** examination has `is_active = true`. The error body lists the offending exam IDs.
- **Behavior:** Wipe and recreate `public.*` per the script. Does not touch `app.*` or any `student_*` schemas.

### 10. List / Create Examinations
- **URL:** `/lecturer/exams`
- **Methods:** `GET`, `POST`
- **POST Payload:** `{"name": "Midterm Exam", "description": "...", "is_active": false}`
- **GET Response 200:** Array of `{"id", "title", "is_active", "activated_at", "archived_at", ...}`.

### 11. Activate Examination
Phase 1 transition.

- **URL:** `/lecturer/exams/:id/activate`
- **Method:** `PUT`
- **Pre-flight:** every question with `exam_id = :id` must have `expected_result_status = 'captured'`.
- **Response 200:** `{"id": 1, "is_active": true, "activated_at": "..."}`
- **Response 409 (pre-flight failed):**
```json
{
  "error": "activation rejected: questions missing valid Correct Result",
  "questions": [
    {"id": 3, "expected_result_status": "failed", "capture_error": "..."},
    {"id": 7, "expected_result_status": "pending"}
  ]
}
```

### 12. Deactivate Examination
Phase 3 transition. Drops the per-exam DML sandbox (if any).

- **URL:** `/lecturer/exams/:id/deactivate`
- **Method:** `PUT`
- **Behavior:** Set `is_active = false`, `archived_at = now()`. Look up `app.exam_sandboxes WHERE exam_id = :id AND dropped_at IS NULL`; if a row exists, `DROP SCHEMA sandbox_exam_{examID} CASCADE` and set `dropped_at = now()`. Pure-SELECT exams have no row and no schema to drop.
- **Response 200:** `{"message": "Examination deactivated and sandboxes cleaned up"}`

### 13. Assign Students to Exam
- **URL:** `/lecturer/exams/:id/students`
- **Method:** `POST`
- **Payload:** `{"student_ids": [1, 2, 3]}`
- **Phase gate:** 409 if exam is active.
- **Behavior:** Insert into `app.exam_students` with `assigned_by = <jwt user id>`, `assigned_at = now()`.

### 14. List Submissions for an Exam
Read-only audit trail. Returned regardless of `is_active` (lecturers can review post-archive).

- **URL:** `/lecturer/exams/:id/submissions`
- **Method:** `GET`
- **Query params:** `student_id` (optional), `question_id` (optional), `latest_only` (default `false`).
- **Response 200:** Array of `app.submissions` rows (without `student_sql` masked).

---

## Notes for Implementers
- All `app.questions` mutations (insert, update, schema_change-via-import) **must** clear or recompute the cache. The simplest implementation is to recompute synchronously inside `UpsertQuestion`, and to refuse `ImportSchema` while any exam is active so curriculum drift can't invalidate live caches.
- The 409 responses carry structured error bodies (see endpoints 8, 9, 11) so the frontend can render actionable messages, not just a generic "conflict."
- `student_sql` in `app.submissions` is stored verbatim. Treat it as untrusted text on read; do not echo it into HTML without escaping.

---

## Practice

All student practice endpoints return **423 Locked** if any examination is currently active system-wide:
`{"error": "practice_unavailable_during_exam"}`

### 15. List Practice Questions (Student only)
- **URL:** `/practice/questions`
- **Method:** `GET`
- **Response 200:** `[{"id": 1, "question_text": "...", "type": "QUERY", "target_table": "employee", "points": 10}, ...]`

### 16. Get Practice Question Detail (Student only)
- **URL:** `/practice/questions/:id`
- **Method:** `GET`
- **Response 200:** `{"id": 1, "question_text": "...", "type": "QUERY", "target_table": "employee", "points": 10}`

### 17. Submit Practice Answer (Student only)
- **URL:** `/practice/submit`
- **Method:** `POST`
- **Payload:** `{"question_id": 1, "sql_text": "..."}`
- **Behavior:** DML goes through global FIFO `SubmissionQueue` with savepoint-rollback against `sandbox_practice` schema.
- **Response 200:** `{"status": "correct"|"incorrect"|"syntax_error", "score": 10, "feedback": "...", "is_correct": true}`

### 18. Preview Practice Answer (Student only)
- **URL:** `/practice/preview`
- **Method:** `POST`
- **Payload:** `{"question_id": 1, "sql_text": "..."}`
- **Response 200:** `{"rows": [...], "columns": [...]}`

### 19. List My Practice Attempts (Student only)
- **URL:** `/practice/attempts`
- **Method:** `GET`
- **Response 200:** `[{"id": 1, "question_id": 1, "sql_text": "...", "is_correct": true, "score": 10, "submitted_at": "..."}, ...]`

### 20. Upsert Practice Question (Admin / Lecturer)
- **URL:** `/lecturer/practice/question`
- **Method:** `POST`
- **Payload:** Same shape as endpoint 8 (Upsert Question).
- **Behavior:** Triggers Correct Result Capture synchronously against `sandbox_practice`.
- **Response 200:** Same shape as endpoint 8.

### 21. List All Practice Questions (Admin / Lecturer)
- **URL:** `/lecturer/practice/questions`
- **Method:** `GET`
- **Response 200:** Array of practice questions including `expected_result_status`.

### 22. Delete Practice Question (Admin / Lecturer)
- **URL:** `/lecturer/practice/question/:id`
- **Method:** `DELETE`
- **Response 200:** `{"message": "deleted"}`

### 23. Reset Practice Sandbox (Admin / Lecturer)
- **URL:** `/lecturer/practice/reset-sandbox`
- **Method:** `POST`
- **Behavior:** Drops `sandbox_practice` schema and re-clones from `public.*`.
- **Response 200:** `{"message": "sandbox reset"}`

### 24. Lecturer Practice Preview (Admin / Lecturer)
- **URL:** `/lecturer/practice/preview`
- **Method:** `POST`
- **Payload:** `{"question_id": 1, "sql_text": "..."}`
- **Response 200:** `{"rows": [...], "columns": [...]}`

---

## Protected Endpoints — Student Bulk Import

### 25. Import Student Roster (Admin / Lecturer)
- **URL:** `/lecturer/students/import`
- **Method:** `POST`
- **Content-Type:** `multipart/form-data`; field name `file` (`.csv` or `.xlsx`)
- **Auth:** ADMIN | LECTURER (403 for STUDENT role)
- **Required file columns:** `Student number`, `SECLEC`, `SECLAB`, `Student_id`, `First Name`, `Last Name` (case-insensitive, column-order-independent).
- **Behavior:** Upserts students by `student_number`. New students are created with `username = student_number` and initial `password = student_number` (bcrypt-hashed). On re-import, profile and section fields are updated; `password_hash` is preserved.
- **Response 200:**
```json
{
  "total": 25,
  "created": 22,
  "updated": 3,
  "skipped": 0,
  "errors": [
    {"row": 5, "student_number": "S-001", "message": "empty student_number"}
  ]
}
```
- **Response 400:** `{"error": "missing required header: <column>"}` — whole-file rejection when a required column is missing or the format is unsupported.
- **Response 403:** when called with a STUDENT role JWT.

---

## Protected Endpoints — Student Exam Results

### 26. Get Exam Results (Student only)
- **URL:** `/quiz/exams/:id/results`
- **Method:** `GET`
- **Auth:** STUDENT only (enrollment enforced — 403 if not enrolled in the exam)
- **Behavior:** Returns the calling student's latest-per-question submissions for the specified exam. Results are always visible regardless of whether the exam is active or archived.
- **Response 200:**
```json
{
  "exam_id": 4,
  "exam_title": "Midterm",
  "questions": [
    {
      "question_id": 1,
      "requirement": "Write a query to list all employees",
      "question_type": "QUERY",
      "points": 10,
      "submission": {
        "sql_text": "SELECT * FROM employee",
        "is_correct": true,
        "score": 10,
        "validation_details": {},
        "submitted_at": "2026-05-10T14:30:00Z"
      }
    }
  ]
}
```
`submission` is `null` when the student has not answered a question.
- **Response 403:** student not enrolled in the exam.

### 27. Get My Results Summary (Student only)
- **URL:** `/quiz/results`
- **Method:** `GET`
- **Auth:** STUDENT only
- **Behavior:** Returns one summary entry per exam the calling student is enrolled in, including exams with zero submissions.
- **Response 200:**
```json
{
  "exams": [
    {
      "exam_id": 4,
      "exam_title": "Midterm",
      "is_active": false,
      "question_count": 5,
      "answered_count": 5,
      "correct_count": 4,
      "total_score": 40,
      "max_score": 50
    }
  ]
}
```
