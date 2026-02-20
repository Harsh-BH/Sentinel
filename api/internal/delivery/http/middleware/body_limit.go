package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodySizeLimit returns a middleware that limits the maximum request body size.
// If the body exceeds maxBytes, a 413 Payload Too Large response is returned.
func BodySizeLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "Request body too large",
			})
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}
