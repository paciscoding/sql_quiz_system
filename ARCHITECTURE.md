# ARCHITECTURE.md - SQL Quiz System

This document serves as the primary technical reference and architecture specification for the SQL Quiz System.

## Project Overview
A PostgreSQL examination system for undergraduate computer science students (90-150 concurrent users). The system evaluates Query, Insert, Update, and Delete commands.

## Technical Stack
- **Frontend:** Vue 3, Vite
- **Backend:** Go 1.26 (Gin framework)
- **Database:** PostgreSQL
- **Infrastructure:** Docker, Nginx (Reverse Proxy)
- **Package Manager:** Bun
- **API Testing:** Bruno

## Schema Layout
The database is split across three logical schemas, each with a distinct role:

| Schema           | Purpose                                                                                          |
| ---------------- | ------------------------------------------------------------------------------------------------ |
| `app`            | System tables (`system_users`, `examinations`, `questions`, `exam_students`).                    |
| `public`         | Curriculum tables imported by the lecturer (e.g. the Elmasri company schema). Treated as a clean template. |
| `student_{id}`   | Per-student sandbox containing a copy of every `public` table. Created on first submission and reused. |

Keeping system tables in their own schema means `public` can be cloned wholesale into student sandboxes without an exclusion list — every table the lecturer imports is automatically available to students.

![Database Schema](docs/database_schema.png)

## Core Requirements & Logic

### 1. Query Validation Logic
Student submissions are checked against a "Correct Answer" (Teacher's query) through multiple layers:
1. **Result Set Match:** Does the output of the student's query match the teacher's?
2. **Command Ordering (Implemented):** Strict check on the SQL command syntax/ordering (e.g., specific keyword sequence).
3. **Attribute Strictness (Implemented):** Check if attribute names match exactly or just values.
4. **Record Order (Implemented):** Check if the sorting of rows matches exactly or allows random order.
5. **Syntax Errors:** If a student's SQL contains syntax errors, the system returns the database error for immediate feedback (Score: 0).

### 2. DML Validation (Insert/Update/Delete)
Validation uses a state-comparison approach:
1. **Backup:** Copy the current state of the target table.
2. **Teacher Execution:** Apply the teacher's correct SQL to the main table.
3. **Diff Capture:** Calculate a row-by-row difference between the backup and the updated main table. This "Corrected Result" is stored as a string representation for comparison.
4. **Restore:** Revert the main table to the backup state.
5. **Student Execution:** Apply the student's SQL submission.
6. **Comparison:** Diff the student's changes against the "Corrected Result".
7. **Idempotent Queries:** If a student's query results in no changes to the data, it is scored as **zero**.
8. **Reset:** Revert the main table to the backup state for the next evaluation.

---

## Architecture Q&A (Design Decisions)

### 1. Security & Sandboxing
*   **Q: How will the system prevent destructive SQL commands (e.g., `DROP TABLE`, `TRUNCATE`, `ALTER DATABASE`)?**
    *   **A:** All commands except for CRUD operations (SELECT, INSERT, UPDATE, DELETE) are strictly restricted via middleware whitelisting and database-level role permissions.
*   **Q: Will there be a restricted database user role for student submissions?**
    *   **A:** **Yes.** Student submissions are executed under the `exam_student` role using `SET ROLE`, which has only CRUD permissions on the student's isolated schema.

### 2. Concurrency & Isolation
*   **Q: How will the system handle 150 students submitting simultaneously for the "Backup -> Diff -> Restore" cycle?**
    *   **A:** Submissions are serialised per student through `LockManager`, which uses **Postgres session-level advisory locks** (`pg_advisory_lock` keyed by an FNV hash of the student ID). This replaces the previous in-memory `sync.Mutex` design and means the "one submission at a time per student" guarantee continues to hold even if multiple backend instances are deployed for horizontal scaling. No per-student state is retained in process memory.
*   **Q: Should every student have a dedicated schema/database instance?**
    *   **A:** **Yes.** Each student is assigned a dedicated schema (`student_{id}`) to prevent data interference.

### 3. Sandbox Lifecycle
*   **Q: When are student sandbox tables created?**
    *   **A:** On first submission. `InitializeSandbox` is **idempotent**: it inspects `pg_catalog.pg_tables` and only clones tables that are missing from the student's schema. Subsequent submissions skip the clone work entirely, so the per-request cost collapses to a few catalog lookups.
*   **Q: How are tables cloned from `public` into the student sandbox?**
    *   **A:** With `CREATE TABLE student_{id}.x (LIKE public.x INCLUDING ALL)` followed by `INSERT INTO student_{id}.x SELECT * FROM public.x`. This preserves primary keys, NOT NULL, defaults, and indexes — properties that the older `CREATE TABLE … AS SELECT` approach silently dropped. (Foreign keys are not propagated by `LIKE`; this is a Postgres limitation.)
*   **Q: What keeps DML state from leaking between submissions when the sandbox is no longer rebuilt every call?**
    *   **A:** Before evaluating a DML question, the controller calls `ResetTable(schema, target)`, which drops and re-clones only the question's target table from `public`. Other sandbox tables stay in their already-initialised state, so the request-time cost is bounded by a single small table copy regardless of how many tables the curriculum contains.

### 4. Data Integrity & Diffing
*   **Q: What defines a "Diff" for Update/Delete operations?**
    *   **A:** A row-by-row comparison of the entire table state. The state is serialized to a string format for deterministic comparison.
*   **Q: How are idempotent queries (no changes) handled?**
    *   **A:** These result in a **zero score** as per requirement.
*   **Q: What is the expected size/performance of backups?**
    *   **A:** Submissions are queued one-by-one per student, and only the question's target table is rebuilt between submissions. Cloning uses `LIKE … INCLUDING ALL` plus `INSERT … SELECT`, so primary keys and indexes are preserved.

### 5. Implementation Details
*   **Database Structure:** Lecturers can import any custom SQL schema (e.g., Elmasri Company) via the dashboard, which becomes the global `public` template for all student sandboxes. System tables live in the `app` schema and are never cloned.
*   **Lecturer Answer Submission:** Fully implemented via the "Manage Questions" dashboard.
*   **Command Ordering:** Implemented using a keyword-sequence token check in the `ValidationService`.

---

## Additional Information
*(This header is intentionally left blank per project instructions)*
