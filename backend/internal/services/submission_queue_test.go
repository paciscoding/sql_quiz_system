package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSubmissionQueue_FIFOOrder(t *testing.T) {
	q := NewSubmissionQueue(10, 5*time.Second)

	var mu sync.Mutex
	var order []int

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		errs[0] = q.Run(context.Background(), func(context.Context) error {
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			return nil
		})
	}()

	// Give the first job a moment to be enqueued first.
	time.Sleep(2 * time.Millisecond)

	go func() {
		defer wg.Done()
		errs[1] = q.Run(context.Background(), func(context.Context) error {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			return nil
		})
	}()

	wg.Wait()
	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("unexpected errors: %v, %v", errs[0], errs[1])
	}
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected FIFO order [1 2], got %v", order)
	}
}

func TestSubmissionQueue_ErrQueueFull(t *testing.T) {
	q := NewSubmissionQueue(1, 5*time.Second)

	block := make(chan struct{})

	// First job: picked up immediately by the worker (blocks the worker).
	go func() {
		_ = q.Run(context.Background(), func(context.Context) error {
			<-block
			return nil
		})
	}()

	// Give first job time to be picked up by the worker.
	time.Sleep(20 * time.Millisecond)

	// Fill the one available channel slot.
	go func() {
		_ = q.Run(context.Background(), func(context.Context) error {
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)

	// Now the channel is full; this call should get ErrQueueFull.
	err := q.Run(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}

	// Unblock.
	close(block)
	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)
}

func TestSubmissionQueue_JobTimeout(t *testing.T) {
	q := NewSubmissionQueue(10, 50*time.Millisecond)

	start := time.Now()
	err := q.Run(context.Background(), func(context.Context) error {
		time.Sleep(200 * time.Millisecond)
		return nil
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected non-nil error due to job timeout")
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("Run should have returned within 150ms, took %v", elapsed)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)
}

func TestSubmissionQueue_PanicRecovery(t *testing.T) {
	q := NewSubmissionQueue(10, 5*time.Second)

	// First job panics.
	err := q.Run(context.Background(), func(context.Context) error {
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected non-nil error after panic")
	}

	// Worker should still be alive. Second job runs normally.
	err = q.Run(context.Background(), func(context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error for second job, got %v", err)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)
}

// TestSubmissionQueue_ContextThreading asserts that a job actually observes
// cancellation of its own context (not merely that the caller-side Run call
// returns). Before ARCH-011, fn had no context parameter at all and closed
// over the caller's request context, so a timed-out job's own work never saw
// runCtx being cancelled.
func TestSubmissionQueue_ContextThreading(t *testing.T) {
	q := NewSubmissionQueue(10, 50*time.Millisecond)

	sawCancel := make(chan error, 1)
	start := time.Now()
	err := q.Run(context.Background(), func(jobCtx context.Context) error {
		<-jobCtx.Done()
		sawCancel <- jobCtx.Err()
		return jobCtx.Err()
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected non-nil error due to job timeout")
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("Run took too long to return: %v", elapsed)
	}

	select {
	case cancelErr := <-sawCancel:
		if cancelErr == nil {
			t.Fatal("job function's context reported a nil error; cancellation was not observed inside fn")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("job function never observed its context being cancelled")
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)
}

// TestSubmissionQueue_HeadOfLineBlocking_UnblocksAtJobTimeout asserts the
// ARCH-011 fix: a slow job that respects its context frees the single worker
// at the job timeout, so a fast job queued behind it starts shortly after
// that timeout instead of waiting for the slow job's simulated long-running
// work to run to completion.
func TestSubmissionQueue_HeadOfLineBlocking_UnblocksAtJobTimeout(t *testing.T) {
	// A generous default job timeout so the fast job's own queueing wait is
	// never at risk of expiring while it sits behind the slow job; the slow
	// job is instead bounded by its own short-deadline caller context. This
	// decouples "how long the slow job is allowed to run" from "how long the
	// fast job is allowed to wait its turn," which is what a real exam
	// workload looks like (many callers, one shared per-request deadline
	// each, not a shared budget).
	q := NewSubmissionQueue(10, 5*time.Second)

	slowStarted := make(chan struct{})
	slowDone := make(chan struct{})
	go func() {
		slowCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_ = q.Run(slowCtx, func(jobCtx context.Context) error {
			close(slowStarted)
			select {
			case <-time.After(5 * time.Second):
				return nil
			case <-jobCtx.Done():
				return jobCtx.Err()
			}
		})
		close(slowDone)
	}()

	<-slowStarted // the slow job has been picked up by the single worker

	start := time.Now()
	fastStarted := make(chan time.Time, 1)
	fastDone := make(chan struct{})
	go func() {
		_ = q.Run(context.Background(), func(context.Context) error {
			fastStarted <- time.Now()
			return nil
		})
		close(fastDone)
	}()

	select {
	case ts := <-fastStarted:
		elapsed := ts.Sub(start)
		// The fast job should start shortly after the slow job's 50ms job
		// timeout elapses, not after its simulated 5s of work.
		if elapsed > 400*time.Millisecond {
			t.Fatalf("fast job started too late (%v after enqueue); head-of-line blocking not fixed", elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("fast job never started; worker stayed blocked on the slow job")
	}

	<-fastDone
	<-slowDone

	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)
}

func TestSubmissionQueue_ShutdownWaitsForInFlight(t *testing.T) {
	q := NewSubmissionQueue(10, 5*time.Second)

	jobDone := make(chan struct{})
	var jobErr error
	go func() {
		jobErr = q.Run(context.Background(), func(context.Context) error {
			time.Sleep(80 * time.Millisecond)
			return nil
		})
		close(jobDone)
	}()

	// Give job time to start.
	time.Sleep(10 * time.Millisecond)

	shutCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	shutErr := q.Shutdown(shutCtx)

	<-jobDone
	if shutErr != nil {
		t.Fatalf("expected nil shutdown error, got %v", shutErr)
	}
	if jobErr != nil {
		t.Fatalf("expected nil job error, got %v", jobErr)
	}
}
