package services

// Regression-tester (step [7]) gap coverage for ARCH-028 C2.
//
// postgres_exam_categories_test.go's TestByteIdenticalGrading_BeforeAfterMigration
// (package repository) proves before/after migration equality of
// models.ValidationResult.Status / .Feedback / .ValidationDetails only — it
// CANNOT touch .Score / .IsCorrect because those two fields are computed by
// ValidationService.decideStatus/decideDMLStatus in package services, and
// package repository cannot import package services (services already
// imports repository; the reverse would be a cycle). repository.Evaluate
// itself never sets Score/IsCorrect at all (see postgres_evaluation.go:
// evaluateQuery/evaluateDML build models.ValidationResult with only
// Status/Feedback/ValidationDetails populated), so a byte-identity check
// confined to package repository has a genuine, structural blind spot on the
// two fields a student and a gradebook actually care about most.
//
// This file closes that gap from the OTHER side: it lives in package
// services (which CAN see both repository.PostgresRepository AND
// ValidationService), drives a pre-existing (uncategorised) question through
// the REAL ValidationService.Validate both before and after
// migrateExamCategories runs, and asserts the FULL models.ValidationResult —
// Status, Score, IsCorrect, Feedback, ValidationDetails — is identical.
//
// Deliberately does NOT use internal/testsupport.SharedRepo. That singleton
// is shared, process-wide, by every test in this package's test binary,
// including dml_grading_integration_test.go's pre-existing, known,
// unrelated "student SQL pools not initialised" failure (see that file's
// header) — testsupport.SharedRepo's bootstrap never calls InitStudentPools.
// If this file called repo.InitStudentPools on that same shared singleton,
// it would silently flip dml_grading_integration_test.go from FAIL to PASS
// as a side effect of test ordering within one process, which is exactly the
// kind of accidental "fix" the regression-tester must not produce. Opening a
// fully independent repository connection (mirroring package repository's
// own openTestRepo bootstrap sequence) keeps this file's side effects
// contained to itself.
//
// Run: TEST_DATABASE_URL=postgres://... go test ./internal/services/... -run ByteIdenticalGrading_FullServiceResult -v

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

var (
	egOnce    sync.Once
	egRepo    *repository.PostgresRepository
	egInitErr error
	egSkip    bool
)

