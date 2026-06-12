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
  font: "Times New Roman",
  size: 12pt
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
The SQL Quiz System is a robust, scalable platform designed for computer science examinations. It facilitates real-time evaluation of PostgreSQL commands (Query and DML) for up to 150 concurrent students while ensuring data integrity and strict security sandboxing through a four-phase lifecycle.

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

- *`app`* hosts the system tables (`system_users`, `examinations`, `questions`, `exam_students`, `submissions`, `exam_sandboxes`) used by the application backend.
- *`public`* holds the lecturer-imported curriculum and is treated as a read-only template; it is frozen during active examinations.
- *`sandbox_exam_{examID}`* is a shared sandbox cloned from `public` at activation for DML operations.

#figure(
  image("database_schema.png", width: 100%),
  caption: [Database Entity-Relationship Diagram],
)

= Phase Lifecycle
The system operates through a strict four-phase lifecycle to ensure validation accuracy and system stability:

== Phase 0 — Correct Result Capture
On question creation, the teacher's SQL is executed against `public` inside a transaction. The result-set (Query) or post-mutation table state (DML) is captured and cached in `app.questions`.

== Phase 1 — Activation Gate
Activation requires all questions to have valid cached results. A shared DML sandbox is cloned from `public` at this stage.

== Phase 2 — Transactional Evaluation
Evaluation occurs through two optimized paths:
1. *Query Path:* Lock-free reads against `public` under a read-only role.
2. *DML Path:* FIFO queued execution against the shared exam sandbox with savepoint-rollback semantics to ensure isolation between students.

== Phase 3 — Deactivation and Cleanup
On deactivation, the shared sandbox schema is dropped and final scores are archived.

= Query Validation (Select)
Queries are checked against the cached ground truth with optional strictness for:
- *Row Ordering:* Student results must match the teacher's order if enabled.
- *Exact Attributes:* Column names must match the teacher's exactly if enabled.

= Security and Isolation
- *Identity Integrity:* Student identity is derived strictly from the JWT context.
- *Sandbox Isolation:* DML submissions share one per-exam sandbox with serialised FIFO queueing and savepoint rollback. SELECT submissions read frozen `public` directly under a read-only Postgres role.
- *Concurrency Management:* A single in-process FIFO `SubmissionQueue` manages all DML evaluations.

= Implementation Status
The system is fully implemented and verified according to the architectural specification. Core features including the Phase Lifecycle, DML Sandboxing, Submission Queueing, and Student Roster Import are complete.
