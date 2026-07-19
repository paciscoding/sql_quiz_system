package repository

// Regression tests for ARCH-008/ARCH-009 (nullable practice_questions.title):
//   - CreatePracticeQuestion persists Title; List/Get read it back.
//   - UpdatePracticeQuestion changes it.
//   - Omitting a title reads back as "" (frontend falls back to a #ordinal).
//   - The idempotent `ADD COLUMN IF NOT EXISTS title` migration is exercised
//     implicitly: openTestRepo bootstraps schema via InitializeSystemTables on
//     every test binary run, so a second run against an already-migrated DB
//     (CI re-runs, shared TEST_DATABASE_URL) proves the migration is safe to
//     re-apply.
//
// Uses openTestRepo/uniqueUsername (shared with postgres_password_events_test.go
// in this package); skips cleanly without TEST_DATABASE_URL.

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkPracticeCategory creates a lecturer + a fresh practice category (with its
// own owned schema) and returns the category id.
func mkPracticeCategory(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix string) (int, int) {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername(namePrefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cat, err := repo.CreatePracticeCategory(ctx, uniqueUsername(namePrefix+"_cat"), uid)
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	return int(cat.ID), uid
}

func TestCreatePracticeQuestion_TitleRoundTrip(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "title_rt")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "return the number 1",
		Points:      3,
		Title:       "Select the constant one",
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	if q.ID == 0 {
		t.Fatal("CreatePracticeQuestion did not assign an id")
	}

	got, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if got.Title != "Select the constant one" {
		t.Fatalf("GetPracticeQuestion Title = %q, want %q", got.Title, "Select the constant one")
	}

	list, err := repo.ListPracticeQuestions(ctx)
	if err != nil {
		t.Fatalf("ListPracticeQuestions: %v", err)
	}
	found := false
	for _, lq := range list {
		if lq.ID == q.ID {
			found = true
			if lq.Title != "Select the constant one" {
				t.Fatalf("ListPracticeQuestions Title = %q, want %q", lq.Title, "Select the constant one")
			}
		}
	}
	if !found {
		t.Fatalf("created question id %d not found in ListPracticeQuestions", q.ID)
	}
}

func TestUpdatePracticeQuestion_TitleChanges(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "title_upd")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "original",
		Points:      1,
		Title:       "Original Title",
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	q.Title = "Renamed Title"
	if err := repo.UpdatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("UpdatePracticeQuestion: %v", err)
	}

	got, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if got.Title != "Renamed Title" {
		t.Fatalf("Title after update = %q, want %q", got.Title, "Renamed Title")
	}
}

func TestPracticeQuestion_NoTitle_ReadsEmptyString(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "title_empty")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "no title set",
		Points:      1,
		// Title intentionally left empty.
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	got, err := repo.GetPracticeQuestion(ctx, int(q.ID))
	if err != nil {
		t.Fatalf("GetPracticeQuestion: %v", err)
	}
	if got.Title != "" {
		t.Fatalf("Title for a row with no title set = %q, want empty string (UI falls back to ordinal)", got.Title)
	}
}
