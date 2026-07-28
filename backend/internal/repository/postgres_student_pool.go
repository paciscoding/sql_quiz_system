package repository

// ARCH-022: the restricted execution pools for student-submitted SQL.
//
// WHY THIS EXISTS: `pool` (postgres.go) connects with whatever LOGIN role
// DATABASE_URL names, and in production that is the Postgres superuser
// (.env DB_USER=postgres). `SET LOCAL ROLE exam_student*` is not a security
// boundary on that connection, because a single student statement running as a
// superuser session_user can `RESET ROLE` / `SET ROLE postgres` back to full
// privilege and read app.system_users (password hashes) and every correct_sql,
// or reach `COPY ... TO PROGRAM` (shell exec). Every student statement must
// therefore run on a connection whose LOGIN role is a non-superuser that owns
// nothing.
//
// TOPOLOGY — FOUR restricted LOGIN roles, one per privilege class, NOT one
// shared role. Each login role is a member of EXACTLY ONE sandbox role:
//
//	quiz_exec_ro       -> exam_student_readonly           (graded QUERY; readonly QUERY preview)
//	quiz_exec_rw       -> exam_student                    (graded DML;   DML preview)
//	quiz_exec_prev_ro  -> exam_student_preview_readonly   (decoy QUERY preview)
//	quiz_exec_prev_rw  -> exam_student_preview            (decoy DML preview)
//
// A single login role that was a member of all four (the retired ARCH-025
// `quiz_exec`) MEASURABLY re-opened two boundaries: it let a preview role
// `SET ROLE` across into the real grading role (defeating the ARCH-020 decoy
// isolation) and let a readonly class `SET ROLE` into the read-write class.
// One membership per login role makes both `permission denied to set role`.
//
// NOINHERIT is load-bearing: the login role by itself holds nothing, so an
// idle pooled connection carries zero privilege and every privilege use is an
// explicit, transaction-scoped `SET LOCAL ROLE`. Forgetting the SET LOCAL ROLE
// fails LOUDLY (permission denied) instead of silently running privileged.
//
// NO FALLBACK, EVER: poolForRole returns a hard error for an unknown or
// unprovisioned role and NEVER returns r.pool. A silent fallback to the
// privileged application connection would restore the exact escape this file
// exists to close.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The four restricted LOGIN roles student SQL authenticates as. See the file
// header for the one-membership-each topology and why four (not one).
const (
	loginRoleExamStudentReadonly        = "quiz_exec_ro"
	loginRoleExamStudent                = "quiz_exec_rw"
	loginRoleExamStudentPreviewReadonly = "quiz_exec_prev_ro"
	loginRoleExamStudentPreview         = "quiz_exec_prev_rw"
)

// studentLoginRoles maps each SANDBOX role name (the role the evaluator SET
// LOCAL ROLEs into, and the key poolForRole is called with) to the dedicated
// LOGIN role whose sole membership is that one sandbox role. studentPools is
// keyed by the same sandbox-role names.
var studentLoginRoles = map[string]string{
	"exam_student_readonly":         loginRoleExamStudentReadonly,
	"exam_student":                  loginRoleExamStudent,
	"exam_student_preview_readonly": loginRoleExamStudentPreviewReadonly,
	"exam_student_preview":          loginRoleExamStudentPreview,
}

// studentPoolMaxConns sizes each class's pool. exam_student_readonly carries
// graded SELECT (the hot path) so it is the largest; the DML pool is small
// because graded DML is serialised by the single-worker SubmissionQueue; the
// two decoy-preview pools are idle in most deployments. Worst case is
// 20 (privileged r.pool) + 20 (these four) = 40 backends, well within
// postgres:15-alpine's default max_connections of 100.
var studentPoolMaxConns = map[string]int32{
	"exam_student_readonly":         8,
	"exam_student":                  4,
	"exam_student_preview":          4,
	"exam_student_preview_readonly": 4,
}

