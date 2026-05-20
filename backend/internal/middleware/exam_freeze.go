package middleware

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type examActiveChecker interface {
	IsAnyExamActive(ctx context.Context) (bool, error)
}

var cache struct {
	mu      sync.Mutex
	expires time.Time
	value   bool
}

const ttl = 5 * time.Second

// ExamFreezeMiddleware returns a gin.HandlerFunc that checks if any exam is active.
// If an exam is active, it aborts the request with 423 Locked.
func ExamFreezeMiddleware(checker examActiveChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		cache.mu.Lock()
		if time.Now().Before(cache.expires) {
			active := cache.value
			cache.mu.Unlock()
			if active {
				c.AbortWithStatusJSON(http.StatusLocked, gin.H{"error": "practice_unavailable_during_exam"})
				return
			}
			c.Next()
			return
		}
		cache.mu.Unlock()

		active, err := checker.IsAnyExamActive(c.Request.Context())
		if err != nil {
			log.Printf("ExamFreezeMiddleware: failed to check active exams: %v", err)
			c.AbortWithStatusJSON(http.StatusLocked, gin.H{"error": "practice_unavailable_during_exam"})
			return
		}

		cache.mu.Lock()
		cache.value = active
		cache.expires = time.Now().Add(ttl)
		cache.mu.Unlock()

		if active {
			c.AbortWithStatusJSON(http.StatusLocked, gin.H{"error": "practice_unavailable_during_exam"})
			return
		}
		c.Next()
	}
}

// InvalidateExamFreezeCache invalidates the exam freeze cache.
func InvalidateExamFreezeCache() {
	cache.mu.Lock()
	cache.expires = time.Time{}
	cache.mu.Unlock()
}
