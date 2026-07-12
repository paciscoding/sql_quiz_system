-- ============================================================================
-- Audit: app.questions id=0 collision damage
-- ============================================================================
-- READ-ONLY. Diagnoses whether the pre-2026-07-12 questions.id=0 collision bug
-- (see docs / commit "fix(questions): auto-assign question ids") corrupted this
-- database. Before the fix, app.questions.id had no sequence, so every question
-- created without an explicit id landed on id=0 and each new no-id insert
-- overwrote the previous one globally (ON CONFLICT (id) DO UPDATE), reassigning
-- its exam_id. This script only SELECTs; it changes nothing.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_questions_id_collision.sql
--       (or:  psql -h HOST -U USER -d DBNAME -f scripts/audit_questions_id_collision.sql)
--
-- How to read the verdict:
--   * "id column default"  = present  -> the fix migration has run on this DB.
--                          = MISSING  -> old code / migration not yet applied.
--   * "questions at id=0"  > 0 together with exams that have 0 questions is the
--     collision fingerprint: those empty exams most likely lost their question.
--   * "exams with submissions but no questions" is the strongest evidence of
--     real data loss — students submitted against a question that was clobbered.
-- ============================================================================

-- Guarantee we can never write, even by accident.
BEGIN;
SET TRANSACTION READ ONLY;

\pset footer off
\echo '==================================================================='
\echo ' 1. Fix status — does app.questions.id auto-assign yet?'
\echo '==================================================================='
SELECT
    COALESCE(
        (SELECT pg_get_expr(d.adbin, d.adrelid)
         FROM pg_attrdef d
         JOIN pg_class c        ON c.oid = d.adrelid
         JOIN pg_namespace n    ON n.oid = c.relnamespace
         JOIN pg_attribute a    ON a.attrelid = c.oid AND a.attnum = d.adnum
         WHERE n.nspname = 'app' AND c.relname = 'questions' AND a.attname = 'id'),
        '(MISSING — pre-fix schema; new questions will still collide on id=0)'
    ) AS id_column_default;

\echo ''
\echo '==================================================================='
\echo ' 2. Collision fingerprint — totals'
\echo '==================================================================='
SELECT
    (SELECT count(*) FROM app.examinations)                     AS total_exams,
    (SELECT count(*) FROM app.questions)                        AS total_questions,
    (SELECT count(*) FROM app.questions WHERE id = 0)           AS questions_at_id_0,
    (SELECT count(DISTINCT exam_id) FROM app.questions)         AS exams_that_have_a_question;

\echo ''
\echo '==================================================================='
\echo ' 3. The id=0 survivor row (if any) — which exam currently owns it'
\echo '==================================================================='
SELECT q.id,
       q.exam_id,
       e.title            AS exam_title,
       q.question_type,
       q.points,
       q.requirement,
       q.created_at,
       q.updated_at
FROM app.questions q
LEFT JOIN app.examinations e ON e.id = q.exam_id
WHERE q.id = 0;

\echo ''
\echo '==================================================================='
\echo ' 4. Exams with NO questions (suspected clobber victims)'
\echo '==================================================================='
SELECT e.id            AS exam_id,
       e.title,
       e.is_active,
       e.created_at,
       (SELECT count(*) FROM app.submissions s WHERE s.exam_id = e.id) AS submissions_on_this_exam
FROM app.examinations e
LEFT JOIN app.questions q ON q.exam_id = e.id
WHERE q.id IS NULL
ORDER BY submissions_on_this_exam DESC, e.id;

\echo ''
\echo '==================================================================='
\echo ' 5. STRONGEST evidence of data loss:'
\echo '    exams that HAVE submissions but have NO questions'
\echo '==================================================================='
SELECT e.id AS exam_id,
       e.title,
       count(s.id)               AS submission_count,
       count(DISTINCT s.student_id) AS students_affected,
       min(s.submitted_at)       AS first_submission,
       max(s.submitted_at)       AS last_submission
FROM app.examinations e
JOIN app.submissions s      ON s.exam_id = e.id
LEFT JOIN app.questions q   ON q.exam_id = e.id
WHERE q.id IS NULL
GROUP BY e.id, e.title
ORDER BY submission_count DESC;

\echo ''
\echo '==================================================================='
\echo ' 6. Submissions grouped by question_id (id=0 = pointed at survivor)'
\echo '==================================================================='
SELECT s.question_id,
       (s.question_id = 0)                          AS is_collision_id,
       (q.id IS NULL)                               AS question_row_missing,
       count(*)                                     AS submissions,
       count(DISTINCT s.exam_id)                    AS distinct_exams,
       count(DISTINCT s.student_id)                 AS distinct_students
FROM app.submissions s
LEFT JOIN app.questions q ON q.id = s.question_id
GROUP BY s.question_id, q.id
ORDER BY submissions DESC;

\echo ''
\echo '==================================================================='
\echo ' 7. Verdict'
\echo '==================================================================='
SELECT
    CASE
        WHEN (SELECT count(*) FROM app.submissions s
              JOIN app.examinations e ON e.id = s.exam_id
              LEFT JOIN app.questions q ON q.exam_id = e.id
              WHERE q.id IS NULL) > 0
            THEN 'DATA LOSS LIKELY: exams have submissions but no surviving questions (see section 5).'
        WHEN (SELECT count(*) FROM app.questions WHERE id = 0) > 0
             AND (SELECT count(*) FROM app.examinations e
                  LEFT JOIN app.questions q ON q.exam_id = e.id
                  WHERE q.id IS NULL) > 0
            THEN 'SUSPICIOUS: an id=0 row exists and some exams have zero questions (see sections 3,4).'
        WHEN (SELECT count(*) FROM app.questions WHERE id = 0) > 0
            THEN 'MINOR: a legacy id=0 row exists but no empty exams — likely a single pre-fix question. Review section 3.'
        ELSE 'CLEAN: no id=0 collision fingerprint found.'
    END AS verdict;

ROLLBACK;
