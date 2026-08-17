package services

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// emailJobRetention is how long a FINISHED job stays readable, so a reloaded
// admin page can still show the outcome of a send that ended while it was away.
const emailJobRetention = 30 * time.Minute

// emailJobCap bounds the registry. Past this, the oldest finished job is
// evicted first; this is a runaway backstop, not an operational limit.
const emailJobCap = 100

// maxTrackedFailures caps the per-job failure list so a pathological run cannot
// grow the entry without bound. Beyond it the counter still rises.
const maxTrackedFailures = 50

// EmailJob is the progress snapshot of one background credential-email run.
// Sent+Failed approaches Total; Done marks the worker as finished.
type EmailJob struct {
	ID       string   `json:"job_id"`
	OwnerID  int      `json:"-"`
	Total    int      `json:"total"`
	Sent     int      `json:"sent"`
	Failed   int      `json:"failed"`
	Done     bool     `json:"done"`
	Failures []string `json:"failures"`

	finishedAt time.Time
}

// EmailJobRegistry is an in-memory, mutex-guarded store of email-job progress.
// It exists ONLY to back the admin progress bubble: nothing about the import's
// correctness depends on it, and every password it reports on is also present
// in the HTTP response the admin already holds. Single-process by design — the
// deployment runs one backend, and a lost job degrades to a missing bubble.
type EmailJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*EmailJob
}

func NewEmailJobRegistry() *EmailJobRegistry {
	return &EmailJobRegistry{jobs: make(map[string]*EmailJob)}
}

// Register mints a job with a 128-bit crypto/rand id — unguessable and never
// sequential, since the id is the only handle on another admin's progress.
func (r *EmailJobRegistry) Register(ownerID, total int) *EmailJob {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing is fatal for id secrecy; degrade to no tracking
		// rather than to a predictable id.
		return &EmailJob{OwnerID: ownerID, Total: total}
	}
	job := &EmailJob{ID: hex.EncodeToString(buf), OwnerID: ownerID, Total: total}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	r.jobs[job.ID] = job
	return job
}

// sweepLocked drops expired finished jobs, then enforces the cap by dropping
// the oldest finished job. Callers must hold r.mu.
func (r *EmailJobRegistry) sweepLocked() {
	cutoff := time.Now().Add(-emailJobRetention)
	for id, j := range r.jobs {
		if j.Done && j.finishedAt.Before(cutoff) {
			delete(r.jobs, id)
		}
	}
	for len(r.jobs) >= emailJobCap {
		oldestID := ""
		var oldest time.Time
		for id, j := range r.jobs {
			if !j.Done {
				continue
			}
			if oldestID == "" || j.finishedAt.Before(oldest) {
				oldestID, oldest = id, j.finishedAt
			}
		}
		if oldestID == "" {
			return // every job is still running; leave them alone
		}
		delete(r.jobs, oldestID)
	}
}

func (r *EmailJobRegistry) MarkSent(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Sent++
	}
}

func (r *EmailJobRegistry) MarkFailed(id, studentIDExt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Failed++
		if len(j.Failures) < maxTrackedFailures {
			j.Failures = append(j.Failures, studentIDExt)
		}
	}
}

func (r *EmailJobRegistry) Finish(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		j.Done = true
		j.finishedAt = time.Now()
	}
}

// Get returns a COPY of the job. An id belonging to another admin is
// indistinguishable from an unknown id, so the endpoint cannot be used to probe
// for other admins' activity.
func (r *EmailJobRegistry) Get(id string, ownerID int) (EmailJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || j.OwnerID != ownerID {
		return EmailJob{}, false
	}
	copied := *j
	copied.Failures = append([]string(nil), j.Failures...)
	return copied, true
}
