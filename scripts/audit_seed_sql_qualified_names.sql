-- ============================================================================
-- Audit: seeds that name a schema (practice-dml-schema-gate residual)
-- ============================================================================
-- READ-ONLY. Performs NO writes. Lists every persisted lecturer seed whose SQL
-- references a system, shared, or foreign schema. After the qualification
-- refusal ships, each row below will refuse BOTH on save and at grade time,
-- so every one of them needs a lecturer to edit the seed before deploy.
--
-- Background: seed replay runs inside the grading transaction as exam_student,
-- which holds DML on every real schema. A seed writing public.t therefore takes
-- locks the ARCH-059 schema gate cannot see, because the gate keys on the
-- execution schema alone. Two categories graded concurrently whose seeds touch
-- the same shared tables in opposite order deadlock with SQLSTATE 40P01.
--
-- The pattern mirrors schemaQualifiedNameRe in
-- backend/internal/repository/postgres_schema.go. It is deliberately the same
-- denylist, so this report and the Go refusal agree.
--
-- KNOWN FALSE POSITIVE: this scans raw seed_sql, whereas the Go check strips
-- string literals first. A seed whose only match is inside a quoted value —
-- VALUES ('see public.foo') — is listed here but will NOT be refused. Read
-- each row before acting on it.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_seed_sql_qualified_names.sql
-- ============================================================================

-- Guarantee we can never write, even by accident.
SET default_transaction_read_only = on;
BEGIN;

\echo ''
\echo '== 1. Practice questions whose seed names a schema =='

SELECT
    pq.id            AS question_id,
    pq.category_id,
    pc.name          AS category_name,
    pc.schema_name,
    pq.target_table,
    pq.seed_sql
FROM app.practice_questions pq
LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
WHERE pq.seed_sql IS NOT NULL
  AND btrim(pq.seed_sql) <> ''
  AND pq.seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.'
ORDER BY pq.category_id NULLS FIRST, pq.id;

\echo ''
\echo '== 2. Exam questions whose seed names a schema =='

SELECT
    q.id             AS question_id,
    q.exam_id,
    q.category_id,
    q.target_table,
    q.seed_sql
FROM app.questions q
WHERE q.seed_sql IS NOT NULL
  AND btrim(q.seed_sql) <> ''
  AND q.seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.'
ORDER BY q.exam_id NULLS FIRST, q.id;

\echo ''
\echo '== 3. Totals =='

SELECT
    (SELECT count(*) FROM app.practice_questions
      WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> ''
        AND seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.')
      AS practice_questions_affected,
    (SELECT count(*) FROM app.questions
      WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> ''
        AND seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.')
      AS exam_questions_affected,
    (SELECT count(*) FROM app.practice_questions WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> '')
      AS practice_seeds_total,
    (SELECT count(*) FROM app.questions WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> '')
      AS exam_seeds_total;

ROLLBACK;
