# SQL Quiz System

A secure, scalable, and isolated PostgreSQL examination platform designed for computer science students.

## Key Features
- **Hybrid Schema Isolation**: SELECT submissions read directly from frozen `public.*` under a SELECT-only Postgres role (`exam_student_readonly`) — no clone, no queue, and data-modifying CTEs are rejected at the engine level. DML submissions share one per-exam sandbox `sandbox_exam_{examID}` cloned from `public.*` at activation; cross-student isolation is via the global in-process FIFO submission queue plus mandatory savepoint rollback. System tables live in `app.*`.
- **Pre-cached Correct Result**: Teacher SQL runs **once** at question creation; the expected output is captured on `app.questions` and reused for every submission. Grading never re-executes teacher SQL.
- **Freeze-on-Activate**: Activating an examination freezes its questions and the curriculum, guaranteeing both the cached Correct Result stays valid and the DML sandbox stays a faithful copy of `public.*` throughout the active window.
- **Hermetic DML Evaluation**: Every DML submission runs inside `BEGIN; SAVEPOINT student_eval; … ROLLBACK TO SAVEPOINT student_eval; COMMIT` so the student's writes never persist; the shared sandbox returns to its activation-time state for the next student in the queue.
- **One-Shot Sandbox**: The per-exam sandbox is cloned once at activation (only when the exam has DML questions) and dropped once at deactivation. No per-student schemas, no first-submission clone latency.
- **Global FIFO Submission Queue**: A single in-process queue (one worker goroutine, channel capacity 256, 30 s per-job timeout) serialises all DML submissions across the whole system. SELECT submissions skip the queue entirely and run in parallel under the read-only role, bounded by a 30 s per-query timeout.
- **Durable Submission Audit**: Every attempt is persisted to `app.submissions` with score and validation details — kept read-only after the exam is archived.
- **Dynamic Grading**: Supports both Query (SELECT) and DML (Insert/Update/Delete) validation.
- **Strict Mode**: Per-question toggles for result-row ordering and column-name strictness.
- **RBAC**: Role-based access control for Admins, Lecturers, and Students.
- **Real-time Feedback**: Students receive immediate scores and validation results.
- **Polished Vue 3 Frontend**: Reactive auth store + router guards (admin wildcard with per-route opt-out); inline form validation; single-flight mutation guards; brutalist confirm dialog and focus trap; sticky table heads; sortable/filterable question table; quiz progress indicator; mobile hamburger nav below 768 px; WCAG AA contrast; keyboard shortcuts (Ctrl/Cmd+Enter to submit, `Mod+/` to toggle line comment in the SQL editor, Format SQL button); 198/198 vitest tests passing.

## Project Structure
- `backend/`: Go (Gin) API server.
- `frontend/`: Vue 3 (Vite) single-page application.
- `docs/`: Architectural diagrams and system documentation.
- `api_tests/`: Bruno API collection for testing endpoints.

## Quick Start
1. **Prerequisites**: Docker and Docker Compose installed.
2. **Setup Environment**: Copy `.env.example` to `.env` (if provided) or ensure environment variables are set.
3. **Launch**:
   ```bash
   docker-compose up -d --build
   ```
4. **Access**:
   - Web UI: `http://localhost`
   - Default Admin: `admin` / `admin123`

## Documentation
- [Architecture](ARCHITECTURE.md)
- [API Specification](API_SPEC.md)
- [System Manual](MANUAL.md)
- [Development Guide](DEVELOPMENT.md)
- [Project Goals](PROJECT.md)

## License
(To be added)
