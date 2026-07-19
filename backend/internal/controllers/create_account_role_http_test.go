package controllers

// Regression test for ARCH-004 (POST /api/v1/users narrowed to RoleAdmin
// only): a lecturer JWT must now get 403 (previously allowed), while an admin
// JWT still succeeds. Mirrors the HTTP-integration pattern in
// password_http_integration_test.go. Requires TEST_DATABASE_URL; skips
// cleanly via openCtrlTestRepo -> testsupport.SharedRepo when absent.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// buildCreateAccountRouter wires exactly /login + POST /users with the real
// middleware stack, mirroring main.go's `staff.POST("/users", RoleMiddleware(
// models.RoleAdmin), userCtrl.CreateAccount)` registration (ARCH-004).
func buildCreateAccountRouter(auth *services.AuthService, repo *repository.PostgresRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)
	staff := v1.Group("/")
	staff.Use(AuthMiddleware(auth))
	{
		staff.POST("/users", RoleMiddleware(models.RoleAdmin), userCtrl.CreateAccount)
	}
	return r
}

func postJSON(r *gin.Engine, path, token string, body interface{}) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCreateAccount_LecturerForbidden_AdminAllowed(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildCreateAccountRouter(auth, repo)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")

	adminToken := login(t, r, admin.Username, "adminpw")
	lecToken := login(t, r, lecturer.Username, "lecpw")

	newLecturerUsername := fmt.Sprintf("new_staff_%d", time.Now().UnixNano())

	// Lecturer -> 403 (this is the ARCH-004 tightening; used to be allowed).
	w := postJSON(r, "/api/v1/users", lecToken, map[string]string{
		"username": newLecturerUsername + "_denied",
		"role":     string(models.RoleLecturer),
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("lecturer POST /users: want 403, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Admin -> not-403 (201 Created with a generated password on success).
	w = postJSON(r, "/api/v1/users", adminToken, map[string]string{
		"username": newLecturerUsername,
		"role":     string(models.RoleLecturer),
	})
	if w.Code == http.StatusForbidden {
		t.Fatalf("admin POST /users: got 403, want not-403 (body: %s)", w.Body.String())
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("admin POST /users: want 201, got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp struct {
		ID                string `json:"id"`
		GeneratedPassword string `json:"generated_password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID == "" || resp.GeneratedPassword == "" {
		t.Fatalf("expected id + generated_password in response, got %+v", resp)
	}
}

// TestCreateAccount_StudentRoleStillRejected pins that /students (not /users)
// remains the correct path for student creation — untouched by ARCH-004, this
// just guards against a regression where the role-narrowing accidentally
// changed CreateAccount's existing student-role rejection.
func TestCreateAccount_StudentRoleStillRejected(t *testing.T) {
	repo := openCtrlTestRepo(t)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	r := buildCreateAccountRouter(auth, repo)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw3")
	adminToken := login(t, r, admin.Username, "adminpw3")

	w := postJSON(r, "/api/v1/users", adminToken, map[string]string{
		"username": fmt.Sprintf("student_via_users_%d", time.Now().UnixNano()),
		"role":     string(models.RoleStudent),
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("admin POST /users role=STUDENT: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}
