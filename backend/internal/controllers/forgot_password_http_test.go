package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"golang.org/x/crypto/bcrypt"
)

// TestForgotPassword_GenericResponse_HTTP proves the public endpoint returns an
// identical 200 body whether the username exists or not (no user enumeration via
// status code or body).
func TestForgotPassword_GenericResponse_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Public route: NO auth middleware.
	r.POST("/api/v1/forgot-password", userCtrl.ForgotPassword)

	// An existing user (with no email → nothing actually sent, but response identical).
	existing := mkCtrlUser(t, repo, models.RoleLecturer, "somepw")

	post := func(username string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": username})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/forgot-password", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	wExisting := post(existing.Username)
	wMissing := post("no_such_user_here_xyz")

	if wExisting.Code != http.StatusOK || wMissing.Code != http.StatusOK {
		t.Fatalf("expected 200 for both, got existing=%d missing=%d", wExisting.Code, wMissing.Code)
	}
	if wExisting.Body.String() != wMissing.Body.String() {
		t.Fatalf("response bodies differ (enumeration leak):\n existing=%s\n missing=%s",
			wExisting.Body.String(), wMissing.Body.String())
	}
	// Body must be the generic message.
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(wExisting.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Message == "" {
		t.Fatalf("expected a generic message in the response body")
	}
}

// newGatedForgotPasswordRouter wires the two PUBLIC forgot-password routes
// exactly as cmd/main.go does — behind PasswordResetEmailGate, whose enabled
// flag is read from the environment at registration time.
func newGatedForgotPasswordRouter(t *testing.T, userCtrl *UserController) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	gate := PasswordResetEmailGate(PasswordResetEmailEnabled())
	r.POST("/api/v1/forgot-password", gate, userCtrl.ForgotPassword)
	r.POST("/api/v1/forgot-password/verify", gate, userCtrl.VerifyForgotPassword)
	return r
}

// TestForgotPasswordGate_HTTP proves the PASSWORD_RESET_EMAIL_ENABLED gate:
// unset (the production default) both public routes are a constant 404 that
// never reaches the handlers, and explicitly enabling it restores the existing
// generic-200 / generic-400 behavior.
func TestForgotPasswordGate_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))

	existing := mkCtrlUser(t, repo, models.RoleLecturer, "somepw")

	post := func(r *gin.Engine, path string, payload map[string]string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("disabled by default", func(t *testing.T) {
		// Explicitly empty rather than merely absent: t.Setenv also restores any
		// value a developer happens to have exported in their shell.
		t.Setenv(PasswordResetEmailEnabledEnv, "")
		r := newGatedForgotPasswordRouter(t, userCtrl)

		cases := []struct {
			path    string
			payload map[string]string
		}{
			{"/api/v1/forgot-password", map[string]string{"username": existing.Username}},
			{"/api/v1/forgot-password", map[string]string{"username": "no_such_user_here_xyz"}},
			{"/api/v1/forgot-password/verify", map[string]string{"username": existing.Username, "otp": "000000"}},
			{"/api/v1/forgot-password/verify", map[string]string{"username": "no_such_user_here_xyz", "otp": "000000"}},
		}
		const want = `{"error":"Password reset by email is unavailable. Please contact your administrator to have your password reset."}`
		for _, tc := range cases {
			w := post(r, tc.path, tc.payload)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %v: expected 404, got %d %s", tc.path, tc.payload, w.Code, w.Body.String())
			}
			// The body must be byte-identical for every username: the gate must
			// not become an enumeration oracle the live handlers carefully avoid.
			if got := w.Body.String(); got != want {
				t.Fatalf("%s %v: body\n got=%s\nwant=%s", tc.path, tc.payload, got, want)
			}
		}
	})

	t.Run("explicitly enabled preserves existing behavior", func(t *testing.T) {
		t.Setenv(PasswordResetEmailEnabledEnv, "true")
		r := newGatedForgotPasswordRouter(t, userCtrl)

		// Step 1: generic 200, identical for existing and unknown usernames.
		wExisting := post(r, "/api/v1/forgot-password", map[string]string{"username": existing.Username})
		wMissing := post(r, "/api/v1/forgot-password", map[string]string{"username": "no_such_user_here_xyz"})
		if wExisting.Code != http.StatusOK || wMissing.Code != http.StatusOK {
			t.Fatalf("expected 200 for both, got existing=%d missing=%d", wExisting.Code, wMissing.Code)
		}
		if wExisting.Body.String() != wMissing.Body.String() {
			t.Fatalf("bodies differ (enumeration leak):\n existing=%s\n missing=%s",
				wExisting.Body.String(), wMissing.Body.String())
		}
		var parsed struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(wExisting.Body.Bytes(), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Message != forgotPasswordMessage {
			t.Fatalf("expected the generic step-1 message, got %q", parsed.Message)
		}

		// Step 2: generic 400 on a bad code (the handler ran, not the gate).
		wVerify := post(r, "/api/v1/forgot-password/verify",
			map[string]string{"username": existing.Username, "otp": "000000"})
		if wVerify.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for a wrong code, got %d %s", wVerify.Code, wVerify.Body.String())
		}
		var parsedErr struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(wVerify.Body.Bytes(), &parsedErr); err != nil {
			t.Fatal(err)
		}
		if parsedErr.Error != services.ErrInvalidResetCode.Error() {
			t.Fatalf("expected the generic verify error, got %q", parsedErr.Error)
		}
	})
}

