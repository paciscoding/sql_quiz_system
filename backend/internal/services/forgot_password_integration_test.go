package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

// captureSender records SendCredential and SendResetCode calls so tests can
// assert whether (and with what recipient/payload) an email would have been
// sent.
type captureSender struct {
	mu        sync.Mutex
	calls     []struct{ to, username, password string }
	codeCalls []struct{ to, username, code string }
}

func (c *captureSender) SendCredential(to, username, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, struct{ to, username, password string }{to, username, password})
	return nil
}
func (c *captureSender) SendResetCode(to, username, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.codeCalls = append(c.codeCalls, struct{ to, username, code string }{to, username, code})
	return nil
}
func (c *captureSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	return nil
}
func (c *captureSender) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}
func (c *captureSender) codeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.codeCalls)
}

// waitForCode polls until the sender has captured at least one reset-code email
// (the step-1 delivery is asynchronous) and returns the first captured call.
func (c *captureSender) waitForCode(t *testing.T) struct{ to, username, code string } {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.codeCalls) > 0 {
			call := c.codeCalls[0]
			c.mu.Unlock()
			return call
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the reset-code email")
	return struct{ to, username, code string }{}
}

func mkUserWithEmail(t *testing.T, repo *repository.PostgresRepository, role models.Role, pw, email string) models.User {
	t.Helper()
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("fp_%s_%d", role, time.Now().UnixNano())
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	id, err := repo.CreateUser(ctx, models.User{Username: username, Password: string(hash), Role: role, Email: emailPtr})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return models.User{ID: id, Username: username, Role: role}
}

// TestPerformForgotReset_HappyPath proves the ASYNC worker (invoked directly, so
// no goroutine timing is involved): a user WITH an email gets a new password
// (login hash changed), a forgot_reset event is written (key configured),
// token_version is bumped, and the credential is emailed exactly once.
func TestPerformForgotReset_HappyPath(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")
	// Carry the email through, as the verify step would.
	full, _ := repo.GetUserByID(ctx, user.ID)

	beforeVersion, _ := repo.GetTokenVersion(ctx, user.ID)

	auth.performForgotReset(ctx, full)

	if sender.count() != 1 {
		t.Fatalf("expected exactly one credential email, got %d", sender.count())
	}
	sent := sender.calls[0]
	if sent.to != "student@example.com" {
		t.Fatalf("expected email to the user's address, got %q", sent.to)
	}
	// New password must actually be the one emailed and must log in.
	updated, err := repo.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte(sent.password)); err != nil {
		t.Fatalf("emailed password does not validate against stored hash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("oldpw")); err == nil {
		t.Fatalf("old password should no longer validate")
	}
	// token_version bumped → old sessions evicted.
	afterVersion, _ := repo.GetTokenVersion(ctx, user.ID)
	if afterVersion != beforeVersion+1 {
		t.Fatalf("expected token_version %d, got %d", beforeVersion+1, afterVersion)
	}
	// forgot_reset event written and decryptable (key IS configured here).
	_, events, err := auth.GetPasswordEventsView(ctx, user, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventForgotReset {
		t.Fatalf("expected 1 forgot_reset event, got %+v", events)
	}
	if events[0].NewPassword != sent.password {
		t.Fatalf("changelog value %q != emailed password %q", events[0].NewPassword, sent.password)
	}
}

// TestPerformForgotReset_NoEmail_NoSend proves the worker is a no-op for a user
// with no email on file (defensive: beginForgotReset already filters these out).
func TestPerformForgotReset_NoEmail_NoSend(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "")
	full, _ := repo.GetUserByID(ctx, user.ID)

	auth.performForgotReset(ctx, full)
	if sender.count() != 0 {
		t.Fatalf("expected no send for a user without email, got %d", sender.count())
	}
}

