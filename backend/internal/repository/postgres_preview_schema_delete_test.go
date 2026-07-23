package repository

// Integration tests for ARCH-019 (GAP 2): DeleteExamination and
// DeletePracticeCategory must reclaim their decoy preview schemas
// (curriculum_exam_<id>_preview / sandbox_exam_<id>_preview /
// practice_cat_<id>_preview) instead of leaking them forever.
//
// Every test here creates its own scratch exam or practice category, drops
// whatever schemas it created, and skips cleanly when TEST_DATABASE_URL is
// unset (openTestRepo, shared with postgres_password_events_test.go /
// postgres_dml_grading_integration_test.go), so `go test ./...` stays green
// on a machine with no database.
//
// Run:  TEST_DATABASE_URL=postgres://... go test ./internal/repository/... -run PreviewSchemaDelete

import (
	"context"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkExam creates a lecturer + a fresh examination and returns the exam id.
func mkExam(t *testing.T, repo *PostgresRepository, ctx context.Context, namePrefix string) int {
	t.Helper()
	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername(namePrefix + "_author"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	id, err := repo.CreateExamination(ctx, models.Examination{
		Title:     uniqueUsername(namePrefix + "_exam"),
		CreatedBy: models.UserID(uid),
	})
	if err != nil {
		t.Fatalf("CreateExamination: %v", err)
	}
	return id
}

// schemaExists reports whether a schema is present in pg_namespace.
func schemaExists(t *testing.T, repo *PostgresRepository, ctx context.Context, schema string) bool {
	t.Helper()
	var count int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_namespace WHERE nspname = $1", schema).Scan(&count); err != nil {
		t.Fatalf("schemaExists(%q): %v", schema, err)
	}
	return count > 0
}

// TestDeleteExamination_DropsPreviewSchemas proves DeleteExamination reclaims
// both decoy schemas by canonical name, deliberately WITHOUT stamping
// preview_curriculum_schema first — the fix must not depend on that column
// having been written (partial/failed decoy imports leave it blank). Also
// checks the pre-existing behaviour (real sandbox + non-public curriculum +
// the examinations row) still holds (B1 + B2).
func TestDeleteExamination_DropsPreviewSchemas(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID := mkExam(t, repo, ctx, "del_preview")

	previewCurriculum := ExamPreviewCurriculumSchemaName(examID)
	previewSandbox := ExamPreviewSandboxName(examID)
	realCurriculum := ExamCurriculumSchemaName(examID)
	realSandbox := ExamSandboxName(examID)

	for _, schema := range []string{previewCurriculum, previewSandbox, realCurriculum, realSandbox} {
		if err := repo.EnsureSchema(ctx, schema); err != nil {
			t.Fatalf("EnsureSchema(%q): %v", schema, err)
		}
	}
	// Point the exam at the real (non-public) curriculum and register the real
	// sandbox row so the pre-existing drop paths (5 / 5b) have something to do,
	// mirroring what ImportSchemaIntoExamCurriculum / InitializeExamSandbox
	// would have stamped. preview_curriculum_schema is deliberately left blank.
	if _, err := repo.pool.Exec(ctx,
		"UPDATE app.examinations SET curriculum_schema = $1 WHERE id = $2", realCurriculum, examID); err != nil {
		t.Fatalf("stamp curriculum_schema: %v", err)
	}
	if _, err := repo.pool.Exec(ctx,
		"INSERT INTO app.exam_sandboxes (exam_id, schema_name, created_at) VALUES ($1, $2, NOW())", examID, realSandbox); err != nil {
		t.Fatalf("insert exam_sandboxes row: %v", err)
	}

	if err := repo.DeleteExamination(ctx, examID); err != nil {
		t.Fatalf("DeleteExamination: %v", err)
	}

	for _, schema := range []string{previewCurriculum, previewSandbox, realSandbox} {
		if schemaExists(t, repo, ctx, schema) {
			t.Errorf("schema %q still present in pg_namespace after DeleteExamination", schema)
		}
	}
	if schemaExists(t, repo, ctx, realCurriculum) {
		t.Errorf("real curriculum schema %q still present in pg_namespace after DeleteExamination", realCurriculum)
	}

	var examCount int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM app.examinations WHERE id = $1", examID).Scan(&examCount); err != nil {
		t.Fatalf("count examinations row: %v", err)
	}
	if examCount != 0 {
		t.Errorf("expected app.examinations row for %d to be deleted, still present", examID)
	}
}

// TestDeleteExamination_NoPreviewSchemas_NoOp proves DeleteExamination on an
// exam that never had any preview schemas still succeeds and deletes the exam
// exactly as before — the new unconditional DROP SCHEMA IF EXISTS statements
// must be no-ops when nothing was ever provisioned (B3).
func TestDeleteExamination_NoPreviewSchemas_NoOp(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID := mkExam(t, repo, ctx, "del_no_preview")

	if err := repo.DeleteExamination(ctx, examID); err != nil {
		t.Fatalf("DeleteExamination: %v", err)
	}

	var examCount int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM app.examinations WHERE id = $1", examID).Scan(&examCount); err != nil {
		t.Fatalf("count examinations row: %v", err)
	}
	if examCount != 0 {
		t.Errorf("expected app.examinations row for %d to be deleted, still present", examID)
	}
}

