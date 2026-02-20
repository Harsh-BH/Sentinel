package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

// SubmissionHandler handles HTTP requests for code submissions.
type SubmissionHandler struct {
	submitUC *usecase.SubmitJobUsecase
	getJobUC *usecase.GetJobUsecase
	logger   *zap.Logger
}

// NewSubmissionHandler creates a new SubmissionHandler.
func NewSubmissionHandler(submitUC *usecase.SubmitJobUsecase, getJobUC *usecase.GetJobUsecase, logger *zap.Logger) *SubmissionHandler {
	return &SubmissionHandler{
		submitUC: submitUC,
		getJobUC: getJobUC,
		logger:   logger,
	}
}

// Submit handles POST /api/v1/submissions
func (h *SubmissionHandler) Submit(c *gin.Context) {
	var req domain.SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request body: " + err.Error(),
		})
		return
	}

	resp, err := h.submitUC.Execute(c.Request.Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidLanguage):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, domain.ErrEmptySourceCode):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		case errors.Is(err, domain.ErrPayloadTooLarge):
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
		case errors.Is(err, domain.ErrPublishFailed):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Service temporarily unavailable"})
		default:
			h.logger.Error("Submit job failed", zap.Error(err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		}
		return
	}

	c.JSON(http.StatusAccepted, resp)
}

// GetByID handles GET /api/v1/submissions/:id
func (h *SubmissionHandler) GetByID(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job ID format"})
		return
	}

	job, err := h.getJobUC.Execute(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, domain.ErrJobNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		h.logger.Error("Get job failed", zap.Error(err), zap.String("job_id", idStr))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
		return
	}

	c.JSON(http.StatusOK, job)
}
