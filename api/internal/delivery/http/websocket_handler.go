package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/Harsh-BH/Sentinel/api/internal/delivery/http/middleware"
	"github.com/Harsh-BH/Sentinel/api/internal/domain"
	"github.com/Harsh-BH/Sentinel/api/internal/usecase"
)

const (
	// Maximum duration a WebSocket connection can remain open.
	wsMaxDuration = 5 * time.Minute

	// Safety-net poll interval.
	//
	// Updates are delivered by Postgres LISTEN/NOTIFY (see notifier.go); this slow
	// poll exists only to cover the window where a notification could be missed —
	// principally a listener reconnect. At 500 ms this was the delivery mechanism
	// and cost 2 queries/second per connected viewer; at 5 s it is a backstop.
	wsPollInterval = 5 * time.Second

	// Ping/pong keepalive intervals.
	wsPingInterval = 30 * time.Second
	wsPongTimeout  = 10 * time.Second

	// Max message size the server will read from the client.
	wsMaxMessageSize = 512
)

// WebSocketHandler handles WebSocket connections for real-time job status updates.
//
// Updates are genuinely pushed: the status-change trigger fires pg_notify inside
// the same transaction as the write, a per-pod listener receives it, and this
// handler is woken to read and forward the row. A slow poll runs alongside purely
// as a safety net for a missed notification (e.g. across a listener reconnect).
//
// The previous implementation polled every 500 ms per connection and called that
// real-time. It worked, but its cost scaled with the number of viewers rather
// than the number of events.
type WebSocketHandler struct {
	getJobUC       *usecase.GetJobUsecase
	notifier       *StatusNotifier
	logger         *zap.Logger
	upgrader       websocket.Upgrader
	allowedOrigins []string
}

// NewWebSocketHandler creates a new WebSocketHandler. notifier may be nil, in
// which case the handler degrades to poll-only.
func NewWebSocketHandler(getJobUC *usecase.GetJobUsecase, notifier *StatusNotifier, logger *zap.Logger, allowedOrigins []string) *WebSocketHandler {
	return &WebSocketHandler{
		getJobUC:       getJobUC,
		notifier:       notifier,
		logger:         logger,
		allowedOrigins: allowedOrigins,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			// gorilla/websocket runs its own origin check, separate from the CORS
			// middleware, and returning true unconditionally (the previous behaviour)
			// permits cross-site WebSocket hijacking: any page can open a socket to
			// this endpoint with the visitor's cookies and read the streamed job,
			// including its source code. The same-origin policy does not apply to
			// WebSocket handshakes, so this check is the only thing standing there.
			CheckOrigin: func(r *http.Request) bool {
				return middleware.OriginAllowed(allowedOrigins, r.Header.Get("Origin"))
			},
		},
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

	// Verify the job exists before upgrading. Distinguish "no such job" from
	// "database is down" so a Postgres outage does not masquerade as 404.
	if _, err = h.getJobUC.Execute(c.Request.Context(), id); err != nil {
		if errors.Is(err, domain.ErrJobNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		h.logger.Error("WebSocket pre-flight read failed", zap.String("job_id", idStr), zap.Error(err))
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Service temporarily unavailable"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Error("WebSocket upgrade failed", zap.Error(err))
		return
	}
	defer func() { _ = conn.Close() }()

	h.logger.Debug("WebSocket connection opened", zap.String("job_id", idStr))

	// Configure connection
	conn.SetReadLimit(wsMaxMessageSize)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongTimeout + wsPingInterval))
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

	// Subscribe BEFORE the first read, so a status change that lands between the
	// read and the subscribe cannot be missed.
	var statusChanged <-chan struct{}
	if h.notifier != nil {
		ch, unsubscribe := h.notifier.Subscribe(id)
		defer unsubscribe()
		statusChanged = ch
	}

	// Safety-net poll. Notifications are the delivery path; this only covers a
	// missed event.
	pollTicker := time.NewTicker(wsPollInterval)
	defer pollTicker.Stop()

	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	maxTimer := time.NewTimer(wsMaxDuration)
	defer maxTimer.Stop()

	var lastStatus domain.ExecutionStatus

	// sendIfChanged reads the row and forwards it if the status moved. Returns
	// true when the stream should end (terminal status, or an unrecoverable read).
	//
	// Shared by both wake-up sources so a notification and a safety-net tick
	// cannot diverge in behaviour.
	sendIfChanged := func() bool {
		job, err := h.getJobUC.Execute(c.Request.Context(), id)
		if err != nil {
			// A transient read failure must not terminate the stream — the job is
			// still running and the client would lose its result. Only a genuinely
			// missing job ends it.
			if !errors.Is(err, domain.ErrJobNotFound) {
				h.logger.Warn("WebSocket read failed, will retry",
					zap.String("job_id", idStr), zap.Error(err))
				return false
			}
			_ = conn.WriteJSON(gin.H{"error": "Job not found"})
			return true
		}

		if job.Status != lastStatus {
			_ = conn.SetWriteDeadline(time.Now().Add(wsPongTimeout))
			if err := conn.WriteJSON(job); err != nil {
				h.logger.Debug("WebSocket write failed", zap.Error(err))
				return true
			}
			lastStatus = job.Status
		}

		if job.Status.IsTerminal() {
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "job completed"))
			h.logger.Debug("Job reached terminal state, closing WebSocket",
				zap.String("job_id", idStr),
				zap.String("status", string(job.Status)),
			)
			return true
		}
		return false
	}

	// Send the current state immediately rather than making the client wait for
	// the first event — the job may already have finished.
	if sendIfChanged() {
		return
	}

	for {
		select {
		case <-clientDone:
			h.logger.Debug("WebSocket client disconnected", zap.String("job_id", idStr))
			return

		case <-maxTimer.C:
			h.logger.Debug("WebSocket max duration exceeded, closing", zap.String("job_id", idStr))
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "max connection duration exceeded"))
			return

		case <-pingTicker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsPongTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				h.logger.Debug("WebSocket ping failed", zap.Error(err))
				return
			}

		case <-statusChanged:
			if sendIfChanged() {
				return
			}

		case <-pollTicker.C:
			if sendIfChanged() {
				return
			}
		}
	}
}
