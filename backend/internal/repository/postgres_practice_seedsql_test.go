package repository

// Regression-tester coverage for lecturer-authored seed rows on the practice
// path (2026-08-08-lecturer-seed-rows-design.md, ARCH-045/046/047), Task 4.
// Reuses the DB-test bootstrap (openTestRepo), the category/user fixture
// helper (mkPracticeCategory, postgres_practice_title_test.go) and the row
// counting / seed-absence helpers (countSchemaTableRows,
// assertSeedRowAbsentBySSN, dml_autoseed_schema_isolation_test.go) already
// established for the ARCH-037/038 derived auto-seed tests.
//
// Every subtest uses its own table name, matching the precedent set by
// dml_autoseed_schema_isolation_test.go: r.pool runs with
// pgx.QueryExecModeCacheStatement, whose plan cache key is the raw SQL text
// only, so two categories sharing a table name with a different shape can
// collide on "cached plan must not change result type" (SQLSTATE 0A000).
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/ -run TestPracticeSeedSQL -p 1 -v

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkSeedSQLFixture creates a category with an owned schema and a two-row
// employee-shaped table, returning the category id, the owning user id, and
// the resolved schema name.
func mkSeedSQLFixture(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix, tbl string) (catID, uid int, schema string) {
	t.Helper()
	catID, uid = mkPracticeCategory(t, repo, ctx, namePrefix)
	importSQL := fmt.Sprintf(`
		CREATE TABLE %s (ssn TEXT PRIMARY KEY, name TEXT, salary INTEGER, dno INTEGER);
		INSERT INTO %s (ssn, name, salary, dno) VALUES
			('111111111', 'Alice', 5000, 1),
			('222222222', 'Bob', 6000, 2);`, tbl, tbl)
	if err := repo.ImportSchemaIntoCategory(ctx, catID, importSQL, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	var err error
	schema, err = repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	return catID, uid, schema
}

// TestPracticeSeedSQL_CaptureSucceeds_AndAutoSeedStaysNil is case 1: a DELETE
// question whose target row exists ONLY in seed_sql captures successfully,
// and derivation is never attempted (ARCH-045 precedence).
func TestPracticeSeedSQL_CaptureSucceeds_AndAutoSeedStaysNil(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	const tbl = "employee_seedsql_ok"
	catID, uid, schema := mkSeedSQLFixture(t, repo, ctx, "seedsql_ok", tbl)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name, salary, dno) VALUES ('999999999', 'Seeded', 1000, 1)", tbl),
		Requirement: "delete the seeded employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected expected_result_status=captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if saved.ExpectedRowsAffected == nil || *saved.ExpectedRowsAffected != 1 {
		t.Fatalf("expected expected_rows_affected=1, got %v", saved.ExpectedRowsAffected)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("ARCH-045: auto_seed must stay NULL when seed_sql is used, got %s", saved.AutoSeed)
	}
	if saved.SeedSQL == "" {
		t.Fatalf("expected seed_sql to be persisted, got empty")
	}

	// CASE 2 — SCHEMA ISOLATION (the core guarantee): the real category
	// schema's row count is unchanged after capture, and no seeded row
	// remains.
	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("capture must roll back its seed: row count changed from %d to %d", baseline, got)
	}
	assertSeedRowAbsentBySSN(t, repo, ctx, schema, tbl, "999999999")
}

// TestPracticeSeedSQL_ConstraintViolation_FailsWithSeedInsertMessage is case
// 3: a seed_sql that violates a constraint (duplicate primary key) saves
// failed with a capture_error starting with "Seed INSERT failed: ".
func TestPracticeSeedSQL_ConstraintViolation_FailsWithSeedInsertMessage(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	const tbl = "employee_seedsql_dup"
	catID, uid, schema := mkSeedSQLFixture(t, repo, ctx, "seedsql_dup", tbl)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '111111111'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		// ssn '111111111' already exists in the baseline fixture rows, so this
		// seed INSERT violates the primary key.
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name, salary, dno) VALUES ('111111111', 'Dup', 1, 1)", tbl),
		Requirement: "delete alice",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected expected_result_status=failed, got %q", saved.ExpectedResultStatus)
	}
	wantPrefix := "Seed INSERT failed: "
	if !strings.HasPrefix(saved.CaptureError, wantPrefix) {
		t.Fatalf("expected capture_error to start with %q, got %q", wantPrefix, saved.CaptureError)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to stay NULL on a failed seed insert, got %s", saved.AutoSeed)
	}

	// Schema isolation must hold on the failure path too: the failed seed
	// INSERT rolled back to its savepoint, leaving the real schema untouched.
	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("a failed seed insert must not touch the real schema: baseline=%d got=%d", baseline, got)
	}
}

// TestPracticeSeedSQL_SeededRowNotMatchedByCorrectSQL_FailsWithZeroRowsMessage
// is case 4: a seed_sql that inserts a row the correct_sql's WHERE clause
// does NOT match saves failed with the ARCH-048 zero-rows message.
func TestPracticeSeedSQL_SeededRowNotMatchedByCorrectSQL_FailsWithZeroRowsMessage(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	const tbl = "employee_seedsql_mismatch"
	catID, uid, schema := mkSeedSQLFixture(t, repo, ctx, "seedsql_mismatch", tbl)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		// The seed inserts ssn='888888888' but the correct SQL deletes a
		// different, non-existent ssn — the seed succeeds but correct_sql
		// still affects 0 rows.
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, name, salary, dno) VALUES ('888888888', 'Seeded', 1000, 1)", tbl),
		Requirement: "delete the seeded employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected expected_result_status=failed, got %q", saved.ExpectedResultStatus)
	}
	wantErr := "Your seed rows were inserted, but the correct SQL still affected 0 rows. Check the WHERE clause of the correct SQL against the values you seeded."
	if saved.CaptureError != wantErr {
		t.Fatalf("capture_error mismatch:\n got:  %q\n want: %q", saved.CaptureError, wantErr)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to stay NULL, got %s", saved.AutoSeed)
	}

	// The seed INSERT itself committed inside the capture transaction, which
	// is entirely rolled back regardless of the later 0-row failure.
	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("capture must roll back its seed even on the 0-rows failure path: baseline=%d got=%d", baseline, got)
	}
	assertSeedRowAbsentBySSN(t, repo, ctx, schema, tbl, "888888888")
}

// TestPracticeSeedSQL_EmptySeedStillDerives is case 5: a REGRESSION check —
// a DELETE question with an empty seed_sql whose row is missing still
// derives via the existing ARCH-037/038 auto-seed path, byte-identical to
// before this feature existed.
func TestPracticeSeedSQL_EmptySeedStillDerives(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	const tbl = "employee_seedsql_derive"
	catID, uid, schema := mkSeedSQLFixture(t, repo, ctx, "seedsql_derive", tbl)
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	baseline := countSchemaTableRows(t, repo, ctx, schema, tbl)

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     "",
		Requirement: "delete a specific employee",
		Points:      5,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected expected_result_status=captured (derived rescue), got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if len(saved.AutoSeed) == 0 {
		t.Fatalf("expected a non-nil derived auto_seed plan when seed_sql is empty and the model answer affects 0 rows")
	}
	if saved.SeedSQL != "" {
		t.Fatalf("expected seed_sql to stay empty, got %q", saved.SeedSQL)
	}

	if got := countSchemaTableRows(t, repo, ctx, schema, tbl); got != baseline {
		t.Fatalf("capture must roll back its derived seed: baseline=%d got=%d", baseline, got)
	}
	assertSeedRowAbsentBySSN(t, repo, ctx, schema, tbl, "999999999")
}
