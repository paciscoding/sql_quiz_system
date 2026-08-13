package controllers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// ExaminationResponse is the outbound exam DTO with the derived effective_state field.
type ExaminationResponse struct {
	ID               models.ExamID `json:"id"`
	Title            string        `json:"title"`
	IsActive         bool          `json:"is_active"`
	CreatedBy        models.UserID `json:"created_by"`
	CreatedAt        time.Time     `json:"created_at"`
	ActivatedAt      *time.Time    `json:"activated_at,omitempty"`
	ArchivedAt       *time.Time    `json:"archived_at,omitempty"`
	ScheduledStartAt *time.Time    `json:"scheduled_start_at,omitempty"`
	ScheduledEndAt   *time.Time    `json:"scheduled_end_at,omitempty"`
	EffectiveState   string        `json:"effective_state"`
	Introduction     string        `json:"introduction"`
}

type QuizController struct {
	valService      *services.ValidationService
	submissionQueue *services.SubmissionQueue
	repo            *repository.PostgresRepository
	hub             *services.Hub
	authService     *services.AuthService
}

func NewQuizController(valService *services.ValidationService, submissionQueue *services.SubmissionQueue, repo *repository.PostgresRepository, hub *services.Hub, authService *services.AuthService) *QuizController {
	return &QuizController{
		valService:      valService,
		submissionQueue: submissionQueue,
		repo:            repo,
		hub:             hub,
		authService:     authService,
	}
}

// examToResponse converts a models.Examination to ExaminationResponse with computed effective_state.
func examToResponse(e models.Examination) ExaminationResponse {
	state := repository.ExaminationEffectiveState(e)
	return ExaminationResponse{
		ID:               e.ID,
		Title:            e.Title,
		IsActive:         e.IsActive,
		CreatedBy:        e.CreatedBy,
		CreatedAt:        e.CreatedAt,
		ActivatedAt:      e.ActivatedAt,
		ArchivedAt:       e.ArchivedAt,
		ScheduledStartAt: e.ScheduledStartAt,
		ScheduledEndAt:   e.ScheduledEndAt,
		EffectiveState:   state.Phase,
		Introduction:     e.Introduction,
	}
}

