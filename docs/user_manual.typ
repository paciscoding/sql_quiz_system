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
  font: "Times New Roman",
  size: 12pt
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
Lecturers prepare the curriculum, initialize the database schema, and manage the examination lifecycle.

== Step 1: Initialize the Exam Database
Before any questions can be created or answered, you must set up the database structure:
1. Navigate to the *Questions* tab.
2. Locate the *Initialize Exam Database* card.
3. Paste your full SQL setup script (e.g. `company.sql`) into the textarea.
4. Click *Import Schema & Data*.

== Step 2: Create Questions (Phase 0)
1. In the *Manage Questions* section, define your question.
2. *Type:* Choose *QUERY* (SELECT) or *DML* (INSERT/UPDATE/DELETE).
3. *Correct SQL:* Your reference answer.
4. *Strict Mode:*
  - *Row Ordering:* Check this if the specific order of rows matters.
  - *Exact Attributes:* Check this if students must use specific column aliases.
5. Click *Save Question*. The system will immediately capture the "Ground Truth" result. If capture fails (e.g. syntax error in your SQL), the status will show as "failed".

== Step 3: Activation (Phase 1)
1. Navigate to the *Examination Events* section.
2. Create an examination event and assign questions to it.
3. Assign students to the exam.
4. Click *Activate*. The system will verify that every question has a successfully captured ground truth before allowing the exam to start. Once activated, the exam settings are frozen.

== Step 5: End the Examination (Phase 3)
1. When the exam session is over, click *Deactivate*.
2. The system will automatically drop the shared exam sandbox schema to clean up the database.

== Step 6: Reviewing Results
1. Navigate to the *Results* tab in the Lecturer dashboard.
2. View consolidated student performance across all exams and practice.
3. Export the complete scores list as a CSV file for grading.

= Student Workflow
Students interact only with the examination interface.

== Logging In
1. Log in with the credentials provided by your Lecturer.
2. The system will automatically fetch all active examinations you are enrolled in.

== Submitting Answers
1. Select an *Examination* and then a *Question*.
2. Enter your SQL query in the editor.
3. Click *Submit Answer*.
4. The system evaluates your query in a sharedTransactional sandbox using a FIFO queue. Your answer is compared against the ground truth captured in Step 2.
5. Scores and feedback are returned immediately and stored for the lecturer's review.

== Practice Mode
1. Access the *Practice* section from the navigation bar.
2. Attempt questions from the practice bank to build your skills.
3. Practice is automatically locked when an examination is active.

= Technical Architecture Notes
- *Schema Isolation:* System tables are in `app`, templates in `public`, and shared sandboxes in `sandbox_exam_{examID}`.
- *FIFO Submission Queue:* DML submissions are serialised through a global queue to ensure consistent evaluation.
- *Transactional Rollback:* Every student submission is rolled back to a savepoint after evaluation, ensuring a clean slate for subsequent attempts.
- *Audit:* Every attempt is recorded in the `app.submissions` table for audit and final grading.