// provisionStudentLoginRoles creates (if absent) and re-keys the four LOGIN
// roles, granting each its single sandbox-role membership, and returns the
// freshly generated password shared by all four.
//
// One password for all four is safe: the password is NOT the boundary between
// privilege classes — WHICH ROLE YOU AUTHENTICATE AS is — and the db service
// publishes no port (docker-compose.yml has no `ports:` under `db`), so 5432 is
// reachable only from the compose network. The password is generated fresh
// every boot (crypto/rand, hex-encoded) and never logged or written to disk.
//
// HEX IS DELIBERATE: CREATE/ALTER ROLE cannot take a bind parameter, so the
// password is interpolated as a SQL literal; a 64-char hex alphabet contains no
// quote, backslash or dollar sign, so there is nothing to escape and no
// injection surface.
//
// The CREATE sets the superuser-gated attributes (NOSUPERUSER, NOREPLICATION,
// NOBYPASSRLS) ONCE; the per-boot ALTER MUST NOT name them, because a
// non-superuser role holding CREATEROLE is refused those attributes on ALTER
// but accepts exactly this form. So boot provisioning works whenever the app
// connection holds CREATEROLE — which is true today (DB_USER=postgres is a
// superuser).
//
// OPEN QUESTION (posture-gated, does NOT affect today's deploy): both the
// CREATE and the password-rotating ALTER here require the app to hold CREATEROLE
// (altering another role's password needs CREATEROLE or superuser). But
// scripts/deprivilege_app_db_user.sql deliberately makes the app NOCREATEROLE to
// close a separate self-grant RCE. Those two are incompatible: once that script
// is applied, this boot provisioning would fail (log.Fatal). Generate-at-boot
// therefore only holds while the app is CREATEROLE/superuser. Moving to the
// NOCREATEROLE posture requires a design choice — provision the four roles +
// their password out of band (that script) and read it from env (the retired
// ARCH-025 approach), OR keep the app CREATEROLE. Do not apply that script with
// this code without resolving it first.
func (r *PostgresRepository) provisionStudentLoginRoles(ctx context.Context) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate student login password: %w", err)
	}
	password := hex.EncodeToString(buf) // 64 hex chars, injection-free as a SQL literal

	// Deterministic order so a boot's role work is reproducible.
	pairs := []struct{ sandbox, login string }{
		{"exam_student_readonly", loginRoleExamStudentReadonly},
		{"exam_student", loginRoleExamStudent},
		{"exam_student_preview_readonly", loginRoleExamStudentPreviewReadonly},
		{"exam_student_preview", loginRoleExamStudentPreview},
	}
	for _, p := range pairs {
		createStmt := fmt.Sprintf(
			"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname='%s') "+
				"THEN CREATE ROLE %s LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS; END IF; END $$",
			p.login, p.login)
		if _, err := r.pool.Exec(ctx, createStmt); err != nil {
			return "", fmt.Errorf("create login role %s: %w", p.login, err)
		}
		// Rotate the password every boot. Attribute list is deliberately the
		// CREATEROLE-safe subset (no NOSUPERUSER/NOREPLICATION/NOBYPASSRLS).
		alterStmt := fmt.Sprintf(
			"ALTER ROLE %s LOGIN NOINHERIT NOCREATEDB NOCREATEROLE PASSWORD '%s'",
			p.login, password)
		if _, err := r.pool.Exec(ctx, alterStmt); err != nil {
			return "", fmt.Errorf("set password on login role %s: %w", p.login, err)
		}
		// Exactly one membership: this login role can SET ROLE into its own
		// class and nothing else. Idempotent.
		if _, err := r.pool.Exec(ctx, fmt.Sprintf("GRANT %s TO %s", p.sandbox, p.login)); err != nil {
			return "", fmt.Errorf("grant %s to %s: %w", p.sandbox, p.login, err)
		}
	}
	return password, nil
}

// InitStudentPools provisions the four LOGIN roles and builds one restricted
// pgxpool per privilege class, keyed by sandbox-role name. cmd/main.go calls
// this once at boot immediately after CreateRestrictedUser (which creates the
// sandbox roles the memberships reference) and is expected to log.Fatalf on a
// non-nil error, matching the ID_SALT precedent — there is NO degraded mode and
// NEVER a fallback to the privileged pool.
func (r *PostgresRepository) InitStudentPools(ctx context.Context) error {
	password, err := r.provisionStudentLoginRoles(ctx)
	if err != nil {
		return fmt.Errorf("provision student login roles: %w", err)
	}

	pools := make(map[string]*pgxpool.Pool, len(studentLoginRoles))
	closeAll := func() {
		for _, p := range pools {
			p.Close()
		}
	}
	for sandbox, login := range studentLoginRoles {
		// Derive from the privileged pool's config so host/port/dbname/sslmode
		// are inherited unchanged — no second DSN anywhere. Override ONLY the
		// login identity, sizing, and the exec mode.
		config := r.pool.Config().Copy()
		config.ConnConfig.User = login
		config.ConnConfig.Password = password
		config.MaxConns = studentPoolMaxConns[sandbox]
		config.MinConns = 0
		config.MaxConnIdleTime = 5 * time.Minute

		// ARCH-025 finding, PRESERVED (do not revert to CacheStatement): student
		// SQL runs the SAME text against a DIFFERENT table shape each eval (a
		// fresh schema selected via SET LOCAL search_path), so
		// QueryExecModeCacheStatement can hand back a plan prepared against a
		// prior student's differently-shaped table and Postgres rejects it with
		// "cached plan must not change result type" — a correctness bug this
		// pool's access pattern provokes constantly. DescribeExec re-describes on
		// every call and is safe under concurrent schema change. This overrides
		// the CacheStatement pin inherited from NewPostgresRepository.
		config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec

		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			closeAll()
			return fmt.Errorf("create student pool for %s (login %s): %w", sandbox, login, err)
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			closeAll()
			return fmt.Errorf("ping student pool for %s (login %s): %w", sandbox, login, err)
		}
		pools[sandbox] = pool
	}
	r.studentPools = pools
	return nil
}

// poolForRole returns the restricted pool whose LOGIN role is a member of the
// given sandbox role (the same role the caller will SET LOCAL ROLE into).
//
// IT MUST NEVER RETURN r.pool. A silent fallback to the privileged application
// connection would restore the exact superuser escape this design closes; an
// unknown or unprovisioned role is a hard error instead.
func (r *PostgresRepository) poolForRole(role string) (*pgxpool.Pool, error) {
	if r.studentPools == nil {
		return nil, fmt.Errorf("student SQL pools not initialised (InitStudentPools was not called)")
	}
	pool, ok := r.studentPools[role]
	if !ok || pool == nil {
		return nil, fmt.Errorf("no restricted student pool for role %q", role)
	}
	return pool, nil
}