// TestForgotPassword_StoresHashedOTP proves step 1 (via the deterministic SYNC
// path) persists the SHA-256 hash of the code — never the plaintext — with an
// expiry ~ResetOTPTTL out and a zeroed attempt counter.
func TestForgotPassword_StoresHashedOTP(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")

	_, otp, ok := auth.beginForgotReset(ctx, user.Username)
	if !ok {
		t.Fatal("first forgot-password should be authorized")
	}
	if len(otp) != ResetOTPLength {
		t.Fatalf("expected a %d-digit code, got %q", ResetOTPLength, otp)
	}

	hash, expiresAt, attempts, err := repo.GetResetOTP(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetResetOTP: %v", err)
	}
	if hash == nil || expiresAt == nil {
		t.Fatal("expected a stored code hash and expiry")
	}
	if *hash == otp {
		t.Fatal("code stored in PLAINTEXT — must be hashed")
	}
	if *hash != hashResetOTP(otp) {
		t.Fatalf("stored hash %q != SHA-256 of the issued code", *hash)
	}
	if attempts != 0 {
		t.Fatalf("expected attempts reset to 0, got %d", attempts)
	}
	ttl := time.Until(*expiresAt)
	if ttl < ResetOTPTTL-time.Minute || ttl > ResetOTPTTL+time.Minute {
		t.Fatalf("expected expiry ~%s out, got %s", ResetOTPTTL, ttl)
	}
}

// TestForgotPassword_EmailsCodeNotPassword proves the full step-1 entry point
// emails the one-time code (asynchronously) and does NOT change the password or
// send any credential.
func TestForgotPassword_EmailsCodeNotPassword(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")

	if err := auth.ForgotPassword(ctx, user.Username); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	sent := sender.waitForCode(t)
	if sent.to != "student@example.com" {
		t.Fatalf("expected the code emailed to the user's address, got %q", sent.to)
	}
	// The emailed code matches the stored hash.
	hash, _, _, err := repo.GetResetOTP(ctx, user.ID)
	if err != nil || hash == nil {
		t.Fatalf("GetResetOTP: hash=%v err=%v", hash, err)
	}
	if hashResetOTP(sent.code) != *hash {
		t.Fatalf("emailed code does not match the stored hash")
	}
	// No password change, no credential email at step 1.
	if sender.count() != 0 {
		t.Fatalf("step 1 must not send a credential email, got %d", sender.count())
	}
	updated, _ := repo.GetUserByID(ctx, user.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("oldpw")); err != nil {
		t.Fatalf("password must be unchanged at step 1")
	}
}

// TestVerifyForgotPassword_HappyPath drives the full two-step flow with the
// heavy pieces called synchronously: step 1 issues the code, the verify core
// accepts it and clears it, and performForgotReset regenerates + emails the new
// password (hash updated, token_version bumped, forgot_reset event written).
// A replay of the same code after success must fail.
func TestVerifyForgotPassword_HappyPath(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")
	beforeVersion, _ := repo.GetTokenVersion(ctx, user.ID)

	_, otp, ok := auth.beginForgotReset(ctx, user.Username)
	if !ok {
		t.Fatal("step 1 should be authorized")
	}

	verified, err := auth.verifyResetOTP(ctx, user.Username, otp)
	if err != nil {
		t.Fatalf("verifyResetOTP with the correct code: %v", err)
	}
	// The code is cleared synchronously → replay fails.
	if hash, _, _, err := repo.GetResetOTP(ctx, user.ID); err != nil || hash != nil {
		t.Fatalf("expected the code cleared after success, hash=%v err=%v", hash, err)
	}
	if _, err := auth.verifyResetOTP(ctx, user.Username, otp); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("replaying a used code must fail with ErrInvalidResetCode, got %v", err)
	}

	// Drive the async worker synchronously, as VerifyForgotPassword would spawn it.
	auth.performForgotReset(ctx, verified)

	if sender.count() != 1 {
		t.Fatalf("expected exactly one credential email, got %d", sender.count())
	}
	sent := sender.calls[0]
	updated, _ := repo.GetUserByID(ctx, user.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte(sent.password)); err != nil {
		t.Fatalf("emailed password does not validate against stored hash: %v", err)
	}
	if afterVersion, _ := repo.GetTokenVersion(ctx, user.ID); afterVersion != beforeVersion+1 {
		t.Fatalf("expected token_version bumped to %d, got %d", beforeVersion+1, afterVersion)
	}
	_, events, err := auth.GetPasswordEventsView(ctx, user, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Source != models.PasswordEventForgotReset {
		t.Fatalf("expected 1 forgot_reset event, got %+v", events)
	}
}

