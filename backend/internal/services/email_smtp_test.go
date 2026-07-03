package services

import (
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// TestBuildMessage verifies the pure RFC-5322 message builder: correct headers,
// subject, body, CRLF line endings, and a blank line separating headers from the
// body. No network or credentials are involved.
func TestBuildMessage(t *testing.T) {
	date := time.Date(2026, time.July, 1, 9, 30, 0, 0, time.UTC)
	msg := string(buildMessage("sender@example.com", "rcpt@example.com", "Your credentials", "hello\nworld", date))

	// Every physical line must terminate with CRLF (no bare LF).
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.Contains(line, "\n") {
			t.Fatalf("found a bare LF not preceded by CR in line %q", line)
		}
	}

	wantHeaders := []string{
		"From: sender@example.com\r\n",
		"To: rcpt@example.com\r\n",
		"Subject: Your credentials\r\n",
		"Date: Wed, 01 Jul 2026 09:30:00 +0000\r\n",
	}
	for _, h := range wantHeaders {
		if !strings.Contains(msg, h) {
			t.Errorf("message missing header %q\nfull message:\n%s", h, msg)
		}
	}

	// Headers and body must be separated by a blank line (CRLF CRLF).
	idx := strings.Index(msg, "\r\n\r\n")
	if idx < 0 {
		t.Fatalf("no header/body separator (CRLF CRLF) found")
	}
	body := msg[idx+4:]
	if !strings.Contains(body, "hello") || !strings.Contains(body, "world") {
		t.Errorf("body did not contain expected text, got %q", body)
	}
	// Body text must appear AFTER the separator, never in the header block.
	header := msg[:idx]
	if strings.Contains(header, "hello") {
		t.Errorf("body text leaked into headers: %q", header)
	}
}

// TestBuildMessage_HeaderInjectionSanitized proves CR/LF in the to/subject (and
// from) values cannot inject an extra header line. A crafted value containing
// "\r\nBcc: evil@x" must NOT produce a "Bcc:" header — the header block (bytes
// before the CRLF CRLF separator) must contain no injected header.
func TestBuildMessage_HeaderInjectionSanitized(t *testing.T) {
	date := time.Date(2026, time.July, 1, 9, 30, 0, 0, time.UTC)

	craftedTo := "rcpt@example.com\r\nBcc: evil@x"
	craftedSubject := "Hi\r\nBcc: evil2@x"
	craftedFrom := "sender@example.com\r\nX-Injected: 1"
	msg := string(buildMessage(craftedFrom, craftedTo, craftedSubject, "hello\nworld", date))

	// Isolate the header block (everything before the blank-line separator).
	idx := strings.Index(msg, "\r\n\r\n")
	if idx < 0 {
		t.Fatalf("no header/body separator found")
	}
	header := msg[:idx]

	// The attack is neutralised when the crafted CR/LF are removed, so the
	// injected text is FOLDED into the preceding header's value rather than
	// becoming its own header LINE. Assert no header line is the injected header.
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "X-Injected:") {
			t.Fatalf("header injection not sanitized: injected header line %q present in:\n%s", line, header)
		}
	}

	// The legitimate recipient/subject text should survive (CR/LF simply stripped),
	// collapsed onto a single physical header line.
	if !strings.Contains(header, "To: rcpt@example.com") {
		t.Errorf("expected sanitized To header to retain the address, got:\n%s", header)
	}
	if !strings.Contains(header, "Subject: Hi") {
		t.Errorf("expected sanitized Subject header to retain the text, got:\n%s", header)
	}
}

// TestSendResetCode_MessageAndSanitization exercises SendResetCode end-to-end
// against the (indirected) SMTP send: the subject and code land in the built
// message, the code sits in the body (not the headers), and a crafted
// recipient cannot inject headers. No network is involved.
func TestSendResetCode_MessageAndSanitization(t *testing.T) {
	var gotMsg []byte
	var gotTo []string
	orig := smtpSendMail
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotTo = to
		gotMsg = msg
		return nil
	}
	t.Cleanup(func() { smtpSendMail = orig })

	s := &SMTPEmailSender{from: "sender@example.com", appPassword: "pw", host: "smtp.example.com", port: "587"}
	craftedTo := "rcpt@example.com\r\nBcc: evil@x"
	if err := s.SendResetCode(craftedTo, "alice", "123456"); err != nil {
		t.Fatalf("SendResetCode: %v", err)
	}
	if len(gotTo) != 1 || gotTo[0] != craftedTo {
		t.Fatalf("unexpected envelope recipients: %v", gotTo)
	}

	msg := string(gotMsg)
	idx := strings.Index(msg, "\r\n\r\n")
	if idx < 0 {
		t.Fatalf("no header/body separator found")
	}
	header, body := msg[:idx], msg[idx+4:]

	if !strings.Contains(header, "Subject: Your password reset code") {
		t.Errorf("missing reset-code subject, headers:\n%s", header)
	}
	// Header injection via the recipient must be neutralised.
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("header injection not sanitized: %q", line)
		}
	}
	// The code and the validity note live in the body, never the headers.
	if strings.Contains(header, "123456") {
		t.Errorf("code leaked into headers:\n%s", header)
	}
	if !strings.Contains(body, "123456") {
		t.Errorf("body missing the code, got:\n%s", body)
	}
	if !strings.Contains(body, "10 minutes") {
		t.Errorf("body missing the validity note, got:\n%s", body)
	}
	if !strings.Contains(body, "ignore") {
		t.Errorf("body missing the 'ignore if not requested' note, got:\n%s", body)
	}
}

// TestSendResetCode_EmptyRecipientSkips proves an empty address is a silent
// no-op (no SMTP call).
func TestSendResetCode_EmptyRecipientSkips(t *testing.T) {
	called := false
	orig := smtpSendMail
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		called = true
		return nil
	}
	t.Cleanup(func() { smtpSendMail = orig })

	s := &SMTPEmailSender{from: "sender@example.com"}
	if err := s.SendResetCode("", "alice", "123456"); err != nil {
		t.Fatalf("SendResetCode with empty recipient: %v", err)
	}
	if called {
		t.Fatalf("expected no SMTP send for empty recipient")
	}
}
