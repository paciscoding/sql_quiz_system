package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestUpsertStudent_DuplicateStudentNumberKeepsBothRows guards against the
// pre-fix bug where two students sharing a student_number collapsed into one
// row (ON CONFLICT (student_number) DO UPDATE). student_number is
// duplicate-able by design, so both imported rows must survive as distinct
// students.
func TestUpsertStudent_DuplicateStudentNumberKeepsBothRows(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	creatorID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("importer"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (creator): %v", err)
	}

	sharedNumber := uniqueUsername("DUP")
	row1 := models.StudentImportRow{
		StudentNumber: sharedNumber,
		StudentIDExt:  uniqueUsername("ext1"),
		FirstName:     "Alice",
		LastName:      "First",
		PasswordHash:  "x",
	}
	row2 := models.StudentImportRow{
		StudentNumber: sharedNumber,
		StudentIDExt:  uniqueUsername("ext2"),
		FirstName:     "Bob",
		LastName:      "Second",
		PasswordHash:  "x",
	}

	var id1, id2 int
	var outcome1, outcome2 string
	err = repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcome1, id1, txErr = repo.UpsertStudentByNumber(ctx, tx, row1, creatorID)
		return txErr
	})
	if err != nil {
		t.Fatalf("UpsertStudentByNumber (row1): %v", err)
	}
	err = repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcome2, id2, txErr = repo.UpsertStudentByNumber(ctx, tx, row2, creatorID)
		return txErr
	})
	if err != nil {
		t.Fatalf("UpsertStudentByNumber (row2): %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(), `DELETE FROM app.system_users WHERE id = ANY($1)`, []int{id1, id2, creatorID})
	})

	if outcome1 != "created" {
		t.Errorf("row1 outcome = %q, want created", outcome1)
	}
	if outcome2 != "created" {
		t.Errorf("row2 outcome = %q, want created", outcome2)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct user ids for distinct student_id_ext, got both = %d", id1)
	}

	var count int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM app.system_users WHERE student_number = $1`, sharedNumber,
	).Scan(&count); err != nil {
		t.Fatalf("count by student_number: %v", err)
	}
	if count != 2 {
		t.Errorf("rows sharing student_number = %d, want 2 (both must survive)", count)
	}
}

// TestUpsertStudent_SameStudentIdExtUpdatesInPlace verifies student_id_ext is
// now the upsert conflict key: re-importing the same student_id_ext updates
// the existing row (including a changed student_number) instead of creating
// a new one.
func TestUpsertStudent_SameStudentIdExtUpdatesInPlace(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	creatorID, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("importer"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser (creator): %v", err)
	}

	ext := uniqueUsername("ext_stable")
	rowInitial := models.StudentImportRow{
		StudentNumber: uniqueUsername("SN-orig"),
		StudentIDExt:  ext,
		FirstName:     "Original",
		LastName:      "Name",
		PasswordHash:  "x",
	}
	rowUpdated := models.StudentImportRow{
		StudentNumber: uniqueUsername("SN-changed"),
		StudentIDExt:  ext,
		FirstName:     "Updated",
		LastName:      "Name",
		PasswordHash:  "x",
	}

	var idFirst, idSecond int
	var outcomeFirst, outcomeSecond string
	err = repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcomeFirst, idFirst, txErr = repo.UpsertStudentByNumber(ctx, tx, rowInitial, creatorID)
		return txErr
	})
	if err != nil {
		t.Fatalf("UpsertStudentByNumber (initial): %v", err)
	}
	err = repo.WithTransaction(ctx, func(tx pgx.Tx) error {
		var txErr error
		outcomeSecond, idSecond, txErr = repo.UpsertStudentByNumber(ctx, tx, rowUpdated, creatorID)
		return txErr
	})
	if err != nil {
		t.Fatalf("UpsertStudentByNumber (updated): %v", err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(), `DELETE FROM app.system_users WHERE id = ANY($1)`, []int{idFirst, creatorID})
	})

	if outcomeFirst != "created" {
		t.Errorf("first outcome = %q, want created", outcomeFirst)
	}
	if outcomeSecond != "updated" {
		t.Errorf("second outcome = %q, want updated", outcomeSecond)
	}
	if idFirst != idSecond {
		t.Fatalf("expected same id for same student_id_ext, got %d then %d", idFirst, idSecond)
	}

	var count int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM app.system_users WHERE student_id_ext = $1`, ext,
	).Scan(&count); err != nil {
		t.Fatalf("count by student_id_ext: %v", err)
	}
	if count != 1 {
		t.Errorf("rows for student_id_ext = %d, want 1", count)
	}

	var firstName, studentNumber string
	if err := repo.pool.QueryRow(ctx,
		`SELECT first_name, student_number FROM app.system_users WHERE id = $1`, idFirst,
	).Scan(&firstName, &studentNumber); err != nil {
		t.Fatalf("read back updated row: %v", err)
	}
	if firstName != "Updated" {
		t.Errorf("first_name = %q, want Updated", firstName)
	}
	if studentNumber != rowUpdated.StudentNumber {
		t.Errorf("student_number = %q, want %q", studentNumber, rowUpdated.StudentNumber)
	}
}

// TestStudentNumberIndex_IsNonUnique verifies the schema migration replaced
// the legacy UNIQUE index on student_number with a plain (non-unique) one.
func TestStudentNumberIndex_IsNonUnique(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	var exists bool
	var isUnique bool
	err := repo.pool.QueryRow(ctx, `
		SELECT true, i.indisunique
		FROM pg_index i
		JOIN pg_class c     ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app'
		  AND c.relname = 'idx_system_users_student_number'
	`).Scan(&exists, &isUnique)
	if err != nil {
		t.Fatalf("query pg_index for idx_system_users_student_number: %v", err)
	}
	if !exists {
		t.Fatal("idx_system_users_student_number does not exist")
	}
	if isUnique {
		t.Error("idx_system_users_student_number is UNIQUE, want non-unique (student_number must allow duplicates)")
	}
}
