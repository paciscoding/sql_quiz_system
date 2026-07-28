package controllers

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// ExamCategoryController holds the lecturer CRUD handlers (ARCH-029) and the
// student-facing category-headers handler (ARCH-030) for exam categories. It
// is deliberately its own file/type — lecturer_controller.go is already
// claimed by ARCH-028 — mirroring PracticeController's category handlers
// (practice_controller.go :531-693) one-for-one. Route registration is done
// in cmd/main.go (see NewExamCategoryController and the staff/quiz route
// groups there), wired by TODO-073.
type ExamCategoryController struct {
	repo *repository.PostgresRepository
}

func NewExamCategoryController(repo *repository.PostgresRepository) *ExamCategoryController {
	return &ExamCategoryController{repo: repo}
}

// examCategoryQuestionCountRe extracts the attached-question count out of the
// error text DeleteExamCategory wraps around ErrExamCategoryHasQuestions
// ("... N question(s) still attached to this category"), so the 409 response
// can carry it as a structured field as well as in the message.
var examCategoryQuestionCountRe = regexp.MustCompile(`(\d+) question\(s\)`)

// ListCategories (GET /lecturer/exams/:id/categories)
func (ctrl *ExamCategoryController) ListCategories(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	if _, err := ctrl.repo.GetExamByID(ctx, examID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	cats, err := ctrl.repo.ListExamCategories(ctx, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cats)
}

// CreateCategory (POST /lecturer/exams/:id/categories)
func (ctrl *ExamCategoryController) CreateCategory(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "category name is required"})
		return
	}

	ctx := c.Request.Context()
	if _, err := ctrl.repo.GetExamByID(ctx, examID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	createdBy := int(lecturer.ID)
	cat, err := ctrl.repo.CreateExamCategory(ctx, examID, name, &createdBy)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrExamCategoryNameTaken):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusCreated, cat)
}

// RenameCategory (PUT /lecturer/exams/:id/categories/:categoryId)
func (ctrl *ExamCategoryController) RenameCategory(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	categoryID, ok := idParam(c, "categoryId", publicid.TypeExamCategory)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "category name is required"})
		return
	}

	if err := ctrl.repo.RenameExamCategory(c.Request.Context(), examID, categoryID, name); err != nil {
		switch {
		case errors.Is(err, repository.ErrExamCategoryNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrExamCategoryNameTaken):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Category renamed"})
}

// DeleteCategory (DELETE /lecturer/exams/:id/categories/:categoryId)
//
// Per ARCH-036 (overriding ARCH-029's original force-detach text), this route
// accepts NO force/detach query parameter of any kind: repo.DeleteExamCategory
// refuses whenever any question still references the category, returning
// ErrExamCategoryHasQuestions (mapped to 409, carrying the attached count).
// The lecturer must reassign or delete the attached questions first.
func (ctrl *ExamCategoryController) DeleteCategory(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	categoryID, ok := idParam(c, "categoryId", publicid.TypeExamCategory)
	if !ok {
		return
	}

	if err := ctrl.repo.DeleteExamCategory(c.Request.Context(), examID, categoryID); err != nil {
		switch {
		case errors.Is(err, repository.ErrExamCategoryNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrExamCategoryHasQuestions):
			body := gin.H{"error": err.Error()}
			if m := examCategoryQuestionCountRe.FindStringSubmatch(err.Error()); m != nil {
				if n, convErr := strconv.Atoi(m[1]); convErr == nil {
					body["attached_question_count"] = n
				}
			}
			c.JSON(http.StatusConflict, body)
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Category deleted"})
}

// ImportCategorySchema (POST /lecturer/exams/:id/categories/:categoryId/import[?confirm_replace=true])
func (ctrl *ExamCategoryController) ImportCategorySchema(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	categoryID, ok := idParam(c, "categoryId", publicid.TypeExamCategory)
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

	if err := ctrl.repo.ImportSchemaIntoExamCategory(c.Request.Context(), examID, categoryID, req.SQL, int(lecturer.ID), confirmReplace); err != nil {
		switch {
		case errors.Is(err, repository.ErrExamCategoryNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrSchemaQualifiedName):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrExamCategoryNotEmpty), errors.Is(err, repository.ErrExamActive):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Database imported into category"})
}

// ImportCategoryPreviewSchema (POST /lecturer/exams/:id/categories/:categoryId/import-preview[?confirm_replace=true])
func (ctrl *ExamCategoryController) ImportCategoryPreviewSchema(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	categoryID, ok := idParam(c, "categoryId", publicid.TypeExamCategory)
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

	parityWarning, err := ctrl.repo.ImportSchemaIntoExamCategoryPreview(c.Request.Context(), examID, categoryID, req.SQL, int(lecturer.ID), confirmReplace)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrExamCategoryNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrSchemaQualifiedName):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrExamCategoryNotEmpty), errors.Is(err, repository.ErrExamActive):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Decoy database imported into category", "parity_warning": parityWarning})
}

// SetCategoryIntroduction (PUT /lecturer/exams/:id/categories/:categoryId/introduction)
func (ctrl *ExamCategoryController) SetCategoryIntroduction(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	categoryID, ok := idParam(c, "categoryId", publicid.TypeExamCategory)
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

	if err := ctrl.repo.UpdateExamCategoryIntroduction(c.Request.Context(), examID, categoryID, text); err != nil {
		if errors.Is(err, repository.ErrExamCategoryNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Category introduction updated"})
}

// examCategoryStudentView is the student-facing category-header shape
// (ARCH-030 (2)(a)). It NEVER carries schema_name / preview_schema_name —
// those are grading internals, unlike the lecturer-facing models.ExamCategory
// returned by the handlers above.
type examCategoryStudentView struct {
	ID            models.ExamCategoryID       `json:"id"`
	Name          string                      `json:"name"`
	Introduction  string                      `json:"introduction"`
	QuestionCount int                         `json:"question_count"`
	Imported      bool                        `json:"imported"`
	Tables        []models.TableIntrospection `json:"tables"`
}

// GetExamCategories (GET /quiz/exams/:id/categories) - student-facing
// category-headers endpoint. Enrollment-gated exactly like
// QuizController.GetExamIntroduction: 404 when the exam does not exist, 403
// when a student is not enrolled. NOT active-gated, so it can back the intro
// page. Admin/Lecturer skip the enrollment check, matching the intro-images
// precedent (QuizController.ListExamIntroImages). Ordered by name ASC, id ASC
// (ListExamCategories' own order), the same order categorised questions are
// grouped by (ARCH-028 (2)(b)).
func (ctrl *ExamCategoryController) GetExamCategories(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	if _, err := ctrl.repo.GetExamByID(ctx, examID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	if user.Role == models.RoleStudent {
		enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, int(user.ID), examID)
		if err != nil || !enrolled {
			c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled in this examination"})
			return
		}
	}

	cats, err := ctrl.repo.ListExamCategories(ctx, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	views := make([]examCategoryStudentView, 0, len(cats))
	for _, cat := range cats {
		imported, err := ctrl.repo.SchemaHasTables(ctx, cat.SchemaName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		tables, err := ctrl.repo.GetSchemaTablesWithColumns(ctx, cat.SchemaName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		views = append(views, examCategoryStudentView{
			ID:            cat.ID,
			Name:          cat.Name,
			Introduction:  cat.Introduction,
			QuestionCount: cat.QuestionCount,
			Imported:      imported,
			Tables:        tables,
		})
	}

	c.JSON(http.StatusOK, gin.H{"categories": views})
}
