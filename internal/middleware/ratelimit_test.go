package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiterExcludedPaths(t *testing.T) {
	rl := NewRateLimiter(1.0, 2) // 1 req/sec, burst 2

	excludedPaths := []string{
		"/health",
		"/ready",
		"/info",
		"/metrics",
		"/internal/cache/stats",
		"/internal/system/status",
	}

	for _, path := range excludedPaths {
		t.Run(path, func(t *testing.T) {
			// Create a handler that will be called if rate limiting doesn't block
			called := false
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			middleware := rl.Handler()
			handler := middleware(nextHandler)

			// Make requests beyond burst limit
			for i := 0; i < 10; i++ {
				req := httptest.NewRequest("GET", path, nil)
				req.RemoteAddr = "127.0.0.1:12345"
				w := httptest.NewRecorder()

				handler.ServeHTTP(w, req)
			}

			if !called {
				t.Errorf("Expected excluded path %s to bypass rate limiting, but was blocked", path)
			}
		})
	}
}

func TestRateLimiterBlocksExcessiveRequests(t *testing.T) {
	rl := NewRateLimiter(1.0, 2) // 1 req/sec, burst 2

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// First burst size (2) requests should succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request %d: expected status 200, got %d", i+1, w.Code)
		}
	}

	// Next request should be rate limited (429)
	req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Expected status 429 (Too Many Requests), got %d", w.Code)
	}
}

func TestRateLimiterPerIPTracking(t *testing.T) {
	rl := NewRateLimiter(1.0, 1) // 1 req/sec, burst 1

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// IP1 makes a request (should succeed, within burst)
	req1 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req1.RemoteAddr = "192.168.1.1:12345"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("IP1 first request: expected 200, got %d", w1.Code)
	}

	// IP2 makes a request (should also succeed, different limiter)
	req2 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req2.RemoteAddr = "192.168.1.2:12345"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("IP2 first request: expected 200, got %d", w2.Code)
	}

	// IP1 makes another request (should fail, exhausted burst)
	req3 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req3.RemoteAddr = "192.168.1.1:12345"
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("IP1 second request: expected 429, got %d", w3.Code)
	}
}

func TestRateLimiterXForwardedForHeader(t *testing.T) {
	rl := NewRateLimiter(1.0, 1) // 1 req/sec, burst 1

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// First request with X-Forwarded-For header
	req1 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req1.RemoteAddr = "10.0.0.1:12345" // This is the proxy
	req1.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.1")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("First request: expected 200, got %d", w1.Code)
	}

	// Second request with same X-Forwarded-For (should be rate limited)
	req2 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req2.RemoteAddr = "10.0.0.1:12345"
	req2.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.1")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Second request (same X-Forwarded-For): expected 429, got %d", w2.Code)
	}

	// Third request with different X-Forwarded-For (should succeed, different IP)
	req3 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req3.RemoteAddr = "10.0.0.1:12345"
	req3.Header.Set("X-Forwarded-For", "203.0.113.99")
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("Third request (different X-Forwarded-For): expected 200, got %d", w3.Code)
	}
}

func TestRateLimiterXRealIPHeader(t *testing.T) {
	rl := NewRateLimiter(1.0, 1) // 1 req/sec, burst 1

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// First request with X-Real-IP header
	req1 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req1.RemoteAddr = "10.0.0.1:12345"
	req1.Header.Set("X-Real-IP", "203.0.113.50")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("First request: expected 200, got %d", w1.Code)
	}

	// Second request with same X-Real-IP (should be rate limited)
	req2 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req2.RemoteAddr = "10.0.0.1:12345"
	req2.Header.Set("X-Real-IP", "203.0.113.50")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Second request: expected 429, got %d", w2.Code)
	}
}

func TestRateLimiterRemoteAddrFallback(t *testing.T) {
	rl := NewRateLimiter(1.0, 1) // 1 req/sec, burst 1

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// First request with RemoteAddr only (no proxy headers)
	req1 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req1.RemoteAddr = "192.168.1.50:12345"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("First request: expected 200, got %d", w1.Code)
	}

	// Second request with same RemoteAddr (should be rate limited)
	req2 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req2.RemoteAddr = "192.168.1.50:12345"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Second request: expected 429, got %d", w2.Code)
	}
}

func TestRateLimiterRetryAfterHeader(t *testing.T) {
	rl := NewRateLimiter(1.0, 1) // 1 req/sec, burst 1

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// First request succeeds
	req1 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req1.RemoteAddr = "192.168.1.1:12345"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	// Second request is rate limited
	req2 := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req2.RemoteAddr = "192.168.1.1:12345"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("Expected 429, got %d", w2.Code)
	}

	// Check for Retry-After header
	retryAfter := w2.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("Expected Retry-After header in 429 response, but it was empty")
	}
}

func TestRateLimiterCustomExcludePaths(t *testing.T) {
	rl := NewRateLimiter(1.0, 1)
	rl.WithExcludePaths([]string{"/custom/excluded", "/admin"})

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	// Multiple requests to excluded path should all succeed
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/custom/excluded", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request %d to excluded path: expected 200, got %d", i+1, w.Code)
		}
	}
}

func TestRateLimiterBurstBehavior(t *testing.T) {
	rl := NewRateLimiter(0.5, 5) // 0.5 req/sec, burst 5

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	ip := "192.168.1.1:12345"

	// All burst requests should succeed
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Burst request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// Next request should fail
	req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
	req.RemoteAddr = ip
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Request after burst exhausted: expected 429, got %d", w.Code)
	}
}

func TestGetClientIPPriority(t *testing.T) {
	rl := NewRateLimiter(10.0, 1000) // High limit to avoid rate limiting

	tests := []struct {
		name          string
		xForwardedFor string
		xRealIP       string
		remoteAddr    string
		expectedIP    string
	}{
		{
			name:          "X-Forwarded-For takes priority",
			xForwardedFor: "203.0.113.1, 198.51.100.1",
			xRealIP:       "203.0.113.2",
			remoteAddr:    "10.0.0.1:12345",
			expectedIP:    "203.0.113.1",
		},
		{
			name:          "X-Real-IP when X-Forwarded-For missing",
			xForwardedFor: "",
			xRealIP:       "203.0.113.2",
			remoteAddr:    "10.0.0.1:12345",
			expectedIP:    "203.0.113.2",
		},
		{
			name:          "RemoteAddr fallback",
			xForwardedFor: "",
			xRealIP:       "",
			remoteAddr:    "192.168.1.1:12345",
			expectedIP:    "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}
			req.RemoteAddr = tt.remoteAddr

			ip := rl.getClientIP(req)
			if ip != tt.expectedIP {
				t.Errorf("Expected IP %q, got %q", tt.expectedIP, ip)
			}
		})
	}
}

func BenchmarkRateLimiterAllow(b *testing.B) {
	rl := NewRateLimiter(1000.0, 1000) // Very high limit

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
	}
}

func BenchmarkRateLimiterMultipleIPs(b *testing.B) {
	rl := NewRateLimiter(1000.0, 1000)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := rl.Handler()
	handler := middleware(nextHandler)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Simulate requests from different IPs
		ip := "192.168.1." + string(rune(48+(i%256)))
		req := httptest.NewRequest("GET", "/v1/merchants/test", nil)
		req.RemoteAddr = ip + ":12345"
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)
	}
}
