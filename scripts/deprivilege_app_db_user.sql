-- =====================================================================
-- Mitigation: stop the app from connecting to Postgres as a SUPERUSER,
-- and stop the student-evaluation connection from owning anything.
-- =====================================================================
--
-- REVISION 2026-07-23. The previous version of this script was WRONG in a
-- way that mattered: it created the app role with CREATEROLE, and on
-- PostgreSQL 15 a non-superuser CREATEROLE role can grant itself the
-- predefined roles. Measured on 15.18:
--
--     GRANT pg_execute_server_program TO t_app;                  -> GRANT ROLE
--     COPY (SELECT 1) TO PROGRAM 'touch /tmp/proof';             -> COPY 1
--     (file appeared, owned by the postgres OS user)
--
-- With NOCREATEROLE the same attempt fails:
--     ERROR:  must have admin option on role "pg_execute_server_program"
--
-- So this script now creates the app role NOCREATEROLE and pre-creates the
-- roles the application would otherwise have needed CREATEROLE to make.
--
-- STATUS (updated 2026-07-27, ARCH-022 SS7) -- read this before the history
-- below, which is now CLOSED for students and kept only for context.
-- ---------------------------------------------------------------------
-- The STUDENT superuser-escape (the SET ROLE / RESET ROLE history a few
-- paragraphs down) is now closed in APPLICATION CODE by ARCH-022: the
-- backend provisions four per-class restricted LOGIN roles at boot
-- (CreateRestrictedUser), so student SQL no longer runs on a LOGIN role
-- that is a superuser in the first place -- there is nothing left for
-- `SET ROLE` / `RESET ROLE` to escalate to.
--
-- This script is COMPLEMENTARY, not superseded. It is still the only thing
-- that addresses residual finding 6.6: the LECTURER / admin schema-import
-- path. Schema import executes arbitrary, multi-statement SQL as the
-- application's own LOGIN role, and that role is a superuser today -- so
-- that SQL can still reach `COPY ... TO PROGRAM` (arbitrary shell execution
-- on the database host) and raw file read/write. Only de-privileging
-- sqlquiz_app itself (this script) closes that.
--
-- WHY ANY OF THIS -- history: the now-closed student escape
-- -----------------------------------------------------------------------
-- Student-submitted SQL runs inside a transaction that does
-- `SET LOCAL ROLE exam_student*`. That was NOT a security boundary while the
-- LOGIN role was a superuser: PostgreSQL checks SET ROLE against
-- **session_user**, not current_user. Both of these are ONE statement and
-- both escaped (measured, pre-ARCH-022):
--
--     SET ROLE postgres;
--     DO $$ BEGIN RESET ROLE; INSERT INTO <target> SELECT ... ; END $$;
--
-- WHAT THIS SCRIPT FIXES AND DOES NOT FIX -- read before relying on it
-- --------------------------------------------------------------------
--   FIXES:      the app's own LOGIN role running as superuser, which closes
--               residual 6.6: `COPY ... TO PROGRAM` (arbitrary shell
--               execution on the database host) and server-side file
--               read/write reachable from a lecturer's schema-import SQL;
--               the CREATEROLE self-grant path above.
--
--   DOES NOT FIX (on its own): this script says nothing about the student
--               SQL execution path -- that is owned by ARCH-022 in
--               application code (see STATUS above), not by this script.
--               This script pre-creates the sandbox roles / quiz_exec
--               (see step 3's KNOWN RESIDUAL note) purely so the app never
--               needs CREATEROLE, not to fix the student path itself.
--
-- HOW TO APPLY -- do not paste this blindly
-- -----------------------------------------
--   1. BACK UP FIRST:   pg_dump -Fc sql_quiz > sql_quiz_backup.dump
--   2. Rehearse against a throwaway database before production.
--   3. Stop the backend:  docker compose stop backend
--   4. Run as the postgres superuser, connected to the application DB.
--   5. Set real passwords below (two places: sqlquiz_app and quiz_exec).
--   6. Update .env:
--        - DATABASE_URL      -> user sqlquiz_app
--        - STUDENT_DB_PASSWORD -> the quiz_exec password
--        - LEAVE DB_USER ALONE. It feeds the postgres CONTAINER's
--          POSTGRES_USER and its healthcheck, not the app's connection.
--          initdb does not re-run on an existing volume, so changing it
--          does not change the cluster -- it only breaks the healthcheck.
--   7. docker compose up -d
--   8. Verify the app boots, a student DML question still grades, and a
--      lecturer schema import still works.
--
-- ROLLBACK: revert .env and restart. Nothing here is dropped; the roles are
-- additive. If boot fails complaining about language plpgsql, run as
-- superuser:  GRANT USAGE ON LANGUAGE plpgsql TO PUBLIC;
-- =====================================================================

\set ON_ERROR_STOP on

-- ---------------------------------------------------------------------
-- 1. Application login role: NOT a superuser, and NOT CREATEROLE.
--    NOCREATEROLE is load-bearing -- see the header. Because of it, every
--    role the app would have created at boot is pre-created in step 2.
-- ---------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'sqlquiz_app') THEN
        CREATE ROLE sqlquiz_app LOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB
            NOREPLICATION NOBYPASSRLS PASSWORD 'CHANGE_ME_APP_PASSWORD';
    ELSE
        ALTER ROLE sqlquiz_app NOSUPERUSER NOCREATEROLE NOCREATEDB
            NOREPLICATION NOBYPASSRLS;
    END IF;
