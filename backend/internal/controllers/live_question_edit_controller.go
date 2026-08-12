package controllers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// LiveQuestionEditController serves the one edit a lecturer may make to a
// question of a RUNNING examination: its wording.
//
// The normal authoring path (POST /lecturer/question -> CreateQuestion) still
// hard-refuses every question mutation while the exam is active, and that guard
// is untouched. This endpoint does not relax it — it is a separate, narrower
// door that can only ever change app.questions.requirement.
//
// What it deliberately does NOT do:
//   - it never re-runs the Phase-0 expected-result capture (the answer key is
//     unchanged by construction, so the existing capture stays valid);
//   - it never reads or writes app.submissions. No score, is_correct or
//     validation_details is touched, so grades already recorded cannot move.
//
// Any attempt to change a field grading reads — or the points a submission is
// worth — is rejected with 400 and the offending field names, so the lecturer
// finds out immediately rather than discovering mid-exam that half the class
// was graded against a different question than the other half.
type LiveQuestionEditController struct {
	repo *repository.PostgresRepository
}

func NewLiveQuestionEditController(repo *repository.PostgresRepository) *LiveQuestionEditController {
	return &LiveQuestionEditController{repo: repo}
}

// liveEditResponse is the success payload. The zero-valued grading fields are
// present and always zero on purpose: the lecturer UI reports "0 submissions
// re-graded" from them, which is the reassurance this endpoint exists to give.
type liveEditResponse struct {
	UpdatedFields     []string `json:"updated_fields"`
	AnswerKeyChanged  bool     `json:"answer_key_changed"`
	RegradedCount     int      `json:"regraded_count"`
	PointsRescaled    bool     `json:"points_rescaled"`
	ScoreChangedCount int      `json:"score_changed_count"`
	CaptureStatus     string   `json:"capture_status"`
}

// LiveEditQuestion handles PUT /api/v1/lecturer/exams/:id/questions/:questionId/live-edit
func (ctrl *LiveQuestionEditController) LiveEditQuestion(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	// Exam-question ids are readable ints (they are not sqids-encoded), same as
	// :imageId on the sibling intro-image routes.
	questionID, err := strconv.Atoi(c.Param("questionId"))
	if err != nil || questionID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid question id"})
		return
	}

	var req models.Question
	if err := c.ShouldBindBodyWithJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// DMLKind/SeedSQL are json:"-" on models.Question (ARCH-053), so the bind
	// above cannot populate them — recover exactly those two from the same
	// cached body, the way UpsertQuestion does. They are recovered only to be
	// COMPARED against what is stored; they are never written here.
	var extra struct {
		DMLKind string `json:"dml_kind"`
		SeedSQL string `json:"seed_sql"`
		Confirm bool   `json:"confirm"`
	}
	if err := c.ShouldBindBodyWithJSON(&extra); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !extra.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be true to edit a question of a running examination"})
		return
	}
	req.DMLKind, req.SeedSQL = extra.DMLKind, extra.SeedSQL

	ctx := c.Request.Context()

	// Ids come from the URL, never from the body — a body-supplied exam_id or
	// id must not be able to redirect the write at another exam's question.
	stored, err := ctrl.repo.GetQuestion(ctx, questionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "question not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if stored.ExamID == nil || int(*stored.ExamID) != examID {
		c.JSON(http.StatusNotFound, gin.H{"error": "question not found"})
		return
	}

	if changed := lockedFieldsChanged(stored, req); len(changed) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "this examination is running, so only the question wording can be changed. " +
				"Deactivate the exam to change: " + strings.Join(changed, ", "),
			"locked_fields_changed": changed,
		})
		return
	}

	requirement := strings.TrimSpace(req.Requirement)
	if requirement == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "requirement cannot be empty"})
		return
	}

	if err := ctrl.repo.UpdateQuestionRequirementLive(ctx, examID, questionID, requirement); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "question not found"})
			return
		}
		c.JSON(statusForRepoErr(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, liveEditResponse{
		UpdatedFields: []string{repository.LiveEditableField},
		// Constant by construction: reaching this line means nothing grading
		// reads was allowed to change, so no submission was re-graded and no
		// score moved.
		AnswerKeyChanged:  false,
		RegradedCount:     0,
		PointsRescaled:    false,
		ScoreChangedCount: 0,
		CaptureStatus:     stored.ExpectedResultStatus,
	})
}

// lockedFieldsChanged returns the JSON names of every field the request would
// change that is NOT editable while the exam runs.
//
// The list covers everything grading reads (see services/validation_service.go:
// decideStatus, decideDMLStatus and compareResults branch on Type, TargetTable,
// StrictMode and the captured expected_* columns) plus Points, which decides
// what an already-graded submission is worth, plus the schema-selecting
// CategoryID. A field missing from this list would be a silent hole: it would
// let a mid-exam edit move the answer key while the endpoint reports that
// nothing changed.
func lockedFieldsChanged(stored, req models.Question) []string {
	var changed []string
	add := func(name string) { changed = append(changed, name) }

	if strings.TrimSpace(req.CorrectSQL) != strings.TrimSpace(stored.CorrectSQL) {
		add("correct_sql")
	}
	// target_table is stored as the pg_class-resolved name, so compare
	// case-insensitively — a lecturer whose form echoes back the same table in
	// different casing has not actually changed anything.
	if !strings.EqualFold(strings.TrimSpace(req.TargetTable), strings.TrimSpace(stored.TargetTable)) {
		add("target_table")
	}
	if req.Type != stored.Type {
		add("type")
	}
	if req.Points != stored.Points {
		add("points")
	}
	if req.StrictMode != stored.StrictMode {
		add("strict_mode")
	}
	if req.DualSchema != stored.DualSchema {
		add("dual_schema")
	}
	if strings.TrimSpace(req.DMLKind) != strings.TrimSpace(stored.DMLKind) {
		add("dml_kind")
	}
	if strings.TrimSpace(req.SeedSQL) != strings.TrimSpace(stored.SeedSQL) {
		add("seed_sql")
	}
	if !sameCategory(req.CategoryID, stored.CategoryID) {
		add("category_id")
	}
	return changed
}

func sameCategory(a, b *models.ExamCategoryID) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
