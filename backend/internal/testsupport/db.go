// Package testsupport provides shared helpers for database integration tests.
// It is imported only from _test.go files, so it is linked exclusively into
// test binaries.
package testsupport

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

var (
	once    sync.Once
	shared  *repository.PostgresRepository
	initErr error
	skip    bool
)

// SharedRepo returns a process-wide singleton repository connected to
// TEST_DATABASE_URL, with the schema bootstrapped exactly once. It skips the
// calling test when no test database is configured. A single shared pool per
// test binary keeps the total connection count bounded (per-test pools would
// otherwise exhaust Postgres' max_connections under `go test ./...`).
func SharedRepo(t *testing.T) *repository.PostgresRepository {
	t.Helper()
	once.Do(func() {
		dsn := os.Getenv("TEST_DATABASE_URL")
		if dsn == "" {
			skip = true
			return
		}
		repo, err := repository.NewPostgresRepository(dsn)
		if err != nil {
			initErr = err
			return
		}
		ctx := context.Background()
		// The bootstrap is idempotent but issues DDL (CREATE ROLE / GRANT). When
		// several package test binaries bootstrap the same database concurrently,
		// Postgres can transiently report "tuple concurrently updated"; retry a
		// few times to absorb that race.
		for attempt := 0; attempt < 8; attempt++ {
			if err = bootstrap(ctx, repo); err == nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
		}
		if err != nil {
			initErr = err
			return
		}
		shared = repo
	})
	if skip {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	if initErr != nil {
		t.Fatalf("test repo bootstrap: %v", initErr)
	}
	return shared
}

func bootstrap(ctx context.Context, repo *repository.PostgresRepository) error {
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		return err
	}
	return repo.InitializeSystemTables(ctx)
}
