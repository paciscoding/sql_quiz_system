# DEVELOPMENT.md

## Local Setup
### Prerequisites
- **Go:** 1.26+
- **Bun:** 1.0+ (Package Management)
- **PostgreSQL:** 15+
- **Docker:** For containerized environment

### Getting Started

#### 1. Database Initialization
The backend expects a database named `sql_quiz` to exist. You can set this up in two ways:

**Option A: Using Docker (Recommended)**
```bash
docker-compose up -d db
```
*This starts only the database container with the correct user, password, and database name as defined in `.env`.*

**Option B: Manual Setup**
1. Create a PostgreSQL database named `sql_quiz`.
2. Ensure you have a user with the same credentials as your `.env` file.

#### 2. Environment Configuration
Create a `.env` file in the root directory. For **local development** (running Go outside Docker), use `localhost` instead of `db` in the `DATABASE_URL`:

```env
DB_USER=postgres
DB_PASSWORD=secure_exam_password
DB_NAME=sql_quiz
# Use localhost for local development
DATABASE_URL=postgres://postgres:secure_exam_password@localhost:5432/sql_quiz?sslmode=disable
JWT_SECRET=your_secret_key_at_least_32_chars
PORT=8080
```

#### 3. Running the Backend
Since the Go code does not automatically load `.env` files from the parent directory, you must export the variables or use a tool:

```bash
# From the root directory
export $(cat .env | xargs) && cd backend && go run cmd/main.go
```
*Alternatively, ensure the environment variables are set in your IDE or shell profile.*

#### 4. Running the Frontend
```bash
cd frontend
bun install
bun run dev
```

### Troubleshooting "Database Not Found"
- **Connection Error:** Ensure PostgreSQL is running and reachable at the host/port specified in `DATABASE_URL`.
- **Database Name:** Verify that the database `sql_quiz` (or whatever is in your URL) has been created.
- **Environment Variables:** Run `env | grep DATABASE_URL` to confirm the variable is actually set in your current shell before running `go run`.
- **Docker Networking:** If the backend is inside Docker, use `@db:5432`. If the backend is local, use `@localhost:5432`.

## Database Layout (Quick Reference)
The backend assumes three logical schemas inside the `sql_quiz` database:

- `app.*` — system tables, created/migrated by `InitializeSystemTables` on backend startup. All DDL there must be idempotent (`CREATE TABLE IF NOT EXISTS`, `ALTER TABLE … ADD COLUMN IF NOT EXISTS`):
  - `app.system_users` — identity (admin / lecturer / student) keyed by a `role` discriminator.
  - `app.examinations` — exam definitions with `is_active`, `activated_at`, `archived_at` lifecycle flags.
  - `app.questions` — question definitions **plus the cached Correct Result** (`expected_result jsonb`, `expected_columns jsonb`, `expected_result_status`, `expected_result_captured_at`, `capture_error`).
  - `app.exam_students` — enrollment junction with `assigned_by`, `assigned_at`.
  - `app.submissions` — durable per-attempt record (`sql_text`, `is_correct`, `score`, `validation_details`, `submitted_at`).
  - `app.exam_sandboxes` — bookkeeping for per-exam DML sandbox schemas (`exam_id PK`, `schema_name`, `created_at`, `dropped_at`). One row per exam; pure-SELECT exams don't have a row.
- `public.*` — lecturer-imported curriculum. Treated as a frozen clone template during the active window. SELECT submissions read from here directly under role `exam_student_readonly`; mutations (via `ImportSchema`) are blocked while any exam is active.
- `sandbox_exam_{examID}.*` — shared per-exam DML sandbox cloned from `public.*` at Phase 1 activation (only when the exam has DML questions). All enrolled students of that exam read and write the same physical tables here under role `exam_student`; cross-student isolation is via the global in-process FIFO `SubmissionQueue` (single worker goroutine, channel capacity 256, 30 s per-job timeout) plus mandatory savepoint rollback. Dropped at Phase 3.

For full details on what each phase reads / writes from these tables, see `ARCHITECTURE.md § Phase Lifecycle`.

When writing repository methods, qualify system-table queries with `app.`, curriculum queries with `public.` (and student SELECTs that read it under `exam_student_readonly`), and student DML queries with the dynamic `sandbox_exam_{examID}` schema. Always quote schema and table names through `pgx.Identifier{}.Sanitize()`.

## Coding Conventions
...

- **Framework:** Gin
- **JSON Tags:** Use `snake_case` (e.g., `json:"question_id"`).
- **Architecture:** Maintain a clean separation between `controllers`, `services`, and `models`.
- **Errors:** Return meaningful JSON errors with appropriate HTTP status codes.

### Frontend (Vue 3)
- **Composition API:** Use `<script setup>`.
- **Styling:** Vanilla CSS or scoped styles within components. Brutalist monotone aesthetic; `prefers-reduced-motion` honored on all animated elements.
- **Layout:**
  - `frontend/src/composables/` — reusable behaviour (`useFormValidation`, `useSingleFlight`, `useAutosave`, `useKeyboardShortcut`, `useFocusTrap`).
  - `frontend/src/services/` — singletons / module-state (`auth`, `confirm_service`, `error_handler`, `notification_service`).
  - `frontend/src/utils/` — pure helpers (`format_date`, `format_sql`).
  - `frontend/src/constants/` — governed copy (`ui_copy.ts`).
  - `frontend/src/components/` — Vue components (views + shared building blocks).
- **Validation:** every `<form>` that uses `useFormValidation` must carry the `novalidate` attribute so the composable owns error display; do not add HTML5 `required`/`minlength`.
- **Error handling:** mutation/fetch catch blocks route through `services/error_handler.ts > handleApiError`. `!response.ok` branches throw via `toApiError`. Form-level inline errors (LoginView) are the documented exception.
- **Router:** ADMIN is a wildcard via `Rule 3` short-circuit in `beforeEach`. Use `meta: { adminBlocked: true }` for routes that must reject ADMIN (e.g. STUDENT-only screens that record per-user analytics).
- **State:** auth state lives in `services/auth.ts` (`useAuth()`); never read `localStorage` from a component or guard.

## Testing Strategy
- **API Testing:** Use the Bruno files located in `/api_tests`.
- **Database:** Test submissions against a local `test_sandbox` schema before committing logic.
- **Frontend tests:** Vitest + `@vue/test-utils` + `happy-dom`. Run with `cd frontend && bun run test` (or `npx vitest run`). Test files live in `frontend/src/__tests__/`, mirroring `composables/`, `services/`, `utils/`, and `components/`. Current count: **22 files, 198 cases.** `vite.config.ts` imports `defineConfig` from `vitest/config` so `vue-tsc -b` accepts the `test` block; do not change that import.
