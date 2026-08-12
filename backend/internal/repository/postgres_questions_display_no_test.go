package repository

// postgres_questions_display_no_test.go pins models.Question.DisplayNo: the
// per-exam, 1-based position GetQuestionsByExamID computes with ROW_NUMBER().
//
// The defect it guards against: app.questions.id comes from a single global
// sequence, so the lecturer-facing question number kept climbing forever —
// deleting a question left a permanent gap, and deleting a whole examination
// did not stop the next exam's first question from being labelled #57. DisplayNo
// is derived per read, so it always runs 1..N inside an exam and is unaffected
// by anything deleted before it.
//
// DB-gated like the rest of this package: skips cleanly without
// TEST_DATABASE_URL. Run with -p 1 (these tests are DB-global).
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run DisplayNo -v -p 1

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkDisplayNoQuestion inserts one QUERY question into examID and returns its id.
func mkDisplayNoQuestion(t *testing.T, repo *PostgresRepository, ctx context.Context, examID, uid int, requirement string) int {
	t.Helper()
	eid := models.ExamID(examID)
	id, err := repo.CreateQuestion(ctx, models.Question{
		ExamID: &eid, CorrectSQL: "SELECT 1", Type: "QUERY", Points: 1,
		Requirement: requirement, CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateQuestion(%q): %v", requirement, err)
	}
	return id
}

// displayNos returns the DisplayNo of every question in examID, in the order
// GetQuestionsByExamID returns them.
func displayNos(t *testing.T, repo *PostgresRepository, ctx context.Context, examID int) []int {
	t.Helper()
	qs, err := repo.GetQuestionsByExamID(ctx, examID)
	if err != nil {
		t.Fatalf("GetQuestionsByExamID(%d): %v", examID, err)
	}
	out := make([]int, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.DisplayNo)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQuestionDisplayNo_PerExamAndGapFree(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	examA, uidA := mkExamWithAuthor(t, repo, ctx, "display_no_a")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examA) })

	q1 := mkDisplayNoQuestion(t, repo, ctx, examA, uidA, "A-first")
	q2 := mkDisplayNoQuestion(t, repo, ctx, examA, uidA, "A-second")
	q3 := mkDisplayNoQuestion(t, repo, ctx, examA, uidA, "A-third")

	// The ids themselves are global and arbitrary — the whole point is that the
	// lecturer never sees them. Assert only that they are distinct, so a
	// regression to the historic id=0 collision fails here too.
	if q1 == q2 || q2 == q3 || q1 == q3 {
		t.Fatalf("expected three distinct question ids, got %d/%d/%d", q1, q2, q3)
	}

	if got := displayNos(t, repo, ctx, examA); !equalInts(got, []int{1, 2, 3}) {
		t.Fatalf("expected exam A display_no = [1 2 3], got %v", got)
	}

	// Delete the MIDDLE question: the survivors must renumber to 1,2 with no
	// hole where q2 was.
	if err := repo.DeleteQuestion(ctx, q2); err != nil {
		t.Fatalf("DeleteQuestion(%d): %v", q2, err)
	}
	if got := displayNos(t, repo, ctx, examA); !equalInts(got, []int{1, 2}) {
		t.Fatalf("after deleting the middle question, expected display_no = [1 2] (gap-free), got %v", got)
	}

	// A SECOND exam, created after exam A already burned ids from the global
	// sequence, must still start at 1. This is the user-visible symptom: a
	// brand-new exam's first question was labelled with the running global id.
	examB, uidB := mkExamWithAuthor(t, repo, ctx, "display_no_b")
	t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examB) })

	mkDisplayNoQuestion(t, repo, ctx, examB, uidB, "B-first")
	mkDisplayNoQuestion(t, repo, ctx, examB, uidB, "B-second")

	if got := displayNos(t, repo, ctx, examB); !equalInts(got, []int{1, 2}) {
		t.Fatalf("a newly created exam must restart at 1: expected display_no = [1 2], got %v", got)
	}

	// Exam A is untouched by exam B's questions — display_no is scoped per exam,
	// not global.
	if got := displayNos(t, repo, ctx, examA); !equalInts(got, []int{1, 2}) {
		t.Fatalf("exam A's numbering changed after exam B got questions: %v", got)
	}

	// Deleting a WHOLE examination must not shift the other exam's numbering
	// either (the old behaviour: the global sequence kept climbing regardless).
	if err := repo.DeleteExamination(ctx, examA); err != nil {
		t.Fatalf("DeleteExamination(%d): %v", examA, err)
	}
	mkDisplayNoQuestion(t, repo, ctx, examB, uidB, "B-third")
	if got := displayNos(t, repo, ctx, examB); !equalInts(got, []int{1, 2, 3}) {
		t.Fatalf("after deleting exam A, exam B's next question must be 3: expected [1 2 3], got %v", got)
	}
}
