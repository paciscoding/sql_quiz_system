-- ============================================================================
-- Audit: names in `public` that DML could still resolve through the removed
--        `, public` search_path fallback
-- ============================================================================
-- READ-ONLY. Performs NO writes and NO automatic re-capture.
--
-- Run BEFORE deploying the change that drops `, public` from the DML
-- search_path (postgres_evaluation.go, postgres_questions.go,
-- postgres_practice.go). After that change, an unqualified name that exists
-- only in `public` stops resolving: the SQL fails with
-- "relation ... does not exist" / "function ... does not exist" instead of
-- silently reading or writing an object shared by every category.
--
-- ---------------------------------------------------------------------------
-- WHAT THIS REPORTS — AND WHAT IT DOES NOT
-- ---------------------------------------------------------------------------
-- Per EXECUTION SCHEMA (listed below), every NAME in `public` that is ABSENT
-- from that schema, for the object classes listed under OBJECT CLASSES COVERED.
-- Those names — and only those — are what the fallback could resolve there.
--
-- Read the output as: "these NAMES were reachable through the fallback in this
-- schema". It is an OVER-APPROXIMATION OVER NAMES, **not a per-question
-- verdict**: it does not parse any question's SQL, so it may list names nothing
-- references, and it never says which question breaks. A NON-empty report is a
-- worklist for a human, not a list of broken questions.
--
-- The completeness claim this script makes — deliberately narrow, so that it is
-- actually TRUE — is:
--
--   For the execution schemas enumerated below, and for the object classes
--   enumerated below, an EMPTY report means no name of those classes in
--   `public` was reachable through the fallback in those schemas.
--
-- It does NOT claim, and must not be read as claiming:
--   * that no question is broken for some other reason (see DELIBERATELY NOT
--     REPORTED below, and the QUERY paths, which this script does not model);
--   * that object classes outside the list below are covered. Operators,
--     operator classes/families, collations, conversions, text-search
--     configurations/dictionaries/parsers and statistics objects are ALSO
--     resolved through search_path and are NOT audited here. They are omitted
--     because nothing in this codebase creates them (curriculum import runs
--     lecturer DDL, so a determined lecturer still could) and because a name
--     collision for them is not expressible in the plain DML this system runs.
--     If your curriculum imports ever start creating them, this script must
--     grow a section for them — it will NOT warn you today;
--   * anything about a database whose execution schemas changed after the run.
--     The report is a snapshot; re-run it immediately before the deploy.
--
-- ---------------------------------------------------------------------------
-- EXECUTION SCHEMAS AUDITED
-- ---------------------------------------------------------------------------
-- The schema a DML statement executes in is NOT the same at capture time, at
-- grading time and at preview time, and an earlier version of this script
-- audited only the capture-side schema for exams. That was a hole big enough
-- to hide a total outage. The execution schemas are:
--
--   practice capture AND grading                       [section 1]
--       app.practice_categories.schema_name   (same schema for both)
--
--   exam CAPTURE (postgres_questions.go:61-84, CreateQuestion.captureSchema)
--       app.exam_categories.schema_name        for a question WITH a category
--       COALESCE(NULLIF(examinations.curriculum_schema,''),'public')
--                                              for an UNCATEGORISED question
--                                                      [section 2]
--
--   exam GRADING (postgres_evaluation.go:141, ExamGradingSandboxFor)
--       sandbox_exam_cat_<category_id>  when the question has a category AND
--                                       that category owns its schema, i.e.
--                                       exam_categories.schema_name =
--                                       'exam_cat_<category_id>'
--       sandbox_exam_<exam_id>          otherwise — including EVERY legacy or
--                                       migrated category, and every
--                                       uncategorised question
--                                                      [section 3]
--
--   DML PREVIEW, dual_schema questions only            [section 4]
--       EvaluatePreview runs the student's DML under
--       `SET LOCAL search_path = <previewSchema>, pg_temp`
--       (postgres_evaluation.go:596-604) whenever previewSchema is non-empty.
--       previewSchema is resolved by the controllers, never by the executor:
--         practice  practice_categories.preview_schema_name
--                   (practice_controller.go:291-294; used only when the decoy
--                   actually holds tables)
--         exam, category that OWNS its schema and has a decoy uploaded
--                   sandbox_exam_cat_<category_id>_preview
--                   (quiz_controller.go:370, ExamCategoryPreviewSandboxName)
--         exam, otherwise (uncategorised, or a legacy/adopted category), when
--         examinations.preview_curriculum_schema is set
--                   sandbox_exam_<exam_id>_preview
--                   (quiz_controller.go:382, ExamPreviewSandboxName)
--       When dual_schema is off, or no usable decoy exists, previewSchema
--       stays '' and the preview runs in the grading schema instead — already
--       covered by sections 1 and 3, so section 4 lists ONLY the questions
--       that really do execute somewhere else.
--
-- Exam grading therefore NEVER runs in curriculum_schema. It runs in a
-- sandbox. And the sandbox is only a PARTIAL clone of the curriculum:
-- ensureTablesInSchema enumerates the source with GetTablesInSchema
-- (postgres_schema.go:517), which reads pg_catalog.pg_tables and so returns
-- ordinary and partitioned TABLES ONLY (relkind 'r'/'p'). VIEWS, MATERIALISED
-- VIEWS, FOREIGN TABLES, SEQUENCES, FUNCTIONS and TYPES are never cloned into
-- a sandbox, no matter how many times the exam is re-activated. The PREVIEW
-- decoy sandboxes are cloned by the SAME tables-only path
-- (InitializeExamPreviewSandbox, postgres_preview_schema.go:467;
-- ensureExamCategoryPreviewSandbox, postgres_exam_categories.go:179), so they
-- are missing exactly the same object classes — which is why section 4 exists
-- as its own population rather than being assumed identical to section 3.
--
-- That is exactly how a legacy exam breaks silently: curriculum_schema is
-- 'public', `public` holds table `employee` and view `senior_employee`,
-- activation clones only `employee` into sandbox_exam_7, and a question
--   DELETE FROM employee WHERE id IN (SELECT id FROM senior_employee)
-- captures green (capture runs in `public`, where the view exists) but grades
-- through the `public` fallback. Drop the fallback and grading fails with
-- "relation senior_employee does not exist" for EVERY student. Auditing
-- curriculum_schema alone reports nothing for that exam; auditing the sandbox
-- reports `senior_employee`. The same question in a dual_schema exam ALSO
-- fails in preview, in sandbox_exam_7_preview — a different schema, reported
-- separately in section 4.
--
-- Consequently there is NO `<> 'public'` exclusion on the exam sections. A
-- sandbox is never `public`, so excluding exams whose curriculum_schema is
-- 'public' would exclude precisely the legacy exams most at risk. Execution
-- schemas that really are `public` still produce zero rows, because every
-- `public` name trivially exists in `public` — they exclude themselves through
-- the presence test rather than through a filter.
--
-- ---------------------------------------------------------------------------
-- OBJECT CLASSES COVERED
-- ---------------------------------------------------------------------------
-- RELATIONS (pg_class, sections 1-4)
--   'r' table, 'p' partitioned table, 'v' view, 'm' materialised view,
--   'f' foreign table, 'S' sequence. Sequences are included because an
--   explicit nextval('seq') with an unqualified name resolves through
--   search_path just like a table, and a sequence in `public` is neither
--   cloned into a sandbox nor otherwise visible to this audit.
--
-- ROUTINES (pg_proc, section 5)
--   functions, procedures, aggregates and window functions. search_path
--   resolves a routine name exactly as it resolves a relation name, and
--   routines are not in pg_class at all — an earlier version of this script
--   filtered pg_class.relkind and was therefore blind to them. A lecturer CAN
--   create them: ImportSchemaIntoCategory has no DDL allowlist
--   (postgres_schema.go:308 only calls validateNoSchemaQualifiedNames), so an
--   imported curriculum may run CREATE FUNCTION, and ensureTablesInSchema
--   never clones the result into a sandbox. `UPDATE invoice SET total =
--   fee(amount)` with fee() living only in `public` resolved solely through
--   the fallback and fails for every student once it is dropped.
--   Names are compared WITHOUT the argument signature: a routine in the
--   execution schema with the same NAME but different arguments counts as
--   present and suppresses the row. That direction over-suppresses in theory,
--   but only for a schema that already defines the name, which is the case a
--   human reviewer would have to look at anyway; comparing full signatures
--   would report noise for every overload.
--   NOTE ON EXTENSIONS: routines installed by an extension into `public`
--   (uuid-ossp, pgcrypto, postgis, …) ARE listed. That is not noise — a
--   `uuid_generate_v4()` call in lecturer DML resolves through the same
--   fallback and breaks the same way. Install extensions in their own schema
--   and add it to the server-level search_path if you want them out of this
--   report legitimately.
--
-- TYPES (pg_type, section 6)
--   standalone composite types, domains, enums, ranges and multiranges.
--   A cast or a column reference names them through search_path too, and
--   like routines they are never cloned into a sandbox. Row types belonging
--   to a table/view (pg_class.relkind other than 'c') and array types are
--   excluded — the first is already reported by sections 1-4 under the
--   relation's own name, the second is unreferenceable except through its
--   element type. The presence test accepts a type of ANY typtype in the
--   execution schema: a cloned table of the same name really does shadow a
--   `public` composite type, so it is correct for it to suppress the row.
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
-- presence test would wrongly hide that. The same reasoning applies verbatim
-- to pg_proc.proname and pg_type.typname.
-- (audit_dml_vacuous_questions.sql uses the lenient form on purpose, to avoid
-- false "broken" claims — the opposite failure mode from here.)
--
-- ---------------------------------------------------------------------------
-- DELIBERATELY NOT REPORTED
-- ---------------------------------------------------------------------------
-- * A question whose target_table exists only in `public`. ResolveTargetTable
--   (postgres_target_table.go:54) resolves target_table against the execution
--   schema only, so such a question is REFUSED at save time today, regardless
--   of search_path — it is already broken and needs no deploy gate. Rows of
--   that shape are reported by sections 2a/2b of
--   scripts/audit_dml_vacuous_questions.sql instead.
-- * The QUERY paths. This script gates the five DML search_path sites only.
-- * The object classes named at the top (operators, collations, text-search
--   objects, …).
--
-- ---------------------------------------------------------------------------
-- SINGLE DEFINITION OF THE PREDICATE
-- ---------------------------------------------------------------------------
-- The reachability predicate is defined ONCE, in the psql variables below, and
-- every section and the verdict are built from it. Duplicating it per section
-- has already produced one lockstep bug on this branch; a read-only
-- transaction forbids CREATE VIEW / CREATE TEMP TABLE, so psql variables are
-- the only way to share one CTE chain across several statements. Every
-- verdict count is computed from the SAME CTE its section selects from, over
-- the SAME population, so a section and its count cannot drift apart.
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
\set cte_examgrade_uncat ' UNION ALL SELECT $$3. exam grading (sandbox)$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), uncategorised$$, $$sandbox_exam_$$ || e.id::text, (SELECT count(*) FROM app.questions q WHERE q.exam_id = e.id AND q.category_id IS NULL AND q.question_type = $$DML$$) FROM app.examinations e WHERE EXISTS (SELECT 1 FROM app.questions q WHERE q.exam_id = e.id AND q.category_id IS NULL AND q.question_type = $$DML$$)'

