package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestUpsertStudentRotating_ConflictRotatesCredential is the core guarantee of
// the rotating importer: re-importing an existing student REPLACES the password
// hash and bumps token_version in the same statement, so the new credential and
// the session eviction can never diverge.
func TestUpsertStudentRotating_ConflictRotatesCredential(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	creatorID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("rot-importer"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (creator): %v", err)
	}

	ext := uniqueUsername("rot-ext")
	first := models.StudentImportRow{
		StudentNumber: uniqueUsername("RN"),
		StudentIDExt:  ext,
		FirstName:     "Alice",
		LastName:      "Before",
		PasswordHash:  "hash-one",
	}
	second := first
	second.LastName = "After"
	second.PasswordHash = "hash-two"

	var id1, id2 int
	var outcome1, outcome2 string
	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcome1, id1, txErr = repo.UpsertStudentRotating(ctx, tx, first, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentRotating (insert): %v", err)
	}
	if outcome1 != "created" {
		t.Fatalf("first upsert outcome = %q, want \"created\"", outcome1)
	}

	versionBefore, err := repo.GetTokenVersion(ctx, id1)
	if err != nil {
		t.Fatalf("GetTokenVersion (before): %v", err)
	}

	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcome2, id2, txErr = repo.UpsertStudentRotating(ctx, tx, second, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentRotating (conflict): %v", err)
	}
	if outcome2 != "updated" {
		t.Fatalf("second upsert outcome = %q, want \"updated\"", outcome2)
	}
	if id1 != id2 {
		t.Fatalf("conflict created a new row: id1=%d id2=%d", id1, id2)
	}

	var hash, lastName string
	if err := repo.pool.QueryRow(ctx,
		`SELECT password_hash, last_name FROM app.system_users WHERE id = $1`, id2,
	).Scan(&hash, &lastName); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if hash != "hash-two" {
		t.Errorf("password_hash = %q, want \"hash-two\" (credential did not rotate)", hash)
	}
	if lastName != "After" {
		t.Errorf("last_name = %q, want \"After\"", lastName)
	}

	versionAfter, err := repo.GetTokenVersion(ctx, id2)
	if err != nil {
		t.Fatalf("GetTokenVersion (after): %v", err)
	}
	if versionAfter != versionBefore+1 {
		t.Errorf("token_version = %d, want %d (sessions were not evicted)", versionAfter, versionBefore+1)
	}
}

// TestUpsertStudentByNumber_StillPreservesPassword guards the OLD importer:
// adding the rotating variant must not change the non-rotating one.
func TestUpsertStudentByNumber_StillPreservesPassword(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	creatorID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("plain-importer"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (creator): %v", err)
	}

	ext := uniqueUsername("plain-ext")
	row := models.StudentImportRow{
		StudentNumber: uniqueUsername("RN"),
		StudentIDExt:  ext,
		FirstName:     "Bob",
		PasswordHash:  "original-hash",
	}
	var id int
	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		_, id, txErr = repo.UpsertStudentByNumber(ctx, tx, row, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentByNumber (insert): %v", err)
	}

	row.PasswordHash = "replacement-hash"
	row.FirstName = "Bobby"
	if err := repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		_, _, txErr := repo.UpsertStudentByNumber(ctx, tx, row, creatorID)
		return txErr
	}); err != nil {
		t.Fatalf("UpsertStudentByNumber (conflict): %v", err)
	}

	var hash, firstName string
	if err := repo.pool.QueryRow(ctx,
		`SELECT password_hash, first_name FROM app.system_users WHERE id = $1`, id,
	).Scan(&hash, &firstName); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if hash != "original-hash" {
		t.Errorf("password_hash = %q, want \"original-hash\": the plain importer must NOT rotate credentials", hash)
	}
	if firstName != "Bobby" {
		t.Errorf("first_name = %q, want \"Bobby\": attributes should still refresh", firstName)
	}
}
