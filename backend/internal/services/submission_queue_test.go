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
		errs[0] = q.Run(context.Background(), func() error {
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
		errs[1] = q.Run(context.Background(), func() error {
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
		_ = q.Run(context.Background(), func() error {
			<-block
			return nil
		})
	}()

	// Give first job time to be picked up by the worker.
	time.Sleep(20 * time.Millisecond)

	// Fill the one available channel slot.
	go func() {
		_ = q.Run(context.Background(), func() error {
			return nil
		})
	}()
	time.Sleep(10 * time.Millisecond)

	// Now the channel is full; this call should get ErrQueueFull.
	err := q.Run(context.Background(), func() error { return nil })
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
	err := q.Run(context.Background(), func() error {
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
	err := q.Run(context.Background(), func() error {
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected non-nil error after panic")
	}

	// Worker should still be alive. Second job runs normally.
	err = q.Run(context.Background(), func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error for second job, got %v", err)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = q.Shutdown(shutCtx)
}

func TestSubmissionQueue_ShutdownWaitsForInFlight(t *testing.T) {
	q := NewSubmissionQueue(10, 5*time.Second)

	jobDone := make(chan struct{})
	var jobErr error
	go func() {
		jobErr = q.Run(context.Background(), func() error {
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
