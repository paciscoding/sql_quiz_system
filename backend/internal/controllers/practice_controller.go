package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
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
	id, ok := idParam(c, "id", publicid.TypePractice)
	if !ok {
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
		PracticeQuestionID models.PracticeQuestionID `json:"practice_question_id"`
		SQLText            string                    `json:"sql_text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	question, err := ctrl.repo.GetPracticeQuestion(ctx, int(req.PracticeQuestionID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "practice question not found"})
		return
	}

	schema, ok := ctrl.resolveCategorySchema(c, ctx, question)
	if !ok {
		return
	}

	q := question.AsQuestion()
	result, err := ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist practice attempt"})
		return
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
		PracticeQuestionID models.PracticeQuestionID `json:"practice_question_id"`
		SQLText            string                    `json:"sql_text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	question, err := ctrl.repo.GetPracticeQuestion(ctx, int(req.PracticeQuestionID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "practice question not found"})
		return
	}

	schema, ok := ctrl.resolveCategorySchema(c, ctx, question)
	if !ok {
		return
	}

	q := question.AsQuestion()
	vResult, rows, columns, rowCount, truncated, err := ctrl.validator.ValidatePreview(ctx, int(student.ID), *q, req.SQLText, schema)
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

	attempts, err := ctrl.repo.ListPracticeAttempts(c.Request.Context(), int(student.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, attempts)
}

func (ctrl *PracticeController) GetSummary(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	summary, err := ctrl.repo.GetPracticeSummary(c.Request.Context(), int(student.ID))
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

	// A practice question must belong to an existing category (the category owns
	// the schema the question runs against).
	if q.CategoryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "category_id is required"})
		return
	}
	exists, existsErr := ctrl.repo.CategoryExists(ctx, int(q.CategoryID))
	if existsErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": existsErr.Error()})
		return
	}
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "category not found"})
		return
	}

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
	saved, err := ctrl.repo.GetPracticeQuestion(ctx, int(q.ID))
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
	id, ok := idParam(c, "id", publicid.TypePractice)
	if !ok {
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

// resolveCategorySchema returns the schema a practice question runs against and
// verifies its database has been imported. On any problem it writes the HTTP
// error response and returns ok=false.
func (ctrl *PracticeController) resolveCategorySchema(c *gin.Context, ctx context.Context, q *models.PracticeQuestion) (string, bool) {
	schema := q.CategorySchema
	if schema == "" && q.CategoryID > 0 {
		s, err := ctrl.repo.GetPracticeCategorySchema(ctx, int(q.CategoryID))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "practice question has no category configured"})
			return "", false
		}
		schema = s
	}
	if schema == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "practice question has no category database configured"})
		return "", false
	}
	hasTables, err := ctrl.repo.SchemaHasTables(ctx, schema)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return "", false
	}
	if !hasTables {
		c.JSON(http.StatusBadRequest, gin.H{"error": repository.ErrCategoryEmpty.Error()})
		return "", false
	}
	return schema, true
}

// GetCategoryIntroduction (GET /practice/categories/:id/introduction)
// Returns the category schema tables + description for enrolled students.
// Returns imported:false and tables:[] when the category schema has no tables yet.
func (ctrl *PracticeController) GetCategoryIntroduction(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeCategory)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	cat, err := ctrl.repo.GetPracticeCategory(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "practice category not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

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

	c.JSON(http.StatusOK, gin.H{
		"category_id":   cat.ID,
		"category_name": cat.Name,
		"introduction":  cat.Introduction,
		"imported":      imported,
		"tables":        tables,
	})
}

// SetCategoryIntroduction (PUT /lecturer/practice/categories/:id/introduction)
// Persists a lecturer-authored description text for a practice category.
func (ctrl *PracticeController) SetCategoryIntroduction(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeCategory)
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

	if err := ctrl.repo.UpdatePracticeCategoryIntroduction(c.Request.Context(), id, text); err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "practice category not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Category introduction updated"})
}

// ListCategories (GET /lecturer/practice/categories)
func (ctrl *PracticeController) ListCategories(c *gin.Context) {
	cats, err := ctrl.repo.ListPracticeCategories(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cats)
}

