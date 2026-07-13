package services

import (
	"net/mail"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestBuildMultipartMessage_HasBodyAndAttachment(t *testing.T) {
	att := ExportAttachment{
		Filename:    "submissions_20260712.csv.gz",
		Data:        []byte{0x1f, 0x8b, 0x00, 0x01, 0x02},
		ContentType: "application/gzip",
	}
	raw, err := buildMultipartMessage("from@x.test", "to@y.test", "Subj", "hello body", "HIDDEN-MARKER-XYZ", []ExportAttachment{att}, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("buildMultipartMessage: %v", err)
	}
	msg := string(raw)

	// Parse headers to confirm a well-formed message with a multipart boundary.
	m, err := mail.ReadMessage(strings.NewReader(msg))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	ct := m.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/mixed") {
		t.Fatalf("Content-Type = %q, want multipart/mixed", ct)
	}
	if !strings.Contains(msg, "hello body") {
		t.Fatal("body text missing")
	}
	if !strings.Contains(msg, "filename=\"submissions_20260712.csv.gz\"") {
		t.Fatal("attachment filename missing")
	}
	if !strings.Contains(msg, "Content-Transfer-Encoding: base64") {
		t.Fatal("attachment not base64-encoded")
	}
	if !strings.Contains(msg, "Content-Type: application/gzip") {
		t.Fatal("attachment content-type missing")
	}
}

func TestBuildMultipartMessage_SanitizesHeaderInjection(t *testing.T) {
	att := ExportAttachment{
		Filename:    "ok.csv.gz\r\nX-Injected-Filename: pwned",
		Data:        []byte("data"),
		ContentType: "application/gzip\r\nX-Injected-CT: pwned",
	}
	raw, err := buildMultipartMessage("from@x.test", "to@y.test", "Subj", "body", "", []ExportAttachment{att}, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("buildMultipartMessage: %v", err)
	}
	msg := string(raw)

	// CR/LF must be stripped so the payload cannot start its own header line.
	if strings.Contains(msg, "\r\nX-Injected-Filename:") {
		t.Fatal("filename CR/LF injection not sanitized")
	}
	if strings.Contains(msg, "\r\nX-Injected-CT:") {
		t.Fatal("content-type CR/LF injection not sanitized")
	}
}

func TestSendExport_DispatchesThroughSMTP(t *testing.T) {
	orig := smtpSendMail
	t.Cleanup(func() { smtpSendMail = orig })

	var gotTo []string
	var gotMsg []byte
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotTo = to
		gotMsg = msg
		return nil
	}

	s := &SMTPEmailSender{from: "from@x.test", appPassword: "pw", host: "smtp.x.test", port: "587"}
	err := s.SendExport("admin@y.test", "Export", "body", []ExportAttachment{{Filename: "a.csv.gz", Data: []byte("z"), ContentType: "application/gzip"}})
	if err != nil {
		t.Fatalf("SendExport: %v", err)
	}
	if len(gotTo) != 1 || gotTo[0] != "admin@y.test" {
		t.Fatalf("recipient = %v", gotTo)
	}
	if !strings.Contains(string(gotMsg), "multipart/mixed") {
		t.Fatal("dispatched message not multipart")
	}
}
