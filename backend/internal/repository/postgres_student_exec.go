package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ARCH-021: this file is the single chokepoint for executing UNTRUSTED
// (student-submitted) SQL.
//
// WHY THIS EXISTS: vendored pgx v5.9.2 conn.go:515 forces the SIMPLE query
// protocol whenever tx.Exec is called with no bind arguments — "// Always use
// simple protocol when there are no arguments." — and the simple protocol
// allows MULTIPLE statements per query string. A student submission such as
// "RESET ROLE; INSERT INTO <target_table> SELECT email, password_hash FROM
// app.system_users" therefore escapes the SET LOCAL ROLE exam_student sandbox
// straight back to the LOGIN role (superuser, per .env DB_USER) and the
// harvested rows return to the student in the post-image capture. Passing
// pgx.QueryExecModeExec as an argument to tx.Exec does NOT help: the simple-
// protocol override at conn.go:515 runs AFTER any exec-mode argument has
// already been consumed, so it is a no-op. The only enforcement that cannot be
// argued with in Go is the PostgreSQL server itself: ExecParams goes over the
// EXTENDED QUERY PROTOCOL, whose Parse message accepts at most one command
// (exec_parse_message() in postgres.c), so multi-statement input is rejected
// with SQLSTATE 42601 before it is ever planned, let alone executed.
//
// Any new code path that executes student-supplied SQL MUST go through
// execStudentStatement. tx.Exec must never be used for student SQL again — see
// evaluateDML / evaluateDMLPreview in postgres_evaluation.go for the two call
// sites this replaced.

// errMultipleStatements is the package-private sentinel recognised by
// studentSQLErrorFeedback (and, via errors.Is, by any future caller) to mean
// "the student's SQL contained more than one statement".
var errMultipleStatements = errors.New("student SQL contained more than one statement")

// MultiStatementFeedback is the student-facing message used when
// execStudentStatement rejects more than one statement. It is exported so
// services/validation_service.go can recognise it by exact string equality
// against this constant and let it survive the ARCH-018 dual-schema feedback
// scrub: unlike the executor's raw SQL error text, it carries no data from any
// schema and is produced before the statement ever reaches the planner, so it
// is not a data-dependent oracle over the hidden grading schema. It names no
// schema, table, role or SQLSTATE.
const MultiStatementFeedback = "Only one SQL statement can be submitted for this question. Please remove the extra statement(s) and submit a single statement."

// execStudentStatement executes exactly one SQL statement submitted by a
// student, via the PostgreSQL extended query protocol
// (tx.Conn().PgConn().ExecParams), bypassing tx.Exec's forced simple-protocol
// multi-statement behaviour described above. The return contract is IDENTICAL
// to tx.Exec: (pgconn.CommandTag, error), so every existing
// tag.RowsAffected()-based guard (e.g. ARCH-008's 0-row vacuity check) is a
// drop-in replacement, and session state already set on this transaction via
// tx.Exec (SET LOCAL search_path, SET LOCAL ROLE) applies unchanged, since
// ExecParams runs on the same underlying connection.
//
// rr.Close() is called exactly once, unconditionally, on every path: the
// pgconn contract requires the ResultReader be closed before the PgConn is
// usable again, and rr.Close() is also what drains any returned rows (so
// "INSERT ... RETURNING" works) and yields the CommandTag — there is nothing
// left to do after it returns, on either the success or the error path.
func execStudentStatement(ctx context.Context, tx pgx.Tx, sql string) (pgconn.CommandTag, error) {
	rr := tx.Conn().PgConn().ExecParams(ctx, sql, nil, nil, nil, nil)
	tag, err := rr.Close()
	if err == nil {
		return tag, nil
	}

	// Error classification below is DISPLAY-ONLY: it only chooses which
	// sentence the student sees. Security never depends on it — the statement
	// was already rejected by the server above regardless of whether this
	// classification matches. Measured discriminator against a live
	// postgres:15-alpine: multi-statement input -> Code 42601 / Routine
	// "exec_parse_message"; an ordinary single-statement syntax error ->
	// Code 42601 / Routine "scanner_yyerror"; a missing relation -> Code
	// 42P01 / Routine "parserOpenTable". pgErr.Routine is the C function name
	// and is never localised, so this check is locale-independent. This is
	// not the text-matching the user rejected: nothing about whether the SQL
	// runs is decided in Go.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42601" && pgErr.Routine == "exec_parse_message" {
		return tag, fmt.Errorf("%w", errMultipleStatements)
	}
	return tag, err
}

// studentSQLErrorFeedback turns an error returned by execStudentStatement
// into the student-facing feedback string: MultiStatementFeedback for the
// multi-statement rejection, otherwise byte-identical to the existing
// "SQL Error: %v" formatting used for every other student SQL error.
func studentSQLErrorFeedback(err error) string {
	if errors.Is(err, errMultipleStatements) {
		return MultiStatementFeedback
	}
	return fmt.Sprintf("SQL Error: %v", err)
}
