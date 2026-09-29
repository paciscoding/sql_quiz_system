package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// validateSeedFields enforces ARCH-044 and ARCH-050 on every question write
// path. Returns a lecturer-facing error suitable for an HTTP 400 body; the
// caller never turns any of these into a 500.
// articleFor returns the indefinite article for a DML kind name, so the
// mismatch message reads "an UPDATE statement" rather than "a UPDATE
// statement". INSERT and UPDATE begin with a vowel sound; DELETE does not.
func articleFor(kind string) string {
	switch kind {
	case "INSERT", "UPDATE":
		return "an"
	default:
		return "a"
	}
}

func validateSeedFields(qType, dmlKind, seedSQL, correctSQL string) error {
	seedSQL = strings.TrimSpace(seedSQL)

	if qType == "QUERY" {
		if dmlKind != "" {
			return errors.New("dml_kind must be empty for a QUERY question")
		}
		if seedSQL != "" {
			return errors.New("seed_sql must be empty for a QUERY question")
		}
		return nil
	}

	// ARCH-057: correct_sql is the authority on what kind of statement this is.
	// The declared kind must agree with it — but only when the system can
	// actually derive it. A CTE-led statement (WITH ... DELETE ...) returns ""
	// here: unverifiable, which is not the same as wrong.
	actual := repository.LeadingDMLKind(correctSQL)

	switch dmlKind {
	case "INSERT", "UPDATE", "DELETE":
		if actual != "" && actual != dmlKind {
			return fmt.Errorf("you selected %s, but the correct SQL is %s %s statement. Pick the kind that matches your SQL", dmlKind, articleFor(actual), actual)
		}
	case "":
		// ARCH-057 rule 2: demand a kind only when one is derivable. Requiring
		// it unconditionally made CTE-led DML questions unsavable (the ARCH-050
		// regression) — the lecturer was blocked on a classification the system
		// could not make either.
		if actual != "" {
			return errors.New("dml_kind is required for a DML question and must be INSERT, UPDATE, or DELETE")
		}
	default:
		return fmt.Errorf("dml_kind must be INSERT, UPDATE, or DELETE, got %q", dmlKind)
	}

	if seedSQL == "" {
		return nil
	}
	if dmlKind == "INSERT" {
		// ARCH-044: an INSERT model answer can only affect 0 rows via
		// INSERT ... SELECT over an empty source, which seeding cannot fix.
		return errors.New("seed_sql applies only to UPDATE and DELETE questions")
	}
	if _, err := repository.ValidateSeedSQL(seedSQL); err != nil {
		return fmt.Errorf("seed_sql is not valid: %w", err)
	}
	return nil
}

// practiceValidator is the narrow slice of *services.ValidationService that the
// practice handlers use (ARCH-061). It exists so the busy-schema branch is
// testable with a fake that returns repository.ErrSchemaBusy directly, instead
// of arranging real contention against a real database.
// *services.ValidationService satisfies it, so cmd/main.go:153 is unchanged.
type practiceValidator interface {
	Validate(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string) (models.ValidationResult, error)
	ValidatePreview(ctx context.Context, studentID int, question models.Question, studentSQL string, sandboxSchema string, previewSchema string) (models.ValidationResult, []map[string]interface{}, []string, int, bool, error)
}

var _ practiceValidator = (*services.ValidationService)(nil)

// practiceRepo is the repository surface PracticeController uses; satisfied by
// *repository.PostgresRepository.
type practiceRepo interface {
	CategoryExists(ctx context.Context, id int) (bool, error)
	CategoryHasMixedDualSchema(ctx context.Context, categoryID int) (bool, error)
	CountCategoryIntroImageBytes(ctx context.Context, catID int) (int64, error)
	CreatePracticeAttempt(ctx context.Context, a *models.PracticeAttempt) error
	CreatePracticeCategory(ctx context.Context, name string, createdBy int) (*models.PracticeCategory, error)
	CreatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error
	DeleteCategoryIntroImage(ctx context.Context, catID, imgID int) error
	DeletePracticeCategory(ctx context.Context, id int, force bool) error
	DeletePracticeQuestion(ctx context.Context, id int) error
	GetCategoryIntroImageBytes(ctx context.Context, catID, imgID int) (mime string, data []byte, updatedAt time.Time, err error)
	GetPracticeCategory(ctx context.Context, id int) (*models.PracticeCategory, error)
	GetPracticeCategorySchema(ctx context.Context, id int) (string, error)
	GetPracticeQuestion(ctx context.Context, id int) (*models.PracticeQuestion, error)
	GetPracticeSummary(ctx context.Context, studentID int) ([]models.PracticeSummary, error)
	GetSchemaTablesWithColumns(ctx context.Context, schema string) ([]models.TableIntrospection, error)
	ImportSchemaIntoCategory(ctx context.Context, categoryID int, sql string, importedBy int, confirmReplace bool) error
	ImportSchemaIntoCategoryPreview(ctx context.Context, categoryID int, sql string, importedBy int, confirmReplace bool) (parityWarning string, err error)
	InsertCategoryIntroImage(ctx context.Context, catID int, mime string, data []byte) (models.IntroImage, error)
	ListCategoryIntroImages(ctx context.Context, catID int) ([]models.IntroImage, error)
	ListPracticeAttempts(ctx context.Context, studentID int) ([]models.PracticeAttempt, error)
	ListPracticeCategories(ctx context.Context) ([]models.PracticeCategory, error)
	ListPracticeQuestions(ctx context.Context) ([]models.PracticeQuestion, error)
	PreviewCorrectSQL(ctx context.Context, qType, targetTable, sqlText string) ([]map[string]interface{}, []string, error)
	PreviewPracticeCorrectSQL(ctx context.Context, categoryID int, qType, targetTable, sqlText string, useDecoy bool, seedSQL string) ([]map[string]interface{}, []string, error)
	RenamePracticeCategory(ctx context.Context, id int, name string) error
	ReorderCategoryIntroImages(ctx context.Context, catID int, order []struct{ ID, SortOrder int }) error
	SchemaHasTables(ctx context.Context, schema string) (bool, error)
	UpdatePracticeCategoryIntroduction(ctx context.Context, id int, introduction string) error
	UpdatePracticeQuestion(ctx context.Context, q *models.PracticeQuestion) error
}

