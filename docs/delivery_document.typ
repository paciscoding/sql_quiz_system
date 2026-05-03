#set page(
  paper: "a4",
  margin: (x: 2cm, y: 2.5cm),
  header: align(right)[
    *SQL Quiz System - Project Delivery*
  ],
  footer: context [
    #align(center)[#counter(page).display()]
  ]
)

#set text(
  font: "linux libertine",
  size: 11pt
)

#align(center)[
  #block(text(weight: 700, 2.5em)[SQL Quiz System])
  #v(1em)
  #text(size: 1.2em)[Technical Design and Delivery Specification]
  #v(2em)
]

#outline(indent: 2em)

#v(2em)

= Introduction
The SQL Quiz System is a robust, scalable platform designed for computer science examinations. It facilitates real-time evaluation of PostgreSQL commands (Query, Insert, Update, and Delete) for up to 150 concurrent students while ensuring data integrity and strict security sandboxing.

= Architecture Overview
The system utilizes a modern, containerized stack to ensure consistent performance and simplified deployment:

- *Frontend:* Vue 3 with Vite for a responsive, high-performance user interface.
- *Backend:* Go 1.26 (Gin framework) providing a high-concurrency API layer.
- *Database:* PostgreSQL, featuring schema-level isolation for student submissions.
- *Reverse Proxy:* Nginx handles SSL termination and request routing.

#figure(
  image("architecture.png", width: 80%),
  caption: [System Architecture Overview],
)

= Database Schema
The system manages exams and users through a relational schema designed for role-based access and data isolation. The database is split across three logical schemas:

- *`app`* hosts the system tables (`system_users`, `examinations`, `questions`, `exam_students`) used by the application backend.
- *`public`* holds the lecturer-imported curriculum (e.g. the Elmasri company schema) and is treated as a read-only clone template.
- *`student_{id}`* is created on first submission for each student and contains a copy of every `public` table.

Keeping the system tables in their own namespace lets the entire `public` schema be cloned wholesale into student sandboxes without any hardcoded exclusion list — every curriculum table the lecturer imports is automatically available to students.

#figure(
  image("database_schema.png", width: 100%),
  caption: [Database Entity-Relationship Diagram],
)

== Sandbox Lifecycle
- *Initialisation:* `InitializeSandbox(student_id)` is idempotent. It inspects `pg_catalog.pg_tables` and only clones tables that are missing from the student's schema, so subsequent submissions skip the work entirely.
- *Cloning Strategy:* New tables are created with `CREATE TABLE student_{id}.x (LIKE public.x INCLUDING ALL)` followed by `INSERT INTO student_{id}.x SELECT * FROM public.x`. This preserves primary keys, NOT NULL, defaults, and indexes — all of which were silently lost by the older `CREATE TABLE … AS SELECT` approach.
- *Between-submission Reset:* For DML questions the controller calls `ResetTable(schema, target)` before evaluation, rebuilding only the question's target table from `public` while leaving the rest of the sandbox untouched.

= Evaluation Logic
The core of the system is its validation engine, which ensures that student submissions are not only syntactically correct but also produce the exact intended side-effects on the database.

== DML Validation Process (Insert/Update/Delete)
To validate changes to the data, the system employs a "State Comparison" model:
1. *State Capture:* The system records the current state of the database.
2. *Lecturer Baseline:* The system applies the lecturer's "correct" SQL and captures the resulting "Diff".
3. *Student Comparison:* The student's SQL is applied to the original state.
4. *Verification:* The system compares the student's diff against the lecturer's baseline row-by-row.

#figure(
  image("dml_validation.png", width: 90%),
  caption: [DML Validation Sequence Flow],
)

== Query Validation (Select)
Queries are checked against the lecturer's result set with optional strictness for:
- Command keyword ordering.
- Attribute (Column) naming precision.
- Result set sorting (Record Order).

= Security and Isolation
To prevent unauthorized database operations and ensure a fair testing environment:
- *Authentication:* A hierarchical RBAC system (Admin, Lecturer, Student) implemented using JWT tokens.
- *Command Filtering:* Only CRUD operations are permitted. Destructive commands (DROP, TRUNCATE, etc.) are blocked by a restricted PostgreSQL role.
- *Sandbox Isolation:* Each student operates within their own dedicated database schema.
- *Concurrency Management:* Submissions are serialised per student through `LockManager`, which acquires a Postgres session-level advisory lock (`pg_advisory_lock`, keyed by an FNV hash of the student ID) for the duration of each evaluation. Because the lock lives in the database itself, the "one submission at a time per student" guarantee continues to hold even when multiple backend instances are deployed for horizontal scaling, and no per-student state is retained in process memory.

= Implementation Status
- *Core Stack:* Finalized.
- *Validation Logic:* Implementation complete, including Strict Mode options.
- *Lecturer Management:* Implementation complete, featuring dynamic SQL schema import and question management.
