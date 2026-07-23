package repository

// ARCH-025: the restricted execution pool for student-submitted SQL.
//
// WHY THIS EXISTS: `pool` (postgres.go) connects with whatever LOGIN role
// DATABASE_URL names, and in production that is the Postgres superuser
// (.env DB_USER=postgres). `SET LOCAL ROLE exam_student*` is not a security
// boundary on that connection, because PostgreSQL evaluates SET ROLE
// against session_user, not current_user: a single student statement can
// undo it outright. Measured on 15.18 against a superuser session_user:
//
//	SET ROLE postgres;                                             -- succeeds
//	SELECT set_config('role', 'postgres', false);                  -- succeeds
//	DO $$ BEGIN RESET ROLE; INSERT INTO app.system_users ...; END $$;  -- succeeds
//	COPY (SELECT 1) TO PROGRAM 'id > /tmp/pwned';                  -- succeeds (shell exec)
//
// Even without any escape at all, a superuser session_user also means
// `RESET ROLE` alone (no injection needed) lands back on a role that owns
// every application table, so a student can read app.system_users (password
// hashes) and every correct_sql.
//
// THE FIX: run every student statement on a SEPARATE pgxpool.Pool whose
// LOGIN role is `quiz_exec` — NOSUPERUSER, NOCREATEROLE, NOINHERIT, owns
// nothing, and has no privileges on the app schema (see
// scripts/deprivilege_app_db_user.sql, which provisions the role in a real
// deployment). On that connection every one of the four bypasses above
// fails outright: `SET ROLE postgres` is rejected because quiz_exec is not a
// member of role postgres, `COPY ... TO PROGRAM` requires superuser or
// pg_execute_server_program membership (neither held), and `RESET ROLE` /
// an aborted transaction land back on quiz_exec itself, which cannot read
// app.* at all. `SET LOCAL ROLE exam_student*` on top of this connection is
// kept unchanged (postgres_evaluation.go) as defence in depth and to
// preserve the existing read-only/read-write role split — it is simply no
// longer the ONLY thing standing between a student and the superuser.
//
// KNOWN RESIDUAL (accepted deliberately for this interim ship, see the brief
// / scripts/deprivilege_app_db_user.sql): quiz_exec is a member of all four
// exam_student* roles, so a student can `SET ROLE` ACROSS classes (e.g. from
// a preview role into the real grading role), which defeats the dual-schema
// decoy isolation. The deferred ARCH-022 design replaces quiz_exec with four
// per-class LOGIN roles to close that. Do not attempt to fix it here.
//
// NO FALLBACK, EVER: if the restricted pool cannot be constructed (missing
// password, role missing, connection refused, ...), every caller below
// returns a hard error. Nothing in this file ever falls back to executing
// student SQL against `pool`.

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StudentDBPasswordEnv is the environment variable carrying the LOGIN
// password for the `quiz_exec` role. It is deliberately NOT generated or
// rotated at boot: the application's own LOGIN role runs NOCREATEROLE in
// production (scripts/deprivilege_app_db_user.sql), so it has no privilege
// to ALTER another role's password even if it wanted to. The operator sets
// this once, out of band, matching quiz_exec's password in Postgres.
const StudentDBPasswordEnv = "STUDENT_DB_PASSWORD"

// studentPoolUser is the LOGIN role every student statement executes as.
// It owns nothing and has no privileges on the app schema — see the file
// header and scripts/deprivilege_app_db_user.sql.
const studentPoolUser = "quiz_exec"

// Sizing for the student-SQL pool. Deliberately smaller and with no warm
// minimum: student evaluations are comparatively rare, short (30s hard
// timeout, see postgres_evaluation.go) and, for DML, already serialised by
// the single-worker SubmissionQueue — there is no high-concurrency case that
// needs this pool sized like the main application pool.
const (
	studentPoolMaxConns int32 = 8
	studentPoolMinConns int32 = 0
)

