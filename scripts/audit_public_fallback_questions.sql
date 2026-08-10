-- ============================================================================
-- Audit: DML questions that could resolve a table name through public
-- ============================================================================
-- READ-ONLY. Performs NO writes and NO automatic re-capture.
--
-- Run BEFORE deploying the change that drops `, public` from the DML
-- search_path (postgres_evaluation.go, postgres_questions.go,
-- postgres_practice.go). After that change, an unqualified table name that
-- exists only in `public` stops resolving: the lecturer's SQL fails with
-- "relation ... does not exist" instead of silently reading or writing a table
-- shared by every category.
--
-- What this reports: for every schema that owns DML questions, each `public`
-- table whose name is ABSENT from that schema. Those names — and only those —
-- are what the fallback can currently resolve. A name already present in the
-- owning schema is found there first and never reaches public; a name absent
-- from public cannot be reached through it at all.
--
-- This is a sound over-approximation: it lists names that COULD be reached,
-- not names any question actually references. Zero rows is a proof that the
-- change is a no-op for this database. Non-zero rows require a human to check
-- whether the listed DML questions actually reference the listed names.
--
-- NOT reported here, because it is already refused today: a question whose
-- target_table exists only in public. ResolveTargetTable
-- (postgres_target_table.go:54) resolves target_table against the owning
-- schema only, so such a question fails to save regardless of search_path.
-- Rows of that shape are reported by sections 2a/2b of
-- scripts/audit_dml_vacuous_questions.sql and are already broken.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql
-- ============================================================================

-- Guarantee we can never write, even by accident.
BEGIN;
SET TRANSACTION READ ONLY;

\echo ''
\echo '==================================================================='
\echo ' 1. Practice categories with DML questions: public table names'
\echo '    absent from the category schema (reachable via the fallback)'
\echo '==================================================================='
SELECT pc.id            AS category_id,
       pc.name          AS category_name,
       pc.schema_name   AS category_schema,
       p.relname        AS reachable_public_table,
       (SELECT count(*)
          FROM app.practice_questions q
         WHERE q.category_id = pc.id
           AND q.question_type = 'DML') AS dml_questions
FROM app.practice_categories pc
JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
JOIN pg_catalog.pg_class p
       ON p.relnamespace = pn.oid
      AND p.relkind IN ('r','p','v','m','f')
WHERE EXISTS (SELECT 1
                FROM app.practice_questions q
               WHERE q.category_id = pc.id
                 AND q.question_type = 'DML')
  AND pc.schema_name <> 'public'
  AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = pc.schema_name
          AND c.relkind IN ('r','p','v','m','f')
          AND lower(c.relname) = lower(p.relname)
      )
ORDER BY pc.id, p.relname;

\echo ''
\echo '==================================================================='
\echo ' 2. Exams with DML questions: public table names absent from the'
\echo '    exam curriculum schema (reachable via the fallback)'
\echo '==================================================================='
SELECT e.id              AS exam_id,
       e.title           AS exam_title,
       COALESCE(NULLIF(e.curriculum_schema, ''), 'public') AS curriculum_schema,
       p.relname         AS reachable_public_table,
       (SELECT count(*)
          FROM app.questions q
         WHERE q.exam_id = e.id
           AND q.question_type = 'DML') AS dml_questions
FROM app.examinations e
JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
JOIN pg_catalog.pg_class p
       ON p.relnamespace = pn.oid
      AND p.relkind IN ('r','p','v','m','f')
WHERE EXISTS (SELECT 1
                FROM app.questions q
               WHERE q.exam_id = e.id
                 AND q.question_type = 'DML')
  AND COALESCE(NULLIF(e.curriculum_schema, ''), 'public') <> 'public'
  AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = COALESCE(NULLIF(e.curriculum_schema, ''), 'public')
          AND c.relkind IN ('r','p','v','m','f')
          AND lower(c.relname) = lower(p.relname)
      )
ORDER BY e.id, p.relname;

\echo ''
\echo '==================================================================='
\echo ' 3. Verdict'
\echo '==================================================================='
-- practice_dml_total / exam_dml_total exist to distinguish "the change is
-- safe here" from "this database has no DML questions, so the gate proved
-- nothing". A zero *_affected count alongside a zero *_total count is the
-- second case, and must be reported as such.
SELECT
  (SELECT count(*) FROM app.practice_questions WHERE question_type = 'DML')
      AS practice_dml_total,
  (SELECT count(*) FROM app.questions WHERE question_type = 'DML')
      AS exam_dml_total,
  (SELECT count(DISTINCT pc.id)
     FROM app.practice_categories pc
     JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
     JOIN pg_catalog.pg_class p
            ON p.relnamespace = pn.oid AND p.relkind IN ('r','p','v','m','f')
    WHERE EXISTS (SELECT 1 FROM app.practice_questions q
                   WHERE q.category_id = pc.id AND q.question_type = 'DML')
      AND pc.schema_name <> 'public'
      AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c
                      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                      WHERE n.nspname = pc.schema_name
                        AND c.relkind IN ('r','p','v','m','f')
                        AND lower(c.relname) = lower(p.relname)))
      AS practice_categories_affected,
  (SELECT count(DISTINCT e.id)
     FROM app.examinations e
     JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
     JOIN pg_catalog.pg_class p
            ON p.relnamespace = pn.oid AND p.relkind IN ('r','p','v','m','f')
    WHERE EXISTS (SELECT 1 FROM app.questions q
                   WHERE q.exam_id = e.id AND q.question_type = 'DML')
      AND COALESCE(NULLIF(e.curriculum_schema, ''), 'public') <> 'public'
      AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c
                      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                      WHERE n.nspname = COALESCE(NULLIF(e.curriculum_schema, ''), 'public')
                        AND c.relkind IN ('r','p','v','m','f')
                        AND lower(c.relname) = lower(p.relname)))
      AS exams_affected;

ROLLBACK;
