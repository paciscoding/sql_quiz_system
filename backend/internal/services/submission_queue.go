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

	// mu guards closed and serialises sends against the close in Shutdown.
	// Run takes RLock (many concurrent senders are fine); Shutdown takes the
	// exclusive Lock, so a send can never race the close of jobCh.
	mu     sync.RWMutex
	closed bool
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

		// Run inline. The worker's single WaitGroup registration (see
		// NewSubmissionQueue) then genuinely covers in-flight execution, so
		// Shutdown's wg.Wait blocks until fn returns. A detached goroutine here
		// would let the worker abandon fn on ctx timeout and advance/exit while
		// the real work kept running — leaking that goroutine and racing with
		// Shutdown and the caller (the -race failure this replaced).
		//
		// Caller-side timeout responsiveness is unaffected: Run's own select on
		// runCtx.Done() still returns to the caller without waiting for fn.
		j.replyCh <- q.runJob(j)
	}
}

// runJob executes j.fn, recovering any panic into an error so the single
// worker goroutine survives a panicking job.
func (q *SubmissionQueue) runJob(j job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("submission queue: panic: %v", r)
		}
	}()
	return j.fn()
}

// Run enqueues fn for serial execution by the single worker goroutine.
// The job inherits a per-job timeout derived from ctx and q.jobTimeout
// (whichever is shorter). Returns ErrQueueFull if the channel is at capacity,
// ErrQueueShutdown if Shutdown was called.
func (q *SubmissionQueue) Run(ctx context.Context, fn func() error) (err error) {
	runCtx, cancel := context.WithTimeout(ctx, q.jobTimeout)
	defer cancel()

	j := job{
		fn:      fn,
		replyCh: make(chan error, 1),
		ctx:     runCtx,
	}

	// Enqueue under RLock so the send cannot race Shutdown's close of jobCh.
	// The non-blocking select keeps the critical section short.
	q.mu.RLock()
	if q.closed {
		q.mu.RUnlock()
		return ErrQueueShutdown
	}
	select {
	case q.jobCh <- j:
		q.mu.RUnlock()
	default:
		q.mu.RUnlock()
		return ErrQueueFull
	}

	// Enqueued; wait for the worker's reply or a caller-side timeout. Returning
	// on runCtx.Done() here (without waiting for fn) is what keeps Run responsive
	// to the per-job timeout even though the worker runs fn inline.
	select {
	case e := <-j.replyCh:
		return e
	case <-runCtx.Done():
		return runCtx.Err()
	}
}

// Shutdown closes the job channel (draining in-flight jobs) and waits for the
// worker goroutine to exit. ctx controls how long to wait.
func (q *SubmissionQueue) Shutdown(ctx context.Context) error {
	q.once.Do(func() {
		q.mu.Lock()
		q.closed = true
		close(q.jobCh)
		q.mu.Unlock()
	})
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
