# SQL Quiz System - System Manual

## Introduction
Welcome to the SQL Quiz System, a secure and scalable platform for PostgreSQL examinations. This manual details the workflows for Administrators, Lecturers, and Students.

## Getting Started
The system is fully containerized. To start the platform:
1. Ensure Docker and Docker Compose are installed.
2. Run: `docker-compose up -d --build`
3. Access the web interface at: `http://localhost`

---

## 1. Administrator Workflow (System Management)
The Administrator is responsible for system infrastructure and the initial creation of staff accounts.

### Initial Login
- **URL:** `http://localhost` (Click "Login")
- **Username:** `admin`
- **Password:** `admin123`
*(Note: Change your password immediately via the **"Profile"** settings or the `/api/v1/users/password` endpoint for security.)*

### Managing Staff (Lecturers)
1. Navigate to the **"Manage Accounts"** tab.
2. Enter a username and temporary password.
3. Select the role: **LECTURER**.
4. Click **"Generate Account"**.

---

## 2. Lecturer Workflow (Exam Preparation)
Lecturers prepare the curriculum, initialize the database schema, and enroll students.

### Step 1: Initialize the Exam Database
Before any questions can be created or answered, you must set up the database structure:
1. Navigate to the **"Questions"** tab.
2. Locate the **"Initialize Exam Database"** card.
3. Paste your full SQL setup script (e.g., `company_elmasri_postgres.sql`) into the textarea.
4. Click **"Import Schema & Data"**. 
   - *This will create all tables in the `public` schema. These tables are automatically cloned to student sandboxes upon submission.*

### Step 2: Manage Questions
1. Below the initialization card, find the **"Manage Questions"** section.
2. **Question ID:** Assign a unique number.
3. **Type:** Choose **QUERY** (SELECT) or **DML** (Insert/Update/Delete).
4. **Target Table:** The primary table affected by the query (for DML state-diffing).
5. **Correct SQL:** The teacher's reference query.
6. **Strict Mode (Query only):** Toggle requirements for keyword ordering, attributes, or sorting.
7. Click **"Save Question"**.

### Step 3: Student Enrollment
1. Navigate to the **"Manage Accounts"** tab.
2. Create student accounts individually.
3. For bulk creation (150+ students), use the API: `POST /api/v1/users/bulk`.

---

## 3. Student Workflow (Taking the Exam)
Students interact only with the examination interface.

### Logging In
1. Log in with the credentials provided by your Lecturer.
2. Your **Student ID** is automatically identified by the system; you do not need to enter it manually.

### Submitting Answers
1. Select the **Question Number**.
2. Enter your SQL query in the editor.
3. Click **"Submit Answer"**.
4. The system will evaluate your query in a dedicated, isolated sandbox. You will receive a score and feedback (Correct/Incorrect) immediately.

---

## 4. Technical Architecture Notes
- **Schema Layout:** System tables live in the `app` schema; the curriculum lives in `public`; each student gets a `student_{id}` sandbox containing a copy of every `public` table.
- **Sandboxing:** Every submission runs in the student's dedicated `student_{id}` schema. The schema is created on first submission and reused on subsequent submissions — only missing tables are cloned, so repeat requests are cheap.
- **DML Reset:** Before each DML evaluation, only the question's target table is rebuilt from `public`, leaving the rest of the sandbox untouched.
- **Security:** Student queries run under a restricted PostgreSQL role (`exam_student`) with limited CRUD-only permissions on the student's own schema.
- **Queueing:** Submissions are serialised per student via Postgres advisory locks (`pg_advisory_lock`), so the "one-by-one per student" guarantee continues to hold even when multiple backend instances are running.
