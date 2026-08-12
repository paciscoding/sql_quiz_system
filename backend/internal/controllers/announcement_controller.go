package controllers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/publicid"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

// maxAnnouncementMessageLen is the longest message a lecturer may send,
// enforced server-side after trimming (per the API contract).
const maxAnnouncementMessageLen = 2000

// AnnouncementController implements the staff and student exam-announcement
// endpoints described in the API contract (ARCH announcement feature).
type AnnouncementController struct {
	repo *repository.PostgresRepository
}

func NewAnnouncementController(repo *repository.PostgresRepository) *AnnouncementController {
	return &AnnouncementController{repo: repo}
}

type createAnnouncementRequest struct {
	Message string `json:"message"`
}

// CreateAnnouncement handles POST /api/v1/lecturer/exams/:id/announcements
func (ctrl *AnnouncementController) CreateAnnouncement(c *gin.Context) {
	userVal, _ := c.Get("user")
	lecturer := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	var req createAnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}
	if len(message) > maxAnnouncementMessageLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message must be at most 2000 characters"})
		return
	}

	ctx := c.Request.Context()

	if _, err := ctrl.repo.GetExamByID(ctx, examID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	announcement, err := ctrl.repo.CreateAnnouncement(ctx, examID, int(lecturer.ID), message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, announcement)
}

// ListAnnouncements handles GET /api/v1/lecturer/exams/:id/announcements
func (ctrl *AnnouncementController) ListAnnouncements(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	if _, err := ctrl.repo.GetExamByID(ctx, examID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "exam not found"})
		return
	}

	announcements, err := ctrl.repo.ListAnnouncementsForExam(ctx, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, announcements)
}

// DeleteAnnouncement handles DELETE /api/v1/lecturer/exams/:id/announcements/:announcementId
func (ctrl *AnnouncementController) DeleteAnnouncement(c *gin.Context) {
	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	// announcementId is the DB-native SERIAL id, not an opaque publicid token
	// (the API contract does not list it among the typed id fields), so it is
	// parsed as a plain integer path param, matching e.g. :imageId.
	announcementID, err := strconv.Atoi(c.Param("announcementId"))
	if err != nil || announcementID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid announcement id"})
		return
	}

	ctx := c.Request.Context()

	rowsDeleted, err := ctrl.repo.DeleteAnnouncement(ctx, examID, announcementID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rowsDeleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "announcement not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "announcement deleted"})
}

// ListActiveAnnouncements handles GET /api/v1/quiz/exams/:id/announcements
func (ctrl *AnnouncementController) ListActiveAnnouncements(c *gin.Context) {
	userVal, _ := c.Get("user")
	student := userVal.(models.User)

	examID, ok := idParam(c, "id", publicid.TypeExam)
	if !ok {
		return
	}

	ctx := c.Request.Context()

	// Every failure below answers with the SAME 403 body, deliberately: a
	// student must not be able to tell "this exam id does not exist" from
	// "it exists but you are not on its roster" or "it is not running". That
	// is the same non-oracle posture the sibling student handlers in
	// quiz_controller.go take.
	exam, err := ctrl.repo.GetExamByID(ctx, examID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled in this examination"})
		return
	}

	enrolled, err := ctrl.repo.IsStudentEnrolledInExam(ctx, int(student.ID), examID)
	if err != nil || !enrolled {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled in this examination"})
		return
	}

	if !repository.ExaminationEffectiveState(exam).ActiveNow {
		c.JSON(http.StatusForbidden, gin.H{"error": "not enrolled in this examination"})
		return
	}

	announcements, err := ctrl.repo.ListActiveAnnouncementsForStudent(ctx, examID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, announcements)
}