// openExamCategoryByteIdentityRepo returns a DEDICATED repository singleton
// for this file only (see the file header for why it must not be
// testsupport.SharedRepo).
func openExamCategoryByteIdentityRepo(t *testing.T) *repository.PostgresRepository {
	t.Helper()
	egOnce.Do(func() {
		dsn := os.Getenv("TEST_DATABASE_URL")
		if dsn == "" {
			egSkip = true
			return
		}
		repo, err := repository.NewPostgresRepository(dsn)
		if err != nil {
			egInitErr = err
			return
		}
		ctx := context.Background()
		for attempt := 0; attempt < 8; attempt++ {
			if err = repo.CreateRestrictedUser(ctx); err == nil {
				if err = repo.InitStudentPools(ctx); err == nil {
					if err = repo.InitializeSystemTables(ctx); err == nil {
						break
					}
				}
			}
			time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
		}
		if err != nil {
			egInitErr = err
			return
		}
		egRepo = repo
	})
	if egSkip {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	if egInitErr != nil {
		t.Fatalf("test repo bootstrap: %v", egInitErr)
	}
	return egRepo
}

func egUnique(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func TestByteIdenticalGrading_FullServiceResult_ScoreAndIsCorrect(t *testing.T) {
	repo := openExamCategoryByteIdentityRepo(t)
	ctx := context.Background()
	svc := NewValidationService(repo)

	t.Run("QUERY", func(t *testing.T) {
		uid, err := repo.CreateUser(ctx, models.User{
			Username: egUnique("bytesvc_query_author"), Password: "hash", Role: models.RoleLecturer,
		})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		examID, err := repo.CreateExamination(ctx, models.Examination{
			Title: egUnique("bytesvc_query_exam"), CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateExamination: %v", err)
		}
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		tbl := egUnique("bytesvc_query_t")
		if _, err := repo.Pool().Exec(ctx, fmt.Sprintf(
			"CREATE TABLE public.%s (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO public.%s VALUES (1, 42)", tbl, tbl)); err != nil {
			t.Fatalf("seed public table: %v", err)
		}
		t.Cleanup(func() { _, _ = repo.Pool().Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl) })

		eid := models.ExamID(examID)
		sql := fmt.Sprintf("SELECT * FROM %s", tbl)
		qID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, CorrectSQL: sql, Type: "QUERY", Points: 7, Requirement: "r", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}

		qBefore, err := repo.GetQuestion(ctx, qID)
		if err != nil {
			t.Fatalf("GetQuestion (before): %v", err)
		}
		if qBefore.CategoryID != nil {
			t.Fatalf("test setup: expected no category before migration, got %v", qBefore.CategoryID)
		}

		resBefore, err := svc.Validate(ctx, uid, qBefore, sql, "")
		if err != nil {
			t.Fatalf("Validate (before): %v", err)
		}
		if resBefore.Status != "correct" || !resBefore.IsCorrect || resBefore.Score != 7 {
			t.Fatalf("test setup: expected the pre-migration submission (the model answer itself) to grade fully correct, got %+v", resBefore)
		}

		if err := repo.InitializeSystemTables(ctx); err != nil {
			t.Fatalf("InitializeSystemTables (trigger migrateExamCategories): %v", err)
		}

		qAfter, err := repo.GetQuestion(ctx, qID)
		if err != nil {
			t.Fatalf("GetQuestion (after): %v", err)
		}
		if qAfter.CategoryID == nil {
			t.Fatalf("test setup: expected the migration to attach the question to a General category")
		}

		resAfter, err := svc.Validate(ctx, uid, qAfter, sql, "")
		if err != nil {
			t.Fatalf("Validate (after): %v", err)
		}

		if resBefore.Status != resAfter.Status ||
			resBefore.IsCorrect != resAfter.IsCorrect ||
			resBefore.Score != resAfter.Score ||
			resBefore.Feedback != resAfter.Feedback ||
			string(resBefore.ValidationDetails) != string(resAfter.ValidationDetails) {
			t.Fatalf("ARCH-028 C2 VIOLATED (full ValidationService result, QUERY — closes the repository-package Score/IsCorrect blind spot): before=%+v after=%+v", resBefore, resAfter)
		}
	})

	t.Run("DML", func(t *testing.T) {
		uid, err := repo.CreateUser(ctx, models.User{
			Username: egUnique("bytesvc_dml_author"), Password: "hash", Role: models.RoleLecturer,
		})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		examID, err := repo.CreateExamination(ctx, models.Examination{
			Title: egUnique("bytesvc_dml_exam"), CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateExamination: %v", err)
		}
		t.Cleanup(func() { _ = repo.DeleteExamination(context.Background(), examID) })

		tbl := egUnique("bytesvc_dml_t")
		if _, err := repo.Pool().Exec(ctx, fmt.Sprintf(
			"CREATE TABLE public.%s (id INTEGER PRIMARY KEY, val INTEGER NOT NULL); INSERT INTO public.%s VALUES (1, 55)", tbl, tbl)); err != nil {
			t.Fatalf("seed public table: %v", err)
		}
		t.Cleanup(func() { _, _ = repo.Pool().Exec(context.Background(), "DROP TABLE IF EXISTS public."+tbl) })

		if err := repo.InitializeExamSandbox(ctx, examID); err != nil {
			t.Fatalf("InitializeExamSandbox: %v", err)
		}

		eid := models.ExamID(examID)
		dmlSQL := fmt.Sprintf("UPDATE %s SET val = val + 1 WHERE id = 1", tbl)
		qID, err := repo.CreateQuestion(ctx, models.Question{
			ExamID: &eid, TargetTable: tbl, CorrectSQL: dmlSQL, Type: "DML", Points: 4, Requirement: "r", CreatedBy: models.UserID(uid),
		})
		if err != nil {
			t.Fatalf("CreateQuestion: %v", err)
		}

		qBefore, err := repo.GetQuestion(ctx, qID)
		if err != nil {
			t.Fatalf("GetQuestion (before): %v", err)
		}
		if qBefore.CategoryID != nil {
			t.Fatalf("test setup: expected no category before migration, got %v", qBefore.CategoryID)
		}
		sandboxBefore := repository.ExamGradingSandboxFor(qBefore)

		resBefore, err := svc.Validate(ctx, uid, qBefore, dmlSQL, sandboxBefore)
		if err != nil {
			t.Fatalf("Validate (before): %v", err)
		}
		if resBefore.Status != "correct" || !resBefore.IsCorrect || resBefore.Score != 4 {
			t.Fatalf("test setup: expected the pre-migration submission (the model answer itself) to grade fully correct, got %+v", resBefore)
		}

		if err := repo.InitializeSystemTables(ctx); err != nil {
			t.Fatalf("InitializeSystemTables (trigger migrateExamCategories): %v", err)
		}

		qAfter, err := repo.GetQuestion(ctx, qID)
		if err != nil {
			t.Fatalf("GetQuestion (after): %v", err)
		}
		if qAfter.CategoryID == nil {
			t.Fatalf("test setup: expected the migration to attach the question to a General category")
		}
		sandboxAfter := repository.ExamGradingSandboxFor(qAfter)
		if sandboxBefore != sandboxAfter {
			t.Fatalf("test invariant violated: resolved DML sandbox changed after migration for a legacy-adopted General, before=%q after=%q", sandboxBefore, sandboxAfter)
		}

		resAfter, err := svc.Validate(ctx, uid, qAfter, dmlSQL, sandboxAfter)
		if err != nil {
			t.Fatalf("Validate (after): %v", err)
		}

		if resBefore.Status != resAfter.Status ||
			resBefore.IsCorrect != resAfter.IsCorrect ||
			resBefore.Score != resAfter.Score ||
			resBefore.Feedback != resAfter.Feedback ||
			string(resBefore.ValidationDetails) != string(resAfter.ValidationDetails) {
			t.Fatalf("ARCH-028 C2 VIOLATED (full ValidationService result, DML — closes the repository-package Score/IsCorrect blind spot): before=%+v after=%+v", resBefore, resAfter)
		}
	})
}
