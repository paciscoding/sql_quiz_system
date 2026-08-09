# SQL Quiz System - System Manual

## Introduction
Welcome to the SQL Quiz System, a secure and scalable platform for PostgreSQL examinations. This manual details the workflows for Administrators, Lecturers, and Students.

## Getting Started
The system is fully containerized. To start the platform:
1. Ensure Docker and Docker Compose are installed.
2. Run: `docker-compose up -d --build`
3. Access the web interface over HTTPS at your server's address (e.g. `https://10.4.29.203`). The first visit shows a certificate warning — this is expected; see §5.

---

## 1. Administrator Workflow (System Management)
The Administrator is responsible for system infrastructure and the initial creation of staff accounts.

### Initial Login
- **URL:** your server's HTTPS address, e.g. `https://10.4.29.203` (Click "Login")
- **Username:** `admin`
- **Password:** `admin123`
*(Note: Change your password immediately via the **"Profile"** settings or the `/api/v1/user/password` endpoint for security.)*

### Account & Password Administration
- **Staff accounts** are created under **"Manage Accounts"**; the system generates the password and shows it **once** — copy it before closing the dialog.
- **Reset a password** for any user via the admin action — this generates a new password and **logs the user out of all devices**.
- **Resend a credential** re-emails the user's current password (requires an email on file and `PASSWORD_LOG_KEY` configured); it does not change the password.
- **Password events** shows a per-user audit trail of password changes.
- Users who forget their password normally ask an administrator for a reset (above). Self-service from the login page exists but is **switched off by default** — it works only where the deployment sets `PASSWORD_RESET_EMAIL_ENABLED=true` *and* the account has an email on file. See the Student workflow's *Forgot Password* note.

**Bulk password reset (lab exams).** From **Account Administration → User Management**, the **"Bulk Reset Passwords"** button opens a dedicated page for resetting many students' passwords in one operation — for example, ahead of a proctored lab exam where every station needs a fresh, unknown password.
- **Pick a selection mode:** *Selected students* (tick individually), *All students*, or *By exam roster* (every student assigned to one exam).
- **Confirm.** The page states plainly how many students will be affected: every one of them is **signed out of all devices immediately**, and **nothing is ever emailed**. The reset button stays disabled until you type the literal word **`RESET`** into the confirmation box — the server independently re-checks that same word on every request, so this cannot be bypassed from the browser.
- **Run.** Large selections are processed in batches (up to 200 students per request; 100 by default) and the page loops automatically, showing progress, until every selected student has a new password.
- **Handle the result like a stack of printed passwords.** The response is a one-time list of plaintext passwords — this is a full credential dump for the students you selected. Download it as a **CSV**, or use **"Print table"** / **"Print slips"** to produce a page of per-student cut slips. Once you've printed or downloaded, hand the credentials out and then destroy the copies (paper and file) — there is no way to look a generated password up again afterward, and a printed batch that is never distributed leaves those students locked out until it is.

### Managing Staff (Lecturers)
1. Navigate to the **"Manage Accounts"** tab.
2. Enter a username and temporary password.
3. Select the role: **LECTURER**.
4. Click **"Generate Account"**.

### Submission-log Export
A background job emails an admin a **delta gzip CSV** of exam and practice submissions on an interval — two attachments, one per submission type, covering only the rows recorded since the last successful export.

- **On by default**, sending every **24 hours**. Change the interval with `SUBMISSION_EXPORT_INTERVAL` (a Go duration string, e.g. `12h`, `30m`).
- Toggle it on or off any time from the admin panel; the toggle is persisted, so it survives restarts and takes precedence over the `SUBMISSION_EXPORT_ENABLED` env var after the first boot (that var only seeds the initial value). The admin panel also has a **"Run now"** button to trigger an out-of-band export for verification.
- **Requires SMTP** — the exporter does not start unless `EMAIL_SENDER_ADDRESS` is configured.
- The recipient is `EXPORT_ADMIN_EMAIL`, falling back to `ADMIN_EMAIL` when unset.
- **No historical back-fill:** the first export after a fresh deployment only captures submissions made *after* the backend started — anything submitted before first boot is not included in any export.

---

## 2. Lecturer Workflow (Exam Preparation)
Lecturers prepare the curriculum, initialize the database schema, and enroll students.

