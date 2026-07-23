# Interim hardening — ship-tonight runbook (2026-07-23)

Ships the security work that fits before the usage-budget reset. The full
design (**ARCH-022**) is deliberately **deferred to Saturday 2026-07-25** —
see "What is deferred" at the bottom. It is fully specified in
`pipeline-state.json` (ARCH-022 / ARCH-023 / ARCH-024, TODO-030…042); nothing
is lost by pausing it.

## What ships tonight

| # | Change | Closes |
|---|---|---|
| 1 | `scripts/deprivilege_app_db_user.sql` (revised) | superuser escalation, `COPY … TO PROGRAM` RCE, CREATEROLE self-grant |
| 2 | **ARCH-025** — student SQL runs on the `quiz_exec` login role | reading `app.system_users` (password hashes) and every `correct_sql` |
| 3 | **ARCH-023** — append `pg_temp` to `search_path` | forging a correct grade with `CREATE TEMP TABLE` |

## Verified, not asserted

All measured on postgres:15-alpine 15.18 against a rehearsal database.

**The revised script's posture blocks all four attacks:**
```
SET ROLE postgres                -> ERROR: permission denied to set role "postgres"
DO $$ BEGIN RESET ROLE; END $$   -> lands on quiz_exec
SELECT * FROM app.system_users   -> ERROR: permission denied for schema app
COPY (SELECT 1) TO PROGRAM '…'   -> ERROR: must be superuser or have privileges
                                          of pg_execute_server_program
GRANT pg_execute_server_program TO quiz_exec
                                 -> ERROR: must have admin option
```

**Two bugs the rehearsal caught in the previous version of the script:**

1. It created the app role with `CREATEROLE`. On PostgreSQL 15 a non-superuser
   `CREATEROLE` role can grant itself the predefined roles, so the RCE the
   script claimed to fix was still reachable:
   ```
   GRANT pg_execute_server_program TO t_app;   -> GRANT ROLE
   COPY (SELECT 1) TO PROGRAM 'touch /tmp/proof';  -> COPY 1   (file appeared)
   ```
   With `NOCREATEROLE`: `ERROR: must have admin option on role …`. The script
   now pre-creates the sandbox roles so the app never needs `CREATEROLE`.
2. `REASSIGN OWNED BY postgres TO sqlquiz_app` **can never succeed** — the
   bootstrap superuser owns pinned system catalogs:
   ```
   ERROR: cannot reassign ownership of objects owned by role postgres
          because they are required by the database system
   ```
   In production it would have aborted *after* the role changes applied,
   leaving a half-migrated cluster. Replaced with an explicit walk over the
   application's own schemas, relations, routines and types.

## Known residual — accepted deliberately tonight

`quiz_exec` is a member of all four sandbox roles, so a student can `SET ROLE`
**across classes**. Measured:
```
SET LOCAL ROLE exam_student_preview_readonly;
SET ROLE exam_student;   -> current_user = exam_student
```
This defeats the dual-schema decoy isolation. **Until ARCH-022 lands, treat
the decoy as a teaching aid, not a security boundary.** ARCH-022 replaces
`quiz_exec` with four login roles, each a member of exactly one sandbox role,
which closes it.

Also still open tonight: the **lecturer import path** runs arbitrary
multi-statement SQL as the app role. That is a privileged-user path, not a
student one, but it is not sandboxed.

## Deploy order

Do not improvise the order — steps 3 and 6 are where it can go wrong.

