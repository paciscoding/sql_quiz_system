#set page(
  paper: "a4",
  margin: (x: 2cm, y: 2.5cm),
  header: align(right)[
    *SQL Quiz System - User Manual*
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
  #text(size: 1.5em)[System Manual and Operational Guide]
  #v(2em)
]

#outline(indent: 2em)

#v(2em)

= Introduction
Welcome to the SQL Quiz System, a secure and scalable platform for PostgreSQL examinations. This manual details the workflows for Administrators, Lecturers, and Students.

= Getting Started
The system is fully containerized for reliability. To start the platform:
1. Ensure Docker and Docker Compose are installed.
2. Run: `docker-compose up -d --build`
3. Access the web interface at: `http://localhost`

= Administrator Workflow
The Administrator is responsible for system infrastructure and the initial creation of staff accounts.

== Initial Login
- *URL:* `http://localhost` (Click "Login")
- *Username:* `admin`
- *Password:* `admin123`

== Managing Staff (Lecturers)
1. Navigate to the *Manage Accounts* tab.
2. Enter a username and temporary password.
3. Select the role: *LECTURER*.
4. Click *Generate Account*.

= Lecturer Workflow
Lecturers prepare the curriculum, initialize the database schema, and enroll students.

== Step 1: Initialize the Exam Database
Before any questions can be created or answered, you must set up the database structure:
1. Navigate to the *Questions* tab.
2. Locate the *Initialize Exam Database* card.
3. Paste your full SQL setup script into the textarea.
4. Click *Import Schema & Data*.

== Step 2: Manage Questions
1. Below the initialization card, find the *Manage Questions* section.
2. *Question ID:* Assign a unique number.
3. *Type:* Choose *QUERY* or *DML*.
4. *Target Table:* The primary table affected by the query.
5. *Correct SQL:* The teacher's reference query.
6. *Strict Mode:* Toggle requirements for keyword ordering, attributes, or sorting.
7. Click *Save Question*.

== Step 3: Student Enrollment
1. Navigate to the *Manage Accounts* tab.
2. Create student accounts individually.
3. For bulk creation (150+ students), use the API endpoint provided in the developer specification.

= Student Workflow
Students interact only with the examination interface.

== Logging In
1. Log in with the credentials provided by your Lecturer.
2. Your *Student ID* is automatically identified by the system.

== Submitting Answers
1. Select the *Question Number*.
2. Enter your SQL query in the editor.
3. Click *Submit Answer*.
4. The system will evaluate your query in a isolated sandbox and return a score immediately.

= Technical Architecture Notes
- *Schema Layout:* System tables live in the `app` schema, the lecturer's curriculum lives in `public`, and each student gets a `student_{id}` sandbox containing a copy of every `public` table.
- *Sandboxing:* The student schema is created on first submission and reused thereafter. Only tables missing from the schema are cloned, so repeat submissions are cheap. For DML questions, only the question's target table is rebuilt between attempts.
- *Security:* Student queries run under a restricted PostgreSQL role with CRUD-only permissions on the student's own schema.
- *Queueing:* Submissions are serialised per student via Postgres advisory locks, which keeps the "one submission at a time per student" guarantee intact even when multiple backend instances are deployed.
