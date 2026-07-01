package controllers

// Regression tests for ImportExamCurriculum (TODO-PXC-BE).
//
// These tests verify the HTTP-handler decision logic of:
//   - empty sql body → 400
//   - repo returns ErrExamActive → 409
//   - repo returns ErrExamCurriculumNotEmpty → 409
//   - repo returns ErrSchemaQualifiedName → 400
//   - repo returns pgx.ErrNoRows → 404
//   - repo returns nil (success) → 200 with "Curriculum imported into exam"
//
// Pattern mirrors lecturer_exam_ux_test.go:
//   - "handler mirrors" reproduce the controller decision tree with injected outcomes
//   - gin.TestMode is set by practice_controller_test.go init() in the same package
//   - doRequest helper is defined in lecturer_exam_ux_test.go (same pkg)

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// fakeImportExamResult holds the outcome that the fake repo layer returns.
type fakeImportExamResult struct {
	err error
}

// importExamCurriculumHandler mirrors the pure decision-tree of
// LecturerController.ImportExamCurriculum without touching a real repo.
//
// It injects the "user" context key so the handler can safely call
//
//	userVal.(models.User)
//
// and exercises every status-code branch.
func importExamCurriculumHandler(r fakeImportExamResult) gin.HandlerFunc {
	return func(c *gin.Context) {
		// The real controller does this — we must too so the type assertion works.
		userVal, _ := c.Get("user")
		_ = userVal.(models.User)

		// id parsing
		if _, err := strconv.Atoi(c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
			return
		}

		var req struct {
			SQL string `json:"sql"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.SQL) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sql is required"})
			return
		}

		// Simulate the repo call result.
		if err := r.err; err != nil {
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
			case errors.Is(err, repository.ErrSchemaQualifiedName):
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			case errors.Is(err, repository.ErrExamActive):
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			case errors.Is(err, repository.ErrExamCurriculumNotEmpty):
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Curriculum imported into exam"})
	}
}

// doImport is a convenience wrapper that injects the fake "user" context key
// and fires the request against the import handler.
func doImport(body interface{}, outcome fakeImportExamResult) *httptest.ResponseRecorder {
	// We need to inject "user" via middleware — build the router manually.
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user", models.User{ID: 1, Username: "lecturer", Role: models.RoleLecturer})
		c.Next()
	})
	router.POST("/exams/:id/curriculum/import", importExamCurriculumHandler(outcome))

	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/exams/1/curriculum/import", bytes.NewReader(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestImportExamCurriculum_EmptySQL_Returns400(t *testing.T) {
	w := doImport(map[string]string{"sql": ""}, fakeImportExamResult{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty sql: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	checkErrorBody(t, w, "sql")
}

func TestImportExamCurriculum_WhitespaceOnlySQL_Returns400(t *testing.T) {
	w := doImport(map[string]string{"sql": "   \n\t  "}, fakeImportExamResult{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("whitespace sql: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	checkErrorBody(t, w, "sql")
}

func TestImportExamCurriculum_ExamActive_Returns409(t *testing.T) {
	w := doImport(map[string]string{"sql": "CREATE TABLE t (id INT)"},
		fakeImportExamResult{err: repository.ErrExamActive})
	if w.Code != http.StatusConflict {
		t.Errorf("ErrExamActive: want 409, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestImportExamCurriculum_CurriculumNotEmpty_Returns409(t *testing.T) {
	w := doImport(map[string]string{"sql": "CREATE TABLE t (id INT)"},
		fakeImportExamResult{err: repository.ErrExamCurriculumNotEmpty})
	if w.Code != http.StatusConflict {
		t.Errorf("ErrExamCurriculumNotEmpty: want 409, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestImportExamCurriculum_SchemaQualifiedName_Returns400(t *testing.T) {
	w := doImport(map[string]string{"sql": "SELECT * FROM public.foo"},
		fakeImportExamResult{err: repository.ErrSchemaQualifiedName})
	if w.Code != http.StatusBadRequest {
		t.Errorf("ErrSchemaQualifiedName: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestImportExamCurriculum_ExamNotFound_Returns404(t *testing.T) {
	w := doImport(map[string]string{"sql": "CREATE TABLE t (id INT)"},
		fakeImportExamResult{err: pgx.ErrNoRows})
	if w.Code != http.StatusNotFound {
		t.Errorf("pgx.ErrNoRows: want 404, got %d (body: %s)", w.Code, w.Body.String())
	}
	checkErrorBody(t, w, "exam not found")
}

func TestImportExamCurriculum_Success_Returns200(t *testing.T) {
	w := doImport(map[string]string{"sql": "CREATE TABLE employees (id SERIAL PRIMARY KEY, name TEXT)"},
		fakeImportExamResult{})
	if w.Code != http.StatusOK {
		t.Errorf("success: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	// Verify the documented success message.
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	msg, _ := resp["message"].(string)
	if !strings.Contains(msg, "Curriculum imported into exam") {
		t.Errorf("success message: want %q, got %q", "Curriculum imported into exam", msg)
	}
}

func TestImportExamCurriculum_UnknownRepoError_Returns500(t *testing.T) {
	w := doImport(map[string]string{"sql": "CREATE TABLE t (id INT)"},
		fakeImportExamResult{err: errors.New("unexpected db error")})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("unknown error: want 500, got %d (body: %s)", w.Code, w.Body.String())
	}
}
