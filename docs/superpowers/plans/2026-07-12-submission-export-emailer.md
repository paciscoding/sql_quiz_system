# Submission-Log Export Emailer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a background service that, on a configurable interval (default 24h), emails an admin a gzip-CSV delta of exam + practice submissions since the last successful export; on by default at boot, live-toggleable from the admin UI.

**Architecture:** A `SubmissionExportService` (services package) owns a ticker loop and a single `ExportNow` operation guarded by a mutex. It reads a watermark from a new singleton table `app.export_state`, queries the delta from Postgres, builds two `.csv.gz` attachments, sends them through an attachment-capable SMTP method, and advances the watermark only on send success. An admin controller exposes get/set-enabled and run-now. The Vue AdminView gets a toggle card.

**Tech Stack:** Go 1.x, gin, pgx/v5 (`pgxpool`), net/smtp, `encoding/csv`, `compress/gzip`, `mime/multipart`; Vue 3 `<script setup>` + `fetch`.

## Global Constraints

- Backend module path: `github.com/paciscoding/sql-quiz-system`. All new backend files are `package services`, `package controllers`, `package models`, or `package repository` matching their directory.
- Repository type is `*repository.PostgresRepository` with field `pool *pgxpool.Pool`; use `r.pool.Query`/`r.pool.QueryRow`/`r.pool.Exec` with `context.Context` first arg. pgx/v5 uses `$1`-style placeholders.
- Schema DDL lives in `InitializeSystemTables` in `backend/internal/repository/postgres_schema.go`; integration tests bootstrap by calling it. New tables use `CREATE TABLE IF NOT EXISTS app.<name>`.
- All app tables are in schema `app`. `submissions.exam_id`, `submissions.student_id`, `submissions.question_id` are **nullable**; `practice_attempts.student_id` and `practice_question_id` are **NOT NULL**.
- Email: `smtpSendMail` (package var in `email_smtp.go`) is the indirection point tests replace to avoid the network. Header values must pass through `sanitizeHeaderValue`.
- HTTP routes are under `/api/v1`; staff routes use `staff.Use(controllers.AuthMiddleware(authService))` then per-route `controllers.RoleMiddleware(models.RoleAdmin)`. Role constant for admin is `models.RoleAdmin` (`"ADMIN"`).
- Frontend auth token: `localStorage.getItem('sql_quiz_token')`, sent as `Authorization: Bearer <token>`. Errors go through `handleApiError`; user feedback through `useNotifications`.
- New env vars: `SUBMISSION_EXPORT_ENABLED` (default `true`), `SUBMISSION_EXPORT_INTERVAL` (default `24h`), `EXPORT_ADMIN_EMAIL` (fallback `ADMIN_EMAIL`).
- Delta semantics: watermark `last_export_at` is the exclusive lower bound; window is `(last_export_at, now]`. Advance only on send success or confirmed-empty window. First-boot watermark = seed time (no historical back-fill).
- Run backend tests with `cd backend && go test ./...`. DB integration tests skip unless `TEST_DATABASE_URL` is set.

---

### Task 1: Attachment-capable MIME email send

Add a multipart send path to the SMTP sender so the exporter can attach gzip files. Pure `buildMultipartMessage` carries the testable logic; `SendExport` is the thin dispatcher, mirroring the existing `buildMessage`/`send` split.

**Files:**
- Modify: `backend/internal/services/email_smtp.go`
- Test: `backend/internal/services/email_smtp_attachment_test.go` (create)

**Interfaces:**
- Consumes: existing `smtpSendMail`, `sanitizeHeaderValue`, `SMTPEmailSender` fields (`from`, `appPassword`, `host`, `port`).
- Produces:
  - `type ExportAttachment struct { Filename string; Data []byte; ContentType string }`
  - `func (s *SMTPEmailSender) SendExport(to, subject, body string, attachments []ExportAttachment) error`
  - `func buildMultipartMessage(from, to, subject, body string, attachments []ExportAttachment, date time.Time) ([]byte, error)`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/services/email_smtp_attachment_test.go`:

```go
package services