### Step 1: Initialize the Exam Database (per-exam curriculum)
Before any questions can be created or answered, set up that exam's database structure:
1. Open the exam from the exam list, then select the **"Schema"** sub-tab in its workspace.
2. Paste your full SQL setup script (e.g., `company_elmasri_postgres.sql`) into the textarea.
3. Click **"Import"**. If the exam already has a curriculum, you'll be asked to **confirm replacement**.
   - *This imports into the exam's **own** isolated schema (`curriculum_exam_{id}`), not the shared `public` schema — each exam's curriculum is independent. Use unqualified table names (no `public.x`). SELECT questions read this schema directly under a SELECT-only role; DML questions clone it into a single per-exam sandbox at activation time. The lecturer never pays a per-student clone cost.*
   - *(A legacy global "import into `public`" endpoint still exists for scripting but is no longer surfaced in the dashboard.)*

### Step 2: Manage Questions
*Allowed only while the examination is **inactive** (`is_active = false`). The system rejects question edits on active exams to keep the cached Correct Result safe.*

1. Below the initialization card, find the **"Manage Questions"** section.
2. **Question ID:** Assign a unique number.
3. **Type:** Choose **QUERY** (SELECT) or **DML** (Insert/Update/Delete).
4. **Target Table:** The primary table affected by the query (for DML state-diffing).
5. **Correct SQL:** The teacher's reference query.
6. **Strict Mode (Query only):** Two toggles — *Ordering* (student's result rows must match the teacher's row-by-row; otherwise comparison is set-equality) and *Attributes* (column names must match the teacher's exactly).
7. Click **"Save Question"**.

When the question is saved, the system **immediately runs your Correct SQL** on a clone of the imported schema, captures the expected output (result-set for QUERY, target-table post-state for DML), and stores it on the question. Two outcomes:
- **Captured.** A green badge appears with the captured row count. The question is ready to ship.
- **Capture failed.** The question is still saved, but a red badge shows the database error (e.g., `syntax error at or near "SELET"`). Fix the SQL and click **"Save Question"** again — capture will retry.

### Step 3: Student Enrollment
*Allowed only while the examination is inactive.*

1. Navigate to the **"Manage Accounts"** tab.
2. Create student accounts individually.
3. For bulk creation (150+ students), use the **roster import** (Step 3b) — there is no separate bulk-user endpoint.
4. Assign students to the examination via the exam workspace's **"Roster"** sub-tab (**"Enroll Students"**).

### Step 3b: Importing a Class Roster
Instead of creating student accounts individually, you can bulk-import an entire class roster from a spreadsheet:

1. Navigate to the **"Students"** tab in the Lecturer dashboard.
2. Locate the **"Import Roster"** panel.
3. Prepare your file in **.csv** or **.xlsx** format. The file must contain a **header row** with the following columns (column order does not matter; names are case-insensitive):

   | Required Column | Accepted Aliases |
   |---|---|
   | Student number | student_number, Student No |
   | SECLEC | sec_lec, lecture_section |
   | SECLAB | sec_lab, lab_section |
   | Student_id | student id, student_id_ext, external_id |
   | First Name | first_name, firstname, given name |
   | Last Name | last_name, lastname, surname, family name |

4. Click **"Choose File"**, select your spreadsheet, then click **"Import"**.
5. Successfully imported students appear with status **Created** or **Updated** (if re-imported). Rows with errors (e.g., missing student number) appear in the **Errors** list with the specific problem.

**Default login credentials for imported students:**
- Username: the student's **student number**
- Password: the student's **student number** (initial; same as username)

Students should change their password on first login via the **"Profile"** settings.

**Re-import behaviour:** Re-uploading the same roster updates name and section columns but **does not reset passwords**. Students who have already changed their password keep it after a re-import.

### Step 4: Activate the Examination
Activation is the point of no return for question edits. Use the **"Activate"** control on the examination card. You can activate two ways:

- **Instant** — the exam becomes live immediately.
- **Scheduled** — supply a **start time** (and optionally an **end time**), in UTC. The exam shows as **scheduled** until the start time passes, then flips to **active** on its own (no background job — the state is computed on read and pushed to waiting students over a live connection). A scheduled **end time** flips it to **ended** automatically.

Each exam card shows a badge for its current effective state: **inactive / scheduled / active / ended**.

- **Pre-flight check.** The system refuses to activate unless every question has a successfully captured Correct Result **and** the exam has at least one question. If any question is in `failed` or `pending` state, the dialog lists them and you must fix them first.
- **Once active**, enrolled students see the exam in their dashboard. Curriculum imports, question edits, and student assignments are blocked until you deactivate.

### Step 5: End the Examination
Click **"Deactivate"** on the examination card. This:
- Rejects any further submissions.
- Drops the per-exam sandbox `sandbox_exam_{examID}` (a single `DROP SCHEMA … CASCADE`). Pure-SELECT exams have no sandbox to drop.
- Retains all submissions and scores in `app.submissions` for read-only review.

### Practice Questions
The practice module lets you publish questions students can attempt at any time outside of active exams.

#### Practice Categories (per-category databases)
Practice questions are organised into **categories**, and **each category owns its own database schema** (`practice_cat_{id}`), fully isolated from the exam curriculum in `public`:
1. In the **"Practice"** tab, create a category ("New Category") or use the built-in **Uncategorized** fallback (which cannot be deleted).
2. Select the category and **import a database** into it (paste unqualified SQL; confirm replacement if it already has tables). Both SELECT and DML questions in that category run against its schema.
3. Deleting a category that still has questions is blocked unless you force it.
4. Each category can carry its own **introduction** text and intro images, shown to students.

#### Authoring Practice Questions
1. In the **"Practice"** tab, pick the **category** the question belongs to.
2. Fill in the question form: Type (QUERY or DML), Target Table (DML only), Correct SQL, and Points.
3. Click **"Save Practice Question"**.
   - The system immediately runs your Correct SQL against the category's schema, captures the expected result, and shows a capture-status badge (green = captured, red = failed with error, amber = pending).
   - Fix the SQL and re-save if the badge is red.
4. Click **"List Practice Questions"** to view all of them.
5. To delete a question, click **"Delete"** next to it. Deletions do not affect existing attempt records.

#### Resetting the Practice Sandbox
If a category's imported schema changes, you can rebuild the practice databases from the **"Practice"** tab:
1. Click **"Reset Practice Sandbox"**.
2. The system rebuilds the practice schema(s) and returns the `reset_at` timestamp.
3. **Caution:** An in-flight practice DML submission can error if a reset races with it. Perform resets when student traffic is low. *(A hardening item to hold a sentinel advisory lock for the duration of the rebuild is tracked but not yet implemented — see `TODO.md`.)*

### Step 6: Reviewing and Exporting Results
Once students have completed their assessments, you can review their performance:

1. Navigate to the **"Results"** tab in the Lecturer dashboard.
2. The table shows a consolidated view of all students, including:
   - Profile information (Student Number, Name, Sections).
   - Latest score for each examination.
   - Total points earned from Practice questions.
   - Grand Total across all assessments.
3. Click **"Export Scores (CSV)"** to download the complete results for all students in a format suitable for grading spreadsheets. The CSV includes dynamic columns for each exam in the system.

---

## 3. Student Workflow (Taking the Exam)
Students interact only with the examination interface.

### Logging In
1. Log in with the credentials provided by your Lecturer.
2. Your **Student ID** is automatically identified by the system; you do not need to enter it manually.

**Forgot your password?** **Ask your instructor or an administrator to reset it** — that is the supported route, and it is the only one on a default installation.

The self-service flow is **disabled unless your deployment turns it on** (`PASSWORD_RESET_EMAIL_ENABLED=true`); where it is off, "Forgot password" simply reports that you should contact an administrator. Where it *is* on and your account has an email on file: enter your username to receive a 6-digit code by email, then enter the code to have a new password emailed to you. The code expires in 10 minutes.

### Selecting an Exam & the Waiting Page
1. Pick an exam you're enrolled in.
2. If the exam is **scheduled** but hasn't started yet, you land on a **waiting page**. You do **not** need to refresh — when the lecturer's start time arrives (or they activate manually), the page switches to the live exam automatically over a live connection.
3. Once **active**, the exam interface opens.

### Submitting Answers
1. Select the **Question Number**.
2. Enter your SQL query in the editor.
3. Click **"Submit Answer"**.
4. The system will evaluate your query in a dedicated, isolated sandbox. You will receive a score and feedback (Correct/Incorrect) immediately.

### Using Practice Mode
Practice mode lets you attempt SQL questions independently of scheduled exams.

1. Navigate to **"/practice"** in the top navigation bar.
2. Browse available practice questions. Each question shows its type (QUERY or DML) and point value.
3. Write your SQL in the editor and click **"Submit"** for a scored attempt, or **"Preview"** to see your result set without recording a score.
4. After submitting, the system shows your score and feedback (Correct / Incorrect / Syntax Error).
5. Click **"My Attempts"** to review your full attempt history for each practice question.

**When practice is unavailable:** If a lecturer activates an examination, practice mode is temporarily locked system-wide. The interface displays a notice and all practice submit/preview requests return HTTP 423. Practice becomes available again once the exam is deactivated.

### Viewing Your Results
1. Click **"Results"** in the top navigation bar, or navigate directly to `/student/results`.
2. The Results page shows a summary card for each exam you are enrolled in: number of questions answered, correct answers, and your total score.
3. Click on an exam card to see your detailed results - one row per question showing your latest submitted SQL, the score, and whether it was marked correct.

Your results are always visible, even while an exam is still active.

---

## 3a. Keyboard Shortcuts and Accessibility
The frontend supports a small set of keyboard shortcuts to speed up common actions:

| Shortcut | Where | Action |
|---|---|---|
| **Ctrl/Cmd + Enter** | Student exam editor, Practice editor, Lecturer question form | Submit the current SQL / save the question |
| **Mod + /** | Any SQL editor | Toggle line comment in the CodeMirror editor |
| **Escape** | Confirm dialog | Cancel the dialog (equivalent to clicking "Cancel") |
| **Escape** | Mobile hamburger drawer | Close the drawer |
| **Tab / Shift+Tab** | Confirm dialog, mobile drawer | Focus stays trapped inside; cycles between focusable elements |

Other UX behaviours worth knowing:

- **Format SQL button** — Each SQL editor has a "Format SQL" button that pretty-prints your query (PostgreSQL dialect, uppercased keywords, two-space indent).
- **Autosave** — When you start typing a Question Requirement in the Lecturer dashboard, your draft is saved to your browser's localStorage every few seconds and restored if you refresh the page.
- **Form validation** — Required fields display an inline red message after you first interact with them. The form blocks submission while errors are showing.
- **Double-submit guard** — Submit / Save buttons are single-flight; clicking twice does not fire twice.
- **Progress strip** — Students see a per-exam progress strip showing answered / correct / total questions and percentage.
- **Quick navigation** — Press **Tab** to move between fields in any form; the modal/drawer focus trap keeps you from accidentally tabbing into background content.
- **Mobile** — Below 768 px viewport width, the top nav collapses into a hamburger button (top right); tap it to open the drawer.

---

## 4. Technical Architecture Notes
For the full design, see `ARCHITECTURE.md § Phase Lifecycle`. Headlines:

- **Schema Layout:** System tables live in `app.*`; the curriculum lives in `public.*` (frozen during the active window); DML submissions share one per-exam sandbox `sandbox_exam_{examID}` cloned from `public.*` at activation.
- **Cached Correct Result:** When a question is saved, the system runs the teacher SQL once and caches the expected output on `app.questions`. Submissions are graded against this cache — teacher SQL is **not** re-executed per submission.
- **Freeze-on-Activate:** Activating an examination freezes its questions and the curriculum. Question edits, schema imports, and student assignments are rejected until deactivation.
- **Two evaluation paths:**
  - *SELECT (Query):* Reads frozen `public.*` directly under role `exam_student_readonly` (only `USAGE` + `SELECT`). No clone, no queue — data-modifying CTEs are rejected at the engine level on permissions. A 30 s per-query timeout bounds pathological queries.
  - *DML:* Runs against `sandbox_exam_{examID}` under role `exam_student` (CRUD), serialised through the global in-process FIFO `SubmissionQueue` (one worker goroutine, channel capacity 256, 30 s per-job timeout) so only one student at a time mutates the sandbox.
- **Hermetic DML Evaluation:** Each DML submission is wrapped in `BEGIN; SAVEPOINT student_eval; … ROLLBACK TO SAVEPOINT student_eval; COMMIT`. The student's writes never persist past the rollback, so the next student in the queue starts from the same byte-identical sandbox state.
- **Persistence:** Every submission (success or failure) is recorded in `app.submissions` for audit and final scoring.
- **Queueing:** **Exam** DML submissions (and exam DML previews) serialise through a single FIFO worker. When the queue is saturated, the submitter receives HTTP 503; on per-job timeout, HTTP 504. SELECT submissions don't queue at all. **Practice submissions bypass this queue entirely** — it is exam-only — but DML is not left unserialised: an in-process gate inside the evaluation engine allows only one DML evaluation per execution schema at a time, for practice and exam alike (SELECT is never gated). A submission or lecturer test-run that cannot get in within the gate's wait budget receives HTTP 503 (`practice_busy` for practice, `schema_busy` for exam) rather than being graded.

---

## 5. About the Browser Security Warning (Students)
The first time you open the site over **https**, your browser will show a warning such as **"Your connection is not private"** (Chrome/Edge) or **"Warning: Potential Security Risk Ahead"** (Firefox). This is **expected** for this system — it does **not** mean your machine or the site has been compromised.

**Why it appears:** the server uses a self-signed certificate because it doesn't have a public domain name, so your browser can't automatically verify who issued it. Your connection is still fully **encrypted**.

**How to proceed:**
- **Chrome / Edge:** Click **"Advanced"**, then **"Proceed to `<address>` (unsafe)"**.
- **Firefox:** Click **"Advanced…"**, then **"Accept the Risk and Continue"**.

You only need to do this **once per browser** — subsequent visits will not show the warning again.

This is a **temporary measure**; a properly trusted certificate is planned for the future.
