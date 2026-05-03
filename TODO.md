# Project Roadmap & TODO

## Phase 1: Core Validation Engine (Current Focus)
- [x] Implement SQL Syntax Error handling (returns zero score and error feedback).
- [x] Develop Query Validation (Select) logic:
    - [x] Result set matching.
    - [x] Optional: Command ordering check (string sequence).
    - [x] Optional: Attribute name strictness.
    - [x] Optional: Record order strictness.
- [x] Develop DML Validation (Insert/Update/Delete) logic:
    - [x] "Backup -> Execute Teacher -> Diff" workflow.
    - [x] "Execute Student -> Diff" workflow.
    - [x] Row-by-row structural comparison of diffs.
    - [x] Idempotent query detection (zero score logic).
- [x] Set up PostgreSQL Repository and API Controller.

## Phase 2: Infrastructure & Security
- [x] Setup Docker Compose for Go Gin + PostgreSQL.
- [x] Implement DB Sandbox:
    - [x] Logic to generate temporary schemas/database instances per student.
    - [x] Restricted database user permissions (CRUD only).
- [x] Implement Request Queueing:
    - [x] Postgres session-level advisory locks (`pg_advisory_lock`) for cross-process serialisation.
- [x] Enable CORS for frontend-backend communication.

## Phase 3: Lecturer Management (Placeholder)
- [x] API for lecturers to submit "Correct Answers".
- [x] API for lecturers to define custom DB structures (via SQL Import).

## Phase 4: Frontend (Vue 3)
- [x] Basic exam interface for students.
- [x] Simple dashboard for lecturers to manage questions.
- [x] Real-time feedback/score display.

## Phase 5: Hierarchical User Management
- [x] Implement `system_users` table and role system.
- [x] Implement JWT-based Authentication.
- [x] Implement Account Generation API (Admin/Lecturer flow).
- [x] Implement Bulk Account Generation (Multiple users at once).
- [x] Frontend: Login Page.
- [x] Frontend: Admin Dashboard for account generation.

## Maintenance & DevOps
- [x] Secure `docker-compose.yml` with `.env` file.
- [x] Containerize frontend using Nginx multi-stage build.
- [x] Upgrade backend to Go 1.26.2.

## Architecture Refactor (2026-05-02, from CRITIQUE.md "Architecture & Design")
- [x] Move system tables (`system_users`, `examinations`, `questions`, `exam_students`) into a dedicated `app` schema and drop the hardcoded exclusion list in `GetPublicTables`.
- [x] Make `InitializeSandbox` idempotent — only clone tables that are missing from the student schema.
- [x] Switch sandbox cloning from `CREATE TABLE … AS SELECT` to `CREATE TABLE … (LIKE public.x INCLUDING ALL)` + `INSERT … SELECT` to preserve PKs / NOT NULL / defaults / indexes.
- [x] Add `ResetTable(schema, target)` and call it before each DML evaluation so per-question state doesn't leak across submissions.
- [x] Replace `fmt.Sprintf("%v", results)` comparison with a structurally-deterministic JSON-based diff.
- [x] Honour `StrictMode` in `compareRows` and DML validation.

### Deferred Architecture (still in CRITIQUE.md)
- [ ] Wrap DML evaluation in a single transaction with savepoints.
- [ ] Replace `restoreTable`'s TRUNCATE/INSERT with transactional rollback.
- [ ] Add an AST-based ordering check or remove the claim from the docs.

## Critical Security Improvements (from CRITIQUE.md)
- [x] **Identity Integrity:** Derive student identity strictly from JWT context in `SubmitAnswer` (prevent impersonation).
- [x] **SQL Injection Prevention:** Sanitize and quote schema names using `pgx.Identifier`.
- [x] **Auth Hardening:**
    - [x] Fail-fast on startup if `JWT_SECRET` is unset or insecure (< 32 bytes).
    - [x] Fix `ValidateToken` typo (`analyzer` -> `alg`).
    - [x] Add explicit type checks for JWT claims to prevent panics.
    - [x] Add login rate limiting / account lockout.
- [ ] **Access Control:** Restrict `ImportSchema` to non-destructive roles or specific SQL subsets.
- [ ] **Admin Security:** Force password rotation for default `admin` account.

## Technical Debt & Refactoring (from CRITIQUE.md)
### Backend (Go)
- [x] Plumb request context through all repository and service methods.
- [x] Tune `pgxpool.Config` (MaxConns, MaxConnIdleTime, etc.).
- [x] Fix ignored errors in controllers (e.g., `strconv.Atoi`).
- [ ] Log cleanup failures in deferred execution.
- [x] Fix database initialization order (FK dependencies).
- [x] Resolve `SERIAL` desync in `questions` table (Switched to INTEGER PRIMARY KEY).
- [x] Add missing indexes for performance (`exam_students`, `questions`).

### Frontend (Vue 3)
- [x] **UX Improvements:**
    - [x] Auto-fetch assigned questions for students based on enrolled exams.
    - [ ] Multi-select component for student assignment.
    - [ ] Integrate a real SQL Editor (CodeMirror or Monaco).
- [x] **Code Quality:**
    - [x] Define TypeScript interfaces and remove `any` usage.
    - [x] Pin dependencies in `package.json` to current stable versions.
    - [ ] Implement `vue-router` for proper routing and guards.
    - [ ] Global error/toast notification component.

### DevOps & Infrastructure
- [x] **Reliability:**
    - [x] Add Postgres healthcheck and `service_healthy` condition in Compose.
    - [x] Add `HEALTHCHECK` instructions to Dockerfiles.
    - [x] Backend: Run as a non-root user in the production stage.
- [x] **Nginx Optimization:**
    - [x] Forward real client IPs (`X-Real-IP`, `X-Forwarded-For`).
    - [x] Add caching, gzip, and security headers (CSP, HSTS).
- [ ] **CI/CD:** Set up GitHub Actions for automated testing.

## Testing Strategy
- [x] Strengthen "Correct DML" validation tests with distinct teacher/student states.
- [ ] Implement unit tests for Auth, Repository, Controllers, and Lock Manager.
- [ ] Add integration tests using a real PostgreSQL instance (e.g., via Testcontainers).
