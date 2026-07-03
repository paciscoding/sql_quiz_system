package services

import (
	"log"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// EmailSender abstracts credential/notification email delivery. The current
// implementation is a no-op stub; swapping in real SMTP later is a one-file
// change with no call-site edits.
type EmailSender interface {
	// SendCredential delivers a freshly generated password to a user.
	SendCredential(toEmail, username, password string) error
	// SendResetCode delivers a forgot-password verification code to a user.
	SendResetCode(toEmail, username, code string) error
	// SendChangeNotice notifies an administrator that a password event occurred.
	SendChangeNotice(adminEmail string, ev models.PasswordEvent) error
}

// NoopEmailSender logs the intended email and returns nil. It never sends
// anything over the network.
type NoopEmailSender struct{}

// SendCredential logs the credential-delivery intent.
func (NoopEmailSender) SendCredential(toEmail, username, password string) error {
	log.Printf("TODO: email credential to %q for user %q (password delivery pending real SMTP)", toEmail, username)
	return nil
}

// SendResetCode logs the reset-code-delivery intent.
func (NoopEmailSender) SendResetCode(toEmail, username, code string) error {
	log.Printf("TODO: email reset code to %q for user %q (code delivery pending real SMTP)", toEmail, username)
	return nil
}

// SendChangeNotice logs the change-notice intent.
func (NoopEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	log.Printf("TODO: email change-notice to admin %q for user_id=%d source=%s (delivery pending real SMTP)", adminEmail, ev.UserID, ev.Source)
	return nil
}