-- 2f. DML PREVIEW, practice -> practice_categories.preview_schema_name
--     (practice_controller.go:291-294). Only dual_schema DML questions
--     execute there; the rest preview in the category schema (2a).
\set cte_prev_practice ' UNION ALL SELECT $$4. DML preview (decoy)$$::text, $$practice category $$ || pc.id::text || $$ ($$ || pc.name || $$) preview$$, pc.preview_schema_name, (SELECT count(*) FROM app.practice_questions q WHERE q.category_id = pc.id AND q.question_type = $$DML$$ AND q.dual_schema) FROM app.practice_categories pc WHERE COALESCE(pc.preview_schema_name, $$$$) <> $$$$ AND EXISTS (SELECT 1 FROM app.practice_questions q WHERE q.category_id = pc.id AND q.question_type = $$DML$$ AND q.dual_schema)'

-- 2g. DML PREVIEW, exam category that OWNS its schema and has a decoy
--     -> sandbox_exam_cat_<id>_preview (quiz_controller.go:370).
\set cte_prev_examcat ' UNION ALL SELECT $$4. DML preview (decoy)$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), category $$ || ec.id::text || $$ ($$ || ec.name || $$) preview$$, $$sandbox_exam_cat_$$ || ec.id::text || $$_preview$$, (SELECT count(*) FROM app.questions q WHERE q.category_id = ec.id AND q.question_type = $$DML$$ AND q.dual_schema) FROM app.exam_categories ec JOIN app.examinations e ON e.id = ec.exam_id WHERE ec.schema_name = $$exam_cat_$$ || ec.id::text AND COALESCE(ec.preview_schema_name, $$$$) <> $$$$ AND EXISTS (SELECT 1 FROM app.questions q WHERE q.category_id = ec.id AND q.question_type = $$DML$$ AND q.dual_schema)'

