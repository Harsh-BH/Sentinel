package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// slidingWindowEntry tracks request counts per time window.
type slidingWindowEntry struct {
	count     int
	timestamp time.Time
}

// RateLimiter returns a middleware that enforces per-IP rate limiting using a sliding window.
// maxRequests is the maximum number of requests allowed per minute per IP.
func RateLimiter(maxRequests int) gin.HandlerFunc {
	var mu sync.Mutex
	clients := make(map[string]*slidingWindowEntry)

	// Cleanup stale entries every 5 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			now := time.Now()
			for ip, entry := range clients {
				if now.Sub(entry.timestamp) > 2*time.Minute {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		mu.Lock()

		entry, exists := clients[ip]
		now := time.Now()

		if !exists || now.Sub(entry.timestamp) > time.Minute {
			// New window
			clients[ip] = &slidingWindowEntry{count: 1, timestamp: now}
			mu.Unlock()
			c.Next()
			return
		}

		if entry.count >= maxRequests {
			mu.Unlock()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Rate limit exceeded. Maximum " + string(rune(maxRequests)) + " requests per minute.",
			})
			return
		}

		entry.count++
		mu.Unlock()
		c.Next()
	}
}
