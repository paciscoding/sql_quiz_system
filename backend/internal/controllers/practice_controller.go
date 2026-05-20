package controllers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

type PracticeController struct {
	repo      *repository.PostgresRepository
	validator *services.ValidationService
	auth      *services.AuthService
}

func NewPracticeController(repo *repository.PostgresRepository, validator *services.ValidationService, auth *services.AuthService) *PracticeController {
	return &PracticeController{
		repo:      repo,
		validator: validator,
		auth:      auth,
	}
}

// ListForStudent (GET /practice/questions)
func (ctrl *PracticeController) ListForStudent(c *gin.Context) {
	questions, err := ctrl.repo.ListPracticeQuestions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Zero out sensitive fields for students
	for i := range questions {
		questions[i].CorrectSQL = ""
		questions[i].ExpectedResult = nil
		questions[i].ExpectedColumns = nil
	}

	c.JSON(http.StatusOK, questions)
}

// GetForStudent (GET /practice/questions/:id)
func (ctrl *PracticeController) GetForStudent(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question id"})
		return
	}

	question, err := ctrl.repo.GetPracticeQuestion(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "practice question not found"})
		return
	}

	// Zero out sensitive fields for students
	question.CorrectSQL = ""
	question.ExpectedResult = nil
	question.ExpectedColumns = nil

	c.JSON(http.StatusOK, question)
}

// Submit (POST /practice/submit)
func (ctrl *PracticeController) Submit(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	var req struct {
		PracticeQuestionID int    `json:"practice_question_id"`
		SQLText            string `json:"sql_text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	question, err := ctrl.repo.GetPracticeQuestion(ctx, req.PracticeQuestionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "practice question not found"})
		return
	}

	q := question.AsQuestion()
	result, err := ctrl.validator.Validate(ctx, student.ID, *q, req.SQLText, "sandbox_practice")

	// Persist attempt regardless of validation outcome
	attempt := models.PracticeAttempt{
		StudentID:          student.ID,
		PracticeQuestionID: req.PracticeQuestionID,
		SQLText:            req.SQLText,
		IsCorrect:          result.IsCorrect,
		ValidationDetails:  result.ValidationDetails,
	}
	if persistErr := ctrl.repo.CreatePracticeAttempt(ctx, &attempt); persistErr != nil {
		fmt.Printf("Warning: failed to persist practice attempt: %v\n", persistErr)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

// Preview (POST /practice/preview)
func (ctrl *PracticeController) Preview(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	var req struct {
		PracticeQuestionID int    `json:"practice_question_id"`
		SQLText            string `json:"sql_text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	question, err := ctrl.repo.GetPracticeQuestion(ctx, req.PracticeQuestionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "practice question not found"})
		return
	}

	q := question.AsQuestion()
	vResult, rows, columns, rowCount, truncated, err := ctrl.validator.ValidatePreview(ctx, student.ID, *q, req.SQLText, "sandbox_practice")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if rows == nil {
		rows = []map[string]interface{}{}
	}
	if columns == nil {
		columns = []string{}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    vResult.Status,
		"feedback":  vResult.Feedback,
		"rows":      rows,
		"columns":   columns,
		"row_count": rowCount,
		"truncated": truncated,
	})
}

// ListMyAttempts (GET /practice/attempts)
func (ctrl *PracticeController) ListMyAttempts(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	attempts, err := ctrl.repo.ListPracticeAttempts(c.Request.Context(), student.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, attempts)
}

func (ctrl *PracticeController) GetSummary(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	summary, err := ctrl.repo.GetPracticeSummary(c.Request.Context(), student.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// Upsert (POST /lecturer/practice/question)

func (ctrl *PracticeController) Upsert(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	var q models.PracticeQuestion
	if err := c.ShouldBindJSON(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q.CreatedBy = lecturer.ID

	ctx := c.Request.Context()
	var err error
	if q.ID == 0 {
		err = ctrl.repo.CreatePracticeQuestion(ctx, &q)
	} else {
		err = ctrl.repo.UpdatePracticeQuestion(ctx, &q)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Re-read to surface capture outcome
	saved, err := ctrl.repo.GetPracticeQuestion(ctx, q.ID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "Practice question saved"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                     "Practice question saved",
		"id":                          saved.ID,
		"expected_result_status":      saved.ExpectedResultStatus,
		"expected_result_captured_at": saved.ExpectedResultCapturedAt,
		"capture_error":               saved.CaptureError,
	})
}

// ListForLecturer (GET /lecturer/practice/questions)
func (ctrl *PracticeController) ListForLecturer(c *gin.Context) {
	questions, err := ctrl.repo.ListPracticeQuestions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, questions)
}

// Delete (DELETE /lecturer/practice/question/:id)
func (ctrl *PracticeController) Delete(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question id"})
		return
	}

	if err := ctrl.repo.DeletePracticeQuestion(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Practice question deleted"})
}

// ResetSandbox (POST /lecturer/practice/reset-sandbox)
func (ctrl *PracticeController) ResetSandbox(c *gin.Context) {
	resetAt, err := ctrl.repo.ResetPracticeSandbox(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"reset_at": resetAt})
}

// LecturerPreview (POST /lecturer/practice/preview)
func (ctrl *PracticeController) LecturerPreview(c *gin.Context) {
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
		// Surface engine errors with status='error' to match LecturerController
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
