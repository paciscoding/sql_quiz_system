# Submission-Log Export Emailer — Design Spec

**Date:** 2026-07-12
**Status:** Approved (design), pending implementation plan
**Author:** brainstorming session

## Problem

Every exam submission (`app.submissions`) and practice attempt (`app.practice_attempts`)
is persisted forever, but there is no way for an admin to receive that history off the
box. If the container or database is lost, the record is gone. Admins want a periodic,
self-arriving copy of student activity in a viewable format.

## Goal

A background service that, on a configurable interval (default 24h), packages the last
window of submission history into compressed CSVs and emails them to an admin address.
The service is on by default at container boot and can be toggled off/on live from the
admin UI.

## Non-Goals

- No full-history snapshots. Each email is a **delta** since the previous successful
  export. Admins reconstruct history from the email series.
- No in-app viewer/download of past exports. Delivery is email-only.
- No per-recipient configuration UI. Recipient is set via env.
- No encryption of attachments beyond transport (SMTP TLS as already configured).

## Decisions (from brainstorming)

| Fork | Decision |
|------|----------|
| Data scope | Delta (rows since last successful export) |
| Format | gzip CSV — one `.csv.gz` per table |
| Toggle | Env var seeds first-boot default; admin UI toggle persists in DB |
| Recipient | Env var (`EXPORT_ADMIN_EMAIL`, fallback `ADMIN_EMAIL`) |
| Interval | Env var (`SUBMISSION_EXPORT_INTERVAL`, default `24h`) |
| Manual trigger | Included — `POST /staff/export/run-now` + AdminView button |

## Architecture

### Component: `SubmissionExportService` (new `services/submission_export.go`)

Owns the ticker loop and the export operation. One clear purpose: given a time window,
produce and send the compressed export, then advance the watermark.

Dependencies (injected):
- A repository interface for delta queries + export-state read/write.
- An attachment-capable email sender.
- Config: interval `time.Duration`, recipient string.

Public surface:
- `NewSubmissionExportService(repo, sender, cfg) *SubmissionExportService`
- `Run(ctx context.Context)` — blocks, drives the ticker until ctx cancelled.
- `ExportNow(ctx context.Context) (ExportResult, error)` — one export cycle; called by
  both the ticker and the run-now endpoint.

Concurrency: a `sync.Mutex` serializes `ExportNow` so the ticker and a manual run-now
cannot overlap.

### The export cycle (`ExportNow`)

1. Read `export_state`. If `enabled == false`, return early (no-op result, no watermark
   change).
2. `windowStart = last_export_at`, `windowEnd = now`.
3. Query `submissions` and `practice_attempts` where `submitted_at > windowStart AND
   submitted_at <= windowEnd`, joined to `system_users` for the username.
4. If both result sets are empty → advance watermark to `windowEnd`, send **no** email,
   return.
5. Build two gzip CSVs in memory (`submissions_<YYYYMMDD>.csv.gz`,
   `practice_attempts_<YYYYMMDD>.csv.gz`).
6. Send one email to the recipient: plaintext body (window bounds + row counts) plus both
   attachments.
7. **On send success only** → set `last_export_at = windowEnd`. On send failure → leave
   watermark unchanged, return the error (logged by caller). The next cycle then covers
   the wider window, so no rows are ever dropped.

### Watermark semantics

`last_export_at` is the exclusive lower bound of the next window. This makes the service
resilient to downtime: a missed interval simply produces a larger next window rather than
a gap. Advancing only on success (or on a confirmed-empty window) guarantees at-least-once
delivery of every row.

### Data model — new table `app.export_state`

Singleton row (`id = 1`), created in `postgres_schema.go` migrations:

