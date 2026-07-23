package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// schemaRebuildLockKey derives the Postgres advisory-lock key that guards a
// given schema against being dropped/rebuilt while an evaluation is running
// against it. Every schema gets its own key (unlike the retired
// practiceResetLockKey, which only ever covered sandbox_practice), so
// evaluations against different schemas never contend with each other.
func schemaRebuildLockKey(schema string) int64 {
	return fnv1a64Signed("sql_quiz/schema-rebuild:" + schema)
}

// acquireSchemaEvalLock takes schemaRebuildLockKey(schema) SHARED
// (pg_advisory_xact_lock_shared) on tx, for ANY non-empty schema — unlike the
// retired acquirePracticeEvalLock, there is no schema-name filter: every
// evaluation path (SELECT and DML, exam and practice, live and preview) takes
// this lock so a schema can never be dropped/rebuilt out from under an
// in-flight evaluation. The lock is held for the life of the transaction and
// releases automatically when it ends. Many shared holders run concurrently;
// only the exclusive rebuild lock (acquireSchemaRebuildLockTx) waits for them.
func acquireSchemaEvalLock(ctx context.Context, tx pgx.Tx, schema string) error {
	if schema == "" {
		return nil
	}
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1)", schemaRebuildLockKey(schema))
	return err
}

// acquireSchemaRebuildLockTx takes schemaRebuildLockKey(schema) EXCLUSIVELY
// (pg_advisory_xact_lock) on tx. Any operation that is about to drop or
// destructively rebuild a schema must take this before doing so: it blocks
// until every in-flight evaluation holding the shared lock (see
// acquireSchemaEvalLock) has finished, and blocks new evaluations from
// starting until the transaction holding the exclusive lock ends.
func acquireSchemaRebuildLockTx(ctx context.Context, tx pgx.Tx, schema string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaRebuildLockKey(schema))
	return err
}
