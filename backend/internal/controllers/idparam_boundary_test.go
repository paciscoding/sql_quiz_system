package controllers

// HTTP boundary tests for idParam (ARCH-008/ARCH-013): a request to an
// id-bearing route with a wrong-type token or a tampered token must return
// 400, never a 500 and never route to the wrong record. These exercise the
// real controllers + real routes (not handler mirrors), routed through
// idParam itself, but stop short of touching the database — every handler
// under test here calls idParam before it ever reaches ctrl.repo/ctrl.
// authService, so a nil-backed controller is safe as long as the token is
// rejected up front.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// doPlain fires a bodyless request against router and returns the recorder.
func doPlain(router *gin.Engine, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// User route: DELETE /api/v1/users/:id
// ─────────────────────────────────────────────────────────────────────────────

func userBoundaryRouter() *gin.Engine {
	ctrl := NewUserController(nil, nil)
	r := gin.New()
	r.DELETE("/api/v1/users/:id", ctrl.DeleteUser)
	return r
}

func TestDeleteUser_WrongTypeToken_Returns400(t *testing.T) {
	r := userBoundaryRouter()
	// A validly-minted token, but for the wrong resource type (exam, not user).
	examToken := publicid.Encode(publicid.TypeExam, 5)

	w := doPlain(r, http.MethodDelete, "/api/v1/users/"+examToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong-type token: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestDeleteUser_TamperedToken_Returns400(t *testing.T) {
	r := userBoundaryRouter()
	token := publicid.Encode(publicid.TypeUser, 42)
	tampered := tamperToken(t, token)

	w := doPlain(r, http.MethodDelete, "/api/v1/users/"+tampered)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("tampered token: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestDeleteUser_ValidUserToken_PassesIdParam(t *testing.T) {
	// A well-formed, correctly-typed token must clear idParam (i.e. must NOT
	// 400 for that reason) even though the downstream authService is nil and
	// will panic — we only assert it panics PAST idParam, proving idParam
	// itself accepted the token and decoded it.
	r := userBoundaryRouter()
	token := publicid.Encode(publicid.TypeUser, 42)

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatalf("expected a panic from the nil authService downstream of idParam (proves idParam accepted the valid token and let the request through)")
		}
	}()
	doPlain(r, http.MethodDelete, "/api/v1/users/"+token)
}

// ─────────────────────────────────────────────────────────────────────────────
// Exam route: PUT /api/v1/lecturer/exams/:id (rename)
// ─────────────────────────────────────────────────────────────────────────────

func examBoundaryRouter() *gin.Engine {
	ctrl := NewLecturerController(nil, nil)
	r := gin.New()
	r.PUT("/api/v1/lecturer/exams/:id", ctrl.RenameExamination)
	return r
}

func TestRenameExamination_WrongTypeToken_Returns400(t *testing.T) {
	r := examBoundaryRouter()
	// A validly-minted token, but for the wrong resource type (user, not exam).
	userToken := publicid.Encode(publicid.TypeUser, 7)

	w := doPlain(r, http.MethodPut, "/api/v1/lecturer/exams/"+userToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("wrong-type token: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestRenameExamination_TamperedToken_Returns400(t *testing.T) {
	r := examBoundaryRouter()
	token := publicid.Encode(publicid.TypeExam, 9)
	tampered := tamperToken(t, token)

	w := doPlain(r, http.MethodPut, "/api/v1/lecturer/exams/"+tampered)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("tampered token: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestRenameExamination_TruncatedToken_Returns400(t *testing.T) {
	r := examBoundaryRouter()
	token := publicid.Encode(publicid.TypeExam, 9)
	truncated := token[:len(token)-2]

	w := doPlain(r, http.MethodPut, "/api/v1/lecturer/exams/"+truncated)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("truncated token: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// tamperToken flips one character in the token body (after the one-byte type
// prefix), preserving length, and fails the test if it cannot construct a
// token that differs from the original.
func tamperToken(t *testing.T, token string) string {
	t.Helper()
	if len(token) < 3 {
		t.Fatalf("token too short to tamper meaningfully: %q", token)
	}
	body := []byte(token)
	orig := body[1]
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] != orig {
			body[1] = alphabet[i]
			break
		}
	}
	tampered := string(body)
	if tampered == token {
		t.Fatalf("failed to construct a tampered token distinct from %q", token)
	}
	return tampered
}
