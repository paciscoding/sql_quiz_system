package controllers

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
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

type LecturerController struct {
	repo          *repository.PostgresRepository
	importService *services.StudentImportService
}

func NewLecturerController(repo *repository.PostgresRepository) *LecturerController {
	return &LecturerController{repo: repo}
}

func (ctrl *LecturerController) SetStudentImportService(importService *services.StudentImportService) {
	ctrl.importService = importService
}

func (ctrl *LecturerController) ListExaminations(c *gin.Context) {
	exams, err := ctrl.repo.GetAllExaminations(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, exams)
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
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ctrl.repo.SetExaminationActive(c.Request.Context(), id, req.Active); err != nil {
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
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

	if err := ctrl.repo.ArchiveExamination(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	middleware.InvalidateExamFreezeCache()
	c.JSON(http.StatusOK, gin.H{"message": "Examination deactivated and sandboxes cleaned up"})
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
// runs everything inside a rolled-back transaction so public.* stays clean.
func (ctrl *LecturerController) PreviewCorrectSQL(c *gin.Context) {
	var req struct {
		Type        string `json:"type"`
		TargetTable string `json:"target_table"`
		CorrectSQL  string `json:"correct_sql"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type != "QUERY" && req.Type != "DML" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be QUERY or DML"})
		return
	}

	rows, err := ctrl.repo.PreviewCorrectSQL(c.Request.Context(), req.Type, req.TargetTable, req.CorrectSQL)
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

func (ctrl *LecturerController) ImportSchema(c *gin.Context) {
	var req struct {
		SQL string `json:"sql" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := ctrl.repo.ExecuteSchemaScript(c.Request.Context(), req.SQL); err != nil {
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

	c.JSON(http.StatusOK, report)
}

func (ctrl *LecturerController) GetConsolidatedScores(c *gin.Context) {
	scores, err := ctrl.repo.GetConsolidatedScores(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, scores)
}

func (ctrl *LecturerController) ExportScoresCSV(c *gin.Context) {
	scores, err := ctrl.repo.GetConsolidatedScores(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Identify all unique exam titles for dynamic columns
	examTitlesMap := make(map[string]bool)
	for _, s := range scores {
		for title := range s.ExamScores {
			examTitlesMap[title] = true
		}
	}
	var examTitles []string
	for title := range examTitlesMap {
		examTitles = append(examTitles, title)
	}
	sort.Strings(examTitles)

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Disposition", "attachment; filename=consolidated_scores.csv")
	c.Header("Content-Type", "text/csv")

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	// Header
	header := []string{"StudentNumber", "Username", "FirstName", "LastName", "SecLec", "SecLab"}
	header = append(header, examTitles...)
	header = append(header, "PracticeTotal", "GrandTotal")
	if err := writer.Write(header); err != nil {
		return
	}

	// Data
	for _, s := range scores {
		row := []string{
			s.StudentNumber, s.Username, s.FirstName, s.LastName, s.SecLec, s.SecLab,
		}
		for _, title := range examTitles {
			score := s.ExamScores[title]
			row = append(row, fmt.Sprintf("%.2f", score))
		}
		row = append(row, fmt.Sprintf("%.2f", s.PracticeTotal))
		row = append(row, fmt.Sprintf("%.2f", s.GrandTotal))
		if err := writer.Write(row); err != nil {
			return
		}
	}
}