-- 2h. DML PREVIEW, everything else under an exam with a decoy curriculum
--     (uncategorised questions, and legacy/adopted categories that do NOT own
--     their schema) -> sandbox_exam_<exam_id>_preview
--     (quiz_controller.go:382). Closes the exec_target CTE.
\set cte_prev_exam ' UNION ALL SELECT $$4. DML preview (decoy)$$::text, $$exam $$ || e.id::text || $$ ($$ || e.title || $$), exam-level preview$$, $$sandbox_exam_$$ || e.id::text || $$_preview$$, (SELECT count(*) FROM app.questions q LEFT JOIN app.exam_categories ec ON ec.id = q.category_id WHERE q.exam_id = e.id AND q.question_type = $$DML$$ AND q.dual_schema AND (q.category_id IS NULL OR ec.schema_name IS DISTINCT FROM ($$exam_cat_$$ || ec.id::text))) FROM app.examinations e WHERE COALESCE(e.preview_curriculum_schema, $$$$) <> $$$$ AND EXISTS (SELECT 1 FROM app.questions q LEFT JOIN app.exam_categories ec ON ec.id = q.category_id WHERE q.exam_id = e.id AND q.question_type = $$DML$$ AND q.dual_schema AND (q.category_id IS NULL OR ec.schema_name IS DISTINCT FROM ($$exam_cat_$$ || ec.id::text))))'