var _ practiceRepo = (*repository.PostgresRepository)(nil)

type PracticeController struct {
	repo      practiceRepo
	validator practiceValidator
	auth      *services.AuthService
}

func NewPracticeController(repo practiceRepo, validator practiceValidator, auth *services.AuthService) *PracticeController {
	return &PracticeController{
		repo:      repo,
		validator: validator,
		auth:      auth,
	}
}

// abortIfSchemaBusy answers 503 when the engine shed this evaluation because
// another one held the same schema (ARCH-059/060). It returns true when it has
// written a response and the caller must return immediately.
//
// 503 and not 500: nothing ran, nothing is broken, and the request is
// retriable. The body follows the question_not_ready shape used at :191.
func (ctrl *PracticeController) abortIfSchemaBusy(c *gin.Context, err error) bool {
	if !errors.Is(err, repository.ErrSchemaBusy) {
		return false
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":   "practice_busy",
		"message": "Another submission for this category is being graded. Please try again.",
	})
	return true
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
		// QUERY questions show their Expected Result to students (parity with
		// the exam view); it is a target output, not the SQL answer. DML
		// questions keep it hidden (it can leak grading-schema row data).
		if questions[i].Type != "QUERY" {
			questions[i].ExpectedResult = nil
			questions[i].ExpectedColumns = nil
		}
		// ARCH-018 (e5): capture_error and expected_rows_affected are
		// B-derived (the lecturer's correct_sql executed on the grading
		// schema); expected_rows_affected in particular is a direct numeric
		// hint about B's data. Dual-only, to keep non-dual byte-identical.
		if questions[i].DualSchema {
			questions[i].CaptureError = ""
			questions[i].ExpectedRowsAffected = nil
		}
		// ARCH-053: auto_seed and seed_sql are `json:"-"` on the model, so there is
		// nothing to scrub here — they cannot reach a student by construction.
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
	// QUERY questions show their Expected Result to students (parity with the
	// exam view); it is a target output, not the SQL answer. DML questions keep
	// it hidden (it can leak grading-schema row data).
	if question.Type != "QUERY" {
		question.ExpectedResult = nil
		question.ExpectedColumns = nil
	}
	// ARCH-018 (e5): capture_error and expected_rows_affected are B-derived
	// (the lecturer's correct_sql executed on the grading schema); dual-only,
	// to keep non-dual byte-identical.
	if question.DualSchema {
		question.CaptureError = ""
		question.ExpectedRowsAffected = nil
	}
	// ARCH-053: auto_seed and seed_sql are `json:"-"` on the model, so there is
	// nothing to scrub here — they cannot reach a student by construction.

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

	// ARCH-004: refuse to grade a question whose model answer was never
	// successfully captured — grading it would silently compare against a nil
	// expected_result and mark every submission "The result is wrong."
	if question.ExpectedResultStatus != "captured" {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "question_not_ready",
			"message": "This question is not ready yet - its model answer could not be verified. Please tell your lecturer.",
		})
		return
	}

	schema, ok := ctrl.resolveCategorySchema(c, ctx, question)
	if !ok {
		return
	}

	q := question.AsQuestion()
	result, err := ctrl.validator.Validate(ctx, int(student.ID), *q, req.SQLText, schema)

	// ARCH-061: this check MUST stay above the persistence below. Practice
	// persists every attempt regardless of validation outcome, so a shed
	// submission would otherwise be recorded as is_correct=false for SQL that
	// never reached the database — corrupting the student's history for a
	// request the server itself refused.
	if ctrl.abortIfSchemaBusy(c, err) {
		return
	}

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

	previewSchema := ""
	if question.DualSchema && question.CategoryPreviewSchema != "" {
		if hasTables, _ := ctrl.repo.SchemaHasTables(ctx, question.CategoryPreviewSchema); hasTables {
			previewSchema = question.CategoryPreviewSchema
		}
	}
	if question.DualSchema && previewSchema == "" {
		// Dual-without-decoy fallback (spec 6.1): single-schema fallback to B. The student is
		// never blocked and never told; the lecturer is warned on save instead (see Upsert).
		log.Printf("dual-schema fallback: practice question %d (category %d) has dual_schema=true but no usable decoy; preview running on grading schema B", question.ID, question.CategoryID)
	}

	q := question.AsQuestion()
	vResult, rows, columns, rowCount, truncated, err := ctrl.validator.ValidatePreview(ctx, int(student.ID), *q, req.SQLText, schema, previewSchema)
	if ctrl.abortIfSchemaBusy(c, err) {
		return
	}
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

	// ARCH-006's DML diff (missing/extra row contents) belongs in Feedback for
	// Submit, where the attempt is recorded and the diff is the whole point.
	// On Preview ("Test Run") nothing is recorded and retries are unlimited, so
	// leaking the diff here would let a student iterate to a correct answer
	// without writing correct SQL. Suppress it for correct/incorrect results;
	// syntax_error feedback is preserved verbatim so students still see real
	// SQL errors. Unlike the exam preview guard, status is left untouched —
	// practice preview has always returned real correct/incorrect status and
	// the frontend does not treat preview status as an answer signal.
	feedback := vResult.Feedback
	if vResult.Status == "correct" || vResult.Status == "incorrect" {
		feedback = ""
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    vResult.Status,
		"feedback":  feedback,
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
	q.CreatedBy = lecturer.ID

	if err := validateSeedFields(q.Type, q.DMLKind, q.SeedSQL, q.CorrectSQL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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

	tw := repository.DMLCorrectSQLTargetTableWarning(saved.Type, saved.CorrectSQL, saved.TargetTable)

	dsw := ""
	if saved.DualSchema && saved.CategoryPreviewSchema == "" {
		dsw = "Dual schema is enabled but no decoy schema has been uploaded for this category/exam - previews will use the grading schema until you upload one."
	}

	// Mixed dual/non-dual warning (GAP 4, ARCH-018): a category holding both
	// dual and non-dual questions lets a student bypass the decoy by opening
	// the non-dual question's preview. Warn-only; a lookup failure must never
	// turn a successful save into an error, so its error is discarded.
	mdw := ""
	mixed, _ := ctrl.repo.CategoryHasMixedDualSchema(ctx, int(saved.CategoryID))
	if mixed {
		mdw = "This category contains both dual-schema and normal questions. A student can open a normal question's preview to read the grading schema, which bypasses the decoy on your dual-schema questions. Consider making every question here dual-schema."
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                     "Practice question saved",
		"id":                          saved.ID,
		"expected_result_status":      saved.ExpectedResultStatus,
		"expected_result_captured_at": saved.ExpectedResultCapturedAt,
		"expected_rows_affected":      saved.ExpectedRowsAffected,
		"capture_error":               saved.CaptureError,
		"table_warning":               tw,
		"dual_schema_warning":         dsw,
		"mixed_dual_warning":          mdw,
	})
}