// TestVerifyForgotPassword_WrongCode proves a wrong code fails with the generic
// sentinel and increments the attempt counter.
func TestVerifyForgotPassword_WrongCode(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")
	_, otp, ok := auth.beginForgotReset(ctx, user.Username)
	if !ok {
		t.Fatal("step 1 should be authorized")
	}

	wrong := "000000"
	if wrong == otp {
		wrong = "999999"
	}
	if err := auth.VerifyForgotPassword(ctx, user.Username, wrong); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("expected ErrInvalidResetCode for a wrong code, got %v", err)
	}
	if _, _, attempts, _ := repo.GetResetOTP(ctx, user.ID); attempts != 1 {
		t.Fatalf("expected attempts=1 after one wrong guess, got %d", attempts)
	}
	// Password untouched, nothing sent.
	if sender.count() != 0 {
		t.Fatalf("wrong code must not trigger a credential email")
	}
	updated, _ := repo.GetUserByID(ctx, user.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("oldpw")); err != nil {
		t.Fatalf("password must be unchanged after a wrong code")
	}
}

// TestVerifyForgotPassword_MaxAttempts proves the attempt cap: after
// ResetOTPMaxAttempts wrong guesses, even the CORRECT code is rejected.
func TestVerifyForgotPassword_MaxAttempts(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")
	_, otp, ok := auth.beginForgotReset(ctx, user.Username)
	if !ok {
		t.Fatal("step 1 should be authorized")
	}

	wrong := "000000"
	if wrong == otp {
		wrong = "999999"
	}
	for i := 0; i < ResetOTPMaxAttempts; i++ {
		if err := auth.VerifyForgotPassword(ctx, user.Username, wrong); !errors.Is(err, ErrInvalidResetCode) {
			t.Fatalf("attempt %d: expected ErrInvalidResetCode, got %v", i+1, err)
		}
	}
	// 6th attempt with the CORRECT code: cap exhausted → still rejected.
	if err := auth.VerifyForgotPassword(ctx, user.Username, otp); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("expected the correct code rejected after the attempt cap, got %v", err)
	}
}

// TestVerifyForgotPassword_ExpiredCode proves an expired code is rejected even
// when it is otherwise correct.
func TestVerifyForgotPassword_ExpiredCode(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")

	// Store a known code with an expiry in the past.
	otp := "246813"
	if err := repo.SetResetOTP(ctx, user.ID, hashResetOTP(otp), time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetResetOTP: %v", err)
	}
	if err := auth.VerifyForgotPassword(ctx, user.Username, otp); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("expected ErrInvalidResetCode for an expired code, got %v", err)
	}
}

// TestVerifyForgotPassword_NoOTPOrUnknownUser proves the generic sentinel comes
// back when no code is outstanding and for unknown usernames — never a
// distinguishing error.
func TestVerifyForgotPassword_NoOTPOrUnknownUser(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")

	// No code outstanding.
	if err := auth.VerifyForgotPassword(ctx, user.Username, "123456"); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("expected ErrInvalidResetCode with no code outstanding, got %v", err)
	}
	// Unknown username.
	unknown := fmt.Sprintf("does_not_exist_%d", time.Now().UnixNano())
	if err := auth.VerifyForgotPassword(ctx, unknown, "123456"); !errors.Is(err, ErrInvalidResetCode) {
		t.Fatalf("expected ErrInvalidResetCode for unknown user, got %v", err)
	}
}

// TestForgotPassword_ThrottleWithKey is a regression guard: with an encryption
// key configured, the throttle still fires — the second authorization within the
// window is denied. Exercised via the deterministic SYNC path.
func TestForgotPassword_ThrottleWithKey(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	if !auth.KeyConfigured() {
		t.Fatal("expected key configured for this test")
	}

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")

	if _, _, ok := auth.beginForgotReset(ctx, user.Username); !ok {
		t.Fatal("first forgot-password should be authorized")
	}
	if _, _, ok := auth.beginForgotReset(ctx, user.Username); ok {
		t.Fatal("second forgot-password within window must be throttled (with key)")
	}
}

