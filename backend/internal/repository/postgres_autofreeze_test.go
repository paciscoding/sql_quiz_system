package repository

// Regression tests for ARCH-001 (auto-freeze practice on exam activation):
// TestGetLecturerPracticeResults_Aggregates lives in a separate file, but this
// file covers the SetExaminationActive / IsPracticeFrozen / SetExamPracticeFreeze
// interaction the validator flagged in regression_tester_must_cover:
//
//   - activating an exam sets block_practice_during_exam=TRUE unconditionally
//   - IsPracticeFrozen reflects that while the exam is active
//   - a manual SetExamPracticeFreeze(false) unfreezes during the active window
//   - deactivating clears is_active (IsPracticeFrozen goes false regardless of
//     the block flag, since IsPracticeFrozen only counts is_active=true exams)
//   - reactivating re-freezes unconditionally, even though the exam was
//     manually unfrozen before the deactivate/reactivate cycle
//
// Uses openTestRepo/mkExamAndTable (shared with postgres_questions_id_test.go
// in this package); skips cleanly without TEST_DATABASE_URL.

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestSetExaminationActive_AutoFreezesPractice(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx)
	_ = uid

	// Seed a captured question so SetExaminationActive's readiness gate passes.
	seedCapturedQuestion(t, repo, ctx, examID, tbl, uid)

	// IsPracticeFrozen is a GLOBAL check ("any active exam with the flag on"),
	// not per-exam — an exam left active would leak into every later test in
	// this package that asserts IsPracticeFrozen()==false. Always deactivate.
	t.Cleanup(func() {
		_ = repo.SetExaminationActive(context.Background(), examID, false, nil, nil)
	})

	if err := repo.SetExaminationActive(ctx, examID, true, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive(true): %v", err)
	}

	exam, err := repo.GetExamByID(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamByID: %v", err)
	}
	if !exam.IsActive {
		t.Fatal("exam should be active after SetExaminationActive(true)")
	}
	if !exam.BlockPracticeDuringExam {
		t.Fatal("activation must unconditionally set block_practice_during_exam=TRUE (ARCH-001)")
	}

	frozen, err := repo.IsPracticeFrozen(ctx)
	if err != nil {
		t.Fatalf("IsPracticeFrozen: %v", err)
	}
	if !frozen {
		t.Fatal("IsPracticeFrozen must be true while an active exam has block_practice_during_exam=TRUE")
	}
}

func TestSetExamPracticeFreeze_UnfreezeDuringActiveWindow(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx)
	seedCapturedQuestion(t, repo, ctx, examID, tbl, uid)
	t.Cleanup(func() {
		_ = repo.SetExaminationActive(context.Background(), examID, false, nil, nil)
	})

	if err := repo.SetExaminationActive(ctx, examID, true, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive(true): %v", err)
	}

	// Lecturer manually toggles the freeze off while the exam stays active.
	if err := repo.SetExamPracticeFreeze(ctx, examID, false); err != nil {
		t.Fatalf("SetExamPracticeFreeze(false): %v", err)
	}

	exam, err := repo.GetExamByID(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamByID: %v", err)
	}
	if exam.BlockPracticeDuringExam {
		t.Fatal("manual unfreeze must persist while the exam is still active")
	}

	frozen, err := repo.IsPracticeFrozen(ctx)
	if err != nil {
		t.Fatalf("IsPracticeFrozen: %v", err)
	}
	if frozen {
		t.Fatal("IsPracticeFrozen must be false after a manual unfreeze during the active window")
	}
}

func TestReactivation_ReFreezesAfterManualUnfreezeAndDeactivate(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx)
	seedCapturedQuestion(t, repo, ctx, examID, tbl, uid)
	t.Cleanup(func() {
		_ = repo.SetExaminationActive(context.Background(), examID, false, nil, nil)
	})

	// Activate, then manually unfreeze.
	if err := repo.SetExaminationActive(ctx, examID, true, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive(true) #1: %v", err)
	}
	if err := repo.SetExamPracticeFreeze(ctx, examID, false); err != nil {
		t.Fatalf("SetExamPracticeFreeze(false): %v", err)
	}

	// Deactivate: IsPracticeFrozen must go false (IsPracticeFrozen only counts
	// is_active=true exams, regardless of the block flag's stored value).
	if err := repo.SetExaminationActive(ctx, examID, false, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive(false) (deactivate): %v", err)
	}
	frozenAfterDeactivate, err := repo.IsPracticeFrozen(ctx)
	if err != nil {
		t.Fatalf("IsPracticeFrozen after deactivate: %v", err)
	}
	if frozenAfterDeactivate {
		t.Fatal("IsPracticeFrozen must be false once the exam is deactivated")
	}
	examAfterDeactivate, err := repo.GetExamByID(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamByID after deactivate: %v", err)
	}
	if examAfterDeactivate.IsActive {
		t.Fatal("exam must be inactive after deactivate")
	}

	// Reactivate: ARCH-001 says every activation unconditionally re-freezes,
	// even though the lecturer had manually unfrozen it on the prior activation.
	if err := repo.SetExaminationActive(ctx, examID, true, nil, nil); err != nil {
		t.Fatalf("SetExaminationActive(true) #2 (reactivate): %v", err)
	}
	examAfterReactivate, err := repo.GetExamByID(ctx, examID)
	if err != nil {
		t.Fatalf("GetExamByID after reactivate: %v", err)
	}
	if !examAfterReactivate.BlockPracticeDuringExam {
		t.Fatal("reactivation must re-freeze (block_practice_during_exam=TRUE) unconditionally, per ARCH-001")
	}
	frozenAfterReactivate, err := repo.IsPracticeFrozen(ctx)
	if err != nil {
		t.Fatalf("IsPracticeFrozen after reactivate: %v", err)
	}
	if !frozenAfterReactivate {
		t.Fatal("IsPracticeFrozen must be true again after reactivation")
	}
}

// seedCapturedQuestion adds one QUERY-type captured question to the exam so
// SetExaminationActive's readiness gate (>=1 question, all captured) passes.
func seedCapturedQuestion(t *testing.T, repo *PostgresRepository, ctx context.Context, examID int, tbl string, uid int) {
	t.Helper()
	eid := models.ExamID(examID)
	q := models.Question{
		ExamID:      &eid,
		TargetTable: tbl,
		CorrectSQL:  "SELECT id FROM " + tbl + " ORDER BY id",
		Type:        "QUERY",
		Points:      5,
		Requirement: "autofreeze seed question",
		CreatedBy:   models.UserID(uid),
	}
	if _, err := repo.CreateQuestion(ctx, q); err != nil {
		t.Fatalf("seed CreateQuestion: %v", err)
	}
}
