package services

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

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
	events, err = repo.GetPasswordEvents(ctx, int(s2.ID))
	if err != nil {
		t.Fatalf("GetPasswordEvents: %v", err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventAdminReset {
		t.Errorf("student 2 events = %+v, want exactly one %q event", events, models.PasswordEventAdminReset)
	}
}
