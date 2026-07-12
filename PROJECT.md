# SQL Quiz System

## Introduction
This system is intended to be a PostgreSQL command examination system for undergraduate computer science students, consisting of Query, Insert, Update, and Delete. The student body within the examination period will be around 90 - 150 students, so the scaling should not be too complex but still tolerant of unexpected system downtime.

## Architecture
### Tech Stacks
- Frontend: Vue 3
- Backend: Go Gin
- DB: PostgreSQL
- Container: Docker
- Reverse Proxy: Nginx

## System Status
The core sprint goals have been achieved. The system successfully validates student submissions (Query, Insert, Update, Delete) against cached teacher results using an optimized Phase Lifecycle.

### Features
#### Query Examination Quiz
- Students submit data in the format: `{ question_number, sql_text }`
- Teachers upload correct answers during Phase 0.
- System captures and caches the expected result-set.
- Validation includes strict/non-strict record ordering and attribute name checks.

#### DML Examination Quiz (Insert, Update, Delete)
- Synchronous capture of post-mutation table state during Phase 0.
- Student submissions are evaluated against a shared per-exam sandbox.
- Transactional isolation: each student's work is rolled back after capture to ensure a clean state for the next submission.
- FIFO queueing ensures serialized access to the sandbox for DML operations.

#### Phase Lifecycle
- **Phase 0 — Prep**: Curriculum import, question definition, and correct result capture.
- **Phase 1 — Activation**: Sandbox preparation and freezing of question definitions.
- **Phase 2 — Active**: Student submissions and real-time grading.
- **Phase 3 — Archive**: Sandbox cleanup and score persistence.

**Scheduling note.** Activation is not only a manual click. An exam carries optional `scheduled_start_at` / `scheduled_end_at` timestamps (RFC3339 UTC), and a single `ExaminationEffectiveState` helper derives a lazy *effective state* over the half-open window `[start, end)`: `inactive` (never activated), `scheduled` (activated but before `start`), `active` (within the window or an instant activation with no start), `ended` (past `end`). There is **no background scheduler** — the state is computed on read, and the DML sandbox is prepared lazily the first time an enrolled student pulls the questions of an exam that has become effectively `active`. Submission gates key off the effective state (`active`), not the raw `is_active` flag.

#### Live Exam Start (SSE)
- Students select an enrolled exam and, if it is `scheduled`, land on a **waiting page** rather than a hard error.
- The backend exposes `GET /api/v1/quiz/exams/:id/stream` (Server-Sent Events, JWT via `access_token` query param since `EventSource` cannot set headers). It pushes `exam-status` frames carrying `effective_state` + `server_time`; the client transitions from *waiting* to *active* the moment the exam flips, with a periodic poll fallback if the stream drops. WebSocket and an Nginx-level load-balancer were rejected in favour of the in-process SSE `Hub`.

#### Student Management & Authentication
- Manual creation and bulk roster import (.csv/.xlsx) are supported.
- Automatic account generation with student numbers as credentials; staff (lecturer/admin) passwords are **server-generated** and shown once.
- Examination enrollment and performance tracking.
- **Email-based password management**: optional email on every account; self-service change-password; a two-step **forgot-password** flow (6-digit OTP emailed, then a fresh password issued); admin **reset-password** / **resend-credential** actions; and a per-user **password-events** audit trail (optionally storing the recoverable password under `PASSWORD_LOG_KEY`). Every password change bumps a JWT `token_version` that evicts stale sessions. Real SMTP delivery is Gmail STARTTLS (best-effort; sends are skipped silently when no address is on file).

#### Practice Mode
- Practice questions are organised into **categories**, each owning its own isolated schema (`practice_cat_{id}`) into which a lecturer imports a database — fully separate from the exam curriculum in `public`.
- Both SELECT and DML practice submissions run against the category schema (SELECT read-only, DML via savepoint-rollback), so students can hone skills outside exam periods.
- Practice stays **live during exams by default**; a lecturer can opt an individual exam into freezing practice (`block_practice_during_exam`), which returns HTTP 423 on practice submit/preview while that exam is active.

##### Lowered Operation Strategy (Optimized Check)
The evaluation lifecycle is split into four phases. Each phase has a fixed set of operations and is gated on the examination's activation state, so the minimization holds end-to-end.

**Phase 0 — Lecturer Prep (exam inactive, submissions rejected):**
1. **Curriculum Import**: `public.*` is populated from the lecturer's SQL import.
2. **Question Definition**: Each question records teacher SQL, type, target table, and strict-mode flags.
3. **Correct Result Capture**: On question creation/upsert, the teacher's SQL is executed inside a transaction on a clone of `public`, the expected output is captured (result-set for SELECT, target-table post-image for DML), and the transaction is rolled back. The captured result is cached on the question row — this is the only time teacher SQL runs.
4. **Student Assignment**: Students are enrolled in the examination.

**Phase 1 — Activation (transition event):**
5. **Pre-flight**: Activation requires every question to have a valid cached Correct Result; otherwise it is rejected.
6. **Sandbox preparation**: If the exam contains at least one DML question, a single shared sandbox `sandbox_exam_{examID}` is cloned from `public.*` once. Pure-SELECT exams skip cloning entirely and read from `public.*` directly under a SELECT-only role.
7. **Freeze**: Once activated, the curriculum, teacher SQL, and question definitions are immutable for the duration of the active window. This is what makes the Phase 0 cache trivially valid throughout Phase 2 and lets the SELECT path safely read frozen `public.*`.

**Phase 2 — Active Exam (submission event loop):**
8. For each submission, the system branches on question type:
   - **SELECT (Query)**: Run the student's SQL directly against `public.*` under role `exam_student_readonly` (only `USAGE` + `SELECT`). No clone, no queue — reads can't contend, and the role rejects writes (including data-modifying CTEs) at the engine level. A 30 s `context.WithTimeout` inside `evaluateQuery` bounds pathological queries. Compare the result against the cached Correct Result.
   - **DML**: Submit the evaluation closure to the global in-process FIFO submission queue (single worker goroutine, channel capacity 256, 30 s per-job timeout) so only one student at a time mutates the sandbox. Inside the worker, `BEGIN; SAVEPOINT student_eval` against `sandbox_exam_{examID}` under role `exam_student`, run the student's SQL, capture `target_table` post-state, `ROLLBACK TO SAVEPOINT student_eval; COMMIT`. The next student in the queue sees the sandbox in its activation-time state.
9. Persist the submission to `app.submissions` regardless of branch. No teacher SQL is executed during this phase; the Phase 0 cache is the sole source of truth.

**Phase 3 — Deactivation / Archive:**
10. The examination is deactivated; new submissions are rejected.
11. The per-exam sandbox `sandbox_exam_{examID}` is dropped (single `DROP SCHEMA … CASCADE`); pure-SELECT exams have no sandbox to drop.
12. Final scores are persisted and the examination is archived for read-only access.
