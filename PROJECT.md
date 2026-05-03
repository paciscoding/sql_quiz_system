# SQL Quiz System

## Introduction
This system is intended to be a PostgreSQL command examination system for undergraduate computer science students, consisting of Query, Insert, Update, and Delete. The student body within the examination period will be around 90 - 150 students, so the scaling should not be too complex but still tolerant of unexpected system downtime.

## Architecture
### Tech Stacks
- Frontend: Vue 3
- Backend: Go Gin
- DB: PostgreSQL
- Container: Docker
- Reverse Proxy: Nginx

## Current Tasks
In the current sprint, the goal is to validate if the backend logic can correctly verify incoming student submissions against the examination questions.

### Requirements
#### Query Examination Quiz
- Students will submit data in the format: `{ question_number, sql_text }`
- Before the examination is held, the teacher uploads the correct answer.
- The system generates output for each question and stores the correct answer.
- Each question has a validation layer consisting of:
  * Checking if the student's answer matches the teacher's answer. A full score is awarded for a match; otherwise, further checks are performed.
  * ***LECTURER CHOICES***: Check if the current question is strict regarding "SQL command input ordering". If not, continue to the next check. If it is, decrease the score for incorrect ordering.
  * ***LECTURER CHOICES***: Check if attribute names are strict.
  * ***LECTURER CHOICES***: Check if record order is strict.

#### Insert Examination Quiz
- Insert all attributes -> check if Lecturer's answer == Student plain text.
- Support for unordered attributes.

#### Update 
##### Additional Notes
- Might have multiple attributes based on the Lecturer's choice.
- Transaction lock for each student answer submission, same for Insert questions.

##### Ordered Check Methods (Original Logic)
1. Back up the main table to a backup table.
2. Execute the lecturer's input to update the main table.
3. Compare the ***main table*** with the ***backup table*** to determine the "Correct Result".
4. Restore the ***main table*** from the ***backup table*** to prepare for student submissions.
5. Maintain the backup table as the ground truth for resetting the main table.
6. Execute the student's submission on the main table.
7. Compare the student's result with the stored "Correct Result" to calculate the score.
8. Reset the main table using the backup table.
9. ***Repeat for each student***.

##### Lowered Operation Strategy (Optimized Check)
To minimize database operations while ensuring a clean state for each evaluation:
1. **Initial State Capture**: Before any submissions, the teacher's schema is imported.
2. **Teacher Reference Execution**: The teacher's SQL is executed within a transaction to capture the "Correct Result" state, then rolled back to maintain the original state.
3. **Student Sandbox Evaluation**:
   - For each submission, the student's schema is initialized (cloned from the teacher's clean public schema).
   - The student's SQL is executed within the student's isolated schema.
   - The resulting state of the target table is compared against the pre-captured "Correct Result".
   - This approach removes the need for manual backup/restore cycles on the main tables between every student submission, instead relying on schema isolation and transactional rollback where appropriate.
