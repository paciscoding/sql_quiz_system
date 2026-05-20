package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

type QuizController struct {
	valService      *services.ValidationService
	submissionQueue *services.SubmissionQueue
	repo            *repository.PostgresRepository
}

func NewQuizController(valService *services.ValidationService, submissionQueue *services.SubmissionQueue, repo *repository.PostgresRepository) *QuizController {
	return &QuizController{
		valService:      valService,
		submissionQueue: submissionQueue,
		repo:            repo,
	}
}

func (ctrl *QuizController) GetExams(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	exams, err := ctrl.repo.GetStudentExams(c.Request.Context(), student.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, exams)
}

func (ctrl *QuizController) GetExamQuestions(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	idStr := c.Param("id")
	examID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	// Verify enrollment
	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(c.Request.Context(), student.ID, examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not enrolled in this examination"})
		return
	}

	// Verify exam is active
	exam, err := ctrl.repo.GetExamByID(c.Request.Context(), examID)
	if err != nil || !exam.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "Examination is not active"})
		return
	}

	questions, err := ctrl.repo.GetQuestionsByExamID(c.Request.Context(), examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Students see the expected result for QUERY questions (it's the target
	// rows they're supposed to reproduce) but NOT for DML — revealing the
	// post-image of a mutation question would leak the answer outright.
	// Correct SQL is also stripped regardless of type.
	for i := range questions {
		questions[i].CorrectSQL = ""
		if questions[i].Type != "QUERY" {
			questions[i].ExpectedResult = nil
			questions[i].ExpectedColumns = nil
		}
	}

	c.JSON(http.StatusOK, questions)
}

func (ctrl *QuizController) GetExamResults(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	idStr := c.Param("id")
	examID, err := strconv.Atoi(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid examination id"})
		return
	}

	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(c.Request.Context(), student.ID, examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not enrolled in this examination"})
		return
	}

	results, err := ctrl.repo.GetStudentExamResults(c.Request.Context(), student.ID, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if results == nil {
		results = []models.StudentExamResult{}
	}

	c.JSON(http.StatusOK, results)
}

func (ctrl *QuizController) GetMyResultsSummary(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	results, err := ctrl.repo.GetStudentResultsSummary(c.Request.Context(), student.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if results == nil {
		results = []models.ExamResultSummary{}
	}

	c.JSON(http.StatusOK, results)
}

func (ctrl *QuizController) SubmitAnswer(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	var req models.SubmissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// 1. Fetch Question
	question, err := ctrl.repo.GetQuestion(ctx, req.QuestionNumber)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to fetch question: %v", err)})
		return
	}

	// 2. Security & Enrollment checks
	if question.ExamID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is not assigned to an examination"})
		return
	}
	exam, err := ctrl.repo.GetExamByID(ctx, *question.ExamID)
	if err != nil || !exam.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "this examination is not active"})
		return
	}
	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, student.ID, *question.ExamID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not enrolled in this examination"})
		return
	}

	// 3. Phase 2 Evaluation. DML serialises through the global in-process FIFO
	// submission queue (single worker). SELECT runs lock-free against frozen
	// public.* under the read-only role.
	var result models.ValidationResult
	evaluate := func() error {
		var evalErr error
		result, evalErr = ctrl.valService.Validate(ctx, student.ID, question, req.SQLText, repository.ExamSandboxName(*question.ExamID))
		return evalErr
	}

	if question.Type == "DML" {
		err = ctrl.submissionQueue.Run(ctx, evaluate)
	} else {
		err = evaluate()
	}
	if err != nil {
		if errors.Is(err, services.ErrQueueFull) || errors.Is(err, services.ErrQueueShutdown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "submission timed out"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 4. Submission Persistence (outside any lock — INSERT only touches app.submissions)
	sub := models.Submission{
		ExamID:            *question.ExamID,
		StudentID:         student.ID,
		QuestionID:        question.ID,
		SQLText:           req.SQLText,
		IsCorrect:         result.IsCorrect,
		Score:             result.Score,
		ValidationDetails: result.ValidationDetails,
	}
	if err := ctrl.repo.CreateSubmission(ctx, sub); err != nil {
		fmt.Printf("Warning: failed to persist submission: %v\n", err)
	}

	c.JSON(http.StatusOK, result)
}

func (ctrl *QuizController) PreviewAnswer(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	var req models.SubmissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	question, err := ctrl.repo.GetQuestion(ctx, req.QuestionNumber)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("question not found: %v", err)})
		return
	}

	if question.ExamID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is not assigned to an examination"})
		return
	}

	exam, err := ctrl.repo.GetExamByID(ctx, *question.ExamID)
	if err != nil || !exam.IsActive {
		c.JSON(http.StatusForbidden, gin.H{"error": "this examination is not active"})
		return
	}

	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, student.ID, *question.ExamID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not enrolled in this examination"})
		return
	}

	var (
		vResult models.ValidationResult
		vRows   []map[string]interface{}
		vCols   []string
		vCount  int
		vTrunc  bool
	)

	preview := func() error {
		var e error
		vResult, vRows, vCols, vCount, vTrunc, e = ctrl.valService.ValidatePreview(ctx, student.ID, question, req.SQLText, repository.ExamSandboxName(*question.ExamID))
		return e
	}

	if question.Type == "DML" {
		err = ctrl.submissionQueue.Run(ctx, preview)
	} else {
		err = preview()
	}

	if err != nil {
		if errors.Is(err, services.ErrQueueFull) || errors.Is(err, services.ErrQueueShutdown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "preview timed out"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if vRows == nil {
		vRows = []map[string]interface{}{}
	}
	if vCols == nil {
		vCols = []string{}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    vResult.Status,
		"feedback":  vResult.Feedback,
		"rows":      vRows,
		"columns":   vCols,
		"row_count": vCount,
		"truncated": vTrunc,
	})
}
