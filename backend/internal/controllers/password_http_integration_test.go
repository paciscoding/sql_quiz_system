package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
	"github.com/paciscoding/sql-quiz-system/internal/testsupport"
	"golang.org/x/crypto/bcrypt"
)

const ctrlTestKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func openCtrlTestRepo(t *testing.T) *repository.PostgresRepository {
	t.Helper()
	return testsupport.SharedRepo(t)
}

func mkCtrlUser(t *testing.T, repo *repository.PostgresRepository, role models.Role, pw string) models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("http_%s_%d", role, time.Now().UnixNano())
	id, err := repo.CreateUser(context.Background(), models.User{Username: username, Password: string(hash), Role: role})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return models.User{ID: models.UserID(id), Username: username, Role: role}
}

func buildAuthRouter(auth *services.AuthService, userCtrl *UserController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/login", userCtrl.Login)
	user := v1.Group("/users")
	user.Use(AuthMiddleware(auth))
	{
		user.POST("/password", userCtrl.ChangePassword)
		admin := user.Group("/")
		admin.Use(RoleMiddleware(models.RoleAdmin))
		{
			admin.GET("/all", userCtrl.GetAllUsers)
			admin.POST("/:id/reset-password", userCtrl.ResetPassword)
			admin.GET("/:id/password-events", userCtrl.GetPasswordEvents)
		}
	}
	return r
}

func login(t *testing.T, r *gin.Engine, username, pw string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": pw})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed for %q: %d %s", username, w.Code, w.Body.String())
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res.Token
}

func do(r *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestResetPassword_AdminOnly_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildAuthRouter(auth, userCtrl)

	_ = mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw2")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	target := mkCtrlUser(t, repo, models.RoleLecturer, "targetpw")

	adminToken := login(t, r, admin.Username, "adminpw2")
	lecToken := login(t, r, lecturer.Username, "lecpw")

	// Non-admin → 403.
	w := do(r, http.MethodPost, fmt.Sprintf("/api/v1/users/%s/reset-password", publicid.Encode(publicid.TypeUser, int(target.ID))), lecToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for lecturer reset, got %d %s", w.Code, w.Body.String())
	}
	// Admin → 200 with generated_password.
	w = do(r, http.MethodPost, fmt.Sprintf("/api/v1/users/%s/reset-password", publicid.Encode(publicid.TypeUser, int(target.ID))), adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin reset, got %d %s", w.Code, w.Body.String())
	}
	var body struct {
		GeneratedPassword string `json:"generated_password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.GeneratedPassword) != services.PasswordLength {
		t.Fatalf("expected generated password, got %q", body.GeneratedPassword)
	}
}

func TestPasswordEvents_AdminOnly_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildAuthRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	lecturer := mkCtrlUser(t, repo, models.RoleLecturer, "lecpw")
	target := mkCtrlUser(t, repo, models.RoleLecturer, "targetpw")

	adminToken := login(t, r, admin.Username, "adminpw")
	lecToken := login(t, r, lecturer.Username, "lecpw")

	// Seed an event via a reset.
	if _, err := auth.ResetPassword(context.Background(), admin, int(target.ID), false); err != nil {
		t.Fatal(err)
	}

	// Non-admin → 403.
	w := do(r, http.MethodGet, fmt.Sprintf("/api/v1/users/%s/password-events", publicid.Encode(publicid.TypeUser, int(target.ID))), lecToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for lecturer changelog, got %d", w.Code)
	}
	// Admin → 200, key_configured true, decrypted value present.
	w = do(r, http.MethodGet, fmt.Sprintf("/api/v1/users/%s/password-events", publicid.Encode(publicid.TypeUser, int(target.ID))), adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin changelog, got %d %s", w.Code, w.Body.String())
	}
	var body struct {
		KeyConfigured bool `json:"key_configured"`
		Events        []struct {
			Source      string `json:"source"`
			NewPassword string `json:"new_password"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.KeyConfigured {
		t.Fatalf("expected key_configured=true")
	}
	if len(body.Events) != 1 || body.Events[0].Source != models.PasswordEventAdminReset || body.Events[0].NewPassword == "" {
		t.Fatalf("expected 1 decrypted admin_reset event, got %+v", body.Events)
	}
}

// TestTokenInvalidation_HTTP proves an authenticated token is rejected (401)
// after an admin reset bumps token_version, and a freshly issued token works.
func TestTokenInvalidation_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))
	r := buildAuthRouter(auth, userCtrl)

	admin := mkCtrlUser(t, repo, models.RoleAdmin, "adminpw")
	victim := mkCtrlUser(t, repo, models.RoleLecturer, "victimpw")

	adminToken := login(t, r, admin.Username, "adminpw")
	oldToken := login(t, r, victim.Username, "victimpw")

	// Authenticated but wrong role → 403 (proves the token passed AuthMiddleware).
	if w := do(r, http.MethodGet, "/api/v1/users/all", oldToken); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (authenticated non-admin) before reset, got %d", w.Code)
	}

	// Admin resets the victim → bumps token_version.
	w := do(r, http.MethodPost, fmt.Sprintf("/api/v1/users/%s/reset-password", publicid.Encode(publicid.TypeUser, int(victim.ID))), adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("reset failed: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		GeneratedPassword string `json:"generated_password"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)

	// Old token now rejected at the middleware → 401.
	if w := do(r, http.MethodGet, "/api/v1/users/all", oldToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for old token after reset, got %d", w.Code)
	}

	// A freshly issued token (login with the new password) is authenticated → 403.
	newToken := login(t, r, victim.Username, body.GeneratedPassword)
	if w := do(r, http.MethodGet, "/api/v1/users/all", newToken); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (authenticated) for fresh token, got %d", w.Code)
	}
}
