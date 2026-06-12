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

// practiceFreezeChecker reports whether practice should be frozen right now,
// honoring the per-exam block_practice_during_exam toggle.
type practiceFreezeChecker interface {
	IsPracticeFrozen(ctx context.Context) (bool, error)
}

var cache struct {
	mu      sync.Mutex
	expires time.Time
	value   bool
}

var practiceCache struct {
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

// PracticeFreezeMiddleware blocks practice routes with 423 only when an active
// exam has its block_practice_during_exam toggle on. Unlike ExamFreezeMiddleware
// (which freezes on ANY active exam), practice is live during exams by default;
// this is the toggle-aware gate wired onto the practice student group. Results
// are cached for `ttl` and invalidated by InvalidateExamFreezeCache.
func PracticeFreezeMiddleware(checker practiceFreezeChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		practiceCache.mu.Lock()
		if time.Now().Before(practiceCache.expires) {
			frozen := practiceCache.value
			practiceCache.mu.Unlock()
			if frozen {
				c.AbortWithStatusJSON(http.StatusLocked, gin.H{"error": "practice_unavailable_during_exam"})
				return
			}
			c.Next()
			return
		}
		practiceCache.mu.Unlock()

		frozen, err := checker.IsPracticeFrozen(c.Request.Context())
		if err != nil {
			log.Printf("PracticeFreezeMiddleware: failed to check practice freeze: %v", err)
			// Fail open: practice stays available if the check errors, since the
			// default posture is live-during-exams.
			c.Next()
			return
		}

		practiceCache.mu.Lock()
		practiceCache.value = frozen
		practiceCache.expires = time.Now().Add(ttl)
		practiceCache.mu.Unlock()

		if frozen {
			c.AbortWithStatusJSON(http.StatusLocked, gin.H{"error": "practice_unavailable_during_exam"})
			return
		}
		c.Next()
	}
}

// InvalidateExamFreezeCache invalidates both the exam-active and practice-freeze
// caches. Called whenever an exam's active state or practice-freeze toggle
// changes so the gates re-evaluate on the next request.
func InvalidateExamFreezeCache() {
	cache.mu.Lock()
	cache.expires = time.Time{}
	cache.mu.Unlock()
	practiceCache.mu.Lock()
	practiceCache.expires = time.Time{}
	practiceCache.mu.Unlock()
}
