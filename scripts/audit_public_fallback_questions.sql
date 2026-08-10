-- ============================================================================
-- Audit: relation names in `public` that DML could still resolve through the
--        removed `, public` search_path fallback
-- ============================================================================
-- READ-ONLY. Performs NO writes and NO automatic re-capture.
--
-- Run BEFORE deploying the change that drops `, public` from the DML
-- search_path (postgres_evaluation.go, postgres_questions.go,
-- postgres_practice.go). After that change, an unqualified relation name that
-- exists only in `public` stops resolving: the SQL fails with
-- "relation ... does not exist" instead of silently reading or writing a
-- relation shared by every category.
--
-- ---------------------------------------------------------------------------
-- WHAT THIS REPORTS
-- ---------------------------------------------------------------------------
-- Per EXECUTION SCHEMA (see below), every relation NAME in `public` that is
-- ABSENT from that schema. Those names — and only those — are what the
-- fallback can currently resolve there.
--
-- Read the output as: "these NAMES were reachable through the fallback in this
-- schema". It is NOT a per-question verdict, and it says nothing about whether
-- any question actually references a listed name. It is a deliberate
-- OVER-approximation: it may list names nothing uses, but it must never omit a
-- name something could use. That asymmetry is the whole point — an EMPTY
-- report is a proof that dropping the fallback is a no-op for this database;
-- a NON-empty report means a human must check whether the listed DML questions
-- actually reference the listed names.
--
-- ---------------------------------------------------------------------------
-- WHY CAPTURE AND GRADING ARE AUDITED SEPARATELY
-- ---------------------------------------------------------------------------
-- The schema a DML statement executes in is NOT the same at capture time and
-- at grading time, and an earlier version of this script audited only the
-- capture-side schema for exams. That was a hole big enough to hide a total
-- outage. The four execution schemas are:
--
--   practice capture AND grading
--       app.practice_categories.schema_name   (same schema for both)
--
--   exam CAPTURE (postgres_questions.go:61-84, CreateQuestion.captureSchema)
--       app.exam_categories.schema_name        for a question WITH a category
--       COALESCE(NULLIF(examinations.curriculum_schema,''),'public')
--                                              for an UNCATEGORISED question
--
--   exam GRADING (postgres_evaluation.go:141, ExamGradingSandboxFor)
--       sandbox_exam_cat_<category_id>  when the question has a category AND
--                                       that category owns its schema, i.e.
--                                       exam_categories.schema_name =
--                                       'exam_cat_<category_id>'
--       sandbox_exam_<exam_id>          otherwise — including EVERY legacy or
--                                       migrated category, and every
--                                       uncategorised question
--
-- Exam grading therefore NEVER runs in curriculum_schema. It runs in a
-- sandbox. And the sandbox is only a PARTIAL clone of the curriculum:
-- ensureTablesInSchema enumerates the source with GetTablesInSchema
-- (postgres_schema.go:517), which reads pg_catalog.pg_tables and so returns
-- ordinary and partitioned TABLES ONLY (relkind 'r'/'p'). VIEWS, MATERIALISED
-- VIEWS, FOREIGN TABLES and SEQUENCES are never cloned into a sandbox, no
-- matter how many times the exam is re-activated.
--
-- That is exactly how a legacy exam breaks silently: curriculum_schema is
-- 'public', `public` holds table `employee` and view `senior_employee`,
-- activation clones only `employee` into sandbox_exam_7, and a question
--   DELETE FROM employee WHERE id IN (SELECT id FROM senior_employee)
-- captures green (capture runs in `public`, where the view exists) but grades
-- through the `public` fallback. Drop the fallback and grading fails with
-- "relation senior_employee does not exist" for EVERY student. Auditing
-- curriculum_schema alone reports nothing for that exam; auditing the sandbox
-- reports `senior_employee`.
--
-- Consequently there is NO `<> 'public'` exclusion on the exam sections. A
-- sandbox is never `public`, so excluding exams whose curriculum_schema is
-- 'public' would exclude precisely the legacy exams most at risk. Execution
-- schemas that really are `public` still produce zero rows, because every
-- `public` name trivially exists in `public` — they exclude themselves through
-- the presence test rather than through a filter.
--
-- ---------------------------------------------------------------------------
-- RELKINDS COVERED
-- ---------------------------------------------------------------------------
-- 'r' table, 'p' partitioned table, 'v' view, 'm' materialised view,
-- 'f' foreign table, 'S' sequence. Sequences are included because an explicit
-- nextval('seq') with an unqualified name resolves through search_path just
-- like a table, and a sequence in `public` is neither cloned into a sandbox
-- nor otherwise visible to this audit.
--
-- ---------------------------------------------------------------------------
-- WHY THE PRESENCE TEST IS AN EXACT MATCH
-- ---------------------------------------------------------------------------
-- Presence in the execution schema is tested by EXACT name match
-- (c.relname = p.relname), not a case-insensitive one: Postgres folds an
-- unquoted identifier to lower case once at parse time and then matches
-- relname EXACTLY per schema in search_path order — it never case-folds
-- catalog contents. A schema holding "Products" does NOT intercept a lookup
-- for products, so public's copy is still reachable, and a case-insensitive
-- presence test would wrongly hide that.
-- (audit_dml_vacuous_questions.sql uses the lenient form on purpose, to avoid
-- false "broken" claims — the opposite failure mode from here.)
--
-- ---------------------------------------------------------------------------
-- DELIBERATELY NOT REPORTED
-- ---------------------------------------------------------------------------
-- A question whose target_table exists only in `public`. ResolveTargetTable
-- (postgres_target_table.go:54) resolves target_table against the execution
-- schema only, so such a question is REFUSED at save time today, regardless of
-- search_path — it is already broken and needs no deploy gate. Rows of that
-- shape are reported by sections 2a/2b of
-- scripts/audit_dml_vacuous_questions.sql instead.
--
-- ---------------------------------------------------------------------------
-- SINGLE DEFINITION OF THE PREDICATE
-- ---------------------------------------------------------------------------
-- The reachability predicate is defined ONCE, in the psql variables below, and
-- every section and the verdict are built from it. Duplicating it per section
-- has already produced one lockstep bug on this branch; a read-only
-- transaction forbids CREATE VIEW / CREATE TEMP TABLE, so psql variables are
-- the only way to share one CTE chain across several statements.
--
-- String literals inside the variables use dollar quoting ($$...$$) so the
-- psql-level single quotes never need doubling.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql
-- ============================================================================

