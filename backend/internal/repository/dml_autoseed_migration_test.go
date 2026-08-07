package repository

// Regression-tester (step [7]) coverage for the dml-autoseed feature, area
// #7: migration idempotency for the new `auto_seed JSONB` column (ARCH-040,
// postgres_schema.go's extraMigrations: "ALTER TABLE app.questions ADD
// COLUMN IF NOT EXISTS auto_seed JSONB" / same for app.practice_questions).
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run AutoSeedColumn -v -p 1

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestAutoSeedColumn_MigrationIsIdempotent_SecondRunNoError proves that
// re-running InitializeSystemTables (which openTestRepo already ran once
// during bootstrap) a second time adds nothing and does not error — the
// ADD COLUMN IF NOT EXISTS auto_seed clauses must be true no-ops on a
// database that already has the column.
func TestAutoSeedColumn_MigrationIsIdempotent_SecondRunNoError(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("second InitializeSystemTables run must be a no-op, got error: %v", err)
	}

	// Confirm the column genuinely exists (not just that the DDL didn't error)
	// and confirm the pre-existing-rows contract: a freshly created row reads
	// back auto_seed = NULL when nothing rescued it.
	var dataType string
	if err := repo.pool.QueryRow(ctx,
		`SELECT data_type FROM information_schema.columns WHERE table_schema='app' AND table_name='questions' AND column_name='auto_seed'`).
		Scan(&dataType); err != nil {
		t.Fatalf("expected app.questions.auto_seed to exist after migration: %v", err)
	}
	if dataType != "jsonb" {
		t.Fatalf("expected app.questions.auto_seed to be jsonb, got %q", dataType)
	}
	if err := repo.pool.QueryRow(ctx,
		`SELECT data_type FROM information_schema.columns WHERE table_schema='app' AND table_name='practice_questions' AND column_name='auto_seed'`).
		Scan(&dataType); err != nil {
		t.Fatalf("expected app.practice_questions.auto_seed to exist after migration: %v", err)
	}
	if dataType != "jsonb" {
		t.Fatalf("expected app.practice_questions.auto_seed to be jsonb, got %q", dataType)
	}
}

// TestAutoSeedColumn_PreExistingRow_ReadsBackNull proves a question row that
// was never rescued (a plain QUERY question, which never invokes derivation
// at all) reads back auto_seed = NULL, both immediately after creation and
// after the idempotent migration re-runs.
func TestAutoSeedColumn_PreExistingRow_ReadsBackNull(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("automig_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cat, err := repo.CreatePracticeCategory(ctx, uniqueUsername("automig_cat"), uid)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	defer func() { _ = repo.DeletePracticeCategory(ctx, int(cat.ID), true) }()

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  cat.ID,
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "trivial",
		Points:      1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	saved, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if saved.AutoSeed != nil {
		t.Fatalf("expected auto_seed=NULL for a QUERY question that never rescues, got %s", saved.AutoSeed)
	}

	// Re-run the migration and confirm the row still reads back NULL — an
	// idempotent ADD COLUMN IF NOT EXISTS must never touch existing data.
	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("InitializeSystemTables (second run): %v", err)
	}
	reread, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion (reread): %v", err)
	}
	if reread.AutoSeed != nil {
		t.Fatalf("expected auto_seed to still read back NULL after a second migration run, got %s", reread.AutoSeed)
	}
}
