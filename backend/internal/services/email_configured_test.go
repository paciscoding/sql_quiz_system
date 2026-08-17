package services

import "testing"

// CRITICAL 2: AuthService.EmailConfigured() is the flag ImportStudentsWithCredentials
// uses to decide whether a "queued" email_status is honest. It must read false
// for the default NoopEmailSender (no EMAIL_SENDER_ADDRESS configured) and true
// once a real sender is wired in via SetEmailSender — a plain unit test, no DB
// needed since NewAuthService(nil, ...) never touches the repo before a
// repo-backed method is called.
func TestAuthService_EmailConfigured(t *testing.T) {
	auth := NewAuthService(nil, "test-jwt-secret-least-32-characters-long")

	if auth.EmailConfigured() {
		t.Error("EmailConfigured() = true with the default NoopEmailSender, want false")
	}

	auth.SetEmailSender(NewSMTPEmailSender())
	if !auth.EmailConfigured() {
		t.Error("EmailConfigured() = false after SetEmailSender(a real sender), want true")
	}

	// Switching back to the no-op stub (e.g. in a test fixture) must be
	// detected too — this is not a one-shot latch.
	auth.SetEmailSender(NoopEmailSender{})
	if auth.EmailConfigured() {
		t.Error("EmailConfigured() = true after reverting to NoopEmailSender, want false")
	}
}