-- --- shared predicate, part 3: existence flag + the reachability test -------
-- `tgt` adds schema_exists so an execution schema that was never created (exam
-- never activated, decoy sandbox never previewed into) is reported in its own
-- bucket instead of naively matching every public name. `reachable` therefore
-- covers ONLY execution schemas that exist.
\set cte_reach ', tgt AS (SELECT t.*, EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname = t.exec_schema) AS schema_exists FROM exec_target t), reachable AS (SELECT g.section, g.owner, g.exec_schema, g.dml_questions, p.relname AS reachable_name, p.relkind AS kind FROM tgt g CROSS JOIN pub p WHERE g.schema_exists AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = g.exec_schema AND c.relkind IN ($$r$$,$$p$$,$$v$$,$$m$$,$$f$$,$$S$$) AND c.relname = p.relname))'

-- --- shared predicate, part 4: routines (pg_proc) ---------------------------
-- Compared by NAME, not by signature (see OBJECT CLASSES COVERED). Runs over
-- EVERY execution schema in tgt, sections 1-4 alike, because a routine name in
-- `public` was reachable from all of them.
\set cte_proc ', pubproc AS (SELECT DISTINCT p.proname AS objname, p.prokind AS kind FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $$public$$), reachable_proc AS (SELECT g.section, g.owner, g.exec_schema, g.dml_questions, f.objname AS reachable_name, f.kind FROM tgt g CROSS JOIN pubproc f WHERE g.schema_exists AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_proc c JOIN pg_catalog.pg_namespace n2 ON n2.oid = c.pronamespace WHERE n2.nspname = g.exec_schema AND c.proname = f.objname))'