// TestDeletePracticeCategory_DropsPreviewSchema proves DeletePracticeCategory
// (force=true) reclaims practice_cat_<id>_preview by canonical name,
// deliberately WITHOUT stamping preview_schema_name first, alongside the
// pre-existing real-schema drop and category-row delete (B4).
func TestDeletePracticeCategory_DropsPreviewSchema(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, _ := mkPracticeCategory(t, repo, ctx, "del_preview_cat")

	realSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	previewSchema := PracticeCategoryPreviewSchemaName(catID)
	if err := repo.EnsureSchema(ctx, previewSchema); err != nil {
		t.Fatalf("EnsureSchema(%q): %v", previewSchema, err)
	}

	if err := repo.DeletePracticeCategory(ctx, catID, true); err != nil {
		t.Fatalf("DeletePracticeCategory: %v", err)
	}

	if schemaExists(t, repo, ctx, previewSchema) {
		t.Errorf("preview schema %q still present in pg_namespace after DeletePracticeCategory", previewSchema)
	}
	if schemaExists(t, repo, ctx, realSchema) {
		t.Errorf("real schema %q still present in pg_namespace after DeletePracticeCategory", realSchema)
	}

	var catCount int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM app.practice_categories WHERE id = $1", catID).Scan(&catCount); err != nil {
		t.Fatalf("count practice_categories row: %v", err)
	}
	if catCount != 0 {
		t.Errorf("expected app.practice_categories row for %d to be deleted, still present", catID)
	}
}

// TestDeletePracticeCategory_NoPreviewSchema_Unaffected proves
// DeletePracticeCategory on a category that never had a decoy preview schema
// behaves exactly as before (B5).
func TestDeletePracticeCategory_NoPreviewSchema_Unaffected(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, _ := mkPracticeCategory(t, repo, ctx, "del_no_preview_cat")

	if err := repo.DeletePracticeCategory(ctx, catID, true); err != nil {
		t.Fatalf("DeletePracticeCategory: %v", err)
	}

	var catCount int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM app.practice_categories WHERE id = $1", catID).Scan(&catCount); err != nil {
		t.Fatalf("count practice_categories row: %v", err)
	}
	if catCount != 0 {
		t.Errorf("expected app.practice_categories row for %d to be deleted, still present", catID)
	}
}

// TestDeletePracticeCategory_BlockedByQuestions_DropsNothing proves a delete
// blocked by ErrCategoryHasQuestions (force=false) drops NEITHER the real nor
// the preview schema — the guard sits above the whole cascading closure,
// including the new preview-drop block (B6).
func TestDeletePracticeCategory_BlockedByQuestions_DropsNothing(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	catID, uid := mkPracticeCategory(t, repo, ctx, "del_blocked_cat")
	defer func() { _ = repo.DeletePracticeCategory(ctx, catID, true) }()

	realSchema, err := repo.GetPracticeCategorySchema(ctx, catID)
	if err != nil {
		t.Fatalf("GetPracticeCategorySchema: %v", err)
	}
	previewSchema := PracticeCategoryPreviewSchemaName(catID)
	if err := repo.EnsureSchema(ctx, previewSchema); err != nil {
		t.Fatalf("EnsureSchema(%q): %v", previewSchema, err)
	}

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		CorrectSQL:  "SELECT 1",
		Type:        "QUERY",
		Requirement: "keep the category non-empty",
		Points:      1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}

	if err := repo.DeletePracticeCategory(ctx, catID, false); err == nil {
		t.Fatal("expected DeletePracticeCategory(force=false) with questions still present to fail")
	}

	if !schemaExists(t, repo, ctx, realSchema) {
		t.Errorf("real schema %q must NOT be dropped by a blocked delete", realSchema)
	}
	if !schemaExists(t, repo, ctx, previewSchema) {
		t.Errorf("preview schema %q must NOT be dropped by a blocked delete", previewSchema)
	}

	var catCount int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM app.practice_categories WHERE id = $1", catID).Scan(&catCount); err != nil {
		t.Fatalf("count practice_categories row: %v", err)
	}
	if catCount != 1 {
		t.Errorf("expected app.practice_categories row for %d to survive a blocked delete", catID)
	}
}

// TestDeletePracticeCategory_Protected_DropsNothing proves deleting the
// protected "Uncategorized" category is rejected and drops nothing, even
// though it uses the same cascading closure that now also drops a decoy
// preview schema (B6, second half).
func TestDeletePracticeCategory_Protected_DropsNothing(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	var uncategorizedID int
	var realSchema string
	if err := repo.pool.QueryRow(ctx,
		"SELECT id, schema_name FROM app.practice_categories WHERE name = 'Uncategorized'").Scan(&uncategorizedID, &realSchema); err != nil {
		t.Fatalf("look up Uncategorized category: %v", err)
	}

	if err := repo.DeletePracticeCategory(ctx, uncategorizedID, true); err == nil {
		t.Fatal("expected DeletePracticeCategory on Uncategorized to fail with ErrCategoryProtected")
	}

	if realSchema != "" && !schemaExists(t, repo, ctx, realSchema) {
		t.Errorf("Uncategorized's real schema %q must NOT be dropped by a rejected delete", realSchema)
	}

	var catCount int
	if err := repo.pool.QueryRow(ctx,
		"SELECT count(*) FROM app.practice_categories WHERE id = $1", uncategorizedID).Scan(&catCount); err != nil {
		t.Fatalf("count practice_categories row: %v", err)
	}
	if catCount != 1 {
		t.Fatal("expected Uncategorized category row to survive a rejected delete")
	}
}
