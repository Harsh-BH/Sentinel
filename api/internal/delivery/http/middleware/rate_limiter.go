package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimiter returns a middleware that enforces per-IP rate limiting
// using a Redis sliding window log algorithm.
// maxRequests is the maximum number of requests allowed per minute per IP.
func RateLimiter(rdb *redis.Client, maxRequests int) gin.HandlerFunc {
	window := time.Minute

	return func(c *gin.Context) {
		ip := c.ClientIP()
		key := fmt.Sprintf("sentinel:ratelimit:%s", ip)
		now := time.Now()
		nowUnixNano := float64(now.UnixNano())
		windowStart := float64(now.Add(-window).UnixNano())

		ctx := context.Background()

		// Use a pipeline for atomicity
		pipe := rdb.Pipeline()

		// Remove entries outside the sliding window
		pipe.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%f", windowStart))

		// Count current entries in the window
		countCmd := pipe.ZCard(ctx, key)

		// Add the current request
		pipe.ZAdd(ctx, key, redis.Z{Score: nowUnixNano, Member: nowUnixNano})

		// Set TTL on the key so it auto-expires
		pipe.Expire(ctx, key, window+time.Second)

		_, err := pipe.Exec(ctx)
		if err != nil {
			// If Redis is down, allow the request (fail-open)
			c.Next()
			return
		}

		count := countCmd.Val()
		if count >= int64(maxRequests) {
			// Remove the entry we just added since we're rejecting
			rdb.ZRemRangeByScore(ctx, key, fmt.Sprintf("%f", nowUnixNano), fmt.Sprintf("%f", nowUnixNano))

			remaining := 0
			c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", maxRequests))
			c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": fmt.Sprintf("Rate limit exceeded. Maximum %d requests per minute.", maxRequests),
			})
			return
		}

		remaining := int64(maxRequests) - count - 1
		if remaining < 0 {
			remaining = 0
		}
		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", maxRequests))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		c.Next()
	}
}

// RateLimiterInMemory returns a simple in-memory rate limiter fallback
// for environments without Redis (e.g. testing).
func RateLimiterInMemory(maxRequests int) gin.HandlerFunc {
	return RateLimiter(nil, maxRequests)
}
