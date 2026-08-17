# Rotating credential import — design

Date: 2026-08-17
Status: approved (design), not implemented

## Problem

`AdminView` has one bulk roster importer (`POST /api/v1/lecturer/students/import`).
It generates and emails a password **only for newly inserted students**. On
re-import of an existing student, `UpsertStudentByNumber` leaves `password_hash`
untouched, so the row is updated silently and no credential is sent.

Admins need a way to hand a whole class file to the system and have **every**
student in that file receive a fresh password by email — new students and
returning ones alike.

## Solution

Add a **second, separate** bulk import — admin-only — that rotates the password
for every row in the file and emails it. The existing importer keeps its current
behaviour and permissions and is not modified.

Because the emails go out on a background worker, AdminView also gets a live
status bubble showing the send progress (`12/40`, failures) rather than a bare
"queued".

### Behaviour matrix

| | existing import | new import |
|---|---|---|
| new student | generate password, email it | same |
| existing student | update fields, keep password, no email | rotate password, bump `token_version`, email it |
| sessions | untouched | every rotated student is logged out |
| report | passwords for created rows only | passwords for **every** row |
| email send | synchronous, inline in the request | async background worker, live progress bubble in AdminView |
| role | Admin + Lecturer | **Admin only** |
| missing/invalid email | silent | row still imports; `email_status: "no_email"`; password shown for hand-delivery |

### File format

Unchanged from the existing importer — same `.csv` / `.xlsx` columns
(Student Number, First Name, Last Name, SECLEC, SECLAB, Student_id, Email).
`services.ParseRoster` already recognises an email column under the aliases
`email`, `e-mail`, `email address`, `e_mail`, `mail`
(`internal/services/roster_parser.go:34`), so the parser needs no change.

A row with no email is imported and reported with `email_status: "no_email"`;
its plaintext password appears in the response so the admin can hand-deliver it.

### Duplicate rows

If the same `student_id_ext` appears twice in one file, the second occurrence
rotates the password again and wins. Last row in the file is authoritative. No
special handling.

## Security posture

Stated explicitly because this endpoint deliberately accepts three risks that
the rest of the codebase treats as opt-in:

1. **Plaintext passwords by email, in bulk.** `AuthService.ResetPassword`
   defaults its `emailPassword` flag OFF precisely because emailing a plaintext
   password is a deliberate tradeoff. This endpoint emails one per row by
   design — that is the feature.
2. **No rate limiter.** Same accepted residual as
   `POST /api/v1/users/bulk-reset-password` (see `TODO.md`, ARCH-036). The audit
   trail in `app.password_events` is the only detective control.
3. **Response body carries every plaintext password.** Necessary so rows whose
   email is missing or whose delivery fails are still recoverable.

Mitigations that ARE in scope: admin-only role gate, a typed confirmation
phrase, and one audit event per affected user.

## Backend design

### Route

```
POST /api/v1/students/import-credentials
  → controllers.RoleMiddleware(models.RoleAdmin)
  → lecturerCtrl.ImportStudentsWithCredentials
```

Registered in `backend/cmd/main.go` next to the existing
`POST /api/v1/students` (`userCtrl.CreateStudent`). The path deliberately does
**not** live under the `/lecturer/...` prefix, because it is not a
lecturer-reachable route.

Request: `multipart/form-data` with

- `file` — the roster (`.csv` or `.xlsx`)
- `confirm` — must equal the literal string `ROTATE`

The response carries a `job_id` alongside the report, identifying the background
email job so the UI can poll its progress (see *Email job registry* below).

A second, admin-only route serves that progress:

```
GET /api/v1/students/import-credentials/:job_id/status
  → controllers.RoleMiddleware(models.RoleAdmin)
  → lecturerCtrl.ImportCredentialsJobStatus
```

