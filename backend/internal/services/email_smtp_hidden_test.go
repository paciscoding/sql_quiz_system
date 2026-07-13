package services

import (
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBuildHTMLMessage_HiddenSpanAndBothParts verifies the credential/reset/
// change-notice mail is now multipart/alternative (text/plain + text/html) and
// that the HTML part carries a fully-hidden span with the configured static text.
func TestBuildHTMLMessage_HiddenSpanAndBothParts(t *testing.T) {
	date := time.Date(2026, time.July, 1, 9, 30, 0, 0, time.UTC)
	msg := string(buildHTMLMessage("from@x.test", "to@y.test", "Subj", "line one\nline two", "SECRET-NOTE-123", date))

	if !strings.Contains(msg, "Content-Type: multipart/alternative;") {
		t.Fatalf("expected multipart/alternative, msg:\n%s", msg)
	}
	if !strings.Contains(msg, "Content-Type: text/plain; charset=UTF-8") {
		t.Error("missing text/plain part")
	}
	if !strings.Contains(msg, "Content-Type: text/html; charset=UTF-8") {
		t.Error("missing text/html part")
	}
	// The hidden span, its hiding CSS, and the static text must be present.
	if !strings.Contains(msg, hiddenSpanStyle) {
		t.Error("hidden span style missing")
	}
	if !strings.Contains(msg, "mso-hide:all") {
		t.Error("Outlook hide directive missing")
	}
	if !strings.Contains(msg, "SECRET-NOTE-123") {
		t.Errorf("hidden static text missing, msg:\n%s", msg)
	}
	// The visible body text survives in both parts.
	if !strings.Contains(msg, "line one") || !strings.Contains(msg, "line two") {
		t.Error("body text missing")
	}
}

// TestRenderHTMLBody_EscapesAndOmits verifies HTML escaping of both the body and
// the hidden text, and that an empty hidden text omits the span entirely.
func TestRenderHTMLBody_EscapesAndOmits(t *testing.T) {
	withHidden := renderHTMLBody("a <b> & c", `x "<script>" y`)
	if strings.Contains(withHidden, "<b>") || strings.Contains(withHidden, "<script>") {
		t.Errorf("HTML not escaped: %s", withHidden)
	}
	if !strings.Contains(withHidden, "&lt;b&gt;") {
		t.Errorf("expected escaped body, got: %s", withHidden)
	}
	if !strings.Contains(withHidden, hiddenSpanStyle) {
		t.Error("expected hidden span when hiddenText non-empty")
	}

	noHidden := renderHTMLBody("hello", "")
	if strings.Contains(noHidden, hiddenSpanStyle) || strings.Contains(noHidden, "<span") {
		t.Errorf("empty hidden text must omit the span, got: %s", noHidden)
	}
}

// TestBuildMultipartMessage_EmbedsHiddenBlock verifies the export mail's nested
// alternative body also carries the hidden static text and an html part.
func TestBuildMultipartMessage_EmbedsHiddenBlock(t *testing.T) {
	att := ExportAttachment{Filename: "a.csv.gz", Data: []byte("z"), ContentType: "application/gzip"}
	raw, err := buildMultipartMessage("from@x.test", "to@y.test", "Subj", "export body", "EXPORT-HIDDEN-NOTE", []ExportAttachment{att}, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("buildMultipartMessage: %v", err)
	}
	msg := string(raw)
	if !strings.Contains(msg, "multipart/mixed") {
		t.Error("outer type must stay multipart/mixed")
	}
	if !strings.Contains(msg, "Content-Type: multipart/alternative;") {
		t.Error("nested alternative body missing")
	}
	if !strings.Contains(msg, "Content-Type: text/html; charset=UTF-8") {
		t.Error("html body part missing")
	}
	if !strings.Contains(msg, "EXPORT-HIDDEN-NOTE") {
		t.Error("hidden static text missing from export body")
	}
	if !strings.Contains(msg, hiddenSpanStyle) {
		t.Error("hidden span style missing from export body")
	}
	// Attachment survives unchanged.
	if !strings.Contains(msg, "filename=\"a.csv.gz\"") || !strings.Contains(msg, "Content-Transfer-Encoding: base64") {
		t.Error("attachment part damaged")
	}
}

// TestHiddenTextFromEnv verifies LookupEnv semantics: unset => default; set (even
// empty) => that value, so an operator can disable the block with an empty var.
func TestHiddenTextFromEnv(t *testing.T) {
	orig, had := os.LookupEnv(EmailHiddenTextEnv)
	t.Cleanup(func() {
		if had {
			os.Setenv(EmailHiddenTextEnv, orig)
		} else {
			os.Unsetenv(EmailHiddenTextEnv)
		}
	})

	os.Unsetenv(EmailHiddenTextEnv)
	if got := hiddenTextFromEnv(); got != defaultHiddenEmailText {
		t.Errorf("unset => default, got %q", got)
	}
	os.Setenv(EmailHiddenTextEnv, "custom note")
	if got := hiddenTextFromEnv(); got != "custom note" {
		t.Errorf("set => value, got %q", got)
	}
	os.Setenv(EmailHiddenTextEnv, "")
	if got := hiddenTextFromEnv(); got != "" {
		t.Errorf("set-empty => empty (disabled), got %q", got)
	}
}

// TestSendCredential_IsHTMLWithHidden confirms the end-to-end credential send now
// dispatches a multipart/alternative message containing the hidden block.
func TestSendCredential_IsHTMLWithHidden(t *testing.T) {
	var gotMsg []byte
	orig := smtpSendMail
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotMsg = msg
		return nil
	}
	t.Cleanup(func() { smtpSendMail = orig })

	s := &SMTPEmailSender{from: "sender@example.com", host: "smtp.example.com", port: "587", hiddenText: "DO-NOT-FORWARD"}
	if err := s.SendCredential("rcpt@example.com", "alice", "pw123"); err != nil {
		t.Fatalf("SendCredential: %v", err)
	}
	msg := string(gotMsg)
	if !strings.Contains(msg, "multipart/alternative") {
		t.Errorf("credential mail not HTML multipart:\n%s", msg)
	}
	if !strings.Contains(msg, "DO-NOT-FORWARD") || !strings.Contains(msg, hiddenSpanStyle) {
		t.Error("hidden block missing from credential mail")
	}
	if !strings.Contains(msg, "pw123") {
		t.Error("password missing from credential body")
	}
}
