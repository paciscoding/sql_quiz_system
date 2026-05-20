package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrQueueFull is returned by Run when the job channel is at capacity.
var ErrQueueFull = errors.New("submission queue: queue is full")

// ErrQueueShutdown is returned by Run after Shutdown has been called.
var ErrQueueShutdown = errors.New("submission queue: queue is shut down")

type job struct {
	fn      func() error
	replyCh chan error
	ctx     context.Context
}

// SubmissionQueue is a single-worker FIFO queue for DML evaluation closures.
// It ensures only one DML submission is evaluated at a time.
type SubmissionQueue struct {
	jobCh      chan job
	jobTimeout time.Duration
	wg         sync.WaitGroup
	once       sync.Once
}

// NewSubmissionQueue creates a new SubmissionQueue and starts its worker goroutine.
// maxDepth is the capacity of the internal job channel.
// jobTimeout is the maximum time allowed for a single job (applied per-job).
func NewSubmissionQueue(maxDepth int, jobTimeout time.Duration) *SubmissionQueue {
	q := &SubmissionQueue{
		jobCh:      make(chan job, maxDepth),
		jobTimeout: jobTimeout,
	}
	q.wg.Add(1)
	go q.worker()
	return q
}

func (q *SubmissionQueue) worker() {
	defer q.wg.Done()
	for j := range q.jobCh {
		// If context already expired while waiting in queue, skip execution.
		select {
		case <-j.ctx.Done():
			j.replyCh <- j.ctx.Err()
			continue
		default:
		}

		done := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- fmt.Errorf("submission queue: panic: %v", r)
				}
			}()
			done <- j.fn()
		}()

		select {
		case e := <-done:
			j.replyCh <- e
		case <-j.ctx.Done():
			j.replyCh <- j.ctx.Err()
		}
	}
}

// Run enqueues fn for serial execution by the single worker goroutine.
// The job inherits a per-job timeout derived from ctx and q.jobTimeout
// (whichever is shorter). Returns ErrQueueFull if the channel is at capacity,
// ErrQueueShutdown if Shutdown was called.
func (q *SubmissionQueue) Run(ctx context.Context, fn func() error) (err error) {
	// Protect against send on closed channel after Shutdown.
	defer func() {
		if r := recover(); r != nil {
			err = ErrQueueShutdown
		}
	}()

	runCtx, cancel := context.WithTimeout(ctx, q.jobTimeout)
	defer cancel()

	j := job{
		fn:      fn,
		replyCh: make(chan error, 1),
		ctx:     runCtx,
	}

	select {
	case q.jobCh <- j:
		// Job enqueued; wait for reply.
		select {
		case e := <-j.replyCh:
			return e
		case <-runCtx.Done():
			return runCtx.Err()
		}
	default:
		return ErrQueueFull
	}
}

// Shutdown closes the job channel (draining in-flight jobs) and waits for the
// worker goroutine to exit. ctx controls how long to wait.
func (q *SubmissionQueue) Shutdown(ctx context.Context) error {
	q.once.Do(func() { close(q.jobCh) })
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
