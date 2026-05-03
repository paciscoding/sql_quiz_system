package services

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LockManager serialises submissions per student using Postgres session-level
// advisory locks. Compared to an in-memory sync.Map this:
//   - works across multiple backend instances (horizontal scaling preserves
//     the "one submission at a time per student" guarantee), and
//   - holds no per-student state in process memory, so the lock table can't
//     grow unboundedly with the number of students seen.
type LockManager struct {
	pool *pgxpool.Pool
}

func NewLockManager(pool *pgxpool.Pool) *LockManager {
	return &LockManager{pool: pool}
}

// studentAdvisoryKey hashes the student identifier into the bigint key space
// expected by pg_advisory_lock. A namespace prefix keeps these keys from
// colliding with any other advisory locks the application might add later.
func studentAdvisoryKey(studentID string) int64 {
	h := fnv.New64a()
	h.Write([]byte("sql_quiz/student/"))
	h.Write([]byte(studentID))
	return int64(h.Sum64())
}

// ExecuteLocked runs fn while holding the per-student advisory lock. The lock
// is bound to the connection that acquired it and is released when fn returns.
func (m *LockManager) ExecuteLocked(studentID string, fn func() error) error {
	ctx := context.Background()
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("lock manager: acquire connection: %w", err)
	}
	defer conn.Release()

	key := studentAdvisoryKey(studentID)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("lock manager: acquire advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
	}()

	return fn()
}