// ListForLecturer (GET /lecturer/practice/questions)
func (ctrl *PracticeController) ListForLecturer(c *gin.Context) {
	questions, err := ctrl.repo.ListPracticeQuestions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.AsLecturerPracticeQuestionViews(questions))
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

// LecturerPreview (POST /lecturer/practice/preview)
func (ctrl *PracticeController) LecturerPreview(c *gin.Context) {
	var req struct {
		Type        string            `json:"type"`
		TargetTable string            `json:"target_table"`
		CorrectSQL  string            `json:"correct_sql"`
		CategoryID  models.CategoryID `json:"category_id"`
		Schema      string            `json:"schema"`
		SeedSQL     string            `json:"seed_sql"`
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

	var rows []map[string]interface{}
	var cols []string
	var err error
	useDecoy := req.Schema == "preview"
	if int(req.CategoryID) > 0 {
		rows, cols, err = ctrl.repo.PreviewPracticeCorrectSQL(c.Request.Context(), int(req.CategoryID), req.Type, req.TargetTable, req.CorrectSQL, useDecoy, req.SeedSQL)
	} else {
		rows, cols, err = ctrl.repo.PreviewCorrectSQL(c.Request.Context(), req.Type, req.TargetTable, req.CorrectSQL)
	}
	if err != nil {
		// Surface engine errors with status='error' to match LecturerController
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": err.Error()})
		return
	}

	// Ordered projection columns from the engine (pgx FieldDescriptions), not a
	// map range — see LecturerController.PreviewCorrectSQL. Never emit null.
	if cols == nil {
		cols = []string{}
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

// ImportCategoryPreviewSchema (POST /lecturer/practice/categories/:id/import-preview[?confirm_replace=true])
func (ctrl *PracticeController) ImportCategoryPreviewSchema(c *gin.Context) {
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

	parityWarning, err := ctrl.repo.ImportSchemaIntoCategoryPreview(c.Request.Context(), id, req.SQL, int(lecturer.ID), confirmReplace)
	if err != nil {
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
	c.JSON(http.StatusOK, gin.H{"message": "Decoy database imported into category", "parity_warning": parityWarning})
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