-- --- shared predicate, part 5: types (pg_type) ------------------------------
-- Standalone composites / domains / enums / ranges / multiranges only: a row
-- type belonging to a table or view is already reported as that relation, and
-- an array type is unreferenceable except through its element type.
\set cte_type ', pubtype AS (SELECT DISTINCT t.typname AS objname, t.typtype AS kind FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname = $$public$$ AND t.typtype IN ($$c$$,$$d$$,$$e$$,$$r$$,$$m$$) AND (t.typrelid = 0 OR EXISTS (SELECT 1 FROM pg_catalog.pg_class rc WHERE rc.oid = t.typrelid AND rc.relkind = $$c$$)) AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_type el WHERE el.oid = t.typelem AND el.typarray = t.oid)), reachable_type AS (SELECT g.section, g.owner, g.exec_schema, g.dml_questions, y.objname AS reachable_name, y.kind FROM tgt g CROSS JOIN pubtype y WHERE g.schema_exists AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_type t2 JOIN pg_catalog.pg_namespace n2 ON n2.oid = t2.typnamespace WHERE n2.nspname = g.exec_schema AND t2.typname = y.objname))'

\set cte :cte_pub :cte_practice :cte_examcap_cat :cte_examcap_uncat :cte_examgrade_cat :cte_examgrade_uncat :cte_prev_practice :cte_prev_examcat :cte_prev_exam :cte_reach :cte_proc :cte_type