The confirmation phrase is validated server-side on every request; the server
never trusts the UI's own confirm dialog. This mirrors `BulkResetPassword`'s
`confirm == "RESET"` check. A missing or mismatched phrase is `400`
`{"error":"confirmation_required"}`.

### Repository

New `UpsertStudentRotating` in
`backend/internal/repository/postgres_users.go`, a clone of
`UpsertStudentByNumber` (line 279) whose conflict clause additionally rotates
the credential:

```sql
ON CONFLICT (student_id_ext) WHERE student_id_ext IS NOT NULL DO UPDATE SET
  username       = EXCLUDED.username,
  student_number = EXCLUDED.student_number,
  sec_lec        = EXCLUDED.sec_lec,
  sec_lab        = EXCLUDED.sec_lab,
  first_name     = EXCLUDED.first_name,
  last_name      = EXCLUDED.last_name,
  email          = EXCLUDED.email,
  password_hash  = EXCLUDED.password_hash,
  token_version  = app.system_users.token_version + 1
RETURNING id, (xmax = 0) AS inserted
```

The insert path is byte-identical to the existing upsert. `token_version` is
bumped inside the same statement, so the rotation and the session eviction can
never diverge.

`UpsertStudentByNumber` is left exactly as it is — the existing importer must
not change behaviour.

### Service

New method on `StudentImportService`
(`backend/internal/services/student_import_service.go`):

```go
func (s *StudentImportService) ImportRotating(
    ctx context.Context, creatorID int, rows []ParsedStudentRow,
) (RotatingImportReport, error)
```

Structurally identical to `Import` (line 129): one outer transaction, one
`SAVEPOINT row_N` per row, the same two mandatory-field guards
(`student_number`, `student_id_ext`), rollback-to-savepoint and `Skipped++` on a
row error, `RELEASE SAVEPOINT` on success. The differences:

- it calls `UpsertStudentRotating` instead of `UpsertStudentByNumber`;
- it emits an account entry for `updated` rows as well as `created` rows,
  because on this path the returned plaintext password is meaningful for both.

The `ImportRepo` interface gains the `UpsertStudentRotating` method.

Report shape:

```go
type RotatedAccount struct {
    UserID        int    `json:"-"`
    Row           int    `json:"row"`
    StudentNumber string `json:"student_number"`
    StudentIDExt  string `json:"student_id_ext"`
    Email         string `json:"email"`
    Password      string `json:"password"`
    Outcome       string `json:"outcome"`      // "created" | "updated"
    EmailStatus   string `json:"email_status"` // "queued" | "no_email"
}

type RotatingImportReport struct {
    Total    int              `json:"total"`
    Created  int              `json:"created"`
    Updated  int              `json:"updated"`
    Skipped  int              `json:"skipped"`
    Errors   []ImportRowError `json:"errors"`
    Accounts []RotatedAccount `json:"accounts"`
    JobID    string           `json:"job_id"` // "" when no row had an email
}
```

`EmailStatus` is decided synchronously from whether the row carries a non-empty
email, before any send is attempted — so it is accurate even though delivery is
async. `"queued"` means handed to the worker; the live progress bubble is what
turns it into a sent/failed count.

### Password-event logging

This path must **not** call `AuthService.logPasswordEvent`. That helper invokes
`s.email.SendChangeNotice` for every event whenever `ADMIN_EMAIL` is configured,
which would turn a 200-row file into 200 emails to the admin — the exact trap
documented at `auth_service.go:476-483` for `BulkResetPasswords`.

Instead, a new `AuthService.LogBulkCredentials(ctx, actor, entries)` encrypts
each plaintext with `s.cipher` and persists a `models.PasswordEvent` via
`s.repo.InsertPasswordEvent` — the same insert `logPasswordEvent` uses, minus
the trailing `SendChangeNotice` block — so **no** email of any kind is sent from
this path. Source per row: `models.PasswordEventCreate` for `created`,
`models.PasswordEventAdminReset` for `updated`. When `s.cipher` is nil it logs
once that event recording is disabled, matching `BulkResetPasswords`.

