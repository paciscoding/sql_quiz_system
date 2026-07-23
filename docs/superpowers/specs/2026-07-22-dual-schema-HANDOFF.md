# Dual-Schema Grading — Session Handoff (2026-07-22)

Durable state so context can be compacted. Full detail lives in
`pipeline-state.json` (21 ARCH decisions, 28 TODOs) and
`docs/superpowers/specs/2026-07-22-dual-schema-grading-design.md`.

## Feature (built, per user's locked decisions)
Per-question `dual_schema` toggle. Preview runs on lecturer-uploaded decoy
schema **A**; grading stays on real schema **B** and is never shown. Purpose:
stop students brute-forcing answers off the preview.

Locked decisions: two schemas same-structure/different-data, lecturer uploads
both · per-question toggle · practice AND exam · DML target-table check is
warn-only · no-decoy → fall back to single schema **and warn the lecturer**,
validated in frontend AND backend (spec §6.1) · mixed dual/non-dual owner →
warn only.

## Status
- **28 of 28 TODOs complete** (TODO-027/028 landed and were re-verified by the
  orchestrator). TODO-029 open: repair the `TestDMLGrading_D2` fixture, which
  chained 3 UPDATEs as student SQL and is now correctly rejected.
- Build/vet green; full suite passed against real Postgres with **zero skips**.
- Smoke test PASSED: non-dual preview→B and submit carries
  `validation_details`; dual preview→A and submit has **no**
  `validation_details` key. Verdict computed on B either way.

## Four leaks found AFTER the feature was "green" — the real story
The spec's own §7 claim ("submit returns no rows today") was factually wrong,
so no TODO covered the biggest leak. Review caught what implementation missed.

| Gap | Leak | Fix |
|---|---|---|
| 1 | `validation_details` returned B's rows on every submit; persisted + re-rendered in results history | ARCH-018 (TODO-021/022) — deferred scrub in `Validate`, after `decideStatus`, so grading is provably unchanged |
| 2 | orphaned decoy schemas on hard delete | ARCH-019 (TODO-023) |
| 3 | `SELECT * FROM curriculum_exam_7.tbl` inside a preview — no submission needed | ARCH-020 (TODO-024) — preview-only roles with USAGE on decoys only |
| 4 | non-dual question's preview in a mixed owner reads B | warn only (TODO-025/026) |

Validator FAILs D2 (`category_id` bound as `int` but IDs are Sqids strings →
"Preview as student" 400s) and D3 (decoy import into deleted category orphans
a schema) are both FIXED.

## CRITICAL — STILL OPEN. ARCH-021 did NOT close it.
Re-validation 2026-07-23 proved the sandbox escape survives ARCH-021, because
**`SET LOCAL ROLE` is not a boundary when the LOGIN role is a superuser** —
PostgreSQL checks `SET ROLE` membership against `session_user`, not
`current_user`. Both bypasses are ONE statement, so `execStudentStatement`
passes them through. Re-verified directly by the orchestrator:

```
BEGIN; SET LOCAL ROLE orch_student;        -- current_user = orch_student
SET ROLE postgres;                          -- succeeds
SELECT v FROM orch_secret.s;                -- SECRET_ROW
```
```
DO $$ BEGIN RESET ROLE;
      INSERT INTO public.orch_target SELECT v FROM orch_secret.s; END $$;
-- one statement; harvest lands in the table returned as the DML post-image
```
Control without escalation → `ERROR: permission denied for schema orch_secret`.

The QUERY path (`postgres_evaluation.go:100`, `tx.Query`) never used
`execStudentStatement` at all; a `DO` block there reaches `COPY ... TO PROGRAM`,
which is non-transactional and survives the rollback — **arbitrary shell
execution as the `postgres` OS user from an ordinary SELECT question.**

Required fix (design change, ARCH-022): run student SQL on a second `pgxpool`
whose **LOGIN role** is restricted (NOSUPERUSER, NOCREATEROLE). Fixes all four
paths at once. Supporting: `REVOKE USAGE ON LANGUAGE plpgsql FROM PUBLIC`;
delete dead `ExecStudentSQL`/`QueryStudentSQL` (`postgres_schema.go:68`,`:96`);
apply `scripts/deprivilege_app_db_user.sql` + change `.env` `DB_USER` as the
fastest partial mitigation (removes RCE, not data theft).