\echo ''
\echo '==================================================================='
\echo ' 1. PRACTICE capture + grading (schema: practice category schema)'
\echo '    public RELATION names absent from the category schema'
\echo '==================================================================='
WITH :cte
SELECT owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable
WHERE section LIKE $$1.%$$
ORDER BY owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 2. EXAM CAPTURE (schema: exam category schema, else curriculum'
\echo '    schema). public RELATION names absent from that schema'
\echo '==================================================================='
WITH :cte
SELECT owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable
WHERE section LIKE $$2.%$$
ORDER BY owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 3. EXAM GRADING (schema: sandbox_exam_cat_<cat> / sandbox_exam_<id>)'
\echo '    public RELATION names absent from the SANDBOX. Views, matviews,'
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
\echo ' 4. DML PREVIEW decoy schemas (dual_schema questions only)'
\echo '    schema: practice preview_schema_name /'
\echo '    sandbox_exam_cat_<cat>_preview / sandbox_exam_<id>_preview.'
\echo '    public RELATION names absent from the decoy. The exam decoys are'
\echo '    cloned by the same tables-only path as the real sandboxes, so'
\echo '    they miss views, matviews, foreign tables and sequences too.'
\echo '    A break here fails the student PREVIEW, not grading.'
\echo '==================================================================='
WITH :cte
SELECT owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable
WHERE section LIKE $$4.%$$
ORDER BY owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 5. public ROUTINES absent from an execution schema (ALL of 1-4)'
\echo '    Functions/procedures/aggregates resolve through search_path just'
\echo '    like relations, are never cloned into any sandbox, and a lecturer'
\echo '    CAN create them (curriculum import has no DDL allowlist).'
\echo '    kind: f function, p procedure, a aggregate, w window.'
\echo '    Compared by NAME only, not by argument signature.'
\echo '==================================================================='
WITH :cte
SELECT section, owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable_proc
ORDER BY section, owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 6. public TYPES absent from an execution schema (ALL of 1-4)'
\echo '    Standalone composites, domains, enums, ranges, multiranges.'
\echo '    kind: c composite, d domain, e enum, r range, m multirange.'
\echo '==================================================================='
WITH :cte
SELECT section, owner, exec_schema, dml_questions, reachable_name, kind
FROM reachable_type
ORDER BY section, owner, reachable_name;

\echo ''
\echo '==================================================================='
\echo ' 7. Execution schemas that DO NOT EXIST yet (NOT counted above)'
\echo '    Typically a sandbox for an exam that has never been activated,'
\echo '    or a decoy preview sandbox nobody has previewed into yet (both'
\echo '    are created lazily on first use).'
\echo '    Reachability there is UNKNOWN, not zero: the schema is created'
\echo '    and populated at activation/first-preview time, so re-run this'
\echo '    audit afterwards, or treat these as potentially affected.'
\echo '==================================================================='
WITH :cte
SELECT section, owner, exec_schema, dml_questions
FROM tgt
WHERE NOT schema_exists
ORDER BY section, owner;

\echo ''
\echo '==================================================================='
\echo ' 8. Verdict'
\echo '==================================================================='
-- practice_dml_total / exam_dml_total exist to distinguish "the change is
-- safe here" from "this database has no DML questions, so the gate proved
-- nothing". A zero *_affected count alongside a zero *_total count is the
-- second case, and must be reported as such.
--
-- Every *_affected count below is computed from the SAME CTE the matching
-- section selects from, filtered by the SAME section predicate, and counts
-- distinct (owner, exec_schema) execution targets — the unit each section
-- lists — so a section and its count cannot drift apart.
--   practice_categories_affected     <- section 1
--   exam_capture_targets_affected    <- section 2
--   exam_grading_targets_affected    <- section 3
--   preview_targets_affected         <- section 4
--   routine_targets_affected         <- section 5 (spans sections 1-4)
--   type_targets_affected            <- section 6 (spans sections 1-4)
--   exec_schemas_missing             <- section 7
-- exec_schemas_missing is neither safe nor affected — it is unknown, and a
-- non-zero value means this audit did not fully answer the question.
WITH :cte
SELECT
  (SELECT count(*) FROM app.practice_questions WHERE question_type = $$DML$$)
      AS practice_dml_total,
  (SELECT count(*) FROM app.questions WHERE question_type = $$DML$$)
      AS exam_dml_total,
  (SELECT count(DISTINCT (owner, exec_schema)) FROM reachable WHERE section LIKE $$1.%$$)
      AS practice_categories_affected,
  (SELECT count(DISTINCT (owner, exec_schema)) FROM reachable WHERE section LIKE $$2.%$$)
      AS exam_capture_targets_affected,
  (SELECT count(DISTINCT (owner, exec_schema)) FROM reachable WHERE section LIKE $$3.%$$)
      AS exam_grading_targets_affected,
  (SELECT count(DISTINCT (owner, exec_schema)) FROM reachable WHERE section LIKE $$4.%$$)
      AS preview_targets_affected,
  (SELECT count(DISTINCT (owner, exec_schema)) FROM reachable_proc)
      AS routine_targets_affected,
  (SELECT count(DISTINCT (owner, exec_schema)) FROM reachable_type)
      AS type_targets_affected,
  (SELECT count(*) FROM tgt WHERE NOT schema_exists)
      AS exec_schemas_missing;

ROLLBACK;
