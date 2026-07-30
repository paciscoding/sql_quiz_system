package repository

import (
	"context"
	"testing"
)

// TestSchemaRebuildLockKey_Stable pins schemaRebuildLockKey so it can never
// drift silently. The key MUST be identical on the rebuild side and the eval
// side (both derive it from the same function); if a refactor changed the
// derivation on only one side the two would stop mutually excluding and the
// race this whole feature closes would come back undetected. No database
// needed.
func TestSchemaRebuildLockKey_Stable(t *testing.T) {
	const schema = "practice_cat_1"
	key := schemaRebuildLockKey(schema)
	if key == 0 {
		t.Fatal("schemaRebuildLockKey(practice_cat_1) must be non-zero")
	}
	if got := fnv1a64Signed("sql_quiz/schema-rebuild:" + schema); got != key {
		t.Fatalf("schemaRebuildLockKey drifted: got %d want %d", key, got)
	}
	// acquireSchemaEvalLock has no schema-name filter: it is keyed per-schema
	// and every distinct schema gets a distinct, deterministic key.
	if schemaRebuildLockKey("sandbox_exam_9") == schemaRebuildLockKey("practice_cat_1") {
		t.Fatal("distinct schemas must not collide on the same lock key")
	}
	// acquireSchemaEvalLock is a true no-op only for an empty schema string —
	// it must not touch the (here nil) tx in that case.
	if err := acquireSchemaEvalLock(context.Background(), nil, ""); err != nil {
		t.Fatalf("empty schema must skip the lock, got %v", err)
	}
}

// The rebuild-vs-in-flight-eval race this lock closes is exercised end to end
// against a real category schema by
// TestDMLGrading_ARCH013_ImportReplaceBlocksOnInFlightEval
// (postgres_dml_grading_integration_test.go). ResetPracticeSandbox / the
// sandbox_practice schema had no real caller — no controller resolved it as an
// evaluation schema — so both were removed rather than kept as dead code with
// a race test propping up its illusion of use.
