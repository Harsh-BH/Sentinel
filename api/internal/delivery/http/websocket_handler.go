package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins in development; restrict in production
	},
}

// WebSocketHandler handles WebSocket connections for real-time job status updates.
type WebSocketHandler struct {
	getJobUC *usecase.GetJobUsecase
	logger   *zap.Logger
}

// NewWebSocketHandler creates a new WebSocketHandler.
func NewWebSocketHandler(getJobUC *usecase.GetJobUsecase, logger *zap.Logger) *WebSocketHandler {
	return &WebSocketHandler{
		getJobUC: getJobUC,
		logger:   logger,
	}
}

// Stream handles GET /api/v1/submissions/:id/stream (WebSocket upgrade)
func (h *WebSocketHandler) Stream(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job ID format"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close()

	h.logger.Debug("WebSocket connection opened", zap.String("job_id", idStr))

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		job, err := h.getJobUC.Execute(c.Request.Context(), id)
		if err != nil {
			conn.WriteJSON(gin.H{"error": "Job not found"})
			return
		}

		if err := conn.WriteJSON(job); err != nil {
			h.logger.Debug("WebSocket write failed (client disconnected)", zap.Error(err))
			return
		}

		// Stop streaming once the job reaches a terminal state
		if job.Status.IsTerminal() {
			h.logger.Debug("Job reached terminal state, closing WebSocket", zap.String("job_id", idStr))
			return
		}
	}
}
