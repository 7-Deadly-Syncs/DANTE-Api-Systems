package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

// RateLimiter implements per-IP rate limiting using token bucket algorithm.
// It tracks limiters per client IP and allows exclusion of specific paths (internal/system endpoints).
type RateLimiter struct {
	limiters     sync.Map // map[string]*rate.Limiter (IP -> Limiter)
	limit        rate.Limit
	burst        int
	excludePaths []string
}

// NewRateLimiter creates a rate limiter with the given requests per second and burst size.
// Default excludes internal endpoints like /health, /ready, /metrics, etc.
func NewRateLimiter(requestsPerSecond float64, burst int) *RateLimiter {
	return &RateLimiter{
		limit: rate.Limit(requestsPerSecond),
		burst: burst,
		excludePaths: []string{
			"/health",
			"/ready",
			"/info",
			"/metrics",
			"/openapi",
			"/docs",
			"/internal/",
		},
	}
}

// WithExcludePaths sets custom paths to exclude from rate limiting.
// Useful for bypassing rate limits on specific endpoints.
func (rl *RateLimiter) WithExcludePaths(paths []string) *RateLimiter {
	rl.excludePaths = paths
	return rl
}

// isExcluded checks if the request path should be excluded from rate limiting.
func (rl *RateLimiter) isExcluded(path string) bool {
	for _, exclude := range rl.excludePaths {
		if strings.HasPrefix(path, exclude) {
			return true
		}
	}
	return false
}

// getClientIP extracts the client IP from the request, supporting proxy headers.
// Priority order:
// 1. X-Forwarded-For (most common proxy header, can contain multiple IPs)
// 2. X-Real-IP (another proxy header)
// 3. r.RemoteAddr (direct connection)
func (rl *RateLimiter) getClientIP(r *http.Request) string {
	// Check X-Forwarded-For first (for proxy scenarios, takes first IP)
	if xForwardedFor := r.Header.Get("X-Forwarded-For"); xForwardedFor != "" {
		// X-Forwarded-For can be a comma-separated list, take the first IP
		ips := strings.Split(xForwardedFor, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP (another common proxy header)
	if xRealIP := r.Header.Get("X-Real-IP"); xRealIP != "" {
		return strings.TrimSpace(xRealIP)
	}

	// Fall back to RemoteAddr from direct connection
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// If no port found, return RemoteAddr as-is
		return r.RemoteAddr
	}
	return ip
}

// getLimiter retrieves or creates a rate limiter for the given IP address.
// Uses sync.Map for thread-safe, lock-free storage of per-IP limiters.
func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	limiter, _ := rl.limiters.LoadOrStore(ip, rate.NewLimiter(rl.limit, rl.burst))
	return limiter.(*rate.Limiter)
}

// Handler returns a Chi-compatible middleware function for rate limiting.
// Excluded paths bypass the limiter entirely.
// Requests exceeding the rate limit receive HTTP 429 (Too Many Requests).
func (rl *RateLimiter) Handler() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting for excluded paths (internal/system endpoints)
			if rl.isExcluded(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Extract client IP and get its rate limiter
			clientIP := rl.getClientIP(r)
			limiter := rl.getLimiter(clientIP)

			// Check if request is allowed under current rate limit
			if !limiter.Allow() {
				// Rate limit exceeded: return HTTP 429
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			// Request allowed: pass to next handler
			next.ServeHTTP(w, r)
		})
	}
}
