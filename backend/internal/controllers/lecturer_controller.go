package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/middleware"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
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
	case errors.Is(err, repository.ErrExamCategoryMismatch):
		// ARCH-028 (5): a question's category_id that does not belong to the
		// same exam is a client input error, not a server fault.
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// LecturerExaminationResponse extends ExaminationResponse with per-exam readiness
// aggregates that are only meaningful to the lecturer/admin view.
type LecturerExaminationResponse struct {
	ID                      models.ExamID `json:"id"`
	Title                   string        `json:"title"`
	IsActive                bool          `json:"is_active"`
	CreatedBy               models.UserID `json:"created_by"`
	CreatedAt               time.Time     `json:"created_at"`
	ActivatedAt             *time.Time    `json:"activated_at,omitempty"`
	ArchivedAt              *time.Time    `json:"archived_at,omitempty"`
	ScheduledStartAt        *time.Time    `json:"scheduled_start_at,omitempty"`
	ScheduledEndAt          *time.Time    `json:"scheduled_end_at,omitempty"`
	BlockPracticeDuringExam bool          `json:"block_practice_during_exam"`
	EffectiveState          string        `json:"effective_state"`
	Introduction            string        `json:"introduction"`
	CurriculumSchema        string        `json:"curriculum_schema"`
	HasPreviewSchema        bool          `json:"has_preview_schema"`
	QuestionCount           int           `json:"question_count"`
	TotalPoints             float64       `json:"total_points"`
	UncapturedCount         int           `json:"uncaptured_count"`
}

type LecturerController struct {
	repo          *repository.PostgresRepository
	importService *services.StudentImportService
	authService   *services.AuthService
	hub           *services.Hub
	emailJobs     *services.EmailJobRegistry
}

func NewLecturerController(repo *repository.PostgresRepository, hub *services.Hub) *LecturerController {
	return &LecturerController{repo: repo, hub: hub}
}

// buildExamStatusEvent formats an SSE data payload string for a given exam and state.
func buildExamStatusEvent(examID int, phase string) string {
	return fmt.Sprintf("event: exam-status\nid: %d\ndata: {\"exam_id\":%q,\"effective_state\":\"%s\",\"server_time\":\"%s\"}\n\n",
		time.Now().UnixMilli(), publicid.Encode(publicid.TypeExam, examID), phase, time.Now().UTC().Format(time.RFC3339))
}

func (ctrl *LecturerController) SetStudentImportService(importService *services.StudentImportService) {
	ctrl.importService = importService
}

// SetAuthService wires the auth service so the bulk importer can log a `create`
// password event and email the generated credential for each new student.
func (ctrl *LecturerController) SetAuthService(authService *services.AuthService) {
	ctrl.authService = authService
}

// SetEmailJobRegistry wires the in-memory registry that backs the rotating
// importer's background credential-email worker and the admin progress poll.
func (ctrl *LecturerController) SetEmailJobRegistry(reg *services.EmailJobRegistry) {
	ctrl.emailJobs = reg
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
			ID:                      e.ID,
			Title:                   e.Title,
			IsActive:                e.IsActive,
			CreatedBy:               e.CreatedBy,
			CreatedAt:               e.CreatedAt,
			ActivatedAt:             e.ActivatedAt,
			ArchivedAt:              e.ArchivedAt,
			ScheduledStartAt:        e.ScheduledStartAt,
			ScheduledEndAt:          e.ScheduledEndAt,
			BlockPracticeDuringExam: e.BlockPracticeDuringExam,
			EffectiveState:          repository.ExaminationEffectiveState(e).Phase,
			Introduction:            e.Introduction,
			CurriculumSchema:        e.CurriculumSchema,
			HasPreviewSchema:        e.HasPreviewSchema,
			QuestionCount:           e.QuestionCount,
			TotalPoints:             e.TotalPoints,
			UncapturedCount:         e.UncapturedCount,
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

	c.JSON(http.StatusCreated, gin.H{"message": "Examination created successfully", "id": publicid.Encode(publicid.TypeExam, id)})
}

func (ctrl *LecturerController) ActivateExamination(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	studentID, ok := idParam(c, "studentId", publicid.TypeUser)
	if !ok {
		return
	}
	if err := ctrl.repo.RemoveStudentFromExam(c.Request.Context(), examID, studentID); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Student removed from roster"})
}