// InitStudentPool eagerly creates and pings the restricted student-SQL pool.
// cmd/main.go calls this once at boot, right after CreateRestrictedUser, so
// a missing STUDENT_DB_PASSWORD or an unreachable/misconfigured quiz_exec
// role fails the deployment immediately (the caller is expected to
// log.Fatalf on a non-nil error, matching the existing ID_SALT precedent)
// rather than surfacing as a mysterious first-submission failure — or worse,
// silently succeeding some other way.
//
// It is safe to call more than once (idempotent via ensureStudentPool's
// sync.Once) and safe to never call at all from a given process: every
// evaluate* call site in postgres_evaluation.go also calls
// ensureStudentPool, so a test binary that never wires InitStudentPool still
// gets a correctly-restricted pool on first use, constructed exactly the
// same way, from the same required environment variable.
func (r *PostgresRepository) InitStudentPool(ctx context.Context) error {
	_, err := r.ensureStudentPool(ctx)
	return err
}

// ensureStudentPool returns the restricted student-SQL pool, creating it on
// first call. The pool (or the construction error) is cached for the
// lifetime of the repository: pgxpool.Pool is safe for concurrent use, and a
// construction failure is not transient (it is always a configuration
// problem — missing password or a role/grant that does not exist), so
// retrying per-call would only mask the failure behind repeated identical
// errors instead of failing loudly once.
func (r *PostgresRepository) ensureStudentPool(ctx context.Context) (*pgxpool.Pool, error) {
	r.studentPoolOnce.Do(func() {
		r.studentPool, r.studentPoolErr = newStudentPool(ctx, r.pool)
	})
	return r.studentPool, r.studentPoolErr
}

// newStudentPool builds the restricted pool from the existing privileged
// pool's own config, overriding ONLY the LOGIN user and password so host,
// port, dbname and sslmode are inherited unchanged — no second DSN is
// introduced anywhere in the codebase, and the restricted pool always talks
// to the exact same database the rest of the application does.
func newStudentPool(ctx context.Context, base *pgxpool.Pool) (*pgxpool.Pool, error) {
	password := os.Getenv(StudentDBPasswordEnv)
	if password == "" {
		return nil, fmt.Errorf(
			"%s is required: student SQL must execute under the restricted %q login role, "+
				"never the privileged application connection (ARCH-025)",
			StudentDBPasswordEnv, studentPoolUser)
	}

	config := base.Config().Copy()
	config.ConnConfig.User = studentPoolUser
	config.ConnConfig.Password = password
	config.MaxConns = studentPoolMaxConns
	config.MinConns = studentPoolMinConns

	// Override the inherited DefaultQueryExecMode (base sets
	// QueryExecModeCacheStatement, see postgres.go). QueryExecModeDescribeExec
	// still runs over the EXTENDED query protocol — the Parse message still
	// accepts at most one command, so this does not reopen the multi-statement
	// bypass ARCH-021 closed (execStudentStatement / evaluateQuery's tx.Query
	// keep the exact same protection they had before). What it drops is
	// PER-SQL-TEXT statement/plan caching: every evaluation on this pool runs
	// student SQL such as the literal text "SELECT * FROM t" against a
	// DIFFERENT table shape each time (a fresh sandbox schema selected only
	// via SET LOCAL search_path), and this pool is small (8 conns) and used
	// for nothing else, so connection reuse across unrelated evaluations is
	// frequent. QueryExecModeCacheStatement would key its cache on the raw SQL
	// text alone and can hand back a plan prepared against a PRIOR student's
	// differently-shaped table, which Postgres then rejects at execution time
	// with "cached plan must not change result type" - a correctness bug, not
	// a security one, but one this pool's access pattern provokes far more
	// often than the general-purpose pool ever did. DescribeExec re-describes
	// the statement fresh against the CURRENT search_path on every call, which
	// the pgx documentation states is "safe even when the database schema is
	// modified concurrently" - exactly this pool's situation on every request.
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to create student SQL pool as %q: %w", studentPoolUser, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to connect student SQL pool as %q (check %s and that the role exists with matching grants): %w",
			studentPoolUser, StudentDBPasswordEnv, err)
	}
	return pool, nil
}