func (ctrl *QuizController) GetExams(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	var exams []models.Examination
	var err error
	if c.Query("include_inactive") == "true" {
		exams, err = ctrl.repo.GetStudentEnrolledExams(c.Request.Context(), int(student.ID))
	} else {
		exams, err = ctrl.repo.GetStudentExams(c.Request.Context(), int(student.ID))
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := make([]ExaminationResponse, 0, len(exams))
	for _, e := range exams {
		resp = append(resp, examToResponse(e))
	}
	c.JSON(http.StatusOK, resp)
}

func (ctrl *QuizController) GetExamQuestions(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	// Verify enrollment
	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(c.Request.Context(), int(student.ID), examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not enrolled in this examination"})
		return
	}

	// Verify exam is effectively active (uses scheduled window)
	exam, err := ctrl.repo.GetExamByID(c.Request.Context(), examID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Examination not found"})
		return
	}
	examState := repository.ExaminationEffectiveState(exam)
	if !examState.ActiveNow {
		c.JSON(http.StatusForbidden, gin.H{"error": "Examination is not active"})
		return
	}
	// Lazy sandbox preparation for DML exams
	if err := ctrl.repo.EnsureExamSandboxReady(c.Request.Context(), examID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare exam sandbox"})
		return
	}
	// Lazy sandbox preparation for DML questions attached to a category that
	// owns its own schema (ARCH-028 (4)(d)).
	if err := ctrl.repo.EnsureExamCategorySchemasReady(c.Request.Context(), examID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to prepare exam category sandboxes"})
		return
	}
	// Broadcast activation if the exam just became active (scheduled lazy trigger)
	if ctrl.hub != nil {
		ctrl.hub.Broadcast(examID, buildExamStatusEvent(examID, "active"))
	}

	questions, err := ctrl.repo.GetQuestionsByExamID(c.Request.Context(), examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Students see the expected result for QUERY questions (it's the target
	// rows they're supposed to reproduce) but NOT for DML — revealing the
	// post-image of a mutation question would leak the answer outright.
	//
	// This used to also strip dual-schema QUERY questions, which in practice
	// meant EVERY exam question (the lecturer UI authors exam questions with
	// dual_schema = true), so the exam view never showed the "Expected Result"
	// table that PracticeView shows. Product decision: show it, so exam and
	// practice behave the same.
	//
	// KNOWN TRADE-OFF: expected_result is captured on grading schema B and
	// grading compares the student's rows against it, so a student who can see
	// those rows can reproduce them literally (SELECT ... FROM (VALUES ...))
	// and score without writing the intended query. It is also B's data while
	// the student's preview runs on decoy schema A, so the shown rows will not
	// match their own test-run output on a dual question.
	//
	// Correct SQL is still stripped regardless of type.
	for i := range questions {
		questions[i].CorrectSQL = ""
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

func (ctrl *QuizController) GetExamResults(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(c.Request.Context(), int(student.ID), examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "Not enrolled in this examination"})
		return
	}

	results, err := ctrl.repo.GetStudentExamResults(c.Request.Context(), int(student.ID), examID)
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

	results, err := ctrl.repo.GetStudentResultsSummary(c.Request.Context(), int(student.ID))
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
	exam, err := ctrl.repo.GetExamByID(ctx, int(*question.ExamID))
	if err != nil || !repository.ExaminationEffectiveState(exam).ActiveNow {
		c.JSON(http.StatusForbidden, gin.H{"error": "this examination is not active"})
		return
	}
	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, int(student.ID), int(*question.ExamID))
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not enrolled in this examination"})
		return
	}

	// 3. Phase 2 Evaluation. DML serialises through the global in-process FIFO
	// submission queue (single worker). SELECT runs lock-free against frozen
	// public.* under the read-only role.
	var result models.ValidationResult
	evaluate := func(jobCtx context.Context) error {
		var evalErr error
		result, evalErr = ctrl.valService.Validate(jobCtx, int(student.ID), question, req.SQLText, repository.ExamGradingSandboxFor(question))
		return evalErr
	}

	if question.Type == "DML" {
		err = ctrl.submissionQueue.Run(ctx, evaluate)
	} else {
		err = evaluate(ctx)
	}
	if err != nil {
		if errors.Is(err, services.ErrQueueFull) || errors.Is(err, services.ErrQueueShutdown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		// ARCH-059: the engine shed this evaluation because another one held the
		// same execution schema. Answers 503, not 504 — nothing ran, so nothing
		// timed out, and the request is retriable. Kept above the
		// DeadlineExceeded arm for readability (busy-then-timeout reads as the
		// natural order of causes), not because ordering is load-bearing:
		// ErrSchemaBusy deliberately does not wrap context.DeadlineExceeded, so
		// the two arms can never both match the same err and a future reader
		// must not treat this position as an invariant.
		if errors.Is(err, repository.ErrSchemaBusy) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "schema_busy",
				"message": "Another submission is being graded against this data. Please try again."})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist submission"})
		return
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

	exam, err := ctrl.repo.GetExamByID(ctx, int(*question.ExamID))
	if err != nil || !repository.ExaminationEffectiveState(exam).ActiveNow {
		c.JSON(http.StatusForbidden, gin.H{"error": "this examination is not active"})
		return
	}

	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, int(student.ID), int(*question.ExamID))
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "you are not enrolled in this examination"})
		return
	}

	// Exam INSERT/UPDATE/DELETE gets no test run at all. The response was already
	// redacted to a bare row_count, but that count is itself a free oracle over
	// the sandbox: a student can binary-search the table with repeated DELETE ...
	// WHERE previews and read out data they were never shown, and can tell a
	// working statement from a no-op without spending a submission. The button is
	// hidden in StudentView, and this is the enforcement — the endpoint is
	// reachable directly with any student's token.
	//
	// QUERY previews are untouched: they are read-only and their rows come from
	// decoy schema A, which the student is allowed to see.
	if question.Type != "QUERY" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "test run is not available for INSERT/UPDATE/DELETE questions in an examination",
		})
		return
	}

	// Type-aware preview resolution to decoy schema A (spec 6.1): exam QUERY
	// reads the curriculum, so A = preview curriculum; exam DML mutates the
	// shared sandbox, so A = a preview sandbox cloned from the preview
	// curriculum, built lazily on first use. If dual_schema is on but no
	// usable decoy exists, previewSchema stays "" and ValidatePreview falls
	// back to grading schema B — exactly today's behaviour — and the
	// fallback is logged server-side. The student is never blocked or told.
	//
	// ARCH-028 (4)(c): when the question has a category that owns its schema,
	// the category's OWN decoy is used instead of the exam-level decoy —
	// otherwise (no category, or a legacy-adopted category) today's exam-level
	// branch runs verbatim.
	previewSchema := ""
	categoryOwnsSchema := question.CategoryID != nil && repository.ExamCategoryOwnsSchema(int(*question.CategoryID), question.CategorySchema)
	if question.DualSchema {
		if categoryOwnsSchema {
			catID := int(*question.CategoryID)
			if question.Type == "QUERY" {
				if question.CategoryPreviewSchema != "" {
					if hasTables, _ := ctrl.repo.SchemaHasTables(ctx, question.CategoryPreviewSchema); hasTables {
						previewSchema = question.CategoryPreviewSchema
					}
				}
			} else {
				if err := ctrl.repo.EnsureExamCategoryPreviewSandboxReady(ctx, catID); err == nil {
					previewSchema = repository.ExamCategoryPreviewSandboxName(catID)
				}
			}
		} else {
			pc, _ := ctrl.repo.GetExamPreviewCurriculumSchema(ctx, int(*question.ExamID))
			if pc != "" {
				if question.Type == "QUERY" {
					if hasTables, _ := ctrl.repo.SchemaHasTables(ctx, pc); hasTables {
						previewSchema = pc
					}
				} else {
					if err := ctrl.repo.EnsureExamPreviewSandboxReady(ctx, int(*question.ExamID)); err == nil {
						previewSchema = repository.ExamPreviewSandboxName(int(*question.ExamID))
					}
				}
			}
		}
		if previewSchema == "" {
			// Dual-without-decoy fallback (spec 6.1): single-schema fallback to B. Student is
			// never blocked/told; the lecturer is warned on save instead (see lecturer_controller.go).
			log.Printf("dual-schema fallback: exam question %d (exam %d) has dual_schema=true but no usable decoy; preview running on grading schema B", question.ID, int(*question.ExamID))
		}
	}

	var (
		vResult models.ValidationResult
		vRows   []map[string]interface{}
		vCols   []string
		vCount  int
		vTrunc  bool
	)

	preview := func(jobCtx context.Context) error {
		var e error
		vResult, vRows, vCols, vCount, vTrunc, e = ctrl.valService.ValidatePreview(jobCtx, int(student.ID), question, req.SQLText, repository.ExamGradingSandboxFor(question), previewSchema)
		return e
	}

	if question.Type == "DML" {
		err = ctrl.submissionQueue.Run(ctx, preview)
	} else {
		err = preview(ctx)
	}

	if err != nil {
		if errors.Is(err, services.ErrQueueFull) || errors.Is(err, services.ErrQueueShutdown) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		// ARCH-059: the engine shed this evaluation because another one held the
		// same execution schema. Answers 503, not 504 — nothing ran, so nothing
		// timed out, and the request is retriable. Kept above the
		// DeadlineExceeded arm for readability (busy-then-timeout reads as the
		// natural order of causes), not because ordering is load-bearing:
		// ErrSchemaBusy deliberately does not wrap context.DeadlineExceeded, so
		// the two arms can never both match the same err and a future reader
		// must not treat this position as an invariant.
		if errors.Is(err, repository.ErrSchemaBusy) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "schema_busy",
				"message": "Another submission is being graded against this data. Please try again."})
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

	// Neutralize correctness so students cannot infer right/wrong during a test
	// run. Syntax errors are preserved verbatim so students still see SQL errors.
	status := vResult.Status
	feedback := vResult.Feedback
	if status == "correct" || status == "incorrect" {
		status = "preview"
		feedback = ""
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    status,
		"feedback":  feedback,
		"rows":      vRows,
		"columns":   vCols,
		"row_count": vCount,
		"truncated": vTrunc,
	})
}

