# Design: Authentication & Password Management

Date: 2026-07-01
Status: Approved decisions, pending spec review

## 1. Goal

Add admin-driven credential provisioning and an auditable password-change log to
the SQL Quiz system, and make single ("manual") student creation produce a row
identical to a one-row bulk roster import.

Flows requested:
- Admin creates a lecturer/admin → system generates a password → shown once on
  the admin page with **Copy** and **Email** (email stubbed) options.
- Admin can reset any user's password (generates a new one, shown once). This is
  the immediate fix for user lockouts / human error.
- Users change only their own password (must supply the old one).
- Every password change is recorded in a changelog the **admin** can view,
  including the new password value.
- Manual single-student add == bulk import (identical resulting row).
- Email delivery is a TODO stub for now.

## 2. Locked decisions

| Decision | Choice |
|----------|--------|
| Changelog content | Store the **new password value** per event (encrypted), plus metadata. Old password = previous event's value (not stored separately). |
| Lockout recovery | Admin **reset** → server-generated new password, shown once. |
| Storage of values | **Encrypted at rest** — AES-256-GCM, key from env `PASSWORD_LOG_KEY`, never in DB. |
| Who manages passwords | **Admin only** (reset + view changelog). Lecturers still create students but cannot see/reset passwords. |
| Force change on first login | **No.** |

### Accepted residual risk
Admin-visible password values mean anyone with **both** the DB and the env key —
or a compromised admin account — can read users' recent passwords. Encryption
limits blast radius (DB leak alone is insufficient) but does not eliminate it.
This is an explicit product tradeoff for immediate human-error recovery.

## 3. Data model

New table:

```sql
CREATE TABLE IF NOT EXISTS app.password_events (
    id           SERIAL PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES app.system_users(id) ON DELETE CASCADE,
    changed_by   INTEGER REFERENCES app.system_users(id),   -- actor; = user_id for self_change
    source       TEXT NOT NULL CHECK (source IN ('create','self_change','admin_reset')),
    new_password_enc BYTEA NOT NULL,   -- AES-256-GCM ciphertext of the new plaintext
    nonce        BYTEA NOT NULL,       -- 12-byte GCM nonce
    notified_admin BOOLEAN NOT NULL DEFAULT FALSE,  -- for the email TODO
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_password_events_user ON app.password_events(user_id, created_at DESC);
```

New column (dormant — reserved for future email delivery; stays NULL for now,
no capture UI or roster wiring):

```sql
ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS email TEXT;
```

Token-invalidation column (see §5b):

```sql
ALTER TABLE app.system_users ADD COLUMN IF NOT EXISTS token_version INTEGER NOT NULL DEFAULT 0;
```

## 4. Encryption

- New util `internal/services/password_crypto.go`:
  - `EncryptPassword(plaintext string) (ciphertext, nonce []byte, err error)`
  - `DecryptPassword(ciphertext, nonce []byte) (string, error)`
  - AES-256-GCM; key loaded once from `PASSWORD_LOG_KEY` (base64 of 32 bytes).
- Key handling: if `PASSWORD_LOG_KEY` is unset/invalid, the app still runs and
  login is unaffected, but password-event **logging is disabled** and the admin
  changelog surfaces a clear "encryption key not configured" state. A fixed test
  key is used in tests. Dev key documented in `.env.example`.
- New util `internal/services/password_gen.go`:
  - `GeneratePassword() string` — 14 chars from an unambiguous alnum alphabet,
    sourced from `crypto/rand` (never `math/rand`).

## 5. Backend endpoints

| Method & path | Role | Behaviour |
|---------------|------|-----------|
| `POST /api/v1/users` | Admin, Lecturer | Create lecturer/admin. Body `{username, role, email?}` (no password). `role` must be LECTURER or ADMIN — a `STUDENT` role is rejected with 400 pointing to `/students` (keeps the single student code path / parity). Server generates password → hash for login + `create` event. Returns `{id, generated_password}` **once**. Lecturers still limited to non-admin per existing hierarchy. |
| `POST /api/v1/students` | Admin, Lecturer | Single student create/upsert. Body = roster fields. Calls the **shared** `UpsertStudentByNumber` (§6). Default password = student_number → `create` event. |
| `POST /api/v1/users/:id/reset-password` | **Admin** | Generate new password → hash + `admin_reset` event. Returns `{generated_password}` once. |
| `GET /api/v1/users/:id/password-events` | **Admin** | Returns `[{source, changed_by_username, created_at, new_password}]` (decrypted). This read is itself logged (app log, metadata only). |
| `POST /api/v1/users/password` | authenticated (self) | Existing self-change; now also writes a `self_change` event capturing the submitted new password. |