-- Guarantee we can never write, even by accident.
BEGIN;
SET TRANSACTION READ ONLY;

-- --- shared predicate, part 1: every relation in `public` -------------------
\set cte_pub 'pub AS (SELECT p.relname, p.relkind FROM pg_catalog.pg_class p JOIN pg_catalog.pg_namespace n ON n.oid = p.relnamespace WHERE n.nspname = $$public$$ AND p.relkind IN ($$r$$,$$p$$,$$v$$,$$m$$,$$f$$,$$S$$))'

-- --- shared predicate, part 2: the schemas DML actually executes in ---------
-- 2a. practice capture AND grading both run in the category's own schema.
--     Unchanged from the original section 1, including its `<> 'public'`
--     filter (a practice schema is never a sandbox, and a practice category
--     sitting on `public` has no separate schema to fall back FROM).
\set cte_practice ', exec_target AS (SELECT $$1. practice capture + grading$$::text AS section, $$practice category $$ || pc.id::text || $$ ($$ || pc.name || $$)$$ AS owner, pc.schema_name AS exec_schema, (SELECT count(*) FROM app.practice_questions q WHERE q.category_id = pc.id AND q.question_type = $$DML$$) AS dml_questions FROM app.practice_categories pc WHERE EXISTS (SELECT 1 FROM app.practice_questions q WHERE q.category_id = pc.id AND q.question_type = $$DML$$) AND pc.schema_name <> $$public$$'