END
$$;

-- ---------------------------------------------------------------------
-- 2. Pre-create the sandbox roles. The application's CreateRestrictedUser
--    boot migration checks pg_roles first and skips creation when they
--    already exist, so it no longer needs CREATEROLE.
--    These are NOLOGIN: they are only ever reached via SET LOCAL ROLE.
-- ---------------------------------------------------------------------
DO $$
DECLARE
    r text;
BEGIN
    FOREACH r IN ARRAY ARRAY[
        'exam_student',
        'exam_student_readonly',
        'exam_student_preview',
        'exam_student_preview_readonly'
    ] LOOP
        IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = r) THEN
            EXECUTE format(
                'CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB '
                'NOREPLICATION NOBYPASSRLS', r);
        END IF;
    END LOOP;
END
$$;

-- ---------------------------------------------------------------------
-- 3. The student-evaluation LOGIN role (ARCH-025).
--    This is the role student SQL actually connects as. It OWNS NOTHING and
--    has no privileges on the app schema, so `RESET ROLE` from inside a
--    student statement lands somewhere useless.
--    NOINHERIT is deliberate: privileges are only acquired by an explicit
--    SET LOCAL ROLE, never passively.
--    Its password lives in .env as STUDENT_DB_PASSWORD, NOT generated at
--    boot -- a NOCREATEROLE app role cannot ALTER another role's password.
-- ---------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'quiz_exec') THEN
        CREATE ROLE quiz_exec LOGIN NOINHERIT NOSUPERUSER NOCREATEROLE
            NOCREATEDB NOREPLICATION NOBYPASSRLS
            PASSWORD 'CHANGE_ME_QUIZ_EXEC_PASSWORD';
    ELSE
        ALTER ROLE quiz_exec LOGIN NOINHERIT NOSUPERUSER NOCREATEROLE
            NOCREATEDB NOREPLICATION NOBYPASSRLS;
    END IF;
END
$$;

-- quiz_exec must be able to SET LOCAL ROLE into each sandbox role.
GRANT exam_student,
      exam_student_readonly,
      exam_student_preview,
      exam_student_preview_readonly
   TO quiz_exec;

-- KNOWN RESIDUAL, accepted deliberately for the interim (ARCH-025):
-- because quiz_exec is a member of all four sandbox roles, a student can
-- `SET ROLE` ACROSS classes -- e.g. from a preview role into the grading
-- role -- which defeats the dual-schema decoy isolation. The full ARCH-022
-- design replaces quiz_exec with four LOGIN roles, one per class, each a
-- member of exactly ONE sandbox role, which closes this. Until then the
-- decoy is a teaching aid, not a security boundary.

GRANT CONNECT ON DATABASE sql_quiz TO quiz_exec;   -- change if DB_NAME differs

-- ---------------------------------------------------------------------
-- 4. The app still creates and drops SCHEMAS (categories, exam curricula,
--    sandboxes, decoys), so it needs CREATE on the database. It does NOT
--    need CREATEROLE for that.
-- ---------------------------------------------------------------------
GRANT CONNECT, CREATE, TEMPORARY ON DATABASE sql_quiz TO sqlquiz_app;

-- ---------------------------------------------------------------------
-- 5. plpgsql: ARCH-022's CreateRestrictedUser now runs
--    `REVOKE USAGE ON LANGUAGE plpgsql FROM PUBLIC` at boot, to stop student
--    DO blocks. Once this script de-privileges sqlquiz_app off superuser, it
--    would lose plpgsql along with PUBLIC, and its own boot DO $$ ... $$
--    migrations would fail with "permission denied for language plpgsql" --
--    measured. This GRANT keeps the app's boot self-healing under the
--    de-privileged posture. Order matters: GRANT before the REVOKE runs.
-- ---------------------------------------------------------------------
GRANT USAGE ON LANGUAGE plpgsql TO sqlquiz_app;