// CreateCategory (POST /lecturer/practice/categories)
func (ctrl *PracticeController) CreateCategory(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

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

	cat, err := ctrl.repo.CreatePracticeCategory(c.Request.Context(), name, int(lecturer.ID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, cat)
}

// RenameCategory (PUT /lecturer/practice/categories/:id)
func (ctrl *PracticeController) RenameCategory(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeCategory)
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

	if err := ctrl.repo.RenamePracticeCategory(c.Request.Context(), id, name); err != nil {
		if errors.Is(err, repository.ErrCategoryNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Category renamed"})
}

// DeleteCategory (DELETE /lecturer/practice/categories/:id[?force=true])
func (ctrl *PracticeController) DeleteCategory(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeCategory)
	if !ok {
		return
	}
	force := c.Query("force") == "true"

	if err := ctrl.repo.DeletePracticeCategory(c.Request.Context(), id, force); err != nil {
		switch {
		case errors.Is(err, repository.ErrCategoryNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrCategoryHasQuestions), errors.Is(err, repository.ErrCategoryProtected):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Category deleted"})
}

// ImportCategorySchema (POST /lecturer/practice/categories/:id/import[?confirm_replace=true])
func (ctrl *PracticeController) ImportCategorySchema(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	id, ok := idParam(c, "id", publicid.TypeCategory)
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

	if err := ctrl.repo.ImportSchemaIntoCategory(c.Request.Context(), id, req.SQL, int(lecturer.ID), confirmReplace); err != nil {
		switch {
		case errors.Is(err, repository.ErrCategoryNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrSchemaQualifiedName):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, repository.ErrCategoryNotEmpty):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Database imported into category"})
}

// ─── Category Intro Images ────────────────────────────────────────────────────

// ListCategoryIntroImages (GET /practice/categories/:id/intro-images)
// Returns the metadata list of intro images for a practice category.
func (ctrl *PracticeController) ListCategoryIntroImages(c *gin.Context) {
	id, ok := idParam(c, "id", publicid.TypeCategory)
	if !ok {
		return
	}

	imgs, err := ctrl.repo.ListCategoryIntroImages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"images": imgs})
}

// GetCategoryIntroImage (GET /practice/categories/:id/intro-images/:imageId)
// Streams the binary content of a single intro image with ETag-based caching.
func (ctrl *PracticeController) GetCategoryIntroImage(c *gin.Context) {
	catID, ok := idParam(c, "id", publicid.TypeCategory)
	if !ok {
		return
	}
	imgID, ok := idParam(c, "imageId", publicid.TypeImage)
	if !ok {
		return
	}

	mime, data, updatedAt, err := ctrl.repo.GetCategoryIntroImageBytes(c.Request.Context(), catID, imgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	etag := fmt.Sprintf("%d", updatedAt.Unix())
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}

	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, max-age=86400, must-revalidate")
	c.Header("Content-Length", fmt.Sprintf("%d", len(data)))
	c.Data(http.StatusOK, mime, data)
}

// UploadCategoryIntroImage (POST /lecturer/practice/categories/:id/intro-images)
// Accepts a multipart upload with field "image", validates the mime type by
// magic bytes, enforces per-file (5 MB) and aggregate (25 MB) size limits, and
// inserts the image. Returns the inserted metadata row (201).
func (ctrl *PracticeController) UploadCategoryIntroImage(c *gin.Context) {
	catID, ok := idParam(c, "id", publicid.TypeCategory)
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not read image"})
		return
	}
	detectedMime, err := sniffImageMime(magic)
	if err != nil {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": err.Error()})
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
	existing, err := ctrl.repo.CountCategoryIntroImageBytes(ctx, catID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if existing+int64(len(data)) > 25*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "aggregate intro-image size exceeds 25 MB"})
		return
	}

	img, err := ctrl.repo.InsertCategoryIntroImage(ctx, catID, detectedMime, data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, img)
}

// DeleteCategoryIntroImage (DELETE /lecturer/practice/categories/:id/intro-images/:imageId)
// Removes a single intro image from a practice category. Returns 204 on success.
func (ctrl *PracticeController) DeleteCategoryIntroImage(c *gin.Context) {
	catID, ok := idParam(c, "id", publicid.TypeCategory)
	if !ok {
		return
	}
	imgID, ok := idParam(c, "imageId", publicid.TypeImage)
	if !ok {
		return
	}

	if err := ctrl.repo.DeleteCategoryIntroImage(c.Request.Context(), catID, imgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "image not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}

// ReorderCategoryIntroImages (PUT /lecturer/practice/categories/:id/intro-images/order)
// Accepts {"order":[{"id":1,"sort_order":0},…]} and applies the new ordering.
func (ctrl *PracticeController) ReorderCategoryIntroImages(c *gin.Context) {
	catID, ok := idParam(c, "id", publicid.TypeCategory)
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

	if err := ctrl.repo.ReorderCategoryIntroImages(c.Request.Context(), catID, order); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Order updated"})
}

// sniffImageMime inspects the leading magic bytes of buf and returns
// "image/png" or "image/jpeg".  It returns an error for any other byte
// sequence, including buffers shorter than 3 bytes.
func sniffImageMime(buf []byte) (string, error) {
	switch {
	case len(buf) >= 4 && buf[0] == 0x89 && buf[1] == 0x50 && buf[2] == 0x4E && buf[3] == 0x47:
		return "image/png", nil
	case len(buf) >= 3 && buf[0] == 0xFF && buf[1] == 0xD8 && buf[2] == 0xFF:
		return "image/jpeg", nil
	default:
		return "", fmt.Errorf("only image/png and image/jpeg are accepted")
	}
}