Called post-commit, before the emails are queued.

### Async email delivery

After the transaction commits and events are logged, the controller registers an
email job and starts a single goroutine that walks the rows with a non-empty
email and calls `SendCredential` serially, with `context.Background()` (the
request context is already cancelled by the time it runs). After each send it
increments the job's `Sent` or `Failed` counter. Each failure is logged with the
target username; nothing is retried, and no failure affects the HTTP response,
which has already been sent.

Serial, single-goroutine delivery is intentional: it keeps the Gmail send rate
to one connection and avoids a burst that the daily quota would reject.

This matches the existing async-send pattern in `AuthService.sendResetCode`.
Like that one, the goroutine is not tracked across server shutdown — a shutdown
mid-send drops the remaining emails. Acceptable because every password is also
present in the HTTP response the admin already holds.

### Email job registry

New `services.EmailJobRegistry` in
`backend/internal/services/email_job_registry.go` — an in-memory,
mutex-guarded `map[string]*EmailJob`. It exists solely to back the progress
bubble; nothing about the import's correctness depends on it.

```go
type EmailJob struct {
    ID       string   `json:"job_id"`
    OwnerID  int      `json:"-"`
    Total    int      `json:"total"`
    Sent     int      `json:"sent"`
    Failed   int      `json:"failed"`
    Done     bool     `json:"done"`
    Failures []string `json:"failures"` // student_id_ext of failed sends, capped at 50
}
```

- `Register(ownerID, total) *EmailJob` mints a job with a 128-bit
  `crypto/rand` hex id — unguessable, never sequential — and returns it.
- `MarkSent(id)` / `MarkFailed(id, studentIDExt)` / `Finish(id)` are the
  worker's only writes.
- `Get(id, ownerID) (*EmailJob, bool)` returns a **copy** and requires the
  caller's user id to equal `OwnerID`, so one admin cannot read another's job.
  Mismatch is indistinguishable from not-found (`404`).
- Retention: a finished job is kept 30 minutes so a reloaded page can still show
  the outcome. Eviction is a sweep of expired entries on each `Register` call —
  no background ticker. The registry is additionally capped at 100 live jobs;
  registering past the cap evicts the oldest finished job first.

Single-process only, deliberately: the deployment runs one backend process, and
a lost job degrades to a missing bubble, never to a lost password. A server
restart drops the registry — the UI treats a `404` on a job it was polling as
"progress unavailable" and falls back to the static `queued` counts it already
has from the import response.

When no row in the file has an email, no job is registered and `job_id` is
`""` — the UI shows no bubble.

`GET .../:job_id/status` returns the job as JSON, or `404`
`{"error":"job not found"}`.

## Frontend design

New component
`frontend/src/components/BulkImportStudentsCredentials.vue`, a sibling of
`BulkImportStudents.vue`, mounted in `AdminView.vue` immediately below the
existing roster-import card. The existing card and component are untouched.

Reuses the project's standard building blocks: `useSingleFlight` for the
mutation, `useFormValidation` with the same `.csv|.xlsx` file rule, `FieldError`,
`Spinner`, `handleApiError`, `useNotifications`, and `useConfirm` for the
confirmation gate.

Flow: pick file → type `ROTATE` into an inline confirmation input → press
**Import & Email Passwords**, which stays disabled until the phrase matches
exactly. This mirrors `BulkPasswordReset.vue`'s `RESET` gate;
`services/confirm_service.ts`'s `ConfirmOptions` has no text-input field, so a
modal could not carry the typed phrase. The server re-checks the phrase on every
request regardless.

Result table columns: **Login ID**, **Outcome** (created / updated), **Email**
(`✉ queued` when sent to the worker, `—` when the row had no address),
**Password**, **Copy**. Plus a **Copy all as CSV** button that yields
`student_id_ext,email,password` for every row, so rows without an email — or
whose delivery silently failed — are recoverable in one action rather than
row-by-row.

