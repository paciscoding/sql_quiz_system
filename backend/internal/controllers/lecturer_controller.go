package controllers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/middleware"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// statusForRepoErr maps repository sentinel errors to HTTP status codes.
// Returns 0 if err is nil, falling back to the caller's default otherwise.
func statusForRepoErr(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, repository.ErrExamActive),
		errors.Is(err, repository.ErrCaptureNotReady),
		errors.Is(err, repository.ErrEmptyExam):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// LecturerExaminationResponse extends ExaminationResponse with per-exam readiness
// aggregates that are only meaningful to the lecturer/admin view.
type LecturerExaminationResponse struct {
	ID               int        `json:"id"`
	Title            string     `json:"title"`
	IsActive         bool       `json:"is_active"`
	CreatedBy        int        `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
	ScheduledStartAt *time.Time `json:"scheduled_start_at,omitempty"`
	ScheduledEndAt   *time.Time `json:"scheduled_end_at,omitempty"`
	EffectiveState   string     `json:"effective_state"`
	Introduction     string     `json:"introduction"`
	CurriculumSchema string     `json:"curriculum_schema"`
	QuestionCount    int        `json:"question_count"`
	TotalPoints      float64    `json:"total_points"`
	UncapturedCount  int        `json:"uncaptured_count"`
}

type LecturerController struct {
	repo          *repository.PostgresRepository
	importService *services.StudentImportService
	authService   *services.AuthService
	hub           *services.Hub
}

func NewLecturerController(repo *repository.PostgresRepository, hub *services.Hub) *LecturerController {
	return &LecturerController{repo: repo, hub: hub}
}

// buildExamStatusEvent formats an SSE data payload string for a given exam and state.
func buildExamStatusEvent(examID int, phase string) string {
	return fmt.Sprintf("event: exam-status\nid: %d\ndata: {\"exam_id\":%d,\"effective_state\":\"%s\",\"server_time\":\"%s\"}\n\n",
		time.Now().UnixMilli(), examID, phase, time.Now().UTC().Format(time.RFC3339))
}

func (ctrl *LecturerController) SetStudentImportService(importService *services.StudentImportService) {
	ctrl.importService = importService
}

// SetAuthService wires the auth service so the bulk importer can log a `create`
// password event and email the generated credential for each new student.
func (ctrl *LecturerController) SetAuthService(authService *services.AuthService) {
	ctrl.authService = authService
}

func (ctrl *LecturerController) ListExaminations(c *gin.Context) {
	exams, err := ctrl.repo.GetAllExaminationsWithCounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp := make([]LecturerExaminationResponse, 0, len(exams))
	for _, e := range exams {
		resp = append(resp, LecturerExaminationResponse{
			ID:               e.ID,
			Title:            e.Title,
			IsActive:         e.IsActive,
			CreatedBy:        e.CreatedBy,
			CreatedAt:        e.CreatedAt,
			ActivatedAt:      e.ActivatedAt,
			ArchivedAt:       e.ArchivedAt,
			ScheduledStartAt: e.ScheduledStartAt,
			ScheduledEndAt:   e.ScheduledEndAt,
			EffectiveState:   repository.ExaminationEffectiveState(e).Phase,
			Introduction:     e.Introduction,
			CurriculumSchema: e.CurriculumSchema,
			QuestionCount:    e.QuestionCount,
			TotalPoints:      e.TotalPoints,
			UncapturedCount:  e.UncapturedCount,
		})
	}
	c.JSON(http.StatusOK, resp)
}

func (ctrl *LecturerController) CreateExamination(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	var exam models.Examination
	if err := c.ShouldBindJSON(&exam); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// SECURITY: Force IsActive=false on create to prevent bypassing activation workflow
	exam.IsActive = false
	exam.CreatedBy = lecturer.ID
	id, err := ctrl.repo.CreateExamination(c.Request.Context(), exam)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Examination created successfully", "id": id})
}

func (ctrl *LecturerController) ActivateExamination(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	var req struct {
		Active           bool       `json:"active"`
		Mode             string     `json:"mode"`
		ScheduledStartAt *time.Time `json:"scheduled_start_at"`
		ScheduledEndAt   *time.Time `json:"scheduled_end_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !req.Active {
		c.JSON(http.StatusBadRequest, gin.H{"error": "use the deactivate endpoint to deactivate"})
		return
	}

	var scheduledStartAt *time.Time
	var scheduledEndAt *time.Time

	switch req.Mode {
	case "scheduled":
		if req.ScheduledStartAt == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scheduled_start_at is required for scheduled mode"})
			return
		}
		scheduledStartAt = req.ScheduledStartAt
		scheduledEndAt = req.ScheduledEndAt
	default:
		// "instant" or empty — pass nil to use now()
	}

	if err := ctrl.repo.SetExaminationActive(c.Request.Context(), id, true, scheduledStartAt, scheduledEndAt); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	if ctrl.hub != nil {
		ctrl.hub.Broadcast(id, buildExamStatusEvent(id, "active"))
	}
	middleware.InvalidateExamFreezeCache()
	c.JSON(http.StatusOK, gin.H{"message": "Examination status updated"})
}

