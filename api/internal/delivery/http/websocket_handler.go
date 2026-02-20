package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

const (
	// Maximum duration a WebSocket connection can remain open.
	wsMaxDuration = 5 * time.Minute

	// How often we poll the database for job updates.
	wsPollInterval = 500 * time.Millisecond

	// Ping/pong keepalive intervals.
	wsPingInterval = 30 * time.Second
	wsPongTimeout  = 10 * time.Second

	// Max message size the server will read from the client.
	wsMaxMessageSize = 512
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
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

	// Verify the job exists before upgrading
	_, err = h.getJobUC.Execute(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close()

	h.logger.Debug("WebSocket connection opened", zap.String("job_id", idStr))

	// Configure connection
	conn.SetReadLimit(wsMaxMessageSize)
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongTimeout + wsPingInterval))
		return nil
	})

	// Read pump: consume messages from client (just to detect disconnection)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	// Timers
	pollTicker := time.NewTicker(wsPollInterval)
	defer pollTicker.Stop()

	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	maxTimer := time.NewTimer(wsMaxDuration)
	defer maxTimer.Stop()

	var lastStatus domain.ExecutionStatus

	for {
		select {
		case <-clientDone:
			h.logger.Debug("WebSocket client disconnected", zap.String("job_id", idStr))
			return

		case <-maxTimer.C:
			h.logger.Debug("WebSocket max duration exceeded, closing", zap.String("job_id", idStr))
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "max connection duration exceeded"))
			return

		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(wsPongTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				h.logger.Debug("WebSocket ping failed", zap.Error(err))
				return
			}

		case <-pollTicker.C:
			job, err := h.getJobUC.Execute(c.Request.Context(), id)
			if err != nil {
				conn.WriteJSON(gin.H{"error": "Job not found"})
				return
			}

			// Only send updates when status changes (avoid flooding)
			if job.Status != lastStatus {
				conn.SetWriteDeadline(time.Now().Add(wsPongTimeout))
				if err := conn.WriteJSON(job); err != nil {
					h.logger.Debug("WebSocket write failed", zap.Error(err))
					return
				}
				lastStatus = job.Status
			}

			// Stop streaming once the job reaches a terminal state
			if job.Status.IsTerminal() {
				// Send final state and close gracefully
				conn.WriteJSON(job)
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "job completed"))
				h.logger.Debug("Job reached terminal state, closing WebSocket",
					zap.String("job_id", idStr),
					zap.String("status", string(job.Status)),
				)
				return
			}
		}
	}
}
