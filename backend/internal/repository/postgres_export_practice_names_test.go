package repository

// Regression test for ARCH-005 (additive category_name/question_requirement
// columns on the practice-attempts export): ExportPracticeAttempts must join
// app.practice_questions/app.practice_categories and populate CategoryName +
// QuestionRequirement, while PracticeQuestionID stays the raw int (no name
// substituted for the id column). Uses openTestRepo/uniqueUsername (shared
// with postgres_password_events_test.go); skips cleanly without
// TEST_DATABASE_URL.

import (
	"context"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestExportPracticeAttempts_JoinsCategoryAndQuestionNames(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "exp_prac_names")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "must select the number one",
		Points:      2,
		Title:       "Export Name Test Q",
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	student, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("exp_prac_names_stu"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := repo.CreatePracticeAttempt(ctx, &models.PracticeAttempt{
		StudentID: models.UserID(student), PracticeQuestionID: q.ID,
		SQLText: "SELECT 1", IsCorrect: true,
	}); err != nil {
		t.Fatalf("CreatePracticeAttempt: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	rows, err := repo.ExportPracticeAttempts(ctx, from, to)
	if err != nil {
		t.Fatalf("ExportPracticeAttempts: %v", err)
	}

	var matched *models.PracticeAttemptExportRow
	for i := range rows {
		if rows[i].StudentID == student && rows[i].PracticeQuestionID == int(q.ID) {
			matched = &rows[i]
		}
	}
	if matched == nil {
		t.Fatalf("expected an export row for student=%d question=%d, got %d rows total", student, q.ID, len(rows))
	}
	if matched.QuestionRequirement != "must select the number one" {
		t.Fatalf("QuestionRequirement = %q, want %q", matched.QuestionRequirement, "must select the number one")
	}
	if matched.CategoryName == "" {
		t.Fatal("CategoryName must be populated from the joined practice_categories row")
	}
	// The id column itself must stay the raw int, never replaced by a name.
	if matched.PracticeQuestionID != int(q.ID) {
		t.Fatalf("PracticeQuestionID = %d, want raw int %d (ids stay raw; names are additive)", matched.PracticeQuestionID, int(q.ID))
	}
}

func TestExportPracticeAttempts_WindowExcludesOutOfRangeAttempts(t *testing.T) {
	// Companion edge case to the join test above: the (from, to] window filter
	// must still work correctly now that the query carries two extra JOINs —
	// an attempt outside the window must not appear even though its question
	// and category exist and would otherwise resolve names successfully.
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "exp_prac_window")

	q := &models.PracticeQuestion{
		CreatedBy: models.UserID(uid), CategoryID: models.CategoryID(catID),
		CorrectSQL: "SELECT 1", Type: "QUERY", Requirement: "window test", Points: 1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	student, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("exp_prac_window_stu"), Password: "hash", Role: models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	old := time.Now().Add(-48 * time.Hour)
	if _, err := repo.pool.Exec(ctx,
		`INSERT INTO app.practice_attempts (student_id, practice_question_id, sql_text, is_correct, submitted_at)
		 VALUES ($1, $2, 'SELECT 1', true, $3)`,
		student, q.ID, old); err != nil {
		t.Fatalf("insert old attempt: %v", err)
	}

	// Window starting after `old` must exclude it.
	rows, err := repo.ExportPracticeAttempts(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ExportPracticeAttempts: %v", err)
	}
	for _, r := range rows {
		if r.StudentID == student && r.PracticeQuestionID == int(q.ID) {
			t.Fatalf("out-of-window attempt leaked into export result: %+v", r)
		}
	}
}