-- ---------------------------------------------------------------------
-- 6. Hand over ownership of existing objects. RUN WITH THE BACKEND STOPPED.
--
--    NOTE: `REASSIGN OWNED BY postgres TO sqlquiz_app` DOES NOT WORK and is
--    deliberately not used here. Measured on 15.18:
--        ERROR: cannot reassign ownership of objects owned by role postgres
--               because they are required by the database system
--    The bootstrap superuser owns pinned system catalogs, so REASSIGN OWNED
--    always aborts -- and it would abort AFTER the role changes above had
--    already been applied, leaving a half-migrated cluster. Instead we walk
--    only the application's own objects explicitly.
-- ---------------------------------------------------------------------
DO $$
DECLARE
    obj record;
BEGIN
    -- Schemas the application owns or creates (app, public, and every
    -- per-category / per-exam / sandbox / decoy schema).
    FOR obj IN
        SELECT nspname
        FROM pg_catalog.pg_namespace
        WHERE nspname NOT LIKE 'pg\_%'
          AND nspname <> 'information_schema'
    LOOP
        EXECUTE format('ALTER SCHEMA %I OWNER TO sqlquiz_app', obj.nspname);
    END LOOP;

    -- Tables, views, materialised views, sequences and foreign tables.
    FOR obj IN
        SELECT n.nspname, c.relname, c.relkind
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname NOT LIKE 'pg\_%'
          AND n.nspname <> 'information_schema'
          AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
    LOOP
        EXECUTE format('ALTER %s %I.%I OWNER TO sqlquiz_app',
            CASE obj.relkind
                WHEN 'v' THEN 'VIEW'
                WHEN 'm' THEN 'MATERIALIZED VIEW'
                WHEN 'S' THEN 'SEQUENCE'
                WHEN 'f' THEN 'FOREIGN TABLE'
                ELSE 'TABLE'
            END, obj.nspname, obj.relname);
    END LOOP;

    -- Functions and procedures.
    FOR obj IN
        SELECT n.nspname, p.oid::regprocedure AS sig, p.prokind
        FROM pg_catalog.pg_proc p
        JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname NOT LIKE 'pg\_%'
          AND n.nspname <> 'information_schema'
    LOOP
        EXECUTE format('ALTER %s %s OWNER TO sqlquiz_app',
            CASE obj.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,
            obj.sig);
    END LOOP;

    -- Types the application defined (skips table row-types, which follow
    -- their table's ownership automatically).
    FOR obj IN
        SELECT n.nspname, t.typname
        FROM pg_catalog.pg_type t
        JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname NOT LIKE 'pg\_%'
          AND n.nspname <> 'information_schema'
          AND t.typtype IN ('e', 'd', 'c')
          AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c
                          WHERE c.reltype = t.oid)
    LOOP
        EXECUTE format('ALTER TYPE %I.%I OWNER TO sqlquiz_app',
                       obj.nspname, obj.typname);
    END LOOP;
END
$$;

ALTER DATABASE sql_quiz OWNER TO sqlquiz_app;

-- ---------------------------------------------------------------------
-- 7. Sanity checks -- all of these should hold afterwards.
-- ---------------------------------------------------------------------
SELECT rolname, rolsuper, rolcreaterole, rolcanlogin, rolinherit
FROM pg_catalog.pg_roles
WHERE rolname IN ('sqlquiz_app', 'quiz_exec')
ORDER BY rolname;
-- expect: sqlquiz_app  super=f createrole=f login=t
--         quiz_exec    super=f createrole=f login=t inherit=f
--         BOTH createrole=f IS THE POINT OF THIS REVISION.

SELECT u.rolname AS member, r.rolname AS member_of
FROM pg_catalog.pg_auth_members m
JOIN pg_catalog.pg_roles r ON r.oid = m.roleid
JOIN pg_catalog.pg_roles u ON u.oid = m.member
WHERE u.rolname IN ('sqlquiz_app', 'quiz_exec')
ORDER BY 1, 2;
-- expect: quiz_exec is a member of the four exam_student* roles.
--         sqlquiz_app should NOT need to be.

SELECT pg_catalog.pg_get_userbyid(datdba) AS db_owner
FROM pg_catalog.pg_database
WHERE datname = 'sql_quiz';
-- expect: sqlquiz_app

-- Negative check: quiz_exec must have NO privileges on the app schema.
SELECT has_schema_privilege('quiz_exec', 'app', 'USAGE')  AS app_usage,
       has_table_privilege('quiz_exec', 'app.system_users', 'SELECT') AS can_read_users;
-- expect: both FALSE. If either is true, the interim fix is not in place.
