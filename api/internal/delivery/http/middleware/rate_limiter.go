package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// slidingWindowScript implements the whole sliding-window check as one atomic
// Redis operation.
//
// The previous implementation used a Redis *pipeline* and a comment claiming it
// gave atomicity. A pipeline only batches commands into one round trip — other
// clients interleave freely — so two concurrent requests could both read the
// same pre-increment ZCARD and both be admitted. Under exactly the burst the
// limiter exists to stop, it let roughly twice the limit through.
//
// A script runs to completion on the server with nothing interleaved, so the
// trim / count / admit / record sequence is indivisible.
//
// KEYS[1] = bucket key
// ARGV[1] = window start (unix nanoseconds, exclusive lower bound)
// ARGV[2] = now (unix nanoseconds, used as both score and member)
// ARGV[3] = max requests in the window
// ARGV[4] = key TTL in seconds
// Returns {admitted (1|0), count_after}
var slidingWindowScript = redis.NewScript(`
	redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
	local count = redis.call('ZCARD', KEYS[1])
	if count >= tonumber(ARGV[3]) then
		return {0, count}
	end
	redis.call('ZADD', KEYS[1], ARGV[2], ARGV[2])
	redis.call('EXPIRE', KEYS[1], ARGV[4])
	return {1, count + 1}
`)

// RateLimiter returns a middleware that enforces per-IP rate limiting using a
// Redis sliding window log.
//
// maxRequests is the maximum number of requests allowed per minute per IP.
//
// Note on the client IP: this trusts gin's c.ClientIP(), which honours
// X-Forwarded-For. That is only safe because the router calls SetTrustedProxies
// with the actual proxy CIDRs — without it, any caller can spoof the header and
// mint themselves a fresh bucket per request.
func RateLimiter(rdb *redis.Client, maxRequests int) gin.HandlerFunc {
	const window = time.Minute
	ttl := int(window.Seconds()) + 1

	return func(c *gin.Context) {
		key := fmt.Sprintf("sentinel:ratelimit:%s", c.ClientIP())
		now := time.Now()

		res, err := slidingWindowScript.Run(c.Request.Context(), rdb,
			[]string{key},
			now.Add(-window).UnixNano(),
			now.UnixNano(),
			maxRequests,
			ttl,
		).Int64Slice()

		if err != nil {
			// Fail open: Redis being down should not take the API down with it.
			//
			// This is a real trade-off, not an oversight. Failing closed would turn a
			// cache outage into a total outage; failing open means the limiter is
			// unavailable exactly when load is highest. We accept that because the
			// limiter is not the only defence — body size limits, per-job cgroup
			// caps and queue backpressure all still apply — but it is the reason
			// SentinelRateLimiterDown deserves an alert rather than a shrug.
			c.Header("X-RateLimit-Bypassed", "true")
			c.Next()
			return
		}

		admitted, count := res[0] == 1, res[1]

		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", maxRequests))
		remaining := int64(maxRequests) - count
		if remaining < 0 {
			remaining = 0
		}
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		if !admitted {
			c.Header("Retry-After", fmt.Sprintf("%d", int(window.Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": fmt.Sprintf("Rate limit exceeded. Maximum %d requests per minute.", maxRequests),
			})
			return
		}

		c.Next()
	}
}