func (ctrl *LecturerController) AssignStudents(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	var req models.ExamStudentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	studentIDs := make([]int, len(req.StudentIDs))
	for i, sid := range req.StudentIDs {
		studentIDs[i] = int(sid)
	}

	if err := ctrl.repo.AddStudentsToExam(c.Request.Context(), id, studentIDs); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Students assigned to examination"})
}

func (ctrl *LecturerController) UpsertQuestion(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	var q models.Question
	if err := c.ShouldBindBodyWithJSON(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// ARCH-055: ARCH-053 gives DMLKind/SeedSQL `json:"-"`, so the bind above
	// cannot populate them. Recover exactly those two from the same body — and
	// only those two, which is why auto_seed still cannot be client-supplied.
	// ShouldBindBodyWithJSON caches the body, so binding it twice is safe;
	// ShouldBindJSON would consume the reader and make this second bind fail.
	var seed struct {
		DMLKind string `json:"dml_kind"`
		SeedSQL string `json:"seed_sql"`
	}
	if err := c.ShouldBindBodyWithJSON(&seed); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q.DMLKind, q.SeedSQL = seed.DMLKind, seed.SeedSQL
	// SECURITY: derive authorship from JWT — never trust client-supplied created_by.
	q.CreatedBy = lecturer.ID

	if err := validateSeedFields(q.Type, q.DMLKind, q.SeedSQL, q.CorrectSQL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	newID, err := ctrl.repo.CreateQuestion(c.Request.Context(), q)
	if err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	// Re-read to surface the Phase 0 capture outcome (status + any capture_error)
	// so the frontend can render a green/red badge per `MANUAL.md § Step 2`.
	saved, err := ctrl.repo.GetQuestion(c.Request.Context(), newID)
	if err != nil {
		// The upsert succeeded; treat read-back failure as best-effort.
		c.JSON(http.StatusOK, gin.H{"message": "Question saved successfully", "id": newID})
		return
	}

	// Warn-only DML correct-answer table check (spec §9): never blocks the save.
	tw := repository.DMLCorrectSQLTargetTableWarning(saved.Type, saved.CorrectSQL, saved.TargetTable)

	// Dual-without-decoy warning (spec §6.1): when dual_schema is on but the
	// owning exam (or, per ARCH-028 (7), the owning category when it owns its
	// own schema) has no decoy curriculum uploaded yet, tell the lecturer the
	// preview will fall back to the grading schema until they upload one.
	// Never blocks the save.
	dsw := ""
	if saved.DualSchema && saved.ExamID != nil {
		categoryOwnsSchema := saved.CategoryID != nil && repository.ExamCategoryOwnsSchema(int(*saved.CategoryID), saved.CategorySchema)
		if categoryOwnsSchema {
			if saved.CategoryPreviewSchema == "" {
				dsw = "Dual schema is enabled but no decoy schema has been uploaded for this category/exam - previews will use the grading schema until you upload one."
			}
		} else {
			pc, _ := ctrl.repo.GetExamPreviewCurriculumSchema(c.Request.Context(), int(*saved.ExamID))
			if pc == "" {
				dsw = "Dual schema is enabled but no decoy schema has been uploaded for this category/exam - previews will use the grading schema until you upload one."
			}
		}
	}

	// Mixed dual/non-dual warning (GAP 4, ARCH-018): an exam holding both dual
	// and non-dual questions lets a student bypass the decoy by opening the
	// non-dual question's preview. Warn-only; a lookup failure must never turn
	// a successful save into an error, so its error is discarded.
	mdw := ""
	if saved.ExamID != nil {
		mixed, _ := ctrl.repo.ExamHasMixedDualSchema(c.Request.Context(), int(*saved.ExamID))
		if mixed {
			mdw = "This exam contains both dual-schema and normal questions. A student can open a normal question's preview to read the grading schema, which bypasses the decoy on your dual-schema questions. Consider making every question here dual-schema."
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                     "Question saved successfully",
		"id":                          saved.ID,
		"expected_result_status":      saved.ExpectedResultStatus,
		"expected_result_captured_at": saved.ExpectedResultCapturedAt,
		"capture_error":               saved.CaptureError,
		"table_warning":               tw,
		"dual_schema_warning":         dsw,
		"mixed_dual_warning":          mdw,
	})
}

func (ctrl *LecturerController) ListSubmissions(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	questions, err := ctrl.repo.GetQuestionsByExamID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.AsLecturerQuestionViews(questions))
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
//
// Optional field category_id: when present (non-zero), ARCH-028 (5): the
// category's own schema wins over the exam curriculum, scoped to exam_id — a
// category id that does not belong to that exam is rejected (400) rather than
// silently falling back to the exam curriculum.
//
// Optional field schema: "grading" (default) previews on the real curriculum,
// matching today's behaviour byte-for-byte; "preview" previews on the exam's
// (or category's) decoy curriculum (dual-schema grading) so the lecturer can
// see exactly what a dual-schema student would see.
func (ctrl *LecturerController) PreviewCorrectSQL(c *gin.Context) {
	var req struct {
		Type        string                `json:"type"`
		TargetTable string                `json:"target_table"`
		CorrectSQL  string                `json:"correct_sql"`
		ExamID      models.ExamID         `json:"exam_id"`               // optional; 0 = use public schema
		CategoryID  models.ExamCategoryID `json:"category_id,omitempty"` // optional; 0 = no category
		Schema      string                `json:"schema"`                // optional; "grading" (default) or "preview"
		SeedSQL     string                `json:"seed_sql"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type != "QUERY" && req.Type != "DML" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be QUERY or DML"})
		return
	}
	if strings.TrimSpace(req.SeedSQL) != "" {
		if _, err := repository.ValidateSeedSQL(req.SeedSQL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("seed_sql is not valid: %s", err.Error())})
			return
		}
	}

	useDecoy := req.Schema == "preview"
	rows, cols, err := ctrl.repo.PreviewCorrectSQLInSchema(c.Request.Context(), req.Type, req.TargetTable, req.CorrectSQL, int(req.ExamID), int(req.CategoryID), useDecoy, req.SeedSQL)
	if err != nil {
		if errors.Is(err, repository.ErrExamCategoryMismatch) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Surface engine errors with status='error' so the frontend can render
		// the message inline (this is expected during authoring).
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": err.Error()})
		return
	}

	// Emit the projection order the engine reported (pgx FieldDescriptions).
	// Ranging rows[0] here used to shuffle the header on every request, since
	// Go randomises map iteration. Keep a non-nil slice so the frontend always
	// gets [] rather than null when the preview returned no columns.
	if cols == nil {
		cols = []string{}
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

	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
		c.Request.Context(), id, req.SQL, int(lecturer.ID), confirmReplace,
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

// ImportExamCurriculumPreview handles
// POST /api/v1/lecturer/exams/:id/curriculum/import-preview
// Loads a SQL script into the exam's decoy curriculum schema (dual-schema
// grading, spec §5/§10). Mirrors ImportExamCurriculum exactly, including the
// confirm_replace=true query param and error-status mapping. On success the
// response also carries parity_warning: a non-blocking notice when the decoy
// curriculum's table/column structure differs from the real curriculum.
func (ctrl *LecturerController) ImportExamCurriculumPreview(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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

	parityWarning, err := ctrl.repo.ImportSchemaIntoExamCurriculumPreview(
		c.Request.Context(), id, req.SQL, int(lecturer.ID), confirmReplace,
	)
	if err != nil {
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
	c.JSON(http.StatusOK, gin.H{"message": "Decoy curriculum imported into exam", "parity_warning": parityWarning})
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

	if err := ctrl.repo.ExecuteSchemaScript(c.Request.Context(), req.SQL, int(lecturer.ID)); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Database schema imported successfully"})
}

// parseRosterFromRequest reads the "file" multipart field off the request and
// parses it into roster rows, writing the same 400 responses/status codes
// ImportStudents has always used on failure. Shared by ImportStudents and
// ImportStudentsWithCredentials so the two endpoints' file-intake behaviour
// (and its error strings) cannot drift apart. Returns ok=false after already
// writing the response; callers must return immediately in that case.
func (ctrl *LecturerController) parseRosterFromRequest(c *gin.Context) (rows []services.ParsedStudentRow, ok bool) {
	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return nil, false
	}

	file, err := header.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	defer file.Close()

	rows, err = services.ParseRoster(header.Filename, file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return rows, true
}

func (ctrl *LecturerController) ImportStudents(c *gin.Context) {
	if ctrl.importService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "student import service is not configured"})
		return
	}

	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	rows, ok := ctrl.parseRosterFromRequest(c)
	if !ok {
		return
	}

	report, err := ctrl.importService.Import(c.Request.Context(), int(lecturer.ID), rows)
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

// ImportStudentsWithCredentialsPreflight handles
// GET /api/v1/students/import-credentials/preflight.
//
// Owner-approved safeguard: rotating every student's password evicts every
// affected student's session immediately, so running this while an exam is
// in progress signs the whole class out mid-exam. The UI calls this BEFORE
// the admin picks a file and commits to the action, so the warning lands
// before anything is uploaded — not after every password has already been
// rotated. This never blocks: it only reports the exam_active flag the UI
// uses to demand a second, explicit confirmation. An admin must keep the
// ability to act in an emergency.
func (ctrl *LecturerController) ImportStudentsWithCredentialsPreflight(c *gin.Context) {
	active, err := ctrl.repo.IsAnyExamActive(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"exam_active": active})
}

// ImportStudentsWithCredentials handles POST /api/v1/students/import-credentials.
//
// Admin-only counterpart to ImportStudents: it rotates the password of EVERY
// row in the roster — new and returning students alike — evicting their
// sessions, and emails each new credential from a background worker whose
// progress the admin can poll. Emailing plaintext passwords in bulk is the
// deliberate point of this endpoint; the typed confirmation phrase and the
// admin-only role gate are what stand in front of it.
func (ctrl *LecturerController) ImportStudentsWithCredentials(c *gin.Context) {
	if ctrl.importService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "student import service is not configured"})
		return
	}

	userVal, _ := c.Get("user")
	admin := userVal.(models.User)

	// The confirmation phrase is checked server-side on EVERY request; the
	// server never trusts the UI's own confirm gate (same rule as
	// BulkResetPassword's "RESET").
	if c.PostForm("confirm") != "ROTATE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirmation_required"})
		return
	}

	// send_email selects delivery mode. Absent means true so the original
	// contract (this endpoint always emailed) still holds for any caller that
	// predates the flag. A malformed value is rejected rather than defaulted:
	// defaulting garbage to true would mail plaintext passwords to a whole
	// roster on a typo, and that send cannot be recalled.
	sendEmail := true
	if v := c.PostForm("send_email"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_send_email"})
			return
		}
		sendEmail = parsed
	}

	rows, ok := ctrl.parseRosterFromRequest(c)
	if !ok {
		return
	}

	// Owner-approved safeguard: warn, but never block. The UI already asked
	// the admin to confirm a second time when its own preflight call reported
	// an exam active; this is the detective control for when that UI check is
	// bypassed (a direct API call, a stale page). Checked at the moment the
	// import actually runs -- not reused from the preflight call, which may be
	// stale or may never have happened -- and recorded on the report itself
	// (like JobID, set by the controller rather than the service) so the
	// audit trail reflects ground truth regardless of what the UI did.
	examActive, err := ctrl.repo.IsAnyExamActive(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if examActive {
		log.Printf("import-credentials: WARNING proceeding while an exam is active -- admin=%s (id=%d) rows=%d; every affected student will be signed out immediately",
			admin.Username, admin.ID, len(rows))
	}

	report, err := ctrl.importService.ImportRotating(c.Request.Context(), int(admin.ID), rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	report.ExamActive = examActive

	// Tell the truth about delivery: ImportRotating marks a row "queued"
	// purely based on whether it has an email address, with no way to know
	// whether a real sender is even wired in. When it is not (the no-op stub
	// is active), every "queued" row would otherwise report success though
	// nothing was ever sent — 200 students signed out holding passwords
	// nobody told them about. Downgrade those rows here, before anything
	// below reads EmailStatus, so the job-registration/worker logic and the
	// response body agree.
	// The admin's own choice is applied FIRST and wins over the
	// configuration-based downgrade below: on a no-email import nothing is
	// sent regardless of whether a sender is wired in, so reporting
	// "not_configured" there would blame the server for a decision the admin
	// made. After this loop no row is left "queued", so the downgrade below
	// is a no-op on this path -- and no job is registered further down,
	// because that too keys off EmailStatusQueued.
	if !sendEmail {
		for i := range report.Accounts {
			if report.Accounts[i].EmailStatus == services.EmailStatusQueued {
				report.Accounts[i].EmailStatus = services.EmailStatusSkipped
			}
		}
		log.Printf("import-credentials: admin=%s (id=%d) rotated %d account(s) with delivery disabled; passwords were returned to the browser only",
			admin.Username, admin.ID, len(report.Accounts))
	}

	emailConfigured := ctrl.authService != nil && ctrl.authService.EmailConfigured()
	if !emailConfigured {
		for i := range report.Accounts {
			if report.Accounts[i].EmailStatus == services.EmailStatusQueued {
				report.Accounts[i].EmailStatus = services.EmailStatusNotConfigured
			}
		}
	}

	// Post-commit audit trail: one password_events row per rotated account, and
	// deliberately NO email from this path (see LogBulkCredentials).
	//
	// context.WithoutCancel: this transaction already committed, and a
	// 500-row roster runs ~40s of per-row bcrypt hashing (past a typical 60s
	// proxy timeout for larger rosters). If the admin's browser disconnects
	// after commit, c.Request.Context() is cancelled and every
	// InsertPasswordEvent below would fail with "context canceled" -- taking
	// down the ONLY server-side audit trail (and thus the resend-credential
	// recovery path) for passwords that are already irrecoverably rotated.
	// The audit write must survive a client disconnect even though the
	// response body cannot be resent.
	if ctrl.authService != nil {
		auditCtx := context.WithoutCancel(c.Request.Context())
		events := make([]services.BulkCredentialEvent, 0, len(report.Accounts))
		for _, a := range report.Accounts {
			source := models.PasswordEventAdminReset
			if a.Outcome == services.OutcomeCreated {
				source = models.PasswordEventCreate
			}
			events = append(events, services.BulkCredentialEvent{
				UserID: a.UserID, Source: source, Plaintext: a.Password,
			})
		}
		if written := ctrl.authService.LogBulkCredentials(auditCtx, admin, events); written < len(events) {
			log.Printf("import-credentials: audit trail incomplete: wrote %d/%d password_events rows (see preceding log lines for the per-row cause)", written, len(events))
		}
	}

	// Hand delivery to a single background goroutine per import. Serial within
	// that goroutine by design: one SMTP connection at a time per import keeps
	// the send under Gmail's rate limits instead of bursting into a rejection
	// (two overlapping imports still run two concurrent send loops). SendCredentialEmail
	// takes no context, so the request's own (already-cancelled-by-the-time-this-runs)
	// context cannot leak into the send.
	//
	// Rows are only ever queued here when emailConfigured is true (see the
	// EmailStatus downgrade above), so no job is registered and no worker
	// starts when delivery is not configured.
	toSend := make([]credentialRecipient, 0, len(report.Accounts))
	for _, a := range report.Accounts {
		if a.EmailStatus == services.EmailStatusQueued {
			toSend = append(toSend, credentialRecipient{Email: a.Email, LoginID: a.StudentIDExt, Password: a.Password})
		}
	}
	report.JobID = startCredentialEmailJob(ctrl.authService, ctrl.emailJobs, int(admin.ID), toSend)

	c.JSON(http.StatusOK, report)
}

// ImportCredentialsJobStatus handles
// GET /api/v1/students/import-credentials/:job_id/status.
// A job belonging to another admin is reported as not-found, so the endpoint
// cannot be used to probe other admins' activity.
func (ctrl *LecturerController) ImportCredentialsJobStatus(c *gin.Context) {
	if ctrl.emailJobs == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	userVal, _ := c.Get("user")
	admin := userVal.(models.User)

	job, ok := ctrl.emailJobs.Get(c.Param("job_id"), int(admin.ID))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, job)
}

// SetExamIntroduction handles PUT /api/v1/lecturer/exams/:id/introduction
// Persists a lecturer-authored description for the exam's curriculum page.
func (ctrl *LecturerController) SetExamIntroduction(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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

// GetPracticeResults handles GET /api/v1/lecturer/practice/results, returning
// one row per student/question pair with at least one practice attempt
// (ARCH-006). No pagination, no request params.
func (ctrl *LecturerController) GetPracticeResults(c *gin.Context) {
	rows, err := ctrl.repo.GetLecturerPracticeResults(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// ─── Exam Intro Images (staff mutations) ─────────────────────────────────────

// UploadExamIntroImage handles POST /api/v1/lecturer/exams/:id/intro-images
// Accepts a multipart upload with field "image", validates the mime type by
// magic bytes, enforces per-file (5 MB) and aggregate (25 MB) size limits, and
// inserts the image. Returns the inserted metadata row (201).
func (ctrl *LecturerController) UploadExamIntroImage(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
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
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	imgID, ok := idParam(c, "imageId", publicid.TypeImage)
	if !ok {
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
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	var req struct {
		Order []struct {
			ID        models.ImageID `json:"id"`
			SortOrder int            `json:"sort_order"`
		} `json:"order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	order := make([]struct{ ID, SortOrder int }, len(req.Order))
	for i, item := range req.Order {
		order[i] = struct{ ID, SortOrder int }{int(item.ID), item.SortOrder}
	}

	if err := ctrl.repo.ReorderExamIntroImages(c.Request.Context(), examID, order); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Order updated"})
}
