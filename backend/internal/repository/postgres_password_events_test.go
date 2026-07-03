package repository

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

var (
	repoOnce    sync.Once
	sharedRepo  *PostgresRepository
	repoInitErr error
	repoSkip    bool
)

// openTestRepo returns a process-wide singleton repository connected to
// TEST_DATABASE_URL with the schema bootstrapped once. It skips the test when
// no test database is configured so `go test ./...` stays green without
// Postgres. A single shared pool keeps the connection count bounded. (This
// package cannot use internal/testsupport, which would create an import cycle.)
func openTestRepo(t *testing.T) *PostgresRepository {
	t.Helper()
	repoOnce.Do(func() {
		dsn := os.Getenv("TEST_DATABASE_URL")
		if dsn == "" {
			repoSkip = true
			return
		}
		repo, err := NewPostgresRepository(dsn)
		if err != nil {
			repoInitErr = err
			return
		}
		ctx := context.Background()
		for attempt := 0; attempt < 8; attempt++ {
			if err = repo.CreateRestrictedUser(ctx); err == nil {
				if err = repo.InitializeSystemTables(ctx); err == nil {
					break
				}
			}
			time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
		}
		if err != nil {
			repoInitErr = err
			return
		}
		sharedRepo = repo
	})
	if repoSkip {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	if repoInitErr != nil {
		t.Fatalf("test repo bootstrap: %v", repoInitErr)
	}
	return sharedRepo
}

func uniqueUsername(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func TestPasswordEvents_InsertAndFetchOrdered(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("pw_events_user"),
		Password: "hash",
		Role:     models.RoleLecturer,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Insert two events in a known order.
	first := models.PasswordEvent{
		UserID:         uid,
		ChangedBy:      &uid,
		Source:         models.PasswordEventCreate,
		NewPasswordEnc: []byte("enc-1"),
		Nonce:          []byte("nonce-1"),
	}
	if _, err := repo.InsertPasswordEvent(ctx, first); err != nil {
		t.Fatalf("InsertPasswordEvent first: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	second := models.PasswordEvent{
		UserID:         uid,
		ChangedBy:      &uid,
		Source:         models.PasswordEventAdminReset,
		NewPasswordEnc: []byte("enc-2"),
		Nonce:          []byte("nonce-2"),
	}
	if _, err := repo.InsertPasswordEvent(ctx, second); err != nil {
		t.Fatalf("InsertPasswordEvent second: %v", err)
	}

	rows, err := repo.GetPasswordEvents(ctx, uid)
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 events, got %d", len(rows))
	}
	// Newest first.
	if rows[0].Source != models.PasswordEventAdminReset {
		t.Fatalf("expected newest (admin_reset) first, got %q", rows[0].Source)
	}
	if string(rows[0].NewPasswordEnc) != "enc-2" || string(rows[0].Nonce) != "nonce-2" {
		t.Fatalf("ciphertext/nonce not round-tripped: %+v", rows[0])
	}
	if rows[0].ChangedByUsername == "" {
		t.Fatalf("expected changed_by username resolved")
	}
	if rows[1].Source != models.PasswordEventCreate {
		t.Fatalf("expected oldest (create) last, got %q", rows[1].Source)
	}
}

func TestTokenVersion_BumpAndGet(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("tokver_user"),
		Password: "hash",
		Role:     models.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	v0, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion: %v", err)
	}
	if v0 != 0 {
		t.Fatalf("expected initial token_version 0, got %d", v0)
	}

	if err := repo.BumpTokenVersion(ctx, uid); err != nil {
		t.Fatalf("BumpTokenVersion: %v", err)
	}
	v1, err := repo.GetTokenVersion(ctx, uid)
	if err != nil {
		t.Fatalf("GetTokenVersion after bump: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("expected token_version 1 after bump, got %d", v1)
	}
}
