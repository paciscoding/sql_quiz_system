package controllers

// Regression tests for the "lecturer multi-exam UX" feature (TODO-001).
//
// These tests verify the pure HTTP-handler decision logic of:
//   - RenameExamination: empty/whitespace title → 400; valid title → 200
//   - DeleteExamination: active exam → 409; inactive → 200; not found → 404
//
// Pattern follows practice_controller_test.go:
//   - "handler mirrors" reproduce the controller logic with injected fake outcomes
//   - gin.TestMode + httptest.Recorder; no live DB needed
//   - init() gin.SetMode already called by practice_controller_test.go (same pkg)

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// ─────────────────────────────────────────────────────────────────────────────
// Injected-outcome types
// ─────────────────────────────────────────────────────────────────────────────

type fakeRenameResult struct{ err error }

type fakeDeleteResult struct {
	exam   models.Examination
	getErr error
	delErr error
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler mirrors
// ─────────────────────────────────────────────────────────────────────────────

// renameExamHandler mirrors the validation + delegation path of
// LecturerController.RenameExamination without *PostgresRepository.
func renameExamHandler(r fakeRenameResult) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := strconv.Atoi(c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
			return
		}

		var req struct {
			Title string `json:"title"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		title := strings.TrimSpace(req.Title)
		if title == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
			return
		}

		if r.err != nil {
			if errors.Is(r.err, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": r.err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Exam renamed"})
	}
}

// deleteExamHandler mirrors the active-guard + delegation path of
// LecturerController.DeleteExamination.
func deleteExamHandler(r fakeDeleteResult) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := strconv.Atoi(c.Param("id")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
			return
		}

		if r.getErr != nil {
			if errors.Is(r.getErr, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": r.getErr.Error()})
			return
		}

		// This is the exact guard from the real controller.
		if repository.ExaminationEffectiveState(r.exam).ActiveNow {
			c.JSON(http.StatusConflict, gin.H{"error": "deactivate the exam before deleting"})
			return
		}

		if r.delErr != nil {
			if errors.Is(r.delErr, pgx.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": r.delErr.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Exam deleted"})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper: fire a request against a single-route test router
// ─────────────────────────────────────────────────────────────────────────────

func doRequest(method, path string, body interface{}, handler gin.HandlerFunc, routeParam string) *httptest.ResponseRecorder {
	router := gin.New()
	router.Handle(method, routeParam, handler)
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, bytes.NewReader(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────────
// RenameExamination tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRenameExamination_EmptyTitle_Returns400(t *testing.T) {
	w := doRequest("PUT", "/exams/1", map[string]string{"title": ""},
		renameExamHandler(fakeRenameResult{}), "/exams/:id")

	if w.Code != http.StatusBadRequest {
		t.Errorf("empty title: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	checkErrorBody(t, w, "title")
}

func TestRenameExamination_WhitespaceOnlyTitle_Returns400(t *testing.T) {
	w := doRequest("PUT", "/exams/1", map[string]string{"title": "   "},
		renameExamHandler(fakeRenameResult{}), "/exams/:id")

	if w.Code != http.StatusBadRequest {
		t.Errorf("whitespace title: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	checkErrorBody(t, w, "title")
}

func TestRenameExamination_ValidTitle_Returns200(t *testing.T) {
	w := doRequest("PUT", "/exams/1", map[string]string{"title": "Midterm Exam"},
		renameExamHandler(fakeRenameResult{}), "/exams/:id")

	if w.Code != http.StatusOK {
		t.Errorf("valid title: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestRenameExamination_ValidTitle_LeadingTrailingSpace_Returns200(t *testing.T) {
	// Title "  Hello  " trimmed → "Hello" (non-empty) → should succeed
	w := doRequest("PUT", "/exams/1", map[string]string{"title": "  Hello  "},
		renameExamHandler(fakeRenameResult{}), "/exams/:id")

	if w.Code != http.StatusOK {
		t.Errorf("padded title: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestRenameExamination_RepoNotFound_Returns404(t *testing.T) {
	w := doRequest("PUT", "/exams/999", map[string]string{"title": "New Name"},
		renameExamHandler(fakeRenameResult{err: pgx.ErrNoRows}), "/exams/:id")

	if w.Code != http.StatusNotFound {
		t.Errorf("repo not-found: want 404, got %d", w.Code)
	}
}

func TestRenameExamination_RepoInternalError_Returns500(t *testing.T) {
	w := doRequest("PUT", "/exams/1", map[string]string{"title": "New Name"},
		renameExamHandler(fakeRenameResult{err: errors.New("db down")}), "/exams/:id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("repo error: want 500, got %d", w.Code)
	}
}

func TestRenameExamination_InvalidID_Returns400(t *testing.T) {
	w := doRequest("PUT", "/exams/notanint", map[string]string{"title": "Fine Title"},
		renameExamHandler(fakeRenameResult{}), "/exams/:id")

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid id: want 400, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteExamination tests
// ─────────────────────────────────────────────────────────────────────────────

func ptrTime(t time.Time) *time.Time { return &t }

func TestDeleteExamination_ActiveExam_Returns409(t *testing.T) {
	// is_active=true, no schedule → ActiveNow=true → must be blocked
	activeExam := models.Examination{IsActive: true}
	w := doRequest("DELETE", "/exams/1", nil,
		deleteExamHandler(fakeDeleteResult{exam: activeExam}), "/exams/:id")

	if w.Code != http.StatusConflict {
		t.Errorf("active exam delete: want 409, got %d (body: %s)", w.Code, w.Body.String())
	}
	checkErrorBody(t, w, "deactivate")
}

func TestDeleteExamination_ActiveExamWithSchedule_Returns409(t *testing.T) {
	// is_active=true, window currently open → ActiveNow=true → 409
	now := time.Now().UTC()
	scheduledExam := models.Examination{
		IsActive:         true,
		ScheduledStartAt: ptrTime(now.Add(-1 * time.Hour)),
		ScheduledEndAt:   ptrTime(now.Add(1 * time.Hour)),
	}
	w := doRequest("DELETE", "/exams/2", nil,
		deleteExamHandler(fakeDeleteResult{exam: scheduledExam}), "/exams/:id")

	if w.Code != http.StatusConflict {
		t.Errorf("scheduled-active exam delete: want 409, got %d", w.Code)
	}
}

func TestDeleteExamination_InactiveExam_Returns200(t *testing.T) {
	inactiveExam := models.Examination{IsActive: false}
	w := doRequest("DELETE", "/exams/1", nil,
		deleteExamHandler(fakeDeleteResult{exam: inactiveExam}), "/exams/:id")

	if w.Code != http.StatusOK {
		t.Errorf("inactive exam delete: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestDeleteExamination_EndedExam_Returns200(t *testing.T) {
	// is_active=true but window already closed → ActiveNow=false → allowed
	now := time.Now().UTC()
	endedExam := models.Examination{
		IsActive:         true,
		ScheduledStartAt: ptrTime(now.Add(-4 * time.Hour)),
		ScheduledEndAt:   ptrTime(now.Add(-1 * time.Hour)),
	}
	w := doRequest("DELETE", "/exams/3", nil,
		deleteExamHandler(fakeDeleteResult{exam: endedExam}), "/exams/:id")

	if w.Code != http.StatusOK {
		t.Errorf("ended exam delete: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestDeleteExamination_ExamNotFound_Returns404(t *testing.T) {
	w := doRequest("DELETE", "/exams/999", nil,
		deleteExamHandler(fakeDeleteResult{getErr: pgx.ErrNoRows}), "/exams/:id")

	if w.Code != http.StatusNotFound {
		t.Errorf("not found: want 404, got %d", w.Code)
	}
}

func TestDeleteExamination_InvalidID_Returns400(t *testing.T) {
	w := doRequest("DELETE", "/exams/notanint", nil,
		deleteExamHandler(fakeDeleteResult{}), "/exams/:id")

	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid id: want 400, got %d", w.Code)
	}
}

func TestDeleteExamination_RepoDeleteError_Returns500(t *testing.T) {
	inactiveExam := models.Examination{IsActive: false}
	w := doRequest("DELETE", "/exams/1", nil,
		deleteExamHandler(fakeDeleteResult{exam: inactiveExam, delErr: errors.New("tx error")}), "/exams/:id")

	if w.Code != http.StatusInternalServerError {
		t.Errorf("repo delete error: want 500, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper: assert the JSON body has an "error" field containing substr
// ─────────────────────────────────────────────────────────────────────────────

func checkErrorBody(t *testing.T, w *httptest.ResponseRecorder, substr string) {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}
	errMsg, ok := resp["error"].(string)
	if !ok {
		t.Errorf("response has no string 'error' field: %v", resp)
		return
	}
	if !strings.Contains(strings.ToLower(errMsg), strings.ToLower(substr)) {
		t.Errorf("error body %q does not contain %q", errMsg, substr)
	}
}
