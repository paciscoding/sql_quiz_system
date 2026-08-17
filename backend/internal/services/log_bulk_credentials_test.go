package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// IMPORTANT 1: ImportStudentsWithCredentials (lecturer_controller.go) hands
// LogBulkCredentials a context.WithoutCancel wrapping the request context,
// specifically so the audit write survives the admin's browser disconnecting
// mid-request -- a real risk here, since the import is one transaction with
// one bcrypt hash per row (~50-100ms each), so a 500-row roster runs ~40s,
// past a typical 60s proxy timeout for anything bigger. Without
// WithoutCancel, every InsertPasswordEvent below would fail with "context
// canceled" the instant the client goes away, silently destroying the last
// server-side recovery path (ResendCredential reads from password_events).
//
// This proves the underlying mechanism LogBulkCredentials relies on:
// a bare cancelled context yields zero events written, while
// context.WithoutCancel(cancelledCtx) still writes normally — exactly the
// difference between the pre-fix and post-fix call at lecturer_controller.go.
func TestLogBulkCredentials_SurvivesCancelledRequestContext(t *testing.T) {
	repo := integrationRepo(t)
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.com")
	auth := newIntegrationAuth(t, repo)
	spy := &noticeSpy{}
	auth.SetEmailSender(spy)

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw-cancel")

	t.Run("a bare cancelled context writes nothing", func(t *testing.T) {
		s := mkUser(t, repo, models.RoleStudent, fmt.Sprintf("bulkcred-cancelled_%d", time.Now().UnixNano()))
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()

		written := auth.LogBulkCredentials(cancelledCtx, admin, []BulkCredentialEvent{
			{UserID: int(s.ID), Source: models.PasswordEventCreate, Plaintext: "pw-cancelled"},
		})
		if written != 0 {
			t.Errorf("written = %d, want 0: a cancelled context must fail the insert (this is the failure mode the fix avoids)", written)
		}
		events, err := repo.GetPasswordEvents(context.Background(), int(s.ID))
		if err != nil {
			t.Fatalf("GetPasswordEvents: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("events = %+v, want none: nothing should have been persisted", events)
		}
	})

	t.Run("context.WithoutCancel survives the same cancellation and writes normally", func(t *testing.T) {
		s := mkUser(t, repo, models.RoleStudent, fmt.Sprintf("bulkcred-survived_%d", time.Now().UnixNano()))
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel() // simulates the admin's browser disconnecting right after the transaction commits

		auditCtx := context.WithoutCancel(cancelledCtx)
		written := auth.LogBulkCredentials(auditCtx, admin, []BulkCredentialEvent{
			{UserID: int(s.ID), Source: models.PasswordEventCreate, Plaintext: "pw-survived"},
		})
		if written != 1 {
			t.Fatalf("written = %d, want 1: context.WithoutCancel must insulate the audit write from the cancelled request context", written)
		}
		events, err := repo.GetPasswordEvents(context.Background(), int(s.ID))
		if err != nil {
			t.Fatalf("GetPasswordEvents: %v", err)
		}
		if len(events) != 1 || events[0].Source != models.PasswordEventCreate {
			t.Errorf("events = %+v, want exactly one %q event", events, models.PasswordEventCreate)
		}
	})
}

// noticeSpy records every SendChangeNotice (and SendCredential) call so the
// test can prove the bulk path never fans out an admin email, nor emails the
// student directly — neither spyEmailSender (password_flows_integration_test.go)
// nor captureSender (forgot_password_integration_test.go) counts
// SendChangeNotice calls, so a dedicated spy is needed here.
type noticeSpy struct {
	credentials int
	notices     int
}

func (s *noticeSpy) SendCredential(toEmail, username, password string) error {
	s.credentials++
	return nil
}
func (s *noticeSpy) SendResetCode(toEmail, username, code string) error { return nil }
func (s *noticeSpy) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	s.notices++
	return nil
}

// LogBulkCredentials must persist one password_events row per entry and send
// NOTHING — the trap documented at auth_service.go:476-483 is that
// logPasswordEvent emails ADMIN_EMAIL for every event, which would turn a
// 200-row import into 200 admin emails.
func TestLogBulkCredentials_PersistsEventsAndSendsNoEmail(t *testing.T) {
	repo := integrationRepo(t)
	// ADMIN_NOTIFY_EMAIL must be set BEFORE newIntegrationAuth (which calls
	// NewAuthService): s.adminEmail is read once at construction, so setting
	// it afterwards would leave the change-notice path permanently disarmed
	// and the spy.notices == 0 assertion below would pass vacuously no
	// matter what LogBulkCredentials does. Mirrors bulk_reset_http_test.go.
	t.Setenv("ADMIN_NOTIFY_EMAIL", "admin-notify@example.com")
	auth := newIntegrationAuth(t, repo)
	spy := &noticeSpy{}
	auth.SetEmailSender(spy)
	ctx := context.Background()

	admin := mkUser(t, repo, models.RoleAdmin, "adminpw")
	s1 := mkUser(t, repo, models.RoleStudent, fmt.Sprintf("bulkcred1pw_%d", time.Now().UnixNano()))
	s2 := mkUser(t, repo, models.RoleStudent, fmt.Sprintf("bulkcred2pw_%d", time.Now().UnixNano()))

	auth.LogBulkCredentials(ctx, admin, []BulkCredentialEvent{
		{UserID: int(s1.ID), Source: models.PasswordEventCreate, Plaintext: "pw-one"},
		{UserID: int(s2.ID), Source: models.PasswordEventAdminReset, Plaintext: "pw-two"},
	})

	if spy.notices != 0 {
		t.Errorf("SendChangeNotice called %d times, want 0: the bulk path must never fan out admin email", spy.notices)
	}
	if spy.credentials != 0 {
		t.Errorf("SendCredential called %d times, want 0: delivery is the worker's job, not the logger's", spy.credentials)
	}

	events, err := repo.GetPasswordEvents(ctx, int(s1.ID))
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventCreate {
		t.Errorf("student 1 events = %+v, want exactly one %q event", events, models.PasswordEventCreate)
	}
	// GetPasswordEvents resolves changed_by to a username via a join, so
	// comparing ChangedByUsername to the acting admin's username proves the
	// row was written with ChangedBy = &actorID (the acting admin's id),
	// not left nil or set to some other user.
	if len(events) == 1 && events[0].ChangedByUsername != admin.Username {
		t.Errorf("student 1 event ChangedByUsername = %q, want %q (the acting admin)", events[0].ChangedByUsername, admin.Username)
	}
	events, err = repo.GetPasswordEvents(ctx, int(s2.ID))
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventAdminReset {
		t.Errorf("student 2 events = %+v, want exactly one %q event", events, models.PasswordEventAdminReset)
	}
	if len(events) == 1 && events[0].ChangedByUsername != admin.Username {
		t.Errorf("student 2 event ChangedByUsername = %q, want %q (the acting admin)", events[0].ChangedByUsername, admin.Username)
	}
}
