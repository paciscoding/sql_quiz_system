package repository

// Regression tests for ARCH-006 (GetLecturerPracticeResults): the query must
// return exactly one row per (student, question) pair that has at least one
// practice_attempts row, aggregating is_solved=bool_or(is_correct),
// attempt_count=count(*), earned_points=points-when-solved-else-0, and must
// NOT return unattempted pairs. Uses openTestRepo/uniqueUsername (shared with
// postgres_password_events_test.go); skips cleanly without TEST_DATABASE_URL.

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestGetLecturerPracticeResults_AggregatesAttemptedPairsOnly(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, authorUID := mkPracticeCategory(t, repo, ctx, "lpr")

	// Two questions in the category, both worth different points, so we can
	// distinguish them in the aggregate.
	solvedQ := &models.PracticeQuestion{
		CreatedBy:   models.UserID(authorUID),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "solved question",
		Points:      4,
		Title:       "Solved Q",
	}
	if err := repo.CreatePracticeQuestion(ctx, solvedQ); err != nil {
		t.Fatalf("CreatePracticeQuestion (solved): %v", err)
	}
	unsolvedQ := &models.PracticeQuestion{
		CreatedBy:   models.UserID(authorUID),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "unsolved question",
		Points:      7,
		Title:       "Unsolved Q",
	}
	if err := repo.CreatePracticeQuestion(ctx, unsolvedQ); err != nil {
		t.Fatalf("CreatePracticeQuestion (unsolved): %v", err)
	}
	// An attempted-by-nobody question: must never appear in the results.
	untouchedQ := &models.PracticeQuestion{
		CreatedBy:   models.UserID(authorUID),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "no attempts at all",
		Points:      9,
		Title:       "Untouched Q",
	}
	if err := repo.CreatePracticeQuestion(ctx, untouchedQ); err != nil {
		t.Fatalf("CreatePracticeQuestion (untouched): %v", err)
	}

	studentNumber := uniqueUsername("lpr_stu_num")
	student, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("lpr_student"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser (student): %v", err)
	}
	// CreateUser's INSERT doesn't carry student_number (a roster-import field);
	// set it directly so GetLecturerPracticeResults' u.student_number join has
	// something deterministic to assert on.
	if _, err := repo.pool.Exec(ctx,
		`UPDATE app.system_users SET student_number = $1 WHERE id = $2`, studentNumber, student); err != nil {
		t.Fatalf("set student_number: %v", err)
	}

	// solvedQ: two attempts, second one correct -> is_solved=true, attempt_count=2, earned=4.
	if err := repo.CreatePracticeAttempt(ctx, &models.PracticeAttempt{
		StudentID: models.UserID(student), PracticeQuestionID: solvedQ.ID,
		SQLText: "SELECT 2", IsCorrect: false,
	}); err != nil {
		t.Fatalf("CreatePracticeAttempt (solvedQ, wrong): %v", err)
	}
	if err := repo.CreatePracticeAttempt(ctx, &models.PracticeAttempt{
		StudentID: models.UserID(student), PracticeQuestionID: solvedQ.ID,
		SQLText: "SELECT 1", IsCorrect: true,
	}); err != nil {
		t.Fatalf("CreatePracticeAttempt (solvedQ, correct): %v", err)
	}

	// unsolvedQ: one wrong attempt -> is_solved=false, attempt_count=1, earned=0.
	if err := repo.CreatePracticeAttempt(ctx, &models.PracticeAttempt{
		StudentID: models.UserID(student), PracticeQuestionID: unsolvedQ.ID,
		SQLText: "SELECT 2", IsCorrect: false,
	}); err != nil {
		t.Fatalf("CreatePracticeAttempt (unsolvedQ): %v", err)
	}

	rows, err := repo.GetLecturerPracticeResults(ctx)
	if err != nil {
		t.Fatalf("GetLecturerPracticeResults: %v", err)
	}

	// Filter to just this test's student (shared DB may carry rows from other tests).
	var mine []models.LecturerPracticeResultRow
	for _, r := range rows {
		if r.StudentID == models.UserID(student) {
			mine = append(mine, r)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("attempted-pair rows for student = %d, want 2 (solvedQ + unsolvedQ, NOT untouchedQ); got %+v", len(mine), mine)
	}

	var solvedRow, unsolvedRow *models.LecturerPracticeResultRow
	for i := range mine {
		switch mine[i].QuestionID {
		case solvedQ.ID:
			solvedRow = &mine[i]
		case unsolvedQ.ID:
			unsolvedRow = &mine[i]
		case untouchedQ.ID:
			t.Fatalf("untouched question must not appear in results, got row %+v", mine[i])
		}
	}
	if solvedRow == nil || unsolvedRow == nil {
		t.Fatalf("expected both solvedQ and unsolvedQ rows, got %+v", mine)
	}

	if !solvedRow.IsSolved {
		t.Fatalf("solvedQ IsSolved = false, want true (bool_or over [false, true])")
	}
	if solvedRow.AttemptCount != 2 {
		t.Fatalf("solvedQ AttemptCount = %d, want 2", solvedRow.AttemptCount)
	}
	if solvedRow.EarnedPoints != 4 {
		t.Fatalf("solvedQ EarnedPoints = %v, want 4 (points when solved)", solvedRow.EarnedPoints)
	}
	if solvedRow.CategoryName == "" {
		t.Fatal("solvedQ CategoryName must be populated from the joined category")
	}
	if solvedRow.QuestionTitle != "Solved Q" {
		t.Fatalf("solvedQ QuestionTitle = %q, want %q", solvedRow.QuestionTitle, "Solved Q")
	}
	if solvedRow.StudentNumber != studentNumber {
		t.Fatalf("solvedQ StudentNumber = %q, want %q", solvedRow.StudentNumber, studentNumber)
	}

	if unsolvedRow.IsSolved {
		t.Fatal("unsolvedQ IsSolved = true, want false (bool_or over [false])")
	}
	if unsolvedRow.AttemptCount != 1 {
		t.Fatalf("unsolvedQ AttemptCount = %d, want 1", unsolvedRow.AttemptCount)
	}
	if unsolvedRow.EarnedPoints != 0 {
		t.Fatalf("unsolvedQ EarnedPoints = %v, want 0 (not solved)", unsolvedRow.EarnedPoints)
	}
}

func TestGetLecturerPracticeResults_DeterministicOrdering(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, authorUID := mkPracticeCategory(t, repo, ctx, "lpr_order")

	q1 := &models.PracticeQuestion{
		CreatedBy: models.UserID(authorUID), CategoryID: models.CategoryID(catID),
		CorrectSQL: "SELECT 1", Type: "QUERY", Requirement: "q1", Points: 1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q1); err != nil {
		t.Fatalf("CreatePracticeQuestion q1: %v", err)
	}
	q2 := &models.PracticeQuestion{
		CreatedBy: models.UserID(authorUID), CategoryID: models.CategoryID(catID),
		CorrectSQL: "SELECT 1", Type: "QUERY", Requirement: "q2", Points: 1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q2); err != nil {
		t.Fatalf("CreatePracticeQuestion q2: %v", err)
	}

	numA := "AAA_" + uniqueUsername("num")
	numB := "ZZZ_" + uniqueUsername("num")
	studentA, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("lpr_order_a"), Password: "hash", Role: models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser A: %v", err)
	}
	studentB, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("lpr_order_b"), Password: "hash", Role: models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser B: %v", err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE app.system_users SET student_number = $1 WHERE id = $2`, numA, studentA); err != nil {
		t.Fatalf("set student_number A: %v", err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE app.system_users SET student_number = $1 WHERE id = $2`, numB, studentB); err != nil {
		t.Fatalf("set student_number B: %v", err)
	}

	// Insert B's attempts first, then A's, to prove ordering isn't insertion-order.
	for _, pair := range []struct {
		student int
		q       models.PracticeQuestionID
	}{
		{studentB, q2.ID}, {studentB, q1.ID}, {studentA, q2.ID}, {studentA, q1.ID},
	} {
		if err := repo.CreatePracticeAttempt(ctx, &models.PracticeAttempt{
			StudentID: models.UserID(pair.student), PracticeQuestionID: pair.q,
			SQLText: "SELECT 1", IsCorrect: true,
		}); err != nil {
			t.Fatalf("CreatePracticeAttempt: %v", err)
		}
	}

	rows, err := repo.GetLecturerPracticeResults(ctx)
	if err != nil {
		t.Fatalf("GetLecturerPracticeResults: %v", err)
	}

	var idxA, idxB []int
	for i, r := range rows {
		if r.StudentID == models.UserID(studentA) {
			idxA = append(idxA, i)
		}
		if r.StudentID == models.UserID(studentB) {
			idxB = append(idxB, i)
		}
	}
	if len(idxA) != 2 || len(idxB) != 2 {
		t.Fatalf("expected 2 rows each for A and B, got A=%d B=%d", len(idxA), len(idxB))
	}
	// ORDER BY student_number ASC: "AAA_..." must sort before "ZZZ_...".
	if idxA[0] >= idxB[0] {
		t.Fatalf("expected student A (student_number %q) rows before student B (%q); indices A=%v B=%v", numA, numB, idxA, idxB)
	}
	// Within a student, ORDER BY question id ASC: q1 (created first, lower id) before q2.
	if rows[idxA[0]].QuestionID != q1.ID || rows[idxA[1]].QuestionID != q2.ID {
		t.Fatalf("expected student A rows ordered q1 then q2, got %d then %d", rows[idxA[0]].QuestionID, rows[idxA[1]].QuestionID)
	}
}
