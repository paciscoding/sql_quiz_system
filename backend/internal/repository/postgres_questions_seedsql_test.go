package repository

// Regression-tester (step [7]) coverage for the lecturer-authored seed rows
// feature (2026-08-08-lecturer-seed-rows-design.md, ARCH-043..052), Task 5:
// the exam-question capture/persistence path in postgres_questions.go.
// Mirrors the five cases the plan lists for Task 4's practice-side coverage
// (postgres_practice_seedsql_test.go), against app.questions instead of
// app.practice_questions.
//
// Uses openTestRepo(t) and the mkExamAndDeleteTable exam+table fixture from
// postgres_questions_autoseed_test.go — each subtest calls it independently
// so every subtest gets its own uniquely-named table (see that file's header
// comment on the pgx cached-plan / SQLSTATE 0A000 hazard of two DML captures
// sharing a table name on the pooled connection).
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run TestExamSeedSQL -v -p 1

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestExamSeedSQL_RowOnlyInSeed_CapturesAndAutoSeedStaysNil is case 1: a
// DELETE question whose row exists ONLY in seed_sql captures successfully,
// with expected_rows_affected == 1 and auto_seed left NULL (ARCH-045:
// derivation is never attempted when seed_sql is non-empty).
func TestExamSeedSQL_RowOnlyInSeed_CapturesAndAutoSeedStaysNil(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "seedsql_capture")

	baseline := countSchemaTableRows(t, repo, ctx, "public", tbl)

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, salary) VALUES ('999999999', 1)", tbl),
		Points:      5,
		Requirement: "delete the seeded employee",
		CreatedBy:   models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if saved.ExpectedRowsAffected == nil || *saved.ExpectedRowsAffected != 1 {
		t.Fatalf("expected expected_rows_affected=1, got %v", saved.ExpectedRowsAffected)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("ARCH-045: expected auto_seed to stay NULL when seed_sql is set, got %s", saved.AutoSeed)
	}
	if saved.SeedSQL == "" {
		t.Fatalf("expected seed_sql to persist")
	}

	// CASE 2 — SCHEMA ISOLATION, the core guarantee: the real table's row
	// count is unchanged from baseline, and no row with the seeded ssn exists.
	if got := countSchemaTableRows(t, repo, ctx, "public", tbl); got != baseline {
		t.Fatalf("capture must roll back its seed: row count changed from %d to %d", baseline, got)
	}
	var n int
	if err := repo.pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM public.%s WHERE ssn = '999999999'", tbl)).Scan(&n); err != nil {
		t.Fatalf("check seed row absence: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected the seeded row to be absent from the real schema, found %d", n)
	}
}

// TestExamSeedSQL_ConstraintViolation_FailsWithSeedInsertPrefix is case 3: a
// seed_sql that violates a constraint (duplicate primary key against the
// fixture's pre-existing row) saves with expected_result_status == "failed"
// and a capture_error starting with "Seed INSERT failed: ".
func TestExamSeedSQL_ConstraintViolation_FailsWithSeedInsertPrefix(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "seedsql_duppk")

	baseline := countSchemaTableRows(t, repo, ctx, "public", tbl)

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '111111111'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		// ssn='111111111' already exists in the fixture (mkExamAndDeleteTable),
		// so re-inserting it violates the primary key.
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, salary) VALUES ('111111111', 999)", tbl),
		Points:      5,
		Requirement: "duplicate seed row",
		CreatedBy:   models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected failed, got %q", saved.ExpectedResultStatus)
	}
	wantPrefix := "Seed INSERT failed: "
	if !strings.HasPrefix(saved.CaptureError, wantPrefix) {
		t.Fatalf("expected capture_error to start with %q, got %q", wantPrefix, saved.CaptureError)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to stay NULL, got %s", saved.AutoSeed)
	}
	if got := countSchemaTableRows(t, repo, ctx, "public", tbl); got != baseline {
		t.Fatalf("a failed seed insert must not touch the real schema: baseline=%d got=%d", baseline, got)
	}
}

// TestExamSeedSQL_CorrectSQLStillAffectsZeroRows_FailsWithGuidance is case 4:
// a seed_sql that inserts a row the correct_sql's WHERE clause does NOT match
// saves failed with the ARCH-048 guidance message.
func TestExamSeedSQL_CorrectSQLStillAffectsZeroRows_FailsWithGuidance(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "seedsql_mismatch")

	baseline := countSchemaTableRows(t, repo, ctx, "public", tbl)

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		// Seeds a DIFFERENT ssn than the one correct_sql deletes.
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (ssn, salary) VALUES ('777777777', 1)", tbl),
		Points:      5,
		Requirement: "mismatched seed",
		CreatedBy:   models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "failed" {
		t.Fatalf("expected failed, got %q", saved.ExpectedResultStatus)
	}
	wantMsg := "Your seed rows were inserted, but the correct SQL still affected 0 rows. Check the WHERE clause of the correct SQL against the values you seeded."
	if saved.CaptureError != wantMsg {
		t.Fatalf("capture_error = %q, want %q", saved.CaptureError, wantMsg)
	}
	if len(saved.AutoSeed) != 0 {
		t.Fatalf("expected auto_seed to stay NULL, got %s", saved.AutoSeed)
	}
	// The seed insert itself succeeded before the mismatch was discovered, so
	// this proves the outer capture transaction still rolled back completely.
	if got := countSchemaTableRows(t, repo, ctx, "public", tbl); got != baseline {
		t.Fatalf("a failed capture must not leave the seeded row behind: baseline=%d got=%d", baseline, got)
	}
}

// TestExamSeedSQL_EmptySeed_StillDerives is case 5, a regression check: a
// DELETE question with an empty seed_sql whose row is missing still derives
// via the existing ARCH-037/038 auto-seed rescue — auto_seed IS NOT NULL and
// status == "captured". Proves the seed_sql precedence branch did not disturb
// the pre-existing derivation fallback.
func TestExamSeedSQL_EmptySeed_StillDerives(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndDeleteTable(t, repo, ctx, "seedsql_regression")

	eid := models.ExamID(examID)
	newID, err := repo.CreateQuestion(ctx, models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("DELETE FROM %s WHERE ssn = '999999999'", tbl),
		Type:        "DML",
		DMLKind:     "DELETE",
		SeedSQL:     "", // no lecturer seed
		Points:      5,
		Requirement: "still derives without a lecturer seed",
		CreatedBy:   models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, newID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("expected captured, got %q (capture_error=%q)", saved.ExpectedResultStatus, saved.CaptureError)
	}
	if len(saved.AutoSeed) == 0 {
		t.Fatalf("expected the derivation fallback to still produce a non-nil auto_seed")
	}
	if saved.SeedSQL != "" {
		t.Fatalf("expected seed_sql to read back empty, got %q", saved.SeedSQL)
	}
}
