package services

import (
	"bytes"
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// SMTP configuration environment variables.
const (
	EmailSenderAddressEnv  = "EMAIL_SENDER_ADDRESS"
	EmailSenderAppPassword = "EMAIL_SENDER_APP_PASSWORD"
	EmailSMTPHostEnv       = "EMAIL_SMTP_HOST"
	EmailSMTPPortEnv       = "EMAIL_SMTP_PORT"

	defaultSMTPHost = "smtp.gmail.com"
	defaultSMTPPort = "587"
)

// smtpSendMail is indirected so tests never touch the network. Production uses
// net/smtp.SendMail, which negotiates STARTTLS on the submission port (587)
// when the server advertises it, then authenticates via PlainAuth.
var smtpSendMail = smtp.SendMail

// SMTPEmailSender delivers credential and change-notice emails over Gmail SMTP
// (STARTTLS submission on 587). It implements EmailSender. The app password is
// held in memory only and is never logged.
type SMTPEmailSender struct {
	from        string
	appPassword string
	host        string
	port        string
}

// NewSMTPEmailSender builds a sender from the environment. Host/port fall back
// to Gmail defaults. The caller is responsible for only constructing this when
// EMAIL_SENDER_ADDRESS is set (see cmd/main.go wiring).
func NewSMTPEmailSender() *SMTPEmailSender {
	host := os.Getenv(EmailSMTPHostEnv)
	if host == "" {
		host = defaultSMTPHost
	}
	port := os.Getenv(EmailSMTPPortEnv)
	if port == "" {
		port = defaultSMTPPort
	}
	return &SMTPEmailSender{
		from:        os.Getenv(EmailSenderAddressEnv),
		appPassword: os.Getenv(EmailSenderAppPassword),
		host:        host,
		port:        port,
	}
}

// From reports the configured sender address (used for startup logging).
func (s *SMTPEmailSender) From() string { return s.from }

// Host reports the configured SMTP host (used for startup logging).
func (s *SMTPEmailSender) Host() string { return s.host }

// SendCredential emails a freshly generated password to a user. If the
// recipient address is empty the send is skipped and nil is returned — many
// accounts have no email on file.
func (s *SMTPEmailSender) SendCredential(toEmail, username, password string) error {
	if toEmail == "" {
		return nil
	}
	subject := "Your SQL Quiz System credentials"
	body := fmt.Sprintf(
		"Hello,\n\nAn account password has been set for username %q.\n\nPassword: %s\n\nPlease sign in and change it if you wish.\n",
		username, password)
	return s.send(toEmail, subject, body)
}

// SendResetCode emails a forgot-password verification code to a user. If the
// recipient address is empty the send is skipped and nil is returned.
func (s *SMTPEmailSender) SendResetCode(toEmail, username, code string) error {
	if toEmail == "" {
		return nil
	}
	subject := "Your password reset code"
	body := fmt.Sprintf(
		"Hello,\n\nA password reset was requested for username %q.\n\nVerification code: %s\n\nThis code is valid for 10 minutes. If you did not request this, you can ignore this email.\n",
		username, code)
	return s.send(toEmail, subject, body)
}

// SendChangeNotice emails an administrator that a password event occurred. If
// the admin address is empty the send is skipped and nil is returned.
func (s *SMTPEmailSender) SendChangeNotice(adminEmail string, ev models.PasswordEvent) error {
	if adminEmail == "" {
		return nil
	}
	subject := "SQL Quiz System: password event"
	body := fmt.Sprintf(
		"A password event was recorded.\n\nUser ID: %d\nSource: %s\nTime: %s\n",
		ev.UserID, ev.Source, ev.CreatedAt.Format(time.RFC1123Z))
	return s.send(adminEmail, subject, body)
}

// send builds the RFC-5322 message and hands it to the (indirected) SMTP send
// call. Kept intentionally thin so the logic worth testing lives in
// buildMessage.
func (s *SMTPEmailSender) send(to, subject, body string) error {
	msg := buildMessage(s.from, to, subject, body, time.Now())
	auth := smtp.PlainAuth("", s.from, s.appPassword, s.host)
	addr := s.host + ":" + s.port
	return smtpSendMail(addr, auth, s.from, []string{to}, msg)
}

// buildMessage assembles a minimal, well-formed RFC-5322 message with CRLF line
// endings and a blank line separating the headers from the plain-text body. It
// is a pure function (no I/O) so it can be unit-tested exhaustively.
func buildMessage(from, to, subject, body string, date time.Time) []byte {
	var b bytes.Buffer
	// Defense-in-depth against header injection: strip any CR/LF from values that
	// are written into header lines, so a crafted address/subject cannot inject
	// extra headers (e.g. "\r\nBcc: evil@x"). The body is left untouched — it is
	// written after the header/body separator and CRLF-normalised below.
	b.WriteString("From: " + sanitizeHeaderValue(from) + "\r\n")
	b.WriteString("To: " + sanitizeHeaderValue(to) + "\r\n")
	b.WriteString("Subject: " + sanitizeHeaderValue(subject) + "\r\n")
	b.WriteString("Date: " + date.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")

	// Normalise body line endings to CRLF for RFC-5322 compliance.
	for i, line := range splitLines(body) {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(line)
	}
	b.WriteString("\r\n")
	return b.Bytes()
}

// sanitizeHeaderValue strips carriage-return and line-feed characters from a
// value destined for an RFC-5322 header. Removing (rather than rejecting) keeps
// delivery best-effort while eliminating the header-injection vector: without CR
// or LF an attacker cannot terminate the current header and start a new one.
func sanitizeHeaderValue(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

// splitLines splits on LF and strips a trailing CR so both "\n" and "\r\n"
// inputs normalise to logical lines without duplicating carriage returns.
func splitLines(s string) []string {
	raw := bytes.Split([]byte(s), []byte("\n"))
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = string(bytes.TrimSuffix(r, []byte("\r")))
	}
	return out
}