// GetExamIntroduction handles GET /api/v1/quiz/exams/:id/introduction
// Returns the public curriculum schema tables + the lecturer's description for
// any enrolled student, regardless of whether the exam is currently active.
func (ctrl *QuizController) GetExamIntroduction(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	exam, err := ctrl.repo.GetExamByID(ctx, examID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, int(student.ID), examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled in this examination"})
		return
	}

	// ARCH-030 (4) bugfix: use the exam's OWN curriculum schema, not a
	// hardcoded 'public' — an exam with a per-exam curriculum has always
	// shown students the wrong table list otherwise. Falls back to 'public'
	// when the exam hasn't had a per-exam import, matching today's behaviour.
	schema := exam.CurriculumSchema
	if schema == "" {
		schema = "public"
	}
	tables, err := ctrl.repo.GetSchemaTablesWithColumns(ctx, schema)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"exam_id":      exam.ID,
		"title":        exam.Title,
		"introduction": exam.Introduction,
		"tables":       tables,
	})
}

// StreamExamStatus handles GET /api/v1/quiz/exams/:id/stream?access_token=<jwt>
// It holds an HTTP connection open and pushes Server-Sent Events to enrolled students.
// Authentication is done via query param because EventSource cannot send Authorization headers.
func (ctrl *QuizController) StreamExamStatus(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	// Authenticate via ?access_token= query param (EventSource cannot send Authorization headers)
	token := c.Query("access_token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "access_token is required"})
		return
	}
	// Use AuthenticateRequest (not signature-only ValidateToken) so a token whose
	// token_version was bumped by a password reset/change is rejected here too —
	// the SSE stream must honour the same immediate-invalidation guarantee.
	ctx := c.Request.Context()
	user, err := ctrl.authService.AuthenticateRequest(ctx, token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}

	// Verify the exam exists
	exam, err := ctrl.repo.GetExamByID(ctx, examID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	// Verify enrollment
	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, int(user.ID), examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled in this examination"})
		return
	}

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Send immediate snapshot so a client connecting after activation gets 'active' at once
	state := repository.ExaminationEffectiveState(exam)
	snapshotPayload := fmt.Sprintf("event: exam-status\nid: %d\ndata: {\"exam_id\":%d,\"effective_state\":\"%s\",\"server_time\":\"%s\"}\n\n",
		time.Now().UnixMilli(), examID, state.Phase, time.Now().UTC().Format(time.RFC3339))
	c.Writer.WriteString(snapshotPayload)
	c.Writer.Flush()

	// Subscribe to hub for activation broadcasts
	ch := ctrl.hub.Subscribe(examID)
	defer ctrl.hub.Unsubscribe(examID, ch)

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case payload, ok := <-ch:
			if !ok {
				return
			}
			// payload is already a complete SSE frame (event/id/data/\n\n) built
			// by buildExamStatusEvent; write it directly to avoid a doubled header.
			c.Writer.WriteString(payload)
			c.Writer.Flush()
		case <-ticker.C:
			c.Writer.WriteString(": ping\n\n")
			c.Writer.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// ─── Exam Intro Images (student-facing GETs) ──────────────────────────────────

// ListExamIntroImages handles GET /api/v1/quiz/exams/:id/intro-images
// Returns the metadata list for intro images attached to an examination.
// Students must be enrolled; Admin/Lecturer skip the enrollment check so the
// IntroImagesEditor on the lecturer view can list freshly-uploaded images.
func (ctrl *QuizController) ListExamIntroImages(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	_, err := ctrl.repo.GetExamByID(ctx, examID)
	if err != nil {
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

	imgs, err := ctrl.repo.ListExamIntroImages(ctx, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"images": imgs})
}

// GetExamIntroImage handles GET /api/v1/quiz/exams/:id/intro-images/:imageId
// Streams the binary content of a single intro image. Students must be enrolled;
// Admin/Lecturer skip the enrollment check so thumbnails render in the editor.
func (ctrl *QuizController) GetExamIntroImage(c *gin.Context) {
	userVal, _ := c.Get("user")
	user := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}
	imgID, ok := idParam(c, "imageId", publicid.TypeImage)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	_, err := ctrl.repo.GetExamByID(ctx, examID)
	if err != nil {
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

	mime, data, updatedAt, err := ctrl.repo.GetExamIntroImageBytes(ctx, examID, imgID)
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