// TestForgotPassword_ThrottleWithoutKey is the core Fix-1 regression: with NO
// encryption key (s.cipher == nil), the 5-minute throttle must STILL fire. The
// throttle reads system_users.last_forgot_reset_at, which is written regardless
// of the key, so the second request within the window is denied even though no
// password_events row is ever persisted.
func TestForgotPassword_ThrottleWithoutKey(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()

	// Force cipher == nil by removing the key from the environment for the
	// lifetime of this test, then constructing the service. Restore afterwards.
	orig, had := os.LookupEnv(PasswordCryptoEnvKey)
	os.Unsetenv(PasswordCryptoEnvKey)
	t.Cleanup(func() {
		if had {
			os.Setenv(PasswordCryptoEnvKey, orig)
		}
	})
	auth := NewAuthService(repo, "test-jwt-secret-least-32-characters-long")
	if auth.KeyConfigured() {
		t.Fatal("expected cipher==nil (no key) for this test")
	}
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "student@example.com")

	// First request: authorized (throttle clock stamped synchronously).
	firstUser, otp, ok1 := auth.beginForgotReset(ctx, user.Username)
	if !ok1 {
		t.Fatal("first forgot-password should be authorized (no key)")
	}
	// Second request within the window: MUST be throttled even without a key.
	if _, _, ok2 := auth.beginForgotReset(ctx, user.Username); ok2 {
		t.Fatal("second forgot-password within window must be throttled without a key (Fix 1)")
	}

	// Verify the issued code and drive the authorized worker once: a single
	// credential email, one password change, and no password_events row (no key).
	if _, err := auth.verifyResetOTP(ctx, user.Username, otp); err != nil {
		t.Fatalf("verifyResetOTP: %v", err)
	}
	auth.performForgotReset(ctx, firstUser)
	if sender.count() != 1 {
		t.Fatalf("expected exactly one send for the single authorized request, got %d", sender.count())
	}
	rows, err := repo.GetPasswordEvents(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no password_events row without a key, got %d", len(rows))
	}
}

// TestForgotPassword_NoEmailOnFile_NoSend proves a user WITHOUT an email gets
// no code and no send, and the call still returns nil (generic success).
func TestForgotPassword_NoEmailOnFile_NoSend(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	user := mkUserWithEmail(t, repo, models.RoleLecturer, "oldpw", "")

	if err := auth.ForgotPassword(ctx, user.Username); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	if sender.codeCount() != 0 || sender.count() != 0 {
		t.Fatalf("expected no send for a user without email, got codes=%d creds=%d",
			sender.codeCount(), sender.count())
	}
	// No code stored, password unchanged.
	if hash, _, _, _ := repo.GetResetOTP(ctx, user.ID); hash != nil {
		t.Fatalf("expected no stored code for a no-email user")
	}
	updated, _ := repo.GetUserByID(ctx, user.ID)
	if err := bcrypt.CompareHashAndPassword([]byte(updated.Password), []byte("oldpw")); err != nil {
		t.Fatalf("password should be unchanged for no-email user")
	}
}

// TestForgotPassword_UnknownUser_NoErrorNoSend proves an unknown username does
// not error (no enumeration via error) and triggers no send.
func TestForgotPassword_UnknownUser_NoErrorNoSend(t *testing.T) {
	repo := integrationRepo(t)
	ctx := context.Background()
	auth := newIntegrationAuth(t, repo)
	sender := &captureSender{}
	auth.SetEmailSender(sender)

	if err := auth.ForgotPassword(ctx, fmt.Sprintf("does_not_exist_%d", time.Now().UnixNano())); err != nil {
		t.Fatalf("expected nil error for unknown user, got %v", err)
	}
	if sender.codeCount() != 0 || sender.count() != 0 {
		t.Fatalf("expected no send for unknown user, got codes=%d creds=%d",
			sender.codeCount(), sender.count())
	}
}
