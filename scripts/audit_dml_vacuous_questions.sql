-- ============================================================================
-- Audit: un-vetted and unresolvable DML questions (dml-grading-correctness)
-- ============================================================================
-- READ-ONLY. This script performs NO writes and NO automatic re-capture. It
-- lists the exact set of DML questions (app.practice_questions / app.questions)
-- that a lecturer should re-save in the lecturer UI — saving re-runs Phase-0
-- capture against the current schema/data, which is the fix for every row
-- this script surfaces.
--
-- Background:
--   * ARCH-008 (D3) closed the "0-row model answer" hole: a DML correct_sql
--     that affects 0 rows is now a hard capture failure. Any row captured
--     BEFORE that fix (expected_rows_affected IS NULL) was never vetted for
--     this hole and may be silently ungradeable (see section 1).
--   * ARCH-001/002 (D1) closed the target_table quoting/case-folding bug.
--     Any row whose target_table does not resolve in its owning schema today
--     will fail every student submission with "relation does not exist"
--     (see section 2).
--   * The exam capture search_path-ordering bug (fixed alongside D1): before
--     the fix, CreateQuestion executed an exam's correct_sql BEFORE setting
--     search_path, so for any exam with a non-'public' curriculum_schema the
--     model answer silently mutated public instead of the exam's own
--     curriculum. Every DML question belonging to such an exam may have been
--     captured against the wrong database (see section 3).
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_dml_vacuous_questions.sql
--       (or:  psql -h HOST -U USER -d DBNAME -f scripts/audit_dml_vacuous_questions.sql)
-- ============================================================================

-- Guarantee we can never write, even by accident.
BEGIN;
SET TRANSACTION READ ONLY;

\pset footer off
\echo '==================================================================='
\echo ' 1a. practice_questions: DML rows never vetted for the 0-row hole'
\echo '     (expected_rows_affected IS NULL -> captured before ARCH-008)'
\echo '==================================================================='
SELECT pq.id,
       pq.title,
       pq.category_id,
       pc.name           AS category_name,
       pq.target_table,
       pq.expected_result_status,
       pq.expected_result_captured_at
FROM app.practice_questions pq
LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
WHERE pq.question_type = 'DML'
  AND pq.expected_rows_affected IS NULL
ORDER BY pq.id;

\echo ''
\echo '==================================================================='
\echo ' 1b. questions (exam): DML rows never vetted for the 0-row hole'
\echo '     (expected_rows_affected IS NULL -> captured before ARCH-008)'
\echo '==================================================================='
SELECT q.id,
       q.exam_id,
       e.title            AS exam_title,
       q.target_table,
       q.expected_result_status,
       q.expected_result_captured_at
FROM app.questions q
LEFT JOIN app.examinations e ON e.id = q.exam_id
WHERE q.question_type = 'DML'
  AND q.expected_rows_affected IS NULL
ORDER BY q.id;

\echo ''
\echo '==================================================================='
\echo ' 2a. practice_questions: DML rows whose target_table does not'
\echo '     resolve (case-sensitively or case-insensitively) in the'
\echo '     category''s own schema'
\echo '==================================================================='
SELECT pq.id,
       pq.title,
       pq.category_id,
       pc.name        AS category_name,
       pc.schema_name AS category_schema,
       pq.target_table
FROM app.practice_questions pq
JOIN app.practice_categories pc ON pc.id = pq.category_id
WHERE pq.question_type = 'DML'
  AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = pc.schema_name
          AND c.relkind IN ('r','p','v','m','f')
          AND (c.relname = pq.target_table OR lower(c.relname) = lower(pq.target_table))
      )
ORDER BY pq.id;

\echo ''
\echo '==================================================================='
\echo ' 2b. questions (exam): DML rows whose target_table does not'
\echo '     resolve (case-sensitively or case-insensitively) in the'
\echo '     exam''s own curriculum_schema'
\echo '==================================================================='
SELECT q.id,
       q.exam_id,
       e.title             AS exam_title,
       e.curriculum_schema,
       q.target_table
FROM app.questions q
JOIN app.examinations e ON e.id = q.exam_id
WHERE q.question_type = 'DML'
  AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = COALESCE(NULLIF(e.curriculum_schema, ''), 'public')
          AND c.relkind IN ('r','p','v','m','f')
          AND (c.relname = q.target_table OR lower(c.relname) = lower(q.target_table))
      )
ORDER BY q.id;

\echo ''
\echo '==================================================================='
\echo ' 3. questions (exam): DML rows on a non-public curriculum_schema,'
\echo '    captured BEFORE the search_path-ordering fix. Their'
\echo '    expected_result may have been captured against public instead'
\echo '    of the real curriculum schema and should be treated as suspect'
\echo '    regardless of expected_result_status.'
\echo '==================================================================='
SELECT q.id,
       q.exam_id,
       e.title             AS exam_title,
       e.curriculum_schema,
       q.target_table,
       q.expected_result_status,
       q.expected_result_captured_at
FROM app.questions q
JOIN app.examinations e ON e.id = q.exam_id
WHERE q.question_type = 'DML'
  AND q.exam_id IS NOT NULL
  AND COALESCE(NULLIF(e.curriculum_schema, ''), 'public') <> 'public'
ORDER BY q.id;

\echo ''
\echo '==================================================================='
\echo ' 4. Verdict'
\echo '==================================================================='
SELECT
    CASE
        WHEN EXISTS (SELECT 1 FROM app.practice_questions WHERE question_type = 'DML' AND expected_rows_affected IS NULL)
          OR EXISTS (SELECT 1 FROM app.questions WHERE question_type = 'DML' AND expected_rows_affected IS NULL)
            THEN 'ACTION NEEDED: re-save the DML questions listed in section 1 (never vetted for the 0-row hole).'
        WHEN EXISTS (
            SELECT 1
            FROM app.practice_questions pq
            JOIN app.practice_categories pc ON pc.id = pq.category_id
            WHERE pq.question_type = 'DML'
              AND NOT EXISTS (
                    SELECT 1 FROM pg_catalog.pg_class c
                    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                    WHERE n.nspname = pc.schema_name
                      AND c.relkind IN ('r','p','v','m','f')
                      AND (c.relname = pq.target_table OR lower(c.relname) = lower(pq.target_table))
                  )
        )
          OR EXISTS (
            SELECT 1
            FROM app.questions q
            JOIN app.examinations e ON e.id = q.exam_id
            WHERE q.question_type = 'DML'
              AND NOT EXISTS (
                    SELECT 1 FROM pg_catalog.pg_class c
                    JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                    WHERE n.nspname = COALESCE(NULLIF(e.curriculum_schema, ''), 'public')
                      AND c.relkind IN ('r','p','v','m','f')
                      AND (c.relname = q.target_table OR lower(c.relname) = lower(q.target_table))
                  )
        )
            THEN 'ACTION NEEDED: re-save the DML questions listed in section 2 (unresolvable target_table).'
        WHEN EXISTS (
            SELECT 1 FROM app.questions q
            JOIN app.examinations e ON e.id = q.exam_id
            WHERE q.question_type = 'DML'
              AND q.exam_id IS NOT NULL
              AND COALESCE(NULLIF(e.curriculum_schema, ''), 'public') <> 'public'
        )
            THEN 'REVIEW NEEDED: exam DML questions on a non-public curriculum_schema exist (see section 3) — verify they were captured after the search_path-ordering fix, or re-save.'
        ELSE 'CLEAN: no un-vetted, unresolvable, or suspect-schema DML questions found.'
    END AS verdict;

ROLLBACK;
