package middleware

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
)

// CORS returns a middleware that handles Cross-Origin Resource Sharing.
//
// allowedOrigins is an explicit allowlist. The wildcard "*" is honoured but
// should only be used in development: this API returns a job's full source code
// on GET, so a permissive origin policy combined with the absence of
// authentication means any page on the internet can read submissions if it can
// guess an ID.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := slices.Contains(allowedOrigins, "*")

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		switch {
		case allowAll:
			c.Header("Access-Control-Allow-Origin", "*")
		case origin != "" && slices.Contains(allowedOrigins, origin):
			// Echo the specific origin rather than the list, and tell caches that
			// the response varies by it — otherwise a shared cache can serve one
			// origin's CORS headers to another.
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Request-ID")
		c.Header("Access-Control-Expose-Headers", "X-Request-ID, X-RateLimit-Limit, X-RateLimit-Remaining, Retry-After")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// OriginAllowed reports whether origin passes the same allowlist CORS uses.
// The WebSocket handler needs this because gorilla/websocket does its own origin
// check outside the CORS middleware, and its default of "allow everything" is
// what makes cross-site WebSocket hijacking possible.
func OriginAllowed(allowedOrigins []string, origin string) bool {
	if slices.Contains(allowedOrigins, "*") {
		return true
	}
	// A missing Origin header means a non-browser client (curl, a test, a native
	// app). Browsers always send it on a WebSocket handshake, so absence cannot
	// be used to mount a cross-site attack.
	if origin == "" {
		return true
	}
	return slices.Contains(allowedOrigins, origin)
}
