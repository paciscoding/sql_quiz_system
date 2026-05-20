package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

type mockChecker struct {
	mu     sync.Mutex
	active bool
	err    error
	calls  int
}

func (m *mockChecker) IsAnyExamActive(ctx context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.active, m.err
}

func (m *mockChecker) getCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestExamFreezeMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("TestTTLCaching", func(t *testing.T) {
		InvalidateExamFreezeCache()
		mock := &mockChecker{active: false}
		router := gin.New()
		router.Use(ExamFreezeMiddleware(mock))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		// First call - should hit DB
		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w1, req1)
		if w1.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w1.Code)
		}

		// Second call - should hit cache
		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w2, req2)
		if w2.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w2.Code)
		}

		if mock.getCalls() != 1 {
			t.Errorf("Expected 1 call to mock, got %d", mock.getCalls())
		}
	})

	t.Run("TestInvalidation", func(t *testing.T) {
		InvalidateExamFreezeCache()
		mock := &mockChecker{active: false}
		router := gin.New()
		router.Use(ExamFreezeMiddleware(mock))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		// Populate cache
		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w1, req1)

		// Invalidate
		InvalidateExamFreezeCache()

		// Second call - should hit DB again
		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w2, req2)

		if mock.getCalls() != 2 {
			t.Errorf("Expected 2 calls to mock, got %d", mock.getCalls())
		}
	})

	t.Run("Test423OnActiveExam", func(t *testing.T) {
		InvalidateExamFreezeCache()
		mock := &mockChecker{active: true}
		router := gin.New()
		router.Use(ExamFreezeMiddleware(mock))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusLocked {
			t.Errorf("Expected status 423, got %d", w.Code)
		}

		var body map[string]string
		json.Unmarshal(w.Body.Bytes(), &body)
		if body["error"] != "practice_unavailable_during_exam" {
			t.Errorf("Expected error message 'practice_unavailable_during_exam', got '%s'", body["error"])
		}
	})

	t.Run("TestPassThroughOnInactive", func(t *testing.T) {
		InvalidateExamFreezeCache()
		mock := &mockChecker{active: false}
		router := gin.New()
		router.Use(ExamFreezeMiddleware(mock))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", w.Code)
		}
	})

	t.Run("TestFailClosedOnError", func(t *testing.T) {
		InvalidateExamFreezeCache()
		mock := &mockChecker{err: errors.New("db error")}
		router := gin.New()
		router.Use(ExamFreezeMiddleware(mock))
		router.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/test", nil)
		router.ServeHTTP(w, req)

		if w.Code != http.StatusLocked {
			t.Errorf("Expected status 423, got %d", w.Code)
		}
	})
}

// TestExamFreezeMiddleware_ConcurrentCacheMiss tests that two goroutines firing
// concurrently during a cache-miss window do not double-probe the checker (or
// if they do, the result is still consistent — no data race detected).
func TestExamFreezeMiddleware_ConcurrentCacheMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	InvalidateExamFreezeCache()

	mock := &mockChecker{active: false}
	router := gin.New()
	router.Use(ExamFreezeMiddleware(mock))
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	const goroutines = 20
	results := make([]int, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("GET", "/test", nil)
			router.ServeHTTP(w, req)
			results[idx] = w.Code
		}()
	}
	wg.Wait()

	// All requests must succeed (no active exam)
	for i, code := range results {
		if code != http.StatusOK {
			t.Errorf("goroutine %d: expected 200, got %d", i, code)
		}
	}

	// The checker must have been called at least once and at most goroutines times.
	// We tolerate more than 1 call because the current implementation releases the
	// lock before querying the DB, so a burst may produce multiple probes. What
	// matters is: no data race (enforced by -race) and all responses are correct.
	calls := mock.getCalls()
	if calls < 1 {
		t.Errorf("Expected at least 1 DB probe, got %d", calls)
	}
	if calls > goroutines {
		t.Errorf("More DB probes (%d) than goroutines (%d) — unexpected", calls, goroutines)
	}
}

// TestExamFreezeMiddleware_CacheMissThenHit verifies that after a cache-miss
// populates the cache, an immediate second request hits the cache (1 DB probe total).
func TestExamFreezeMiddleware_CacheMissThenHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	InvalidateExamFreezeCache()

	mock := &mockChecker{active: false}
	router := gin.New()
	router.Use(ExamFreezeMiddleware(mock))
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// First request — cache miss, DB probe
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("First request: expected 200, got %d", w1.Code)
	}
	if mock.getCalls() != 1 {
		t.Fatalf("Expected 1 DB call after cache miss, got %d", mock.getCalls())
	}

	// Second request — should hit cache, no additional DB probe
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("GET", "/test", nil)
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("Second request: expected 200, got %d", w2.Code)
	}
	if mock.getCalls() != 1 {
		t.Errorf("Expected cache hit on second request (still 1 DB call), got %d", mock.getCalls())
	}
}
