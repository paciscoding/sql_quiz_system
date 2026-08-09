// Package schemagate provides an in-process, per-key serialiser.
//
// It is a leaf package on purpose: internal/services already imports
// internal/repository, so a gate living in services could not be imported by
// repository without closing an import cycle. Nothing here imports anything
// from this project.
package schemagate

import (
	"context"
	"sync"
)

// SchemaGate serialises work per schema name, inside this process.
//
// Practice DML had no serialisation at all: cmd/main.go injects the
// single-worker SubmissionQueue into QuizController only, and
// acquireSchemaEvalLock takes the per-schema advisory lock SHARED so concurrent
// evaluations do not exclude each other. Two practice submissions whose seeds
// touch the same tables in opposite order therefore deadlock — reproduced in
// repository/postgres_practice_concurrent_dml_test.go (SQLSTATE 40P01).
//
// Each schema key owns a buffered channel of capacity 1 used as a mutex.
// A channel rather than sync.Mutex because the wait MUST be cancellable and
// time-bounded: sync.Mutex.Lock is neither, so a wedged evaluation would hang
// every later submitter for that schema with no way to shed them.
//
// The zero value is ready to use; NewSchemaGate is for standalone construction.
//
// Scope note: this is process-local, matching what SubmissionQueue already
// assumes. Two backend instances against one database would each keep their own
// gate. See the design's Known limitations.
type SchemaGate struct {
	mu    sync.Mutex
	gates map[string]chan struct{}
}

// NewSchemaGate returns a ready SchemaGate.
func NewSchemaGate() *SchemaGate {
	return &SchemaGate{}
}

// gateFor returns the channel for schema, creating it — and the map — on first
// use, which is what makes the zero value usable. Entries are never evicted:
// the population is bounded by the number of schemas, so a deleted category
// leaks one channel and one map entry.
func (g *SchemaGate) gateFor(schema string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = make(map[string]chan struct{})
	}
	ch, ok := g.gates[schema]
	if !ok {
		ch = make(chan struct{}, 1)
		g.gates[schema] = ch
	}
	return ch
}

// Acquire blocks until schema is free or ctx ends.
//
// On success it returns a release func that is safe to call more than once —
// callers release with `defer`, and a second call must never free a slot the
// caller no longer owns.
//
// On failure it returns a nil release and ctx's error, so the caller can tell
// context.DeadlineExceeded from context.Canceled. It never returns a nil
// release together with a nil error.
//
// An empty schema is a no-op, mirroring acquireSchemaEvalLock
// (repository/postgres_rebuild_lock.go:27).
func (g *SchemaGate) Acquire(ctx context.Context, schema string) (func(), error) {
	if schema == "" {
		return func() {}, nil
	}
	ch := g.gateFor(schema)
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