-- 2b. exam CAPTURE, categorised question -> exam_categories.schema_name
--     (postgres_questions.go:63-76: the category's schema wins when non-empty).
\set cte_examcap_cat ' UNION ALL SELECT $$2. exam capture$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), category $$ || ec.id::text || $$ ($$ || ec.name || $$)$$, COALESCE(NULLIF(ec.schema_name, $$$$), $$public$$), (SELECT count(*) FROM app.questions q WHERE q.category_id = ec.id AND q.question_type = $$DML$$) FROM app.exam_categories ec JOIN app.examinations e ON e.id = ec.exam_id WHERE EXISTS (SELECT 1 FROM app.questions q WHERE q.category_id = ec.id AND q.question_type = $$DML$$)'

-- 2c. exam CAPTURE, uncategorised question -> curriculum_schema, defaulting to
--     'public' (postgres_questions.go:78-84).
\set cte_examcap_uncat ' UNION ALL SELECT $$2. exam capture$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), uncategorised$$, COALESCE(NULLIF(e.curriculum_schema, $$$$), $$public$$), (SELECT count(*) FROM app.questions q WHERE q.exam_id = e.id AND q.category_id IS NULL AND q.question_type = $$DML$$) FROM app.examinations e WHERE EXISTS (SELECT 1 FROM app.questions q WHERE q.exam_id = e.id AND q.category_id IS NULL AND q.question_type = $$DML$$)'

-- 2d. exam GRADING, categorised question -> the category's own sandbox when the
--     category owns its schema (ExamCategoryOwnsSchema: schema_name =
--     'exam_cat_<id>'), otherwise the exam-level sandbox. Mirrors
--     ExamGradingSandboxFor (postgres_evaluation.go:141).
\set cte_examgrade_cat ' UNION ALL SELECT $$3. exam grading (sandbox)$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), category $$ || ec.id::text || $$ ($$ || ec.name || $$)$$, CASE WHEN ec.schema_name = $$exam_cat_$$ || ec.id::text THEN $$sandbox_exam_cat_$$ || ec.id::text ELSE $$sandbox_exam_$$ || ec.exam_id::text END, (SELECT count(*) FROM app.questions q WHERE q.category_id = ec.id AND q.question_type = $$DML$$) FROM app.exam_categories ec JOIN app.examinations e ON e.id = ec.exam_id WHERE EXISTS (SELECT 1 FROM app.questions q WHERE q.category_id = ec.id AND q.question_type = $$DML$$)'

-- 2e. exam GRADING, uncategorised question -> always the exam-level sandbox.
\set cte_examgrade_uncat ' UNION ALL SELECT $$3. exam grading (sandbox)$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), uncategorised$$, $$sandbox_exam_$$ || e.id::text, (SELECT count(*) FROM app.questions q WHERE q.exam_id = e.id AND q.category_id IS NULL AND q.question_type = $$DML$$) FROM app.examinations e WHERE EXISTS (SELECT 1 FROM app.questions q WHERE q.exam_id = e.id AND q.category_id IS NULL AND q.question_type = $$DML$$))'

-- --- shared predicate, part 3: existence flag + the reachability test -------
-- `tgt` adds schema_exists so a sandbox that was never created (exam never
-- activated) is reported in its own bucket instead of naively matching every
-- public name. `reachable` therefore covers ONLY execution schemas that exist.
\set cte_reach ', tgt AS (SELECT t.*, EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname = t.exec_schema) AS schema_exists FROM exec_target t), reachable AS (SELECT g.section, g.owner, g.exec_schema, g.dml_questions, p.relname AS reachable_name, p.relkind AS kind FROM tgt g CROSS JOIN pub p WHERE g.schema_exists AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = g.exec_schema AND c.relkind IN ($$r$$,$$p$$,$$v$$,$$m$$,$$f$$,$$S$$) AND c.relname = p.relname))'

\set cte :cte_pub :cte_practice :cte_examcap_cat :cte_examcap_uncat :cte_examgrade_cat :cte_examgrade_uncat :cte_reach

\echo ''
\echo '==================================================================='
\echo ' 1. PRACTICE capture + grading (schema: practice category schema)'
\echo '    public relation names absent from the category schema'
\echo '==================================================================='
WITH :cte
SELECT owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable
WHERE section LIKE $$1.%$$
ORDER BY owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 2. EXAM CAPTURE (schema: exam category schema, else curriculum'
\echo '    schema). public relation names absent from that schema'
\echo '==================================================================='
WITH :cte
SELECT owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable
WHERE section LIKE $$2.%$$
ORDER BY owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 3. EXAM GRADING (schema: sandbox_exam_cat_<cat> / sandbox_exam_<id>)'
\echo '    public relation names absent from the SANDBOX. Views, matviews,'
\echo '    foreign tables and sequences are never cloned into a sandbox, so'
\echo '    they show up here even when the curriculum schema has them.'
\echo '==================================================================='
WITH :cte
SELECT owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable
WHERE section LIKE $$3.%$$
ORDER BY owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 4. Execution schemas that DO NOT EXIST yet (NOT counted above)'
\echo '    Typically a sandbox for an exam that has never been activated.'
\echo '    Reachability there is UNKNOWN, not zero: the sandbox is created'
\echo '    and populated at activation time, so re-run this audit after'
\echo '    activating, or treat these as potentially affected.'
\echo '==================================================================='
WITH :cte
SELECT section, owner, exec_schema, dml_questions
FROM tgt
WHERE NOT schema_exists
ORDER BY section, owner;

\echo ''
\echo '==================================================================='
\echo ' 5. Verdict'
\echo '==================================================================='
-- practice_dml_total / exam_dml_total exist to distinguish "the change is
-- safe here" from "this database has no DML questions, so the gate proved
-- nothing". A zero *_affected count alongside a zero *_total count is the
-- second case, and must be reported as such.
--
-- Every *_affected count below is computed from the SAME `reachable` CTE the
-- sections above list, so a section and its count cannot drift apart.
-- exec_schemas_missing counts section 4, which is neither safe nor affected —
-- it is unknown, and a non-zero value means this audit did not fully answer
-- the question.
WITH :cte
SELECT
  (SELECT count(*) FROM app.practice_questions WHERE question_type = $$DML$$)
      AS practice_dml_total,
  (SELECT count(*) FROM app.questions WHERE question_type = $$DML$$)
      AS exam_dml_total,
  (SELECT count(DISTINCT owner) FROM reachable WHERE section LIKE $$1.%$$)
      AS practice_categories_affected,
  (SELECT count(DISTINCT owner) FROM reachable WHERE section LIKE $$2.%$$)
      AS exam_capture_targets_affected,
  (SELECT count(DISTINCT owner) FROM reachable WHERE section LIKE $$3.%$$)
      AS exam_grading_targets_affected,
  (SELECT count(*) FROM tgt WHERE NOT schema_exists)
      AS exec_schemas_missing;

ROLLBACK;
