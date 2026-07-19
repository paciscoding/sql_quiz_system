package controllers

// Regression tests for ARCH-006 (GET /api/v1/lecturer/practice/results):
//   - role gate: RoleStudent -> 403, RoleLecturer -> 200
//   - response shape: rows carry category_name + question_title (JSON field
//     names must match models.LecturerPracticeResultRow exactly, since
//     LecturerResults.vue's local interface is contract-frozen against it)
//
// Mirrors the HTTP-integration pattern in password_http_integration_test.go
// (openCtrlTestRepo/mkCtrlUser/login, real AuthMiddleware+RoleMiddleware).
// Requires TEST_DATABASE_URL; skips cleanly via openCtrlTestRepo ->
// testsupport.SharedRepo when absent.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// buildPracticeResultsRouter wires exactly the /login + GET
// /lecturer/practice/results routes with the real middleware stack, mirroring
// main.go's practiceLecturer group registration.
func buildPracticeResultsRouter(auth *services.AuthService, repo *repository.PostgresRepository, lecturerCtrl *LecturerController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", NewUserController(auth, services.NewStudentImportService(repo)).Login)
	practiceLecturer := v1.Group("/lecturer/practice")
	practiceLecturer.Use(AuthMiddleware(auth), RoleMiddleware(models.RoleAdmin, models.RoleLecturer))
	{
		practiceLecturer.GET("/results", lecturerCtrl.GetPracticeResults)
	}
	return r
}

func TestGetPracticeResults_RoleGate_StudentForbidden_LecturerOK(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	lecturerCtrl := NewLecturerController(repo, nil)
	r := buildPracticeResultsRouter(auth, repo, lecturerCtrl)

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	student := mkCtrlUser(t, repo, models.RoleStudent, "studpw")

	lecToken := login(t, r, lecturer.Username, "lecpw")
	studentToken := login(t, r, student.Username, "studpw")

	// Student -> 403 (RoleMiddleware rejects RoleStudent for this group).
	w := do(r, http.MethodGet, "/api/v1/lecturer/practice/results", studentToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("student GET /lecturer/practice/results: want 403, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Lecturer -> 200 with a JSON array body.
	w = do(r, http.MethodGet, "/api/v1/lecturer/practice/results", lecToken)
	if w.Code != http.StatusOK {
		t.Fatalf("lecturer GET /lecturer/practice/results: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal rows: %v (body: %s)", err, w.Body.String())
	}
	// Not asserting non-empty here (shared DB may have zero attempted pairs);
	// the data-shape assertion lives in TestGetPracticeResults_RowShape_HasCategoryAndTitle.
}

// TestGetPracticeResults_RowShape_HasCategoryAndTitle seeds a real attempted
// pair through the repo and asserts the HTTP response row carries
// category_name and question_title (not just that the repo-level query does,
// per postgres_lecturer_practice_results_test.go — this proves the full
// handler -> JSON encoding path preserves those fields under their frozen
// json tags).
func TestGetPracticeResults_RowShape_HasCategoryAndTitle(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	lecturerCtrl := NewLecturerController(repo, nil)
	r := buildPracticeResultsRouter(auth, repo, lecturerCtrl)

	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw2")
	lecToken := login(t, r, lecturer.Username, "lecpw2")

	ctx := context.Background()
	// Category name and question title must be unique per run (practice_categories.name
	// has a UNIQUE constraint) — a shared TEST_DATABASE_URL persists across runs.
	catName := fmt.Sprintf("HTTP Shape Category %d", time.Now().UnixNano())
	qTitle := fmt.Sprintf("HTTP Shape Question %d", time.Now().UnixNano())
	cat, err := repo.CreatePracticeCategory(ctx, catName, int(lecturer.ID))
	if err != nil {
		t.Fatalf("CreatePracticeCategory: %v", err)
	}
	q := &models.PracticeQuestion{
		CreatedBy: lecturer.ID, CategoryID: cat.ID,
		CorrectSQL: "SELECT 1", Type: "QUERY", Requirement: "shape test", Points: 1,
		Title: qTitle,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion: %v", err)
	}
	student := mkCtrlUser(t, repo, models.RoleStudent, "studpw2")
	if err := repo.CreatePracticeAttempt(ctx, &models.PracticeAttempt{
		StudentID: student.ID, PracticeQuestionID: q.ID, SQLText: "SELECT 1", IsCorrect: true,
	}); err != nil {
		t.Fatalf("CreatePracticeAttempt: %v", err)
	}

	w := do(r, http.MethodGet, "/api/v1/lecturer/practice/results", lecToken)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var rows []struct {
		CategoryName  string  `json:"category_name"`
		QuestionTitle string  `json:"question_title"`
		IsSolved      bool    `json:"is_solved"`
		AttemptCount  int     `json:"attempt_count"`
		EarnedPoints  float64 `json:"earned_points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	found := false
	for _, row := range rows {
		if row.QuestionTitle == qTitle {
			found = true
			if row.CategoryName != catName {
				t.Fatalf("category_name = %q, want %q", row.CategoryName, catName)
			}
			if !row.IsSolved || row.AttemptCount != 1 || row.EarnedPoints != 1 {
				t.Fatalf("aggregate fields wrong: is_solved=%v attempt_count=%d earned_points=%v",
					row.IsSolved, row.AttemptCount, row.EarnedPoints)
			}
		}
	}
	if !found {
		t.Fatalf("expected a row with question_title=%q in response, got %d rows", qTitle, len(rows))
	}
}
