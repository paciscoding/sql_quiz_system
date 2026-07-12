package repository

import (
	"context"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestExportSubmissions_WindowFilter(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("exp_sub_stu"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	// One row inside the window, one before it.
	inWin := base
	before := base.Add(-2 * time.Hour)
	for _, at := range []time.Time{inWin, before} {
		if _, err := repo.pool.Exec(ctx,
			`INSERT INTO app.submissions (student_id, sql_text, is_correct, score, submitted_at)
			 VALUES ($1, $2, true, 1.0, $3)`,
			uid, "SELECT 1", at); err != nil {
			t.Fatalf("insert submission: %v", err)
		}
	}

	from := base.Add(-1 * time.Hour)
	to := base.Add(1 * time.Hour)
	rows, err := repo.ExportSubmissions(ctx, from, to)
	if err != nil {
		t.Fatalf("ExportSubmissions: %v", err)
	}

	// Only the in-window row for this student should appear. Filter to our user
	// so a shared test DB with other rows stays deterministic.
	count := 0
	for _, r := range rows {
		if r.StudentID != nil && *r.StudentID == uid {
			count++
			if r.Username == "" {
				t.Fatal("username not joined")
			}
		}
	}
	if count != 1 {
		t.Fatalf("in-window rows for user = %d, want 1", count)
	}
}

func TestExportSubmissions_BoundaryInclusiveExclusive(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("exp_sub_stu"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	from := time.Now().UTC().Truncate(time.Second)
	to := from.Add(1 * time.Hour)

	// One row exactly on the "from" boundary (must be excluded, predicate is
	// exclusive-from), one row exactly on the "to" boundary (must be included,
	// predicate is inclusive-to).
	for _, at := range []time.Time{from, to} {
		if _, err := repo.pool.Exec(ctx,
			`INSERT INTO app.submissions (student_id, sql_text, is_correct, score, submitted_at)
			 VALUES ($1, $2, true, 1.0, $3)`,
			uid, "SELECT 1", at); err != nil {
			t.Fatalf("insert submission: %v", err)
		}
	}

	rows, err := repo.ExportSubmissions(ctx, from, to)
	if err != nil {
		t.Fatalf("ExportSubmissions: %v", err)
	}

	// Filter to our user so a shared test DB with other rows stays deterministic.
	var matched []models.SubmissionExportRow
	for _, r := range rows {
		if r.StudentID != nil && *r.StudentID == uid {
			matched = append(matched, r)
		}
	}

	if len(matched) != 1 {
		t.Fatalf("boundary rows for user = %d, want 1", len(matched))
	}
	if !matched[0].SubmittedAt.Equal(to) {
		t.Fatalf("SubmittedAt = %v, want %v (to-boundary row, from-boundary row must be excluded)", matched[0].SubmittedAt, to)
	}
}
