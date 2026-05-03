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
type QuizController struct {
	valService  *services.ValidationService
	lockManager *services.LockManager
	repo        *repository.PostgresRepository
}

func NewQuizController(valService *services.ValidationService, lockManager *services.LockManager, repo *repository.PostgresRepository) *QuizController {
	return &QuizController{
		valService:  valService,
		lockManager: lockManager,
		repo:        repo,
	}
}

func (ctrl *QuizController) GetExams(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	exams, err := ctrl.repo.GetStudentExams(student.ID)
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

	c.JSON(http.StatusOK, questions)
}

func (ctrl *QuizController) SubmitAnswer(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	var req models.SubmissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// SECURITY: Prevent student impersonation by deriving studentID from JWT context
	studentID := strconv.Itoa(student.ID)

	var result models.ValidationResult
	var err error

	// Execute validation within a per-student lock
	err = ctrl.lockManager.ExecuteLocked(studentID, func() error {
		ctx := c.Request.Context()
		// 1. Fetch actual question from DB
		question, err := ctrl.repo.GetQuestion(ctx, req.QuestionNumber)
		if err != nil {
			return err
		}

		// 2. Validate Examination Status & Student Enrollment
		if question.ExamID != nil {
			exam, err := ctrl.repo.GetExamByID(ctx, *question.ExamID)
			if err != nil {
				return err
			}
			if !exam.IsActive {
				return fmt.Errorf("this examination is not active")
			}

			enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, student.ID, *question.ExamID)
			if err != nil {
				return err
			}
			if !enrolled {
				return fmt.Errorf("you are not enrolled in this examination")
			}
		}

		// 3. Initialize Sandbox (As superuser)
		schema := "student_" + studentID
		if err := ctrl.repo.InitializeSandbox(ctx, schema); err != nil {
			return err
		}

		// 4. For DML questions, rebuild only the affected target table from the
		// canonical public copy.
		if question.Type == "DML" && question.TargetTable != "" {
			if err := ctrl.repo.ResetTable(ctx, schema, question.TargetTable); err != nil {
				return err
			}
		}

		result, err = ctrl.valService.Validate(ctx, studentID, question, req.SQLText)
		return err
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

