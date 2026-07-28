-- ============================================================================
-- Audit: app.system_users student_number vs student_id_ext duplicates
-- ============================================================================
-- READ-ONLY. Diagnoses whether the pre-2026-07-13 student_number UNIQUE-index
-- bug (see docs / commit "fix(students): student_number is non-unique; upsert
-- keyed on student_id_ext") corrupted this database. Before the fix,
-- idx_system_users_student_number was a UNIQUE partial index and the roster
-- import upsert used "ON CONFLICT (student_number)". Because student_number
-- is duplicate-able by design (several students may legitimately share one),
-- importing a second student with the same student_number silently
-- OVERWROTE the first: same row, new student_id_ext/username/name — a real
-- student identity was lost. This script only SELECTs; it changes nothing.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_student_number_duplicates.sql
--       (or:  psql -h HOST -U USER -d DBNAME -f scripts/audit_student_number_duplicates.sql)
--
-- How to read the verdict:
--   * "index uniqueness" = UNIQUE     -> pre-fix schema; the fix migration has
--                                        not run on this DB yet. Duplicate
--                                        student_number imports will still
--                                        collapse. Apply the fix ASAP.
--                        = non-unique -> fix migration has run. Duplicate
--                                        student_numbers listed in section 2
--                                        are EXPECTED/allowed, not a bug.
--   * Section 3 (duplicate student_id_ext) must ALWAYS be empty. Any row
--     there is a real integrity problem independent of this bug.
-- ============================================================================

-- Guarantee we can never write, even by accident.
BEGIN;
SET TRANSACTION READ ONLY;

\pset footer off
\echo '==================================================================='
\echo ' 1. Fix status — is idx_system_users_student_number UNIQUE?'
\echo '==================================================================='
SELECT
    COALESCE(
        (SELECT CASE WHEN i.indisunique THEN 'UNIQUE (pre-fix schema — imports can still collapse)'
                     ELSE 'non-unique (fixed)'
                END
         FROM pg_index i
         JOIN pg_class c     ON c.oid = i.indexrelid
         JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'app'
           AND c.relname = 'idx_system_users_student_number'),
        '(MISSING — index not found; schema not yet initialized?)'
    ) AS student_number_index_status;

\echo ''
\echo '==================================================================='
\echo ' 2. Duplicate student_numbers (allowed under the fixed schema)'
\echo '==================================================================='
SELECT su.student_number,
       count(*)                          AS row_count,
       array_agg(su.id ORDER BY su.id)             AS user_ids,
       array_agg(su.username ORDER BY su.id)       AS usernames,
       array_agg(su.student_id_ext ORDER BY su.id) AS student_id_exts
FROM app.system_users su
WHERE su.student_number IS NOT NULL
GROUP BY su.student_number
HAVING count(*) > 1
ORDER BY row_count DESC, su.student_number;

\echo ''
\echo '==================================================================='
\echo ' 3. Integrity check — duplicate student_id_ext (must be ZERO rows)'
\echo '==================================================================='
SELECT su.student_id_ext,
       count(*)                    AS row_count,
       array_agg(su.id ORDER BY su.id)       AS user_ids,
       array_agg(su.username ORDER BY su.id) AS usernames
FROM app.system_users su
WHERE su.student_id_ext IS NOT NULL
GROUP BY su.student_id_ext
HAVING count(*) > 1
ORDER BY row_count DESC, su.student_id_ext;

\echo ''
\echo '==================================================================='
\echo ' 4. Unloginnable students — NULL/empty student_id_ext or username'
\echo '==================================================================='
SELECT su.id, su.username, su.student_number, su.student_id_ext,
       su.first_name, su.last_name, su.created_at
FROM app.system_users su
WHERE su.role = 'student'
  AND (su.student_id_ext IS NULL OR su.student_id_ext = ''
       OR su.username IS NULL OR su.username = '')
ORDER BY su.id;

\echo ''
\echo '==================================================================='
\echo ' 5. Verdict'
\echo '==================================================================='
SELECT
    CASE
        WHEN EXISTS (
            SELECT 1 FROM app.system_users su
            WHERE su.student_id_ext IS NOT NULL
            GROUP BY su.student_id_ext
            HAVING count(*) > 1
        )
            THEN 'INTEGRITY ERROR: duplicate student_id_ext found (see section 3) — this must never happen; investigate immediately.'
        WHEN EXISTS (
            SELECT 1 FROM pg_index i
            JOIN pg_class c     ON c.oid = i.indexrelid
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE n.nspname = 'app'
              AND c.relname = 'idx_system_users_student_number'
              AND i.indisunique
        )
            THEN 'PRE-FIX SCHEMA: student_number still UNIQUE — duplicate imports will collapse (see section 1). Apply the fix migration.'
        WHEN EXISTS (
            SELECT 1 FROM app.system_users su
            WHERE su.student_number IS NOT NULL
            GROUP BY su.student_number
            HAVING count(*) > 1
        )
            THEN 'OK: student_number is non-unique; duplicate student_numbers present (see section 2) — this is allowed.'
        ELSE 'CLEAN: student_number is non-unique and no duplicate student_numbers exist.'
    END AS verdict;

ROLLBACK;