func (ctrl *LecturerController) DeactivateExamination(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	var req struct {
		Mode           string     `json:"mode"`
		ScheduledEndAt *time.Time `json:"scheduled_end_at"`
	}
	// Allow empty body (instant deactivate)
	_ = c.ShouldBindJSON(&req)

	if req.Mode == "scheduled" && req.ScheduledEndAt != nil {
		// Set a future end time without dropping the sandbox
		if err := ctrl.repo.SetScheduledEndAt(c.Request.Context(), id, req.ScheduledEndAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else {
		// Instant deactivate: drop sandbox and archive
		if err := ctrl.repo.ArchiveExamination(c.Request.Context(), id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	middleware.InvalidateExamFreezeCache()
	c.JSON(http.StatusOK, gin.H{"message": "Examination deactivated and sandboxes cleaned up"})
}

// RenameExamination handles PUT /api/v1/lecturer/exams/:id
// Renames an examination. Allowed regardless of active state.
func (ctrl *LecturerController) RenameExamination(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
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

	if err := ctrl.repo.UpdateExaminationTitle(c.Request.Context(), id, title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Exam renamed"})
}

// DeleteExamination handles DELETE /api/v1/lecturer/exams/:id
// Permanently deletes an examination and all dependents (submissions, enrollments,
// intro images, questions, sandbox schema). Requires the exam to be inactive first.
func (ctrl *LecturerController) DeleteExamination(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	// Fetch the exam to check active state.
	exam, err := ctrl.repo.GetExamByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if repository.ExaminationEffectiveState(exam).ActiveNow {
		c.JSON(http.StatusConflict, gin.H{"error": "deactivate the exam before deleting"})
		return
	}

	if err := ctrl.repo.DeleteExamination(c.Request.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Exam deleted"})
}

// SetPracticeFreeze toggles the per-exam block_practice_during_exam flag. When
// on, student practice is blocked while this exam is active (the optional focus
// rule); when off (default), practice stays live during the exam.
func (ctrl *LecturerController) SetPracticeFreeze(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	var req struct {
		BlockPractice bool `json:"block_practice"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ctrl.repo.SetExamPracticeFreeze(c.Request.Context(), id, req.BlockPractice); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	middleware.InvalidateExamFreezeCache()
	c.JSON(http.StatusOK, gin.H{"message": "Practice freeze updated", "block_practice": req.BlockPractice})
}

func (ctrl *LecturerController) GetExamRoster(c *gin.Context) {
	idStr := c.Param("id")
	examID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}
	students, err := ctrl.repo.GetExamRoster(c.Request.Context(), examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": len(students), "students": students})
}

func (ctrl *LecturerController) RemoveStudentFromExam(c *gin.Context) {
	idStr := c.Param("id")
	examID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}
	studentIdStr := c.Param("studentId")
	studentID, err := strconv.Atoi(studentIdStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid student id"})
		return
	}
	if err := ctrl.repo.RemoveStudentFromExam(c.Request.Context(), examID, studentID); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Student removed from roster"})
}

func (ctrl *LecturerController) AssignStudents(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	var req models.ExamStudentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ctrl.repo.AddStudentsToExam(c.Request.Context(), id, req.StudentIDs); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Students assigned to examination"})
}

func (ctrl *LecturerController) UpsertQuestion(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	var q models.Question
	if err := c.ShouldBindJSON(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// SECURITY: derive authorship from JWT — never trust client-supplied created_by.
	q.CreatedBy = lecturer.ID

	if err := ctrl.repo.CreateQuestion(c.Request.Context(), q); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	// Re-read to surface the Phase 0 capture outcome (status + any capture_error)
	// so the frontend can render a green/red badge per `MANUAL.md § Step 2`.
	saved, err := ctrl.repo.GetQuestion(c.Request.Context(), q.ID)
	if err != nil {
		// The upsert succeeded; treat read-back failure as best-effort.
		c.JSON(http.StatusOK, gin.H{"message": "Question saved successfully"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                     "Question saved successfully",
		"id":                          saved.ID,
		"expected_result_status":      saved.ExpectedResultStatus,
		"expected_result_captured_at": saved.ExpectedResultCapturedAt,
		"capture_error":               saved.CaptureError,
	})
}

func (ctrl *LecturerController) ListSubmissions(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	submissions, err := ctrl.repo.GetSubmissionsByExamID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, submissions)
}

func (ctrl *LecturerController) GetExamQuestions(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	questions, err := ctrl.repo.GetQuestionsByExamID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, questions)
}

func (ctrl *LecturerController) DeleteQuestion(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question id"})
		return
	}

	if err := ctrl.repo.DeleteQuestion(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Question deleted successfully"})
}

// PreviewCorrectSQL lets the lecturer dry-run their `correct_sql` for a
// candidate question and inspect the resulting rows BEFORE saving. The repo
// runs everything inside a rolled-back transaction so the schema stays clean.
//
// Optional field exam_id: when present and > 0, the preview runs against that
// exam's curriculum schema instead of 'public'. Absent (or 0) keeps the
// existing 'public' behaviour byte-for-byte.
func (ctrl *LecturerController) PreviewCorrectSQL(c *gin.Context) {
	var req struct {
		Type        string `json:"type"`
		TargetTable string `json:"target_table"`
		CorrectSQL  string `json:"correct_sql"`
		ExamID      int    `json:"exam_id"` // optional; 0 = use public schema
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type != "QUERY" && req.Type != "DML" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be QUERY or DML"})
		return
	}

	rows, err := ctrl.repo.PreviewCorrectSQLInSchema(c.Request.Context(), req.Type, req.TargetTable, req.CorrectSQL, req.ExamID)
	if err != nil {
		// Surface engine errors with status='error' so the frontend can render
		// the message inline (this is expected during authoring).
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": err.Error()})
		return
	}

	cols := []string{}
	if len(rows) > 0 {
		for k := range rows[0] {
			cols = append(cols, k)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"rows":    rows,
		"columns": cols,
	})
}

// ImportExamCurriculum handles POST /api/v1/lecturer/exams/:id/curriculum/import
// Loads a SQL script into a per-exam curriculum schema. The exam's
// curriculum_schema column is updated to curriculum_exam_{id} on success.
// Query param confirm_replace=true drops and recreates the schema first.
func (ctrl *LecturerController) ImportExamCurriculum(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
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
	confirmReplace := c.Query("confirm_replace") == "true"

	if err := ctrl.repo.ImportSchemaIntoExamCurriculum(
		c.Request.Context(), id, req.SQL, lecturer.ID, confirmReplace,
	); err != nil {
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

func (ctrl *LecturerController) ImportSchema(c *gin.Context) {
	var req struct {
		SQL string `json:"sql" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get the authenticated user's ID for the audit row
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	if err := ctrl.repo.ExecuteSchemaScript(c.Request.Context(), req.SQL, lecturer.ID); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Database schema imported successfully"})
}

func (ctrl *LecturerController) ImportStudents(c *gin.Context) {
	if ctrl.importService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "student import service is not configured"})
		return
	}

	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}

	file, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer file.Close()

	rows, err := services.ParseRoster(header.Filename, file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	report, err := ctrl.importService.Import(c.Request.Context(), lecturer.ID, rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Post-commit: log a `create` event and email the generated credential for
	// each newly inserted student. Best-effort — LogStudentCreate never fails the
	// import, and SendCredential is a no-op when the student has no email. Large
	// rosters send many emails synchronously; Gmail daily limits may throttle.
	if ctrl.authService != nil {
		for _, cs := range report.CreatedAccounts {
			ctrl.authService.LogStudentCreate(c.Request.Context(), lecturer, cs.UserID, cs.Password, cs.StudentIDExt, cs.Email)
		}
	}

	c.JSON(http.StatusOK, report)
}

// SetExamIntroduction handles PUT /api/v1/lecturer/exams/:id/introduction
// Persists a lecturer-authored description for the exam's curriculum page.
func (ctrl *LecturerController) SetExamIntroduction(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	var req struct {
		Introduction string `json:"introduction"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	text := strings.TrimSpace(req.Introduction)
	if len(text) > 20000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "introduction exceeds 20000 character limit"})
		return
	}

	if err := ctrl.repo.UpdateExaminationIntroduction(c.Request.Context(), id, text); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Exam introduction updated"})
}

func (ctrl *LecturerController) GetExamMaxScores(c *gin.Context) {
	maxScores, err := ctrl.repo.GetExamMaxScores(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"max_scores": maxScores})
}

func (ctrl *LecturerController) GetConsolidatedScores(c *gin.Context) {
	scores, err := ctrl.repo.GetConsolidatedScores(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, scores)
}

// ─── Exam Intro Images (staff mutations) ─────────────────────────────────────

// UploadExamIntroImage handles POST /api/v1/lecturer/exams/:id/intro-images
// Accepts a multipart upload with field "image", validates the mime type by
// magic bytes, enforces per-file (5 MB) and aggregate (25 MB) size limits, and
// inserts the image. Returns the inserted metadata row (201).
func (ctrl *LecturerController) UploadExamIntroImage(c *gin.Context) {
	examID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 5*1024*1024)
	if err := c.Request.ParseMultipartForm(5 * 1024 * 1024); err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large (max 5 MB)"})
		return
	}

	file, _, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "image field is required"})
		return
	}
	defer file.Close()

	// Validate MIME type via magic bytes.
	magic := make([]byte, 8)
	if _, err := io.ReadFull(file, magic); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the image file is empty or truncated"})
		return
	}
	var detectedMime string
	switch {
	case len(magic) >= 4 && magic[0] == 0x89 && magic[1] == 0x50 && magic[2] == 0x4E && magic[3] == 0x47:
		detectedMime = "image/png"
	case len(magic) >= 3 && magic[0] == 0xFF && magic[1] == 0xD8 && magic[2] == 0xFF:
		detectedMime = "image/jpeg"
	default:
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "the uploaded file is not a valid PNG or JPEG — it may be renamed, corrupted, or an unsupported format (e.g. GIF, WebP, HEIC). Re-export it as PNG or JPEG and try again."})
		return
	}

	// Re-read the full image (magic bytes + rest).
	rest, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	data := append(magic, rest...)

	// Aggregate size guard.
	ctx := c.Request.Context()
	existing, err := ctrl.repo.CountExamIntroImageBytes(ctx, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if existing+int64(len(data)) > 25*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "aggregate intro-image size exceeds 25 MB"})
		return
	}

	img, err := ctrl.repo.InsertExamIntroImage(ctx, examID, detectedMime, data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, img)
}

// DeleteExamIntroImage handles DELETE /api/v1/lecturer/exams/:id/intro-images/:imageId
// Removes a single intro image from an examination. Returns 204 on success.
func (ctrl *LecturerController) DeleteExamIntroImage(c *gin.Context) {
	examID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}
	imgID, err := strconv.Atoi(c.Param("imageId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid image id"})
		return
	}

	if err := ctrl.repo.DeleteExamIntroImage(c.Request.Context(), examID, imgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}

// ReorderExamIntroImages handles PUT /api/v1/lecturer/exams/:id/intro-images/order
// Accepts {"order":[{"id":1,"sort_order":0},…]} and applies the new ordering.
func (ctrl *LecturerController) ReorderExamIntroImages(c *gin.Context) {
	examID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	var req struct {
		Order []struct {
			ID        int `json:"id"`
			SortOrder int `json:"sort_order"`
		} `json:"order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	order := make([]struct{ ID, SortOrder int }, len(req.Order))
	for i, item := range req.Order {
		order[i] = struct{ ID, SortOrder int }{item.ID, item.SortOrder}
	}

	if err := ctrl.repo.ReorderExamIntroImages(c.Request.Context(), examID, order); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Order updated"})
}

