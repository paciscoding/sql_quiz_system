package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// ExportController exposes admin controls for the submission-log exporter.
type ExportController struct {
	repo *repository.PostgresRepository
	svc  *services.SubmissionExportService
}

func NewExportController(repo *repository.PostgresRepository, svc *services.SubmissionExportService) *ExportController {
	return &ExportController{repo: repo, svc: svc}
}

type exportSettingsResponse struct {
	Enabled      bool   `json:"enabled"`
	LastExportAt string `json:"last_export_at"`
	Interval     string `json:"interval"`
	Recipient    string `json:"recipient"`
}

// GetSettings returns the current toggle state and static config.
func (c *ExportController) GetSettings(ctx *gin.Context) {
	st, err := c.repo.GetExportState(ctx.Request.Context())
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read export state"})
		return
	}
	ctx.JSON(http.StatusOK, exportSettingsResponse{
		Enabled:      st.Enabled,
		LastExportAt: st.LastExportAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Interval:     c.svc.Interval().String(),
		Recipient:    c.svc.Recipient(),
	})
}

type updateSettingsRequest struct {
	Enabled *bool `json:"enabled"`
}

// UpdateSettings flips the enabled toggle.
func (c *ExportController) UpdateSettings(ctx *gin.Context) {
	var req updateSettingsRequest
	if err := ctx.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "body must be {\"enabled\": bool}"})
		return
	}
	if err := c.repo.SetExportEnabled(ctx.Request.Context(), *req.Enabled); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update export state"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"enabled": *req.Enabled})
}

// RunNow triggers an immediate export cycle (verification aid).
func (c *ExportController) RunNow(ctx *gin.Context) {
	res, err := c.svc.ExportNow(ctx.Request.Context())
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": res})
		return
	}
	ctx.JSON(http.StatusOK, res)
}