```sql
CREATE TABLE IF NOT EXISTS app.export_state (
    id             INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled        BOOLEAN NOT NULL,
    last_export_at TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

First-boot seeding (idempotent): if the row is absent, insert
`(1, <env SUBMISSION_EXPORT_ENABLED default true>, now())`. `last_export_at = now()` at
seed time means the first export covers only activity **after** first boot — historical
rows are intentionally not back-filled (delta semantics; would otherwise be an unbounded
first email). After seeding, the DB row is the source of truth for `enabled`; the env var
is never re-read for the flag, so a UI toggle survives restarts.

### Configuration (env)

| Var | Default | Meaning |
|-----|---------|---------|
| `SUBMISSION_EXPORT_ENABLED` | `true` | First-boot value of `export_state.enabled` only |
| `SUBMISSION_EXPORT_INTERVAL` | `24h` | Ticker period, parsed as Go `time.Duration` |
| `EXPORT_ADMIN_EMAIL` | (falls back to `ADMIN_EMAIL`) | Recipient |

The exporter starts only when the SMTP sender is active (same
`EMAIL_SENDER_ADDRESS`-gated condition that activates `smtpSender` in `main.go`). If SMTP
is inactive, log a warning and do not start the goroutine — there is no way to deliver.

Invalid `SUBMISSION_EXPORT_INTERVAL` → log warning, fall back to 24h.

### Email attachment support (`services/email_smtp.go`)

Current `send(to, subject, body)` builds a plaintext message only. Add:

- A small `ExportAttachment` struct: `{ Filename string; Data []byte; ContentType string }`.
- `SendExport(to, subject, body string, attachments []ExportAttachment) error` — builds a
  `multipart/mixed` MIME message: one `text/plain` part for the body, one
  base64-encoded `application/gzip` part per attachment, then dispatches through the same
  SMTP auth/transport path as `send`.

The export service depends on a minimal local interface (e.g. `exportSender` with the
`SendExport` method), not on the concrete `*SMTPEmailSender`, so it is testable with a
fake.

### Repository additions (`repository/postgres_export.go`)

- `GetExportState(ctx) (ExportState, error)` — read the singleton (seed if absent).
- `SetExportEnabled(ctx, bool) error` — UI toggle write; also bumps `updated_at`.
- `AdvanceExportWatermark(ctx, t time.Time) error` — set `last_export_at`, bump `updated_at`.
- `ExportSubmissions(ctx, from, to time.Time) ([]SubmissionExportRow, error)`
- `ExportPracticeAttempts(ctx, from, to time.Time) ([]PracticeAttemptExportRow, error)`

Rows carry the joined `username` alongside the raw table columns.

### HTTP surface (`controllers/export_controller.go`)

All routes under the existing staff group, gated to `RoleAdmin` (reuse
`RoleMiddleware(models.RoleAdmin)`):

- `GET  /staff/export/settings` → `{ enabled, last_export_at, interval, recipient }`
- `PUT  /staff/export/settings` body `{ enabled: bool }` → persists via `SetExportEnabled`
- `POST /staff/export/run-now` → calls `service.ExportNow`, returns the `ExportResult`
  (row counts, sent bool, window). Respects the mutex; returns the send error if any.

### Frontend (`components/AdminView.vue`)

A card in the admin panel:
- Toggle switch bound to `enabled` (PUT on change).
- Read-only status: last export time, interval, recipient address.
- "Run now" button → `POST run-now`, then shows the returned counts / send result inline.

Reuse the existing admin API client and styling conventions already in `AdminView.vue`.

## CSV schema

**submissions_<date>.csv.gz**
`id, exam_id, student_id, username, question_id, is_correct, score, submitted_at, sql_text, validation_details`

**practice_attempts_<date>.csv.gz**
`id, student_id, username, practice_question_id, is_correct, submitted_at, sql_text, validation_details`

`validation_details` is emitted as its raw JSON string. `submitted_at` in RFC3339. Standard
CSV quoting (Go `encoding/csv`) so embedded newlines/commas in `sql_text` are safe.

## Error handling

- Query error → abort cycle, watermark unchanged, log, return error. Retried next tick.
- Send error → watermark unchanged, log, return error. Retried next tick (wider window).
- Disabled → clean no-op, no watermark change.
- Ticker loop never panics the process: `ExportNow` errors are logged and swallowed inside
  `Run`; the loop continues to the next tick.

## Wiring (`cmd/main.go`)

After `smtpSender` is constructed (inside the `EMAIL_SENDER_ADDRESS != ""` branch):
1. Resolve recipient + interval from env.
2. `exporter := services.NewSubmissionExportService(repo, smtpSender, cfg)`
3. `go exporter.Run(ctx)` using the existing top-level `ctx`.
4. Register the export controller routes in the staff group.

If SMTP is inactive, log that the exporter is disabled for lack of a sender.

## Testing

**Unit (`services/submission_export_test.go`)**
- CSV/gzip builder: given rows → gunzip → assert header + row content + CSV quoting.
- Watermark logic with a fake repo + fake sender:
  - success → watermark advanced, one email with 2 attachments;
  - send failure → watermark unchanged, error returned;
  - empty window → watermark advanced, no email sent;
  - disabled → no-op, no watermark change, no email.
- Interval parsing: bad duration → 24h fallback.
- Mutex: two concurrent `ExportNow` calls do not interleave (single email).

**Integration (`services/submission_export_integration_test.go`)**
Following the real-Postgres pattern in `forgot_password_integration_test.go`: seed a couple
of submissions, run `ExportNow` with a fake sender, assert the delta query returned the
seeded rows and the sender received two gzip attachments; assert a second immediate run
sends nothing (watermark advanced).

## File change summary

New:
- `backend/internal/services/submission_export.go`
- `backend/internal/services/submission_export_test.go`
- `backend/internal/services/submission_export_integration_test.go`
- `backend/internal/controllers/export_controller.go`
- `backend/internal/repository/postgres_export.go`

Modified:
- `backend/internal/services/email_smtp.go` (attachment MIME send)
- `backend/internal/repository/postgres_schema.go` (`export_state` table + seed)
- `backend/cmd/main.go` (goroutine + routes)
- `frontend/src/components/AdminView.vue` (toggle card)
- `.env.example`, `docker-compose.yml` (new env vars)
- Docs (`MANUAL.md` / `API_SPEC.md` as appropriate)
