# SQL Quiz System

A secure, scalable, and isolated PostgreSQL examination platform designed for computer science students.

## Key Features
- **Schema Isolation**: Each student gets an isolated database schema for their submissions. System tables live in a separate `app` schema so the lecturer's `public` schema can be cloned into student sandboxes wholesale.
- **Idempotent Sandbox Init**: Per-student schemas are initialised on first submission and reused; only missing tables are cloned, and only the question's target table is reset between DML attempts.
- **Cross-process Submission Lock**: `LockManager` uses Postgres advisory locks, so the per-student serialisation guarantee survives horizontal scaling.
- **Dynamic Grading**: Supports both Query (SELECT) and DML (Insert/Update/Delete) validation.
- **Strict Mode**: Fine-grained control over keyword ordering, attribute strictness, and record order.
- **RBAC**: Role-based access control for Admins, Lecturers, and Students.
- **Real-time Feedback**: Students receive immediate scores and validation results.

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