All new admin routes use `RoleMiddleware(models.RoleAdmin)`. Reuses existing
`AuthMiddleware`.

## 5b. Immediate token invalidation on password change/reset

Passwords changing must instantly evict existing sessions (incl. a compromised
one), not wait for the 24h JWT expiry.

- `system_users.token_version` (int, default 0) is the source of truth.
- Login embeds the user's current `token_version` in the JWT claims.
- `AuthMiddleware` reads `token_version` from the validated claims and compares it
  to the user's current `token_version` (fetched per request; a small cached
  lookup is acceptable). Mismatch → `401` → client must re-login.
- Any `self_change` and `admin_reset` **increments** `token_version`, so all
  previously-issued tokens for that user are rejected immediately. `create` sets
  version 0 (no prior tokens exist).
- Side effect (intended): changing your own password logs out your other devices.

Repository: `BumpTokenVersion(ctx, userID)`; login and middleware read the value.

## 6. Manual == Bulk parity (single source of truth)

Both the CSV importer and the new single-student endpoint call **one** service
function that wraps `repository.UpsertStudentByNumber`:

```
StudentUpsert(row StudentRow) -> (created|updated, error)
```

- `student_import_service.go` bulk loop and the new `POST /api/v1/students`
  handler both call `StudentUpsert`. Parity is guaranteed by construction, not by
  keeping two code paths in sync.
- Password on create = student_number (unchanged from current bulk behaviour);
  on conflict, demographics update and password hash is preserved (unchanged).
- No email capture: the `email` column stays NULL; roster parser unchanged.

## 7. Email (TODO stub)

- New `internal/services/email.go` with interface:
  ```
  type EmailSender interface {
      SendCredential(toEmail, username, password string) error
      SendChangeNotice(adminEmail string, ev PasswordEvent) error
  }
  ```
- Ship a `NoopEmailSender` that logs `TODO: email <...>` and returns nil.
- Call sites wired now (create, reset → SendCredential; any event →
  SendChangeNotice to `ADMIN_NOTIFY_EMAIL` env), so enabling real SMTP later is a
  one-file swap. No real SMTP in this change.

## 8. Frontend (`AdminView.vue`, `services/auth.ts`)

- **Create staff** form: username + role (LECTURER/ADMIN) only (no password
  field); on success, a reveal panel shows the generated password with **Copy**
  and **Email** (email button calls the stub; shows "email delivery pending").
- **Add student** form: roster fields (student_number, id_ext, sec_lec, sec_lab,
  first/last name); posts to `/students`.
- **Per-user Reset** action → confirm dialog → reveal panel with new password.
- **Password history** drawer per user (admin) → `GET .../password-events`,
  shows source/actor/time/value; renders the "key not configured" state when
  applicable.
- `BulkImportStudents.vue`: document the optional email column.

## 9. Security posture summary

Keeps: bcrypt login hashing, no plaintext in normal responses, role hierarchy,
admin-only gating. Adds: reversible **encrypted** password-event store (explicit
tradeoff), crypto/rand generation, admin-only access with access logging, and
**immediate token invalidation** on change/reset (§5b). Explicitly deferred
(noted, not built): login rate-limiting.

## 10. Testing (TDD)

- `password_crypto`: encrypt→decrypt round-trip; decrypt with wrong key fails;
  distinct nonces per call.
- `password_gen`: length, alphabet, uniqueness across calls.
- repo: insert + fetch password_events ordered desc.
- `reset-password`: 403 for non-admin; returns password; writes `admin_reset`.
- self-change: writes `self_change` event.
- changelog: 403 for non-admin; returns decrypted values.
- **Parity**: single `/students` create and a one-row bulk import yield an
  identical `system_users` row (field-by-field).
- **Token invalidation**: a token minted before a reset is rejected (401) after
  `admin_reset`/`self_change` bumps `token_version`; a freshly minted token works.

## 11. Out of scope

Real SMTP/email transport; login throttling; forced-first-login change.