## What ARCH-021 actually landed — real, but insufficient
Student DML ran as multi-statement simple protocol, so `RESET ROLE` escaped the
sandbox to the `postgres` superuser. **Pre-existing; affected production
independently of this feature.** Any running deployment stays vulnerable until
this ships.

ARCH-021 / TODO-027 + TODO-028, both verified:
- `postgres_student_exec.go` (new) — `execStudentStatement` runs student SQL via
  `tx.Conn().PgConn().ExecParams` (extended protocol, one statement per Parse).
  Both sites use it (`postgres_evaluation.go:168` graded, `:377` preview);
  `grep 'tx.Exec(ctx, studentSQL)'` → no matches.
- Server-side rejection, quoted: `ERROR: cannot insert multiple commands into a
  prepared statement (SQLSTATE 42601)`, `Routine=exec_parse_message`.
  Non-vacuity control passed.
- `postgres.go:129` pins `DefaultQueryExecMode = QueryExecModeCacheStatement`
  so a `?default_query_exec_mode=simple_protocol` DSN cannot reopen the QUERY
  path. Argument-less `Exec` is still forced to simple protocol by
  `conn.go:515`, so lecturer schema imports are unaffected by construction.
- `ROLLBACK TO SAVEPOINT` now precedes `RESET ROLE` — failed student DML no
  longer 500s.

Validator verdict on the feature as a whole: **NOT SHIPPABLE**, solely because
of the open item above. D2, D3 and TODO-029 all PASS and need no rework.

**Product restriction introduced, needs a decision:** student DML answers are
now limited to ONE statement (`repository.MultiStatementFeedback`). A question
whose intended answer is "delete then insert" can no longer be answered.
Existing questions should be audited before deploy.

`scripts/deprivilege_app_db_user.sql` remains worth applying as defence in
depth (it is what stops `COPY ... TO PROGRAM` RCE if any future path regresses).

## Known-and-NOT-fixed (deliberate, separate items)
- `postgres.go:122` — `MaxConnIdleTime = 30 * 60` is **1.8 microseconds**, not
  30 minutes (missing `time.Minute`). Pre-existing; likely churning the pool.
- `TestVerifyForgotPassword_HTTP` / `TestParity_SingleVsBulk` hardcode emails
  with no teardown → `go test ./...` is not idempotent against a persistent DB.
- ARCH-020 residual: no-decoy fallback still previews B (by design, §6.1).
- `expected_rows_affected` / `capture_error` still visible on NON-dual
  questions (unchanged for back-compat).

## Environment / hygiene
- Migrations are idempotent and run at **backend boot** — they apply on
  `docker compose down && up`. No agent ever touched the live DB.
- Disposable verification container is now **`claude_impl_todo027_pg`** on port
  55432 (the older `dualschema_verify_pg` is gone). Tear down when finished:
  `docker rm -f claude_impl_todo027_pg`.
- Test invocation:
  `TEST_DATABASE_URL="postgres://postgres:test@localhost:55432/freshdb?sslmode=disable" ID_SALT=... go test ./...`
- **Use a pristine database.** On a reused DB, `TestVerifyForgotPassword_HTTP`
  and `TestParity_SingleVsBulk` fail on hardcoded-email collisions; both pass on
  a fresh one. Create with
  `docker exec claude_impl_todo027_pg psql -U postgres -c 'CREATE DATABASE <name>'`.

## NOTHING IS COMMITTED
The working tree also still carries the **previous** uncommitted feature
("dml-grading-correctness"). The two are mixed together and should be
separated before any commit. User has not authorised a commit.

## Next steps
1. Confirm TODO-027/028 landed; re-run full suite + smoke against a CLEAN DB.
2. Re-run the validator against the fixes (it last returned NOT SHIPPABLE).
3. Tear down `dualschema_verify_pg`.
4. Decide commit strategy (branch + split the two features).