// TestPasswordResetEmailEnabled_Parsing pins the fail-closed parse: only an
// explicitly truthy value turns the public flow on.
func TestPasswordResetEmailEnabled_Parsing(t *testing.T) {
	cases := map[string]bool{
		"":      false,
		"false": false,
		"0":     false,
		"no":    false, // not a Go bool literal → disabled, not an error
		"yes":   false,
		"maybe": false,
		"true":  true,
		"TRUE":  true,
		"1":     true,
		"t":     true,
	}
	for v, want := range cases {
		t.Setenv(PasswordResetEmailEnabledEnv, v)
		if got := PasswordResetEmailEnabled(); got != want {
			t.Errorf("%s=%q: got %v, want %v", PasswordResetEmailEnabledEnv, v, got, want)
		}
	}
}

// mkCtrlUserWithEmail creates a user with an email on file (required for the
// forgot-password verify path).
func mkCtrlUserWithEmail(t *testing.T, repo *repository.PostgresRepository, role models.Role, pw, email string) models.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("http_fp_%s_%d", role, time.Now().UnixNano())
	id, err := repo.CreateUser(context.Background(), models.User{Username: username, Password: string(hash), Role: role, Email: &email})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return models.User{ID: models.UserID(id), Username: username, Role: role}
}

// TestVerifyForgotPassword_HTTP proves the public verify endpoint: every
// failure (malformed body, unknown user, wrong code) is an IDENTICAL generic
// 400, and the correct code returns 200 with the success message.
func TestVerifyForgotPassword_HTTP(t *testing.T) {
	repo := openCtrlTestRepo(t)
	t.Setenv(services.PasswordCryptoEnvKey, ctrlTestKey)
	auth := services.NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	userCtrl := NewUserController(auth, services.NewStudentImportService(repo))

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Public routes: NO auth middleware.
	r.POST("/api/v1/forgot-password", userCtrl.ForgotPassword)
	r.POST("/api/v1/forgot-password/verify", userCtrl.VerifyForgotPassword)

	user := mkCtrlUserWithEmail(t, repo, models.RoleLecturer, "somepw", "verify@example.com")

	// Seed a known code directly (the SHA-256 hex hash, as the service stores it).
	const otp = "135791"
	sum := sha256.Sum256([]byte(otp))
	if err := repo.SetResetOTP(context.Background(), int(user.ID), hex.EncodeToString(sum[:]), time.Now().Add(10*time.Minute)); err != nil {
		t.Fatalf("SetResetOTP: %v", err)
	}

	postVerify := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/forgot-password/verify", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	verify := func(username, otp string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": username, "otp": otp})
		return postVerify(body)
	}

	// Wrong code → 400 generic.
	wWrong := verify(user.Username, "000000")
	if wWrong.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for wrong code, got %d %s", wWrong.Code, wWrong.Body.String())
	}
	// Unknown username → 400 with the IDENTICAL body (no enumeration).
	wUnknown := verify("no_such_user_here_xyz", "000000")
	if wUnknown.Code != http.StatusBadRequest || wUnknown.Body.String() != wWrong.Body.String() {
		t.Fatalf("verify failure bodies differ (enumeration leak):\n wrong=%s\n unknown=%s",
			wWrong.Body.String(), wUnknown.Body.String())
	}
	// Malformed JSON → 400 with the same generic body.
	wBad := postVerify([]byte("{not json"))
	if wBad.Code != http.StatusBadRequest || wBad.Body.String() != wWrong.Body.String() {
		t.Fatalf("malformed-body response differs from generic failure:\n bad=%s\n wrong=%s",
			wBad.Body.String(), wWrong.Body.String())
	}
	var parsedErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(wWrong.Body.Bytes(), &parsedErr); err != nil {
		t.Fatal(err)
	}
	if parsedErr.Error != "invalid or expired code" {
		t.Fatalf("expected the generic error body, got %q", parsedErr.Error)
	}

	// Correct code → 200 with the success message.
	wOK := verify(user.Username, otp)
	if wOK.Code != http.StatusOK {
		t.Fatalf("expected 200 for the correct code, got %d %s", wOK.Code, wOK.Body.String())
	}
	var parsedOK struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(wOK.Body.Bytes(), &parsedOK); err != nil {
		t.Fatal(err)
	}
	if parsedOK.Message == "" {
		t.Fatalf("expected a success message, got %s", wOK.Body.String())
	}

	// Replay of the used code → 400 again (code cleared synchronously).
	if w := verify(user.Username, otp); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on replay, got %d %s", w.Code, w.Body.String())
	}
}
