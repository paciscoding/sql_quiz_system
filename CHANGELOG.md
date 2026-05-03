# CHANGELOG.md

## [2026-04-20] - Initial Documentation & Design
### Added
- Created `GEMINI.md` as the technical source of truth.
- Created `TODO.md` for task tracking.
- Created `DEVELOPMENT.md` for local setup and coding standards.
- Created `API_SPEC.md` for frontend-backend contract.
- Established `docs/` directory with architectural and logic flow diagrams.
- Finalized architectural decisions on schema isolation and submission queueing.

## [2026-04-22] - Core Backend Implementation
### Added
- Initialized Go backend with Gin.
- Implemented `ValidationService` for Query and DML validation.
- Implemented `PostgresRepository` with `pgx`.
- Implemented `QuizController` and basic submission logic.

## [2026-04-24] - Frontend Development & Styling
### Added
- Created Vue 3 frontend project with Vite.
- Implemented `StudentView` with SQL editor and feedback.
- Implemented `LecturerView` for question management.
- Integrated backend API calls.
- Styled UI with scoped Vanilla CSS.

## [2026-04-26] - Auth & RBAC
### Added
- Implemented `system_users` table and JWT-based authentication.
- Added role-based middleware (Admin, Lecturer, Student).
- Integrated bcrypt for password hashing.
- Added hierarchical account creation.

## [2026-04-28] - Examination & Enrollment Features
### Added
- Implemented `examinations` and `exam_students` models.
- Added endpoints for creating, activating, and assigning students to exams.
- Updated `QuizController` to filter questions by exam enrollment.
- Added bulk student enrollment support.

## [2026-04-30] - Schema Isolation & Refinement
### Added
- Implemented `ImportSchema` for custom exam environments.
- Developed dynamic table cloning for per-student sandboxes.
- Implemented `StrictMode` logic for fine-grained grading.
- Containerized the entire stack with Docker Compose.

## [2026-05-02] - Documentation Audit & Security Fixes
### Fixed
- Corrected typos and grammar across all documentation.
- Updated `API_SPEC.md` to match current implementation.
- Refactored `PROJECT.md` to reflect optimized "Lowered Operation" strategy.
- Implemented user password change endpoint.

## [2026-05-02] - Architecture Refactor (Sandbox & Locking)
### Changed
- Moved system tables (`system_users`, `examinations`, `questions`, `exam_students`) from `public` into a dedicated `app` schema. `public` is now reserved for the lecturer's curriculum and can be cloned wholesale into student sandboxes.
- `InitializeSandbox` is now idempotent: it inspects `pg_catalog.pg_tables` and only clones tables that are missing from the student schema. Repeat submissions no longer pay for a full re-clone.
- Sandbox cloning switched from `CREATE TABLE … AS SELECT` to `CREATE TABLE … (LIKE public.x INCLUDING ALL)` + `INSERT … SELECT`, preserving primary keys, NOT NULL, defaults, and indexes.
- Replaced the in-memory `sync.Map`-based `LockManager` with Postgres session-level advisory locks (`pg_advisory_lock`, keyed by FNV hash of the student ID). Per-student serialisation now survives horizontal scaling and no longer leaks process memory.
### Added
- `repository.ResetTable(schema, target)` — rebuilds a single sandbox table from its `public` counterpart. Called from `QuizController.SubmitAnswer` before each DML evaluation so state changes don't leak across submissions.
- `repository.PostgresRepository.Pool()` accessor so the advisory-lock `LockManager` can acquire its own connection from the shared pool.
### Removed
- Hardcoded system-table exclusion list in `GetPublicTables`. After the schema move it is no longer needed.
