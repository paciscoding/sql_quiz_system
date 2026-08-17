package services

import (
	"sync"
	"testing"
	"time"
)

func TestEmailJobRegistry_CountsAndOwnership(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(42, 3)

	if job.ID == "" {
		t.Fatal("Register returned an empty job id")
	}
	if job.Total != 3 || job.Sent != 0 || job.Done {
		t.Fatalf("fresh job = %+v, want total 3, sent 0, not done", *job)
	}

	reg.MarkSent(job.ID)
	reg.MarkFailed(job.ID, "s0002")
	reg.MarkSent(job.ID)
	reg.Finish(job.ID)

	got, ok := reg.Get(job.ID, 42)
	if !ok {
		t.Fatal("Get by the owning admin returned not-found")
	}
	if got.Sent != 2 || got.Failed != 1 || !got.Done {
		t.Errorf("job = sent:%d failed:%d done:%v, want 2/1/true", got.Sent, got.Failed, got.Done)
	}
	if len(got.Failures) != 1 || got.Failures[0] != "s0002" {
		t.Errorf("Failures = %v, want [s0002]", got.Failures)
	}

	if _, ok := reg.Get(job.ID, 43); ok {
		t.Error("a different admin could read the job: ownership is not enforced")
	}
	if _, ok := reg.Get("no-such-job", 42); ok {
		t.Error("unknown job id returned found")
	}
}

// A returned job must be a snapshot: mutating it must not corrupt the registry.
// This also covers the Failures slice specifically: Get must hand back a
// copy of the slice, not one that aliases the registry's backing array, so
// mutating or appending to the returned slice must not affect what the next
// Get sees.
func TestEmailJobRegistry_GetReturnsCopy(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(1, 1)
	reg.MarkSent(job.ID)
	reg.MarkFailed(job.ID, "s0001")

	got, _ := reg.Get(job.ID, 1)
	got.Sent = 999
	got.Failures[0] = "mutated"
	got.Failures = append(got.Failures, "extra")

	again, _ := reg.Get(job.ID, 1)
	if again.Sent != 1 {
		t.Errorf("Sent = %d, want 1: Get must return a copy", again.Sent)
	}
	if len(again.Failures) != 1 || again.Failures[0] != "s0001" {
		t.Errorf("Failures = %v, want [s0001]: Get must return a copy of the Failures slice", again.Failures)
	}
}

// Register's returned *EmailJob must likewise be a snapshot: it must not
// alias the entry the registry mutates through MarkSent/MarkFailed/Finish,
// including the Failures slice.
func TestEmailJobRegistry_RegisterReturnsCopy(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(1, 1)

	reg.MarkFailed(job.ID, "s0001")

	// Mutating the pointer Register returned must not affect the registry's
	// entry, and must not itself have been mutated by MarkFailed.
	job.Sent = 999
	if job.Failed != 0 {
		t.Fatalf("job.Failed = %d, want 0: Register's returned pointer must not alias the registry's entry", job.Failed)
	}
	if len(job.Failures) != 0 {
		t.Fatalf("job.Failures = %v, want empty: Register's returned pointer must not alias the registry's Failures slice", job.Failures)
	}
	job.Failures = append(job.Failures, "mutated")

	got, _ := reg.Get(job.ID, 1)
	if got.Sent != 0 {
		t.Errorf("Sent = %d, want 0: mutating Register's returned pointer corrupted the registry", got.Sent)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1", got.Failed)
	}
	if len(got.Failures) != 1 || got.Failures[0] != "s0001" {
		t.Errorf("Failures = %v, want [s0001]: mutating Register's returned Failures slice corrupted the registry", got.Failures)
	}
}

// Guards the concurrent read (HTTP poll) against the concurrent write (worker).
// Run with -race.
func TestEmailJobRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewEmailJobRegistry()
	job := reg.Register(9, 100)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			reg.MarkSent(job.ID)
		}
		reg.Finish(job.ID)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			reg.Get(job.ID, 9)
		}
	}()
	wg.Wait()

	got, _ := reg.Get(job.ID, 9)
	if got.Sent != 100 {
		t.Errorf("Sent = %d, want 100", got.Sent)
	}
}

func TestEmailJobRegistry_EvictsExpiredOnRegister(t *testing.T) {
	reg := NewEmailJobRegistry()
	old := reg.Register(1, 1)
	reg.Finish(old.ID)

	// Age the finished job past the retention window without sleeping.
	reg.mu.Lock()
	reg.jobs[old.ID].finishedAt = time.Now().Add(-2 * emailJobRetention)
	reg.mu.Unlock()

	reg.Register(1, 1) // triggers the sweep

	if _, ok := reg.Get(old.ID, 1); ok {
		t.Error("an expired finished job survived the sweep")
	}
}