import (
	"net/mail"
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
	raw, err := buildMultipartMessage("from@x.test", "to@y.test", "Subj", "hello body", []ExportAttachment{att}, time.Unix(0, 0).UTC())
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

func TestSendExport_DispatchesThroughSMTP(t *testing.T) {
	orig := smtpSendMail
	t.Cleanup(func() { smtpSendMail = orig })

	var gotTo []string
	var gotMsg []byte
	smtpSendMail = func(addr string, a smtpAuthType, from string, to []string, msg []byte) error {
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
```

> Note: `smtpAuthType` in the fake must match the type `smtp.SendMail` expects (`smtp.Auth`). Add this alias near the top of the test file if the compiler needs an explicit type:
> ```go
> type smtpAuthType = smtpAuthAlias
> ```
> — but simpler: declare the fake with the real signature directly. Replace the fake line with:
> ```go
> smtpSendMail = func(addr string, a interface{ Start(*tls.ConnectionState) (string, []byte, error) }, from string, to []string, msg []byte) error {
> ```
> is fragile. Instead, in Step 1 use the exact `smtp.Auth` type. Rewrite the fake as shown in Step 3's imports (`"net/smtp"`), i.e. `func(addr string, a smtp.Auth, from string, to []string, msg []byte) error`. Update the test import block to include `"net/smtp"` and drop the `smtpAuthType` note.

(When writing the file, use `a smtp.Auth` and import `net/smtp`; the note above is guidance, not literal code.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/services/ -run 'TestBuildMultipartMessage_HasBodyAndAttachment|TestSendExport_DispatchesThroughSMTP' -v`
Expected: FAIL — `undefined: ExportAttachment`, `undefined: buildMultipartMessage`, `s.SendExport undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `backend/internal/services/email_smtp.go` (add `encoding/base64`, `mime/multipart`, `net/textproto` to imports):

```go
// ExportAttachment is a single file attached to a multipart export email.
type ExportAttachment struct {
	Filename    string
	Data        []byte
	ContentType string
}

// SendExport emails a body plus one or more binary attachments (used by the
// submission-log exporter). If the recipient is empty the send is skipped and
// nil is returned, matching the other Send* helpers.
func (s *SMTPEmailSender) SendExport(to, subject, body string, attachments []ExportAttachment) error {
	if to == "" {
		return nil
	}
	msg, err := buildMultipartMessage(s.from, to, subject, body, attachments, time.Now())
	if err != nil {
		return err
	}
	auth := smtp.PlainAuth("", s.from, s.appPassword, s.host)
	addr := s.host + ":" + s.port
	return smtpSendMail(addr, auth, s.from, []string{to}, msg)
}

// buildMultipartMessage assembles a MIME multipart/mixed message: one text/plain
// part for the body and one base64-encoded part per attachment. Pure (no I/O)
// so it is unit-tested exhaustively. Header values are sanitized against
// header-injection, consistent with buildMessage.
func buildMultipartMessage(from, to, subject, body string, attachments []ExportAttachment, date time.Time) ([]byte, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)

	var h bytes.Buffer
	h.WriteString("From: " + sanitizeHeaderValue(from) + "\r\n")
	h.WriteString("To: " + sanitizeHeaderValue(to) + "\r\n")
	h.WriteString("Subject: " + sanitizeHeaderValue(subject) + "\r\n")
	h.WriteString("Date: " + date.Format(time.RFC1123Z) + "\r\n")
	h.WriteString("MIME-Version: 1.0\r\n")
	h.WriteString("Content-Type: multipart/mixed; boundary=" + w.Boundary() + "\r\n")
	h.WriteString("\r\n")

	// Body part.
	textHdr := make(textproto.MIMEHeader)
	textHdr.Set("Content-Type", "text/plain; charset=UTF-8")
	tp, err := w.CreatePart(textHdr)
	if err != nil {
		return nil, err
	}
	if _, err := tp.Write([]byte(body)); err != nil {
		return nil, err
	}

	// Attachment parts.
	for _, att := range attachments {
		ct := att.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah := make(textproto.MIMEHeader)
		ah.Set("Content-Type", ct)
		ah.Set("Content-Transfer-Encoding", "base64")
		ah.Set("Content-Disposition", "attachment; filename=\""+sanitizeHeaderValue(att.Filename)+"\"")
		ap, err := w.CreatePart(ah)
		if err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(att.Data)
		// Wrap base64 at 76 chars per RFC 2045.
		for i := 0; i < len(enc); i += 76 {
			end := i + 76
			if end > len(enc) {
				end = len(enc)
			}
			if _, err := ap.Write([]byte(enc[i:end] + "\r\n")); err != nil {
				return nil, err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	return append(h.Bytes(), b.Bytes()...), nil
}
```

In the test file, write the SMTP fake with the real signature and import `"net/smtp"`:

```go
smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	gotTo = to
	gotMsg = msg
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/services/ -run 'TestBuildMultipartMessage_HasBodyAndAttachment|TestSendExport_DispatchesThroughSMTP' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/email_smtp.go backend/internal/services/email_smtp_attachment_test.go
git commit -m "feat(email): multipart attachment send for exports"
```

---

### Task 2: Export models + export_state table + repo state methods

Add the model types, the singleton `app.export_state` table, and the repo methods that seed/read/toggle the flag and advance the watermark.

**Files:**
- Create: `backend/internal/models/export.go`
- Create: `backend/internal/repository/postgres_export.go`
- Modify: `backend/internal/repository/postgres_schema.go` (add table to `InitializeSystemTables`)
- Test: `backend/internal/repository/postgres_export_state_test.go` (create)

**Interfaces:**
- Consumes: `*repository.PostgresRepository` (`r.pool`), `InitializeSystemTables`.
- Produces:
  - `models.ExportState{ Enabled bool; LastExportAt time.Time; UpdatedAt time.Time }`
  - `models.SubmissionExportRow{ ID int; ExamID *int; StudentID *int; Username string; QuestionID *int; IsCorrect bool; Score float64; SubmittedAt time.Time; SQLText string; ValidationDetails string }`
  - `models.PracticeAttemptExportRow{ ID int; StudentID int; Username string; PracticeQuestionID int; IsCorrect bool; SubmittedAt time.Time; SQLText string; ValidationDetails string }`
  - `func (r *PostgresRepository) EnsureExportState(ctx context.Context, defaultEnabled bool) error`
  - `func (r *PostgresRepository) GetExportState(ctx context.Context) (models.ExportState, error)`
  - `func (r *PostgresRepository) SetExportEnabled(ctx context.Context, enabled bool) error`
  - `func (r *PostgresRepository) AdvanceExportWatermark(ctx context.Context, t time.Time) error`

- [ ] **Step 1: Write the model file (no test yet — plain types)**

Create `backend/internal/models/export.go`:

```go
package models

import "time"

// ExportState is the singleton row driving the submission-log exporter.
type ExportState struct {
	Enabled      bool      `json:"enabled"`
	LastExportAt time.Time `json:"last_export_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SubmissionExportRow is one exam submission row rendered into the export CSV.
// The FK columns are nullable in the schema, so they are pointers.
type SubmissionExportRow struct {
	ID                int
	ExamID            *int
	StudentID         *int
	Username          string
	QuestionID        *int
	IsCorrect         bool
	Score             float64
	SubmittedAt       time.Time
	SQLText           string
	ValidationDetails string
}

// PracticeAttemptExportRow is one practice attempt rendered into the export CSV.
type PracticeAttemptExportRow struct {
	ID                 int
	StudentID          int
	Username           string
	PracticeQuestionID int
	IsCorrect          bool
	SubmittedAt        time.Time
	SQLText            string
	ValidationDetails  string
}
```

- [ ] **Step 2: Add the table to the schema migrations**

In `backend/internal/repository/postgres_schema.go`, inside `InitializeSystemTables`, add this statement to the same DDL list that contains the other `CREATE TABLE IF NOT EXISTS app.*` entries (place it after the `app.password_events` entry):

```go
`CREATE TABLE IF NOT EXISTS app.export_state (
	id             INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
	enabled        BOOLEAN NOT NULL,
	last_export_at TIMESTAMPTZ NOT NULL,
	updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);`,
```

- [ ] **Step 3: Write the failing test**

Create `backend/internal/repository/postgres_export_state_test.go`:

```go
package repository

import (
	"context"
	"testing"
	"time"
)

func TestExportState_EnsureGetSetAdvance(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// Reset to a known state for a deterministic test.
	if _, err := repo.pool.Exec(ctx, `DELETE FROM app.export_state`); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Ensure seeds exactly once with the given default.
	if err := repo.EnsureExportState(ctx, true); err != nil {
		t.Fatalf("EnsureExportState: %v", err)
	}
	// Second ensure with a different default must NOT overwrite.
	if err := repo.EnsureExportState(ctx, false); err != nil {
		t.Fatalf("EnsureExportState 2: %v", err)
	}

	st, err := repo.GetExportState(ctx)
	if err != nil {
		t.Fatalf("GetExportState: %v", err)
	}
	if !st.Enabled {
		t.Fatal("enabled should still be true after second ensure (no overwrite)")
	}

	if err := repo.SetExportEnabled(ctx, false); err != nil {
		t.Fatalf("SetExportEnabled: %v", err)
	}
	st, _ = repo.GetExportState(ctx)
	if st.Enabled {
		t.Fatal("enabled should be false after SetExportEnabled(false)")
	}

	mark := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if err := repo.AdvanceExportWatermark(ctx, mark); err != nil {
		t.Fatalf("AdvanceExportWatermark: %v", err)
	}
	st, _ = repo.GetExportState(ctx)
	if !st.LastExportAt.Equal(mark) {
		t.Fatalf("watermark = %v, want %v", st.LastExportAt, mark)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./internal/repository/ -run TestExportState_EnsureGetSetAdvance -v`
Expected: FAIL — `repo.EnsureExportState undefined` (or, if `TEST_DATABASE_URL` unset, SKIP; set it to actually exercise this task).

- [ ] **Step 5: Write minimal implementation**

Create `backend/internal/repository/postgres_export.go`:

```go
package repository

import (
	"context"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// EnsureExportState seeds the singleton export_state row on first boot. It is
// idempotent: once the row exists, the defaultEnabled argument is ignored, so a
// later admin UI toggle is never clobbered by the env-derived default.
func (r *PostgresRepository) EnsureExportState(ctx context.Context, defaultEnabled bool) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO app.export_state (id, enabled, last_export_at, updated_at)
		 VALUES (1, $1, now(), now())
		 ON CONFLICT (id) DO NOTHING`,
		defaultEnabled)
	return err
}

// GetExportState reads the singleton row.
func (r *PostgresRepository) GetExportState(ctx context.Context) (models.ExportState, error) {
	var st models.ExportState
	err := r.pool.QueryRow(ctx,
		`SELECT enabled, last_export_at, updated_at FROM app.export_state WHERE id = 1`).
		Scan(&st.Enabled, &st.LastExportAt, &st.UpdatedAt)
	return st, err
}

// SetExportEnabled flips the admin toggle and bumps updated_at.
func (r *PostgresRepository) SetExportEnabled(ctx context.Context, enabled bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.export_state SET enabled = $1, updated_at = now() WHERE id = 1`,
		enabled)
	return err
}

// AdvanceExportWatermark moves last_export_at forward after a successful (or
// confirmed-empty) export cycle.
func (r *PostgresRepository) AdvanceExportWatermark(ctx context.Context, t time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE app.export_state SET last_export_at = $1, updated_at = now() WHERE id = 1`,
		t)
	return err
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd backend && go test ./internal/repository/ -run TestExportState_EnsureGetSetAdvance -v`
Expected: PASS (with `TEST_DATABASE_URL` set) or SKIP (unset). Also run `cd backend && go build ./...` to confirm the models + schema compile.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/models/export.go backend/internal/repository/postgres_export.go backend/internal/repository/postgres_schema.go backend/internal/repository/postgres_export_state_test.go
git commit -m "feat(repo): export_state table + state read/write methods"
```

---

### Task 3: Delta query methods

Add the two repo methods that select the submission and practice-attempt rows in the export window, joined to usernames.

**Files:**
- Modify: `backend/internal/repository/postgres_export.go`
- Test: `backend/internal/repository/postgres_export_query_test.go` (create)

**Interfaces:**
- Consumes: `models.SubmissionExportRow`, `models.PracticeAttemptExportRow`, `r.pool`.
- Produces:
  - `func (r *PostgresRepository) ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error)`
  - `func (r *PostgresRepository) ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error)`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/postgres_export_query_test.go`:

```go
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func TestExportSubmissions_WindowFilter(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsername("exp_sub_stu"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	// One row inside the window, one before it.
	inWin := base
	before := base.Add(-2 * time.Hour)
	for _, at := range []time.Time{inWin, before} {
		if _, err := repo.pool.Exec(ctx,
			`INSERT INTO app.submissions (student_id, sql_text, is_correct, score, submitted_at)
			 VALUES ($1, $2, true, 1.0, $3)`,
			uid, "SELECT 1", at); err != nil {
			t.Fatalf("insert submission: %v", err)
		}
	}

	from := base.Add(-1 * time.Hour)
	to := base.Add(1 * time.Hour)
	rows, err := repo.ExportSubmissions(ctx, from, to)
	if err != nil {
		t.Fatalf("ExportSubmissions: %v", err)
	}

	// Only the in-window row for this student should appear. Filter to our user
	// so a shared test DB with other rows stays deterministic.
	count := 0
	for _, r := range rows {
		if r.StudentID != nil && *r.StudentID == uid {
			count++
			if r.Username == "" {
				t.Fatal("username not joined")
			}
		}
	}
	if count != 1 {
		t.Fatalf("in-window rows for user = %d, want 1", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/repository/ -run TestExportSubmissions_WindowFilter -v`
Expected: FAIL — `repo.ExportSubmissions undefined` (or SKIP if no `TEST_DATABASE_URL`).

- [ ] **Step 3: Write minimal implementation**

Append to `backend/internal/repository/postgres_export.go`:

```go
// ExportSubmissions returns exam submissions with submitted_at in (from, to],
// joined to the student's username. FK columns are nullable, so they scan into
// pointers; validation_details is emitted as its raw JSON text.
func (r *PostgresRepository) ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT s.id, s.exam_id, s.student_id, COALESCE(u.username, ''), s.question_id,
		        s.is_correct, s.score, s.submitted_at, s.sql_text,
		        COALESCE(s.validation_details::text, '')
		 FROM app.submissions s
		 LEFT JOIN app.system_users u ON u.id = s.student_id
		 WHERE s.submitted_at > $1 AND s.submitted_at <= $2
		 ORDER BY s.submitted_at ASC, s.id ASC`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.SubmissionExportRow
	for rows.Next() {
		var r models.SubmissionExportRow
		if err := rows.Scan(&r.ID, &r.ExamID, &r.StudentID, &r.Username, &r.QuestionID,
			&r.IsCorrect, &r.Score, &r.SubmittedAt, &r.SQLText, &r.ValidationDetails); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExportPracticeAttempts returns practice attempts with submitted_at in
// (from, to], joined to the student's username.
func (r *PostgresRepository) ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT a.id, a.student_id, COALESCE(u.username, ''), a.practice_question_id,
		        a.is_correct, a.submitted_at, a.sql_text,
		        COALESCE(a.validation_details::text, '')
		 FROM app.practice_attempts a
		 LEFT JOIN app.system_users u ON u.id = a.student_id
		 WHERE a.submitted_at > $1 AND a.submitted_at <= $2
		 ORDER BY a.submitted_at ASC, a.id ASC`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.PracticeAttemptExportRow
	for rows.Next() {
		var r models.PracticeAttemptExportRow
		if err := rows.Scan(&r.ID, &r.StudentID, &r.Username, &r.PracticeQuestionID,
			&r.IsCorrect, &r.SubmittedAt, &r.SQLText, &r.ValidationDetails); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/repository/ -run TestExportSubmissions_WindowFilter -v`
Expected: PASS (or SKIP without a DB).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_export.go backend/internal/repository/postgres_export_query_test.go
git commit -m "feat(repo): windowed delta queries for submission export"
```

---

### Task 4: Export service — CSV/gzip builders + interval parsing

Build the pure CSV/gzip encoders and the interval parser. These have no I/O and are the most bug-prone logic, so they are isolated and tested first, before the orchestration in Task 5.

**Files:**
- Create: `backend/internal/services/submission_export.go`
- Test: `backend/internal/services/submission_export_csv_test.go` (create)

**Interfaces:**
- Consumes: `models.SubmissionExportRow`, `models.PracticeAttemptExportRow`.
- Produces:
  - `func buildSubmissionsCSVGz(rows []models.SubmissionExportRow) ([]byte, error)`
  - `func buildPracticeAttemptsCSVGz(rows []models.PracticeAttemptExportRow) ([]byte, error)`
  - `func ParseExportInterval(raw string) time.Duration` (empty/invalid → 24h)
  - const `ExportEnabledEnv = "SUBMISSION_EXPORT_ENABLED"`, `ExportIntervalEnv = "SUBMISSION_EXPORT_INTERVAL"`, `ExportRecipientEnv = "EXPORT_ADMIN_EMAIL"`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/services/submission_export_csv_test.go`:

```go
package services

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"io"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func gunzipCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gz: %v", err)
	}
	recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	return recs
}

func TestBuildSubmissionsCSVGz_HeaderAndNullFK(t *testing.T) {
	eid, sid, qid := 3, 7, 9
	rows := []models.SubmissionExportRow{
		{ID: 1, ExamID: &eid, StudentID: &sid, Username: "alice", QuestionID: &qid,
			IsCorrect: true, Score: 2.5, SubmittedAt: time.Unix(0, 0).UTC(),
			SQLText: "SELECT 1, 2", ValidationDetails: `{"ok":true}`},
		{ID: 2, ExamID: nil, StudentID: nil, Username: "", QuestionID: nil,
			IsCorrect: false, Score: 0, SubmittedAt: time.Unix(0, 0).UTC(),
			SQLText: "line1\nline2", ValidationDetails: ""},
	}
	gz, err := buildSubmissionsCSVGz(rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	recs := gunzipCSV(t, gz)
	if recs[0][0] != "id" || recs[0][3] != "username" {
		t.Fatalf("header wrong: %v", recs[0])
	}
	if len(recs) != 3 { // header + 2 rows
		t.Fatalf("row count = %d, want 3", len(recs))
	}
	// Null FK row renders empty strings, not "0".
	if recs[2][1] != "" || recs[2][2] != "" {
		t.Fatalf("null FK not empty: %v", recs[2])
	}
	// Embedded comma + newline survive CSV quoting.
	if recs[1][8] != "SELECT 1, 2" {
		t.Fatalf("sql_text mangled: %q", recs[1][8])
	}
	if recs[2][8] != "line1\nline2" {
		t.Fatalf("multiline sql_text mangled: %q", recs[2][8])
	}
}

func TestParseExportInterval(t *testing.T) {
	if ParseExportInterval("") != 24*time.Hour {
		t.Fatal("empty should default 24h")
	}
	if ParseExportInterval("garbage") != 24*time.Hour {
		t.Fatal("invalid should default 24h")
	}
	if ParseExportInterval("15m") != 15*time.Minute {
		t.Fatal("valid duration not parsed")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/services/ -run 'TestBuildSubmissionsCSVGz_HeaderAndNullFK|TestParseExportInterval' -v`
Expected: FAIL — `undefined: buildSubmissionsCSVGz`, `undefined: ParseExportInterval`.

- [ ] **Step 3: Write minimal implementation**

Create `backend/internal/services/submission_export.go` (builders + helpers only for now; orchestration added in Task 5):

```go
package services

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"strconv"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// Env var names for the exporter.
const (
	ExportEnabledEnv   = "SUBMISSION_EXPORT_ENABLED"
	ExportIntervalEnv  = "SUBMISSION_EXPORT_INTERVAL"
	ExportRecipientEnv = "EXPORT_ADMIN_EMAIL"

	defaultExportInterval = 24 * time.Hour
)

// ParseExportInterval parses a Go duration; empty or invalid input falls back
// to 24h.
func ParseExportInterval(raw string) time.Duration {
	if raw == "" {
		return defaultExportInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return defaultExportInterval
	}
	return d
}

func intPtrToStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildSubmissionsCSVGz(rows []models.SubmissionExportRow) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "exam_id", "student_id", "username", "question_id",
		"is_correct", "score", "submitted_at", "sql_text", "validation_details"})
	for _, r := range rows {
		_ = w.Write([]string{
			strconv.Itoa(r.ID),
			intPtrToStr(r.ExamID),
			intPtrToStr(r.StudentID),
			r.Username,
			intPtrToStr(r.QuestionID),
			strconv.FormatBool(r.IsCorrect),
			strconv.FormatFloat(r.Score, 'f', -1, 64),
			r.SubmittedAt.Format(time.RFC3339),
			r.SQLText,
			r.ValidationDetails,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return gzipBytes(buf.Bytes())
}

func buildPracticeAttemptsCSVGz(rows []models.PracticeAttemptExportRow) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "student_id", "username", "practice_question_id",
		"is_correct", "submitted_at", "sql_text", "validation_details"})
	for _, r := range rows {
		_ = w.Write([]string{
			strconv.Itoa(r.ID),
			strconv.Itoa(r.StudentID),
			r.Username,
			strconv.Itoa(r.PracticeQuestionID),
			strconv.FormatBool(r.IsCorrect),
			r.SubmittedAt.Format(time.RFC3339),
			r.SQLText,
			r.ValidationDetails,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return gzipBytes(buf.Bytes())
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/services/ -run 'TestBuildSubmissionsCSVGz_HeaderAndNullFK|TestParseExportInterval' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/submission_export.go backend/internal/services/submission_export_csv_test.go
git commit -m "feat(export): CSV/gzip builders + interval parsing"
```

---

### Task 5: Export service — ExportNow orchestration + ticker

Add the stateful service: dependencies, the mutex-guarded `ExportNow` cycle with its watermark rules, and the `Run` ticker loop. Tested against fake repo + fake sender covering all four outcome branches.

**Files:**
- Modify: `backend/internal/services/submission_export.go`
- Test: `backend/internal/services/submission_export_test.go` (create)

**Interfaces:**
- Consumes: `buildSubmissionsCSVGz`, `buildPracticeAttemptsCSVGz`, `ExportAttachment` (Task 1), `models.ExportState`, `models.SubmissionExportRow`, `models.PracticeAttemptExportRow`.
- Produces:
  - `type ExportConfig struct { Interval time.Duration; Recipient string }`
  - `type ExportResult struct { Enabled bool; WindowStart, WindowEnd time.Time; SubmissionCount, PracticeCount int; Sent bool }`
  - interfaces `exportRepo` and `exportSender` (below)
  - `func NewSubmissionExportService(repo exportRepo, sender exportSender, cfg ExportConfig) *SubmissionExportService`
  - `func (s *SubmissionExportService) ExportNow(ctx context.Context) (ExportResult, error)`
  - `func (s *SubmissionExportService) Run(ctx context.Context)`
  - `func (s *SubmissionExportService) Interval() time.Duration`
  - `func (s *SubmissionExportService) Recipient() string`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/services/submission_export_test.go`:

```go
package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

type fakeExportRepo struct {
	mu        sync.Mutex
	state     models.ExportState
	subs      []models.SubmissionExportRow
	pracs     []models.PracticeAttemptExportRow
	advanced  []time.Time
	failQuery bool
}

func (f *fakeExportRepo) GetExportState(ctx context.Context) (models.ExportState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}
func (f *fakeExportRepo) AdvanceExportWatermark(ctx context.Context, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.advanced = append(f.advanced, t)
	f.state.LastExportAt = t
	return nil
}
func (f *fakeExportRepo) ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error) {
	if f.failQuery {
		return nil, errors.New("boom")
	}
	return f.subs, nil
}
func (f *fakeExportRepo) ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error) {
	if f.failQuery {
		return nil, errors.New("boom")
	}
	return f.pracs, nil
}

type fakeExportSender struct {
	mu       sync.Mutex
	calls    int
	lastAtts []ExportAttachment
	failSend bool
}

func (f *fakeExportSender) SendExport(to, subject, body string, attachments []ExportAttachment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSend {
		return errors.New("send failed")
	}
	f.calls++
	f.lastAtts = attachments
	return nil
}

func newSvc(repo *fakeExportRepo, sender *fakeExportSender) *SubmissionExportService {
	s := NewSubmissionExportService(repo, sender, ExportConfig{Interval: time.Hour, Recipient: "admin@x.test"})
	s.now = func() time.Time { return time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestExportNow_SendsAndAdvancesOnData(t *testing.T) {
	sid := 5
	repo := &fakeExportRepo{
		state: models.ExportState{Enabled: true, LastExportAt: time.Unix(0, 0).UTC()},
		subs:  []models.SubmissionExportRow{{ID: 1, StudentID: &sid, Username: "a", SubmittedAt: time.Unix(1, 0).UTC()}},
	}
	sender := &fakeExportSender{}
	res, err := newSvc(repo, sender).ExportNow(context.Background())
	if err != nil {
		t.Fatalf("ExportNow: %v", err)
	}
	if !res.Sent || sender.calls != 1 {
		t.Fatalf("expected one send, got calls=%d sent=%v", sender.calls, res.Sent)
	}
	if len(sender.lastAtts) != 2 {
		t.Fatalf("attachments = %d, want 2", len(sender.lastAtts))
	}
	if len(repo.advanced) != 1 {
		t.Fatalf("watermark advanced %d times, want 1", len(repo.advanced))
	}
}

func TestExportNow_EmptyWindowAdvancesNoSend(t *testing.T) {
	repo := &fakeExportRepo{state: models.ExportState{Enabled: true}}
	sender := &fakeExportSender{}
	res, err := newSvc(repo, sender).ExportNow(context.Background())
	if err != nil {
		t.Fatalf("ExportNow: %v", err)
	}
	if res.Sent || sender.calls != 0 {
		t.Fatal("must not send on empty window")
	}
	if len(repo.advanced) != 1 {
		t.Fatal("empty window must still advance watermark")
	}
}

func TestExportNow_DisabledIsNoOp(t *testing.T) {
	repo := &fakeExportRepo{state: models.ExportState{Enabled: false}}
	sender := &fakeExportSender{}
	res, _ := newSvc(repo, sender).ExportNow(context.Background())
	if res.Enabled || sender.calls != 0 || len(repo.advanced) != 0 {
		t.Fatal("disabled must be a pure no-op")
	}
}

func TestExportNow_SendFailureHoldsWatermark(t *testing.T) {
	sid := 5
	repo := &fakeExportRepo{
		state: models.ExportState{Enabled: true},
		subs:  []models.SubmissionExportRow{{ID: 1, StudentID: &sid, SubmittedAt: time.Unix(1, 0).UTC()}},
	}
	sender := &fakeExportSender{failSend: true}
	_, err := newSvc(repo, sender).ExportNow(context.Background())
	if err == nil {
		t.Fatal("expected send error")
	}
	if len(repo.advanced) != 0 {
		t.Fatal("watermark must NOT advance on send failure")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/services/ -run 'TestExportNow_' -v`
Expected: FAIL — `undefined: NewSubmissionExportService`, `undefined: SubmissionExportService`, `s.now` field missing.

- [ ] **Step 3: Write minimal implementation**

Append to `backend/internal/services/submission_export.go` (add `context`, `fmt`, `log`, `sync` to the import block):

```go
// exportRepo is the minimal repository surface the exporter needs (satisfied by
// *repository.PostgresRepository). Defined here so the service is unit-testable
// with a fake.
type exportRepo interface {
	GetExportState(ctx context.Context) (models.ExportState, error)
	AdvanceExportWatermark(ctx context.Context, t time.Time) error
	ExportSubmissions(ctx context.Context, from, to time.Time) ([]models.SubmissionExportRow, error)
	ExportPracticeAttempts(ctx context.Context, from, to time.Time) ([]models.PracticeAttemptExportRow, error)
}

// exportSender is the email surface the exporter needs (satisfied by
// *SMTPEmailSender).
type exportSender interface {
	SendExport(to, subject, body string, attachments []ExportAttachment) error
}

// ExportConfig holds the runtime knobs resolved from the environment.
type ExportConfig struct {
	Interval  time.Duration
	Recipient string
}

// ExportResult reports the outcome of one export cycle (returned by the run-now
// endpoint).
type ExportResult struct {
	Enabled         bool      `json:"enabled"`
	WindowStart     time.Time `json:"window_start"`
	WindowEnd       time.Time `json:"window_end"`
	SubmissionCount int       `json:"submission_count"`
	PracticeCount   int       `json:"practice_count"`
	Sent            bool      `json:"sent"`
}

// SubmissionExportService packages the submission delta into gzip CSVs and emails
// them on an interval. ExportNow is serialized by a mutex so the ticker and a
// manual run-now cannot overlap.
type SubmissionExportService struct {
	repo   exportRepo
	sender exportSender
	cfg    ExportConfig
	mu     sync.Mutex
	now    func() time.Time
}

func NewSubmissionExportService(repo exportRepo, sender exportSender, cfg ExportConfig) *SubmissionExportService {
	return &SubmissionExportService{repo: repo, sender: sender, cfg: cfg, now: time.Now}
}

func (s *SubmissionExportService) Interval() time.Duration { return s.cfg.Interval }
func (s *SubmissionExportService) Recipient() string       { return s.cfg.Recipient }

// ExportNow runs one export cycle. See the watermark rules in the design spec:
// advance only on send success or a confirmed-empty window; hold on send failure.
func (s *SubmissionExportService) ExportNow(ctx context.Context) (ExportResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.repo.GetExportState(ctx)
	if err != nil {
		return ExportResult{}, err
	}
	if !st.Enabled {
		return ExportResult{Enabled: false}, nil
	}

	start := st.LastExportAt
	end := s.now()
	res := ExportResult{Enabled: true, WindowStart: start, WindowEnd: end}

	subs, err := s.repo.ExportSubmissions(ctx, start, end)
	if err != nil {
		return res, err
	}
	pracs, err := s.repo.ExportPracticeAttempts(ctx, start, end)
	if err != nil {
		return res, err
	}
	res.SubmissionCount = len(subs)
	res.PracticeCount = len(pracs)

	if len(subs) == 0 && len(pracs) == 0 {
		if err := s.repo.AdvanceExportWatermark(ctx, end); err != nil {
			return res, err
		}
		return res, nil
	}

	stamp := end.UTC().Format("20060102")
	subGz, err := buildSubmissionsCSVGz(subs)
	if err != nil {
		return res, err
	}
	pracGz, err := buildPracticeAttemptsCSVGz(pracs)
	if err != nil {
		return res, err
	}
	atts := []ExportAttachment{
		{Filename: "submissions_" + stamp + ".csv.gz", Data: subGz, ContentType: "application/gzip"},
		{Filename: "practice_attempts_" + stamp + ".csv.gz", Data: pracGz, ContentType: "application/gzip"},
	}
	subject := "SQL Quiz System: submission log " + stamp
	body := fmt.Sprintf(
		"Submission-log export.\n\nWindow: %s  ->  %s\nExam submissions: %d\nPractice attempts: %d\n\nTwo gzip CSV files are attached.\n",
		start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), len(subs), len(pracs))

	if err := s.sender.SendExport(s.cfg.Recipient, subject, body, atts); err != nil {
		return res, err // watermark intentionally NOT advanced
	}
	if err := s.repo.AdvanceExportWatermark(ctx, end); err != nil {
		return res, err
	}
	res.Sent = true
	return res, nil
}

// Run drives the export ticker until ctx is cancelled. ExportNow errors are
// logged and swallowed so the loop survives transient failures.
func (s *SubmissionExportService) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	log.Printf("submission-export: started (interval=%s recipient=%s)", s.cfg.Interval, s.cfg.Recipient)
	for {
		select {
		case <-ctx.Done():
			log.Printf("submission-export: stopped")
			return
		case <-t.C:
			res, err := s.ExportNow(ctx)
			switch {
			case err != nil:
				log.Printf("submission-export: cycle error: %v", err)
			case res.Sent:
				log.Printf("submission-export: sent (submissions=%d practice=%d)", res.SubmissionCount, res.PracticeCount)
			case res.Enabled:
				log.Printf("submission-export: nothing to send (empty window)")
			}
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/services/ -run 'TestExportNow_' -v`
Expected: PASS (all four branches).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/services/submission_export.go backend/internal/services/submission_export_test.go
git commit -m "feat(export): ExportNow cycle + ticker with watermark rules"
```

---

### Task 6: HTTP controller + main wiring

Expose admin endpoints and start the goroutine at boot. Because the wiring lives in `main.go` (no unit harness), verification is a build + a route-registration smoke check.

**Files:**
- Create: `backend/internal/controllers/export_controller.go`
- Modify: `backend/cmd/main.go`
- Test: `backend/internal/controllers/export_controller_test.go` (create)

**Interfaces:**
- Consumes: `*repository.PostgresRepository` (`GetExportState`, `SetExportEnabled`), `*services.SubmissionExportService` (`ExportNow`, `Interval`, `Recipient`), `models.RoleAdmin`.
- Produces:
  - `type ExportController struct { ... }`
  - `func NewExportController(repo *repository.PostgresRepository, svc *services.SubmissionExportService) *ExportController`
  - handlers `GetSettings`, `UpdateSettings`, `RunNow` (gin.HandlerFunc-shaped methods)

- [ ] **Step 1: Write the failing test (handler-level, no DB)**

Create `backend/internal/controllers/export_controller_test.go`:

```go
package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUpdateSettings_RejectsBadBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &ExportController{} // handlers that fail before touching deps on bad input
	r := gin.New()
	r.PUT("/settings", c.UpdateSettings)

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"enabled":"notabool"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["error"]; !ok {
		t.Fatal("expected error field")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/controllers/ -run TestUpdateSettings_RejectsBadBody -v`
Expected: FAIL — `undefined: ExportController`.

- [ ] **Step 3: Write minimal implementation**

Create `backend/internal/controllers/export_controller.go`:

```go
package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// ExportController exposes admin controls for the submission-log exporter.
type ExportController struct {
	repo *repository.PostgresRepository
	svc  *services.SubmissionExportService
}

func NewExportController(repo *repository.PostgresRepository, svc *services.SubmissionExportService) *ExportController {
	return &ExportController{repo: repo, svc: svc}
}

type exportSettingsResponse struct {
	Enabled      bool   `json:"enabled"`
	LastExportAt string `json:"last_export_at"`
	Interval     string `json:"interval"`
	Recipient    string `json:"recipient"`
}

// GetSettings returns the current toggle state and static config.
func (c *ExportController) GetSettings(ctx *gin.Context) {
	st, err := c.repo.GetExportState(ctx.Request.Context())
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read export state"})
		return
	}
	ctx.JSON(http.StatusOK, exportSettingsResponse{
		Enabled:      st.Enabled,
		LastExportAt: st.LastExportAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Interval:     c.svc.Interval().String(),
		Recipient:    c.svc.Recipient(),
	})
}

type updateSettingsRequest struct {
	Enabled *bool `json:"enabled"`
}

// UpdateSettings flips the enabled toggle.
func (c *ExportController) UpdateSettings(ctx *gin.Context) {
	var req updateSettingsRequest
	if err := ctx.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "body must be {\"enabled\": bool}"})
		return
	}
	if err := c.repo.SetExportEnabled(ctx.Request.Context(), *req.Enabled); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update export state"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"enabled": *req.Enabled})
}

// RunNow triggers an immediate export cycle (verification aid).
func (c *ExportController) RunNow(ctx *gin.Context) {
	res, err := c.svc.ExportNow(ctx.Request.Context())
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": res})
		return
	}
	ctx.JSON(http.StatusOK, res)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/controllers/ -run TestUpdateSettings_RejectsBadBody -v`
Expected: PASS.

- [ ] **Step 5: Wire into `main.go`**

In `backend/cmd/main.go`, inside the `if senderAddr := os.Getenv(services.EmailSenderAddressEnv); senderAddr != "" {` block (right after `authService.SetEmailSender(smtpSender)` and its log line), add the exporter construction and goroutine. First resolve config:

```go
		// Submission-log exporter: seed state, then run the interval loop.
		exportRecipient := os.Getenv(services.ExportRecipientEnv)
		if exportRecipient == "" {
			exportRecipient = os.Getenv("ADMIN_EMAIL")
		}
		exportEnabledDefault := os.Getenv(services.ExportEnabledEnv) != "false" // default true
		if err := repo.EnsureExportState(ctx, exportEnabledDefault); err != nil {
			log.Printf("submission-export: seed state failed: %v", err)
		}
		exportCfg := services.ExportConfig{
			Interval:  services.ParseExportInterval(os.Getenv(services.ExportIntervalEnv)),
			Recipient: exportRecipient,
		}
		exportSvc := services.NewSubmissionExportService(repo, smtpSender, exportCfg)
		go exportSvc.Run(ctx)
		exportCtrl := controllers.NewExportController(repo, exportSvc)
```

Declare `exportCtrl` at a scope visible to route registration. Since it is created only when SMTP is active, declare `var exportCtrl *controllers.ExportController` **before** the `if senderAddr` block, and assign inside (drop the `:=` to `=` for that line). Then in the `staff` route group add:

```go
			staff.GET("/export/settings", controllers.RoleMiddleware(models.RoleAdmin), func(c *gin.Context) {
				if exportCtrl == nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email/export not configured"})
					return
				}
				exportCtrl.GetSettings(c)
			})
			staff.PUT("/export/settings", controllers.RoleMiddleware(models.RoleAdmin), func(c *gin.Context) {
				if exportCtrl == nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email/export not configured"})
					return
				}
				exportCtrl.UpdateSettings(c)
			})
			staff.POST("/export/run-now", controllers.RoleMiddleware(models.RoleAdmin), func(c *gin.Context) {
				if exportCtrl == nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "email/export not configured"})
					return
				}
				exportCtrl.RunNow(c)
			})
```

Ensure `net/http` and `github.com/gin-gonic/gin` are imported in `main.go` (gin almost certainly already is; add `net/http` if missing).

- [ ] **Step 6: Verify build + route smoke**

Run: `cd backend && go build ./... && go vet ./cmd/...`
Expected: no errors.
Run: `cd backend && go test ./... 2>&1 | tail -20`
Expected: all packages PASS or SKIP (no failures).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/controllers/export_controller.go backend/internal/controllers/export_controller_test.go backend/cmd/main.go
git commit -m "feat(export): admin endpoints + boot wiring"
```

---

### Task 7: Integration test — end-to-end delta over real Postgres

Prove the service against a real DB: seed submissions, run `ExportNow` with a capturing sender, assert two attachments and watermark advance, then a second run sends nothing.

**Files:**
- Create: `backend/internal/services/submission_export_integration_test.go`

**Interfaces:**
- Consumes: `*repository.PostgresRepository` (real), `SubmissionExportService`, `ExportAttachment`.

- [ ] **Step 1: Write the test**

Create `backend/internal/services/submission_export_integration_test.go`:

```go
package services

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
	"github.com/paciscoding/sql-quiz-system/internal/repository"
)

type capExportSender struct {
	mu    sync.Mutex
	calls int
	atts  []ExportAttachment
}

func (c *capExportSender) SendExport(to, subject, body string, attachments []ExportAttachment) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.atts = attachments
	return nil
}

func TestSubmissionExport_Integration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	repo, err := repository.NewPostgresRepository(dsn)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	ctx := context.Background()
	if err := repo.CreateRestrictedUser(ctx); err != nil {
		t.Fatalf("CreateRestrictedUser: %v", err)
	}
	if err := repo.InitializeSystemTables(ctx); err != nil {
		t.Fatalf("InitializeSystemTables: %v", err)
	}

	// Fresh watermark well before any seeded rows.
	if err := repo.EnsureExportState(ctx, true); err != nil {
		t.Fatalf("EnsureExportState: %v", err)
	}
	if err := repo.AdvanceExportWatermark(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("AdvanceExportWatermark: %v", err)
	}
	if err := repo.SetExportEnabled(ctx, true); err != nil {
		t.Fatalf("SetExportEnabled: %v", err)
	}

	uid, err := repo.CreateUser(ctx, models.User{
		Username: uniqueUsernameSvc("exp_int_stu"),
		Password: "hash",
		Role:     models.RoleStudent,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := repo.Pool().Exec(ctx,
		`INSERT INTO app.submissions (student_id, sql_text, is_correct, score, submitted_at)
		 VALUES ($1, 'SELECT 1', true, 1.0, now())`, uid); err != nil {
		t.Fatalf("seed submission: %v", err)
	}

	sender := &capExportSender{}
	svc := NewSubmissionExportService(repo, sender, ExportConfig{Interval: time.Hour, Recipient: "admin@x.test"})

	res, err := svc.ExportNow(ctx)
	if err != nil {
		t.Fatalf("ExportNow: %v", err)
	}
	if !res.Sent || sender.calls != 1 || len(sender.atts) != 2 {
		t.Fatalf("first run: sent=%v calls=%d atts=%d", res.Sent, sender.calls, len(sender.atts))
	}

	// Second immediate run: watermark advanced past the seeded row → nothing sent.
	res2, err := svc.ExportNow(ctx)
	if err != nil {
		t.Fatalf("ExportNow 2: %v", err)
	}
	if res2.Sent || sender.calls != 1 {
		t.Fatalf("second run should send nothing: sent=%v calls=%d", res2.Sent, sender.calls)
	}
}

func uniqueUsernameSvc(prefix string) string {
	return prefix + "_" + time.Now().Format("20060102150405.000000000")
}
```

> Note: this test connects its own repo rather than reusing the repository package's `openTestRepo` (that helper is in package `repository`, unexported). The `services` package integration tests already follow this self-connect pattern.

- [ ] **Step 2: Run the test**

Run: `cd backend && go test ./internal/services/ -run TestSubmissionExport_Integration -v`
Expected: PASS with `TEST_DATABASE_URL` set; SKIP without it.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/services/submission_export_integration_test.go
git commit -m "test(export): real-Postgres end-to-end delta export"
```

---

### Task 8: Admin UI toggle card

Add a card to AdminView: toggle switch (PUT on change), read-only status, and a Run-now button.

**Files:**
- Modify: `frontend/src/components/AdminView.vue`

**Interfaces:**
- Consumes: backend routes `GET/PUT /api/v1/export/settings`, `POST /api/v1/export/run-now`; `handleApiError`, `useNotifications` (already imported in AdminView).

- [ ] **Step 1: Add state + fetch/update/run functions in `<script setup>`**

In `frontend/src/components/AdminView.vue`, after the existing `fetchUsers` definition (near line 136), add:

```ts
// --- Submission-log export controls ---
const exportSettings = ref<{
  enabled: boolean
  last_export_at: string
  interval: string
  recipient: string
} | null>(null)
const exportBusy = ref(false)
const { notify } = useNotifications()

const authHeader = () => ({ Authorization: `Bearer ${localStorage.getItem('sql_quiz_token')}` })

const fetchExportSettings = async () => {
  try {
    const res = await fetch('/api/v1/export/settings', { headers: authHeader() })
    if (res.status === 503) { exportSettings.value = null; return } // email/export not configured
    if (!res.ok) throw new Error('Failed to load export settings')
    exportSettings.value = await res.json()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load export settings' })
  }
}

const toggleExport = async () => {
  if (!exportSettings.value) return
  const next = !exportSettings.value.enabled
  exportBusy.value = true
  try {
    const res = await fetch('/api/v1/export/settings', {
      method: 'PUT',
      headers: { ...authHeader(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: next }),
    })
    if (!res.ok) throw new Error('Failed to update export toggle')
    exportSettings.value.enabled = next
    notify({ type: 'success', message: `Submission export ${next ? 'enabled' : 'disabled'}.` })
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to update export toggle' })
  } finally {
    exportBusy.value = false
  }
}

const runExportNow = async () => {
  exportBusy.value = true
  try {
    const res = await fetch('/api/v1/export/run-now', { method: 'POST', headers: authHeader() })
    const data = await res.json()
    if (!res.ok) throw new Error(data?.error || 'Export run failed')
    notify({
      type: 'success',
      message: data.sent
        ? `Export sent: ${data.submission_count} submissions, ${data.practice_count} practice attempts.`
        : 'Export ran — nothing new to send.',
    })
    await fetchExportSettings()
  } catch (err) {
    handleApiError(err, { fallback: 'Export run failed' })
  } finally {
    exportBusy.value = false
  }
}
```

> If `useNotifications`/`notify` is already destructured elsewhere in this file, reuse the existing binding instead of re-declaring `notify` (avoids a duplicate-identifier error). Check the existing imports/usage first.

- [ ] **Step 2: Call `fetchExportSettings` on mount**

Find the existing `onMounted(fetchUsers)` (near line 281) and replace it with:

```ts
onMounted(() => {
  fetchUsers()
  fetchExportSettings()
})
```

- [ ] **Step 3: Add the card to the template**

In the `<template>` (place near the other admin cards/sections, e.g. just before the users table section), add:

```html
<section v-if="exportSettings" class="card export-card">
  <h2 class="section-title">Submission-log export</h2>
  <p class="page-subtitle">
    Emails a compressed CSV of new exam + practice submissions to the admin address on a fixed interval.
  </p>
  <div class="export-row">
    <label class="switch">
      <input
        type="checkbox"
        :checked="exportSettings.enabled"
        :disabled="exportBusy"
        @change="toggleExport"
        data-testid="export-toggle"
      />
      <span>{{ exportSettings.enabled ? 'Enabled' : 'Disabled' }}</span>
    </label>
    <button type="button" class="btn btn-sm" :disabled="exportBusy" @click="runExportNow" data-testid="export-run-now">
      Run now
    </button>
  </div>
  <dl class="export-meta">
    <div><dt>Interval</dt><dd>{{ exportSettings.interval }}</dd></div>
    <div><dt>Recipient</dt><dd>{{ exportSettings.recipient || '— (ADMIN_EMAIL unset)' }}</dd></div>
    <div><dt>Last export</dt><dd>{{ exportSettings.last_export_at }}</dd></div>
  </dl>
</section>
```

- [ ] **Step 4: Verify frontend build/typecheck**

Run: `cd frontend && bun run build`
Expected: build succeeds (no TS errors). If the project has a lint/typecheck script (`bun run type-check` or similar), run it too.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/AdminView.vue
git commit -m "feat(admin-ui): submission-export toggle + run-now card"
```

---

### Task 9: Env samples + docker + docs

Document the new env vars and behavior.

**Files:**
- Modify: `.env.example`
- Modify: `docker-compose.yml`
- Modify: `MANUAL.md` (and `API_SPEC.md` if it enumerates endpoints)

- [ ] **Step 1: Add env vars to `.env.example`**

Append:

```bash
# Submission-log export emailer.
# Emails a gzip-CSV delta of exam + practice submissions to the admin on an interval.
# Requires EMAIL_SENDER_ADDRESS (SMTP) to be configured, else the exporter does not start.
SUBMISSION_EXPORT_ENABLED=true          # first-boot default; after that the admin UI toggle wins
SUBMISSION_EXPORT_INTERVAL=24h          # Go duration (e.g. 24h, 12h, 30m); default 24h
EXPORT_ADMIN_EMAIL=                     # recipient; falls back to ADMIN_EMAIL when empty
```

- [ ] **Step 2: Pass the vars through `docker-compose.yml`**

In the backend service `environment:` block, add (matching the file's existing style — `${VAR}` interpolation or `KEY: value`):

```yaml
      - SUBMISSION_EXPORT_ENABLED=${SUBMISSION_EXPORT_ENABLED:-true}
      - SUBMISSION_EXPORT_INTERVAL=${SUBMISSION_EXPORT_INTERVAL:-24h}
      - EXPORT_ADMIN_EMAIL=${EXPORT_ADMIN_EMAIL:-}
```

> Match the existing list style in the file (some compose files use `KEY: value` maps rather than `- KEY=value` lists). Mirror whichever the backend service already uses.

- [ ] **Step 3: Document in `MANUAL.md`**

Add a short "Submission-log export" section describing: what it sends (delta gzip CSVs), the 24h default interval and how to change it, that it is on by default and toggleable from the admin panel, the env vars, and that first boot only captures activity after startup (no historical back-fill). If `API_SPEC.md` lists endpoints, add `GET/PUT /api/v1/export/settings` and `POST /api/v1/export/run-now` (admin-only).

- [ ] **Step 4: Commit**

```bash
git add .env.example docker-compose.yml MANUAL.md API_SPEC.md
git commit -m "docs: submission-export env vars, compose, manual"
```

---

## Self-Review

**Spec coverage:**
- Delta scope → Tasks 3, 5 (window queries + `ExportNow`). ✓
- gzip CSV format → Task 4 builders. ✓
- Env-seeds-first-boot + DB-persisted UI toggle → Task 2 (`EnsureExportState` ON CONFLICT DO NOTHING) + Task 6 wiring. ✓
- Configurable interval + env recipient → Task 4 `ParseExportInterval`, Task 6 wiring. ✓
- On-by-default at boot → Task 6 (`!= "false"` default true + goroutine start). ✓
- Watermark advance rules (success/empty advance, failure holds) → Task 5, all four branches tested. ✓
- Mutex serialization → Task 5 (`s.mu` in `ExportNow`). ✓
- Run-now endpoint + button → Tasks 6, 8. ✓
- Admin-only gating → Task 6 (`RoleMiddleware(models.RoleAdmin)`). ✓
- Exporter only starts with SMTP active → Task 6 (inside the `EmailSenderAddressEnv != ""` block; `exportCtrl == nil` guard on routes). ✓
- Attachment MIME email → Task 1. ✓
- CSV columns per spec → Task 4 headers match spec §CSV schema. ✓
- Tests (unit + integration) → Tasks 1–7. ✓
- Env/docker/docs → Task 9. ✓

**Placeholder scan:** No TBD/TODO; every code step carries complete code. The two `>`-quoted notes (Task 1 SMTP-fake signature, Task 8 duplicate `notify`) are implementation cautions with the concrete fix stated, not deferred work.

**Type consistency:** `ExportAttachment`, `ExportState`, `SubmissionExportRow`, `PracticeAttemptExportRow`, `ExportConfig`, `ExportResult`, `exportRepo`/`exportSender`, and the repo method signatures (`EnsureExportState`, `GetExportState`, `SetExportEnabled`, `AdvanceExportWatermark`, `ExportSubmissions`, `ExportPracticeAttempts`) are used identically across Tasks 1–8. Service field `now func() time.Time` set in constructor and overridden in tests — consistent. Route paths `/export/settings`, `/export/run-now` match between Tasks 6 and 8.

**Known caveat (accepted):** `RunNow` uses the request context (`ctx.Request.Context()`); a very large export could exceed the HTTP handler's lifetime. Acceptable — run-now is a verification aid; the scheduled path uses the long-lived server context.
