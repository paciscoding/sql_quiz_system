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

- `app.*` — system tables (`system_users`, `examinations`, `questions`, `exam_students`). Created automatically on backend startup by `InitializeSystemTables`.
- `public.*` — lecturer-imported curriculum (e.g. the Elmasri company schema). Treated as the clone template.
- `student_{id}.*` — per-student sandbox, populated lazily on first submission and reused thereafter.

When writing repository methods, qualify system-table queries with `app.` and curriculum queries with `public.` (or `student_{id}.` when running through the sandbox).

## Coding Conventions
...

- **Framework:** Gin
- **JSON Tags:** Use `snake_case` (e.g., `json:"question_id"`).
- **Architecture:** Maintain a clean separation between `controllers`, `services`, and `models`.
- **Errors:** Return meaningful JSON errors with appropriate HTTP status codes.

### Frontend (Vue 3)
- **Composition API:** Use `<script setup>`.
- **Styling:** Vanilla CSS or scoped styles within components.

## Testing Strategy
- **API Testing:** Use the Bruno files located in `/api_tests`.
- **Database:** Test submissions against a local `test_sandbox` schema before committing logic.