The row-errors table is unchanged from the existing component.

### Email progress bubble

New `frontend/src/components/EmailJobBubble.vue`, rendered by `AdminView.vue`
as a fixed-position card in the top-right of the admin area (below the header,
above page content), visible only while a job is being tracked:

```
┌──────────────────────────────┐
│ ✉ Emailing passwords  12/40  │
│ ●●●●●○○○○○  2 failed         │
└──────────────────────────────┘
```

Driven by a new `frontend/src/composables/useEmailJobStatus.ts`:

- `track(jobId)` starts polling `GET /api/v1/students/import-credentials/:job_id/status`
  every 2 s; stops on `done`, on `404`, or after a 15-minute ceiling.
- The active `job_id` is persisted to `localStorage` under
  `sql_quiz_email_job`, and `track` is re-invoked on AdminView mount if a
  non-expired id is present — the worker outlives the page, so a reload or a
  tab switch must not lose the bubble.
- The key is cleared when the job finishes and the bubble is dismissed.
- `404` (server restarted, job evicted) sets a terminal `unavailable` state
  rather than an error toast: the passwords are already in the admin's table.

Bubble states: **in progress** (counter + progress dots), **done, all sent**
(green, auto-dismisses after 10 s), **done with failures** (amber, stays until
dismissed, lists the failed login IDs so the admin can hand-deliver from the
results table), **unavailable** (grey, dismissible). A manual close button is
present in every state. `aria-live="polite"` on the counter so the progress is
announced without stealing focus; `prefers-reduced-motion` disables the dot
animation.

`BulkImportStudentsCredentials.vue` emits the `job_id` from the import response
upward; `AdminView.vue` passes it to `track()`.

## Testing

Backend (`go test` with `-p 1` — the password suites are DB-global and a plain
`./...` run on a shared DB produces false regressions):

- **repository:** conflict path rotates `password_hash` *and* increments
  `token_version`; the insert path produces a row identical to the existing
  importer's; `UpsertStudentByNumber` still leaves `password_hash` untouched on
  conflict (guard against regressing the old importer).
- **service:** a file mixing new and existing students yields a password on
  every account row with the correct `outcome`; a failing row rolls back only
  its own savepoint and lands in `Errors`; rows with no email get
  `email_status: "no_email"`.
- **controller:** a lecturer JWT gets `403`; a missing or wrong `confirm` value
  gets `400`; with a spy email sender, a multi-row import produces exactly one
  `SendCredential` per row that has an email and **zero** `SendChangeNotice`
  calls even with `ADMIN_EMAIL` set.
- **job registry:** counters are correct under a concurrent writer (run under
  `-race`); `Get` with a non-owner id returns not-found; a finished job is
  evicted after its retention window; a file with no emails registers no job.
- **status endpoint:** the owning admin gets the counts; a different admin gets
  `404`; a lecturer gets `403`; an unknown id gets `404`.

Frontend (vitest): the component posts to `/api/v1/students/import-credentials`
with the confirm field, renders the Outcome and Email columns, and the
copy-all-as-CSV button produces one line per account row. `useEmailJobStatus`
polls until `done` and then stops, restores a tracked job from `localStorage` on
mount, and enters `unavailable` on `404`. `EmailJobBubble` renders each of the
four states.

## Out of scope

- Rate limiting this endpoint (tracked with the same open item as
  `bulk-reset-password`).
- Durable delivery status. Progress lives in memory for 30 minutes and dies with
  the process; the per-row `email_status` in the saved report stays `"queued"`
  forever. Persisting delivery state to a table is separate work.
- Multi-process / HA support for the job registry.
- Retrying failed sends.
- Any change to the existing `/lecturer/students/import` route, its component,
  or `UpsertStudentByNumber`.