```bash
# 1. BACK UP. Everything below is reversible; this is what makes that cheap.
docker exec sql-quiz-db pg_dump -Fc -U postgres sql_quiz > sql_quiz_$(date +%F).dump

# 2. Set real passwords in scripts/deprivilege_app_db_user.sql
#    (two places: sqlquiz_app and quiz_exec).

# 3. Stop the BACKEND ONLY. Ownership changes under a live pool are trouble.
docker compose stop backend

# 4. Apply the script as the superuser.
docker exec -i sql-quiz-db psql -U postgres -d sql_quiz \
  < scripts/deprivilege_app_db_user.sql
#    Every sanity check at the end must pass, especially:
#      sqlquiz_app / quiz_exec  ->  rolsuper=f  rolcreaterole=f
#      app_usage=f  can_read_users=f

# 5. Update .env:
#      DATABASE_URL        -> user sqlquiz_app (with its new password)
#      STUDENT_DB_PASSWORD -> the quiz_exec password        (NEW VARIABLE)
#    *** LEAVE DB_USER ALONE. *** It feeds the postgres CONTAINER's
#    POSTGRES_USER and healthcheck, not the app's connection. initdb does not
#    re-run on an existing volume, so changing it cannot change the cluster —
#    it only breaks the healthcheck.

# 6. Bring it back up.
docker compose up -d --build

# 7. Verify, in this order:
#    a. backend logs show no fatal on boot
#    b. a lecturer schema import still works   (needs the app role's CREATE)
#    c. a student QUERY question still grades
#    d. a student DML question still grades
#    e. a student submitting `SELECT session_user` sees quiz_exec, NOT postgres
```

### Your data is safe across this

Verified against `docker-compose.yml`:

- `postgres_data:/var/lib/postgresql/data` is a **named volume** — `docker
  compose down && up` destroys containers, not volumes. Only `down -v` wipes it.
  **Never pass `-v`.**
- `company_elmasri_postgres.sql` is mounted into `/docker-entrypoint-initdb.d/`,
  which Postgres runs **only on an empty data dir**. It cannot re-seed or
  overwrite an existing cluster.
- ARCH-025 and ARCH-023 add roles, a pool and a `search_path` change. No
  `DROP TABLE`, no `DROP COLUMN`, no data rewrite on any application table.
- The only destructive statements in the boot migrations —
  `DROP COLUMN IF EXISTS strict_record_order` and
  `DROP TABLE IF EXISTS app.student_sandboxes`, plus dropping orphan sandbox
  schemas — target legacy per-student evaluation scratch, not submissions or
  accounts, and already ran long ago.

### Rollback

Revert `.env` and restart. The roles are additive; nothing is dropped. If boot
fails complaining about `language plpgsql`, run as superuser:
`GRANT USAGE ON LANGUAGE plpgsql TO PUBLIC;`

## Post-deploy: treat credentials as exposed

For any period the pre-fix build was live, **every password hash and every
`correct_sql` was readable by any authenticated student**. Confirm the hashing
is bcrypt/argon2, then plan a forced password rotation. Also assume answer keys
for any already-published exam may be compromised.

## What is deferred to Saturday 2026-07-25 (post usage-budget reset)

**ARCH-022 — four restricted login roles, one per privilege class, four pools.**
12 serialized TODOs (they all share `postgres_evaluation.go`, `postgres.go`,
`postgres_schema.go`, so there is no parallelism to exploit). Estimated 600k–1.2M
tokens including merge-check, validator and regression-tester. It supersedes
ARCH-025: `quiz_exec` becomes unused and can be dropped, or left — it owns
nothing either way.

It closes:
- the cross-class `SET ROLE` residual above (the decoy becomes a real boundary);
- `REVOKE USAGE ON LANGUAGE plpgsql FROM PUBLIC`, blocking student `DO` blocks
  — **must** be paired with `GRANT USAGE ON LANGUAGE plpgsql TO CURRENT_USER`
  first, or a de-privileged app role bricks its own boot migrations;
- `pg_terminate_backend` / `pg_cancel_backend` revokes;
- ARCH-024 cleanup: delete dead `ExecStudentSQL`/`QueryStudentSQL`, fix the
  `RESET ROLE`/`ROLLBACK TO SAVEPOINT` ordering at `postgres_evaluation.go:393`
  and `:403`, and fix `postgres.go` `MaxConnIdleTime = 30 * 60` (1.8µs — missing
  `time.Minute`).

Open questions for the user still standing on ARCH-022: whether to accept the
plpgsql coupling, and whether the generated-at-boot password approach survives
(it requires `CREATEROLE`, which the deprivileged posture removes — so the full
design will likely need to adopt ARCH-025's `.env` credential approach instead).
