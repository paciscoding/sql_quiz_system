# Load Test: Email Authentication Flow

Runbook + design notes for load-testing the password-reset / email-auth flow at
~150 samples. The executable tooling lives in `backend/loadtest/`; this document
explains **why** the test is shaped the way it is, how to run it, and how to read
the results.

---

## 1. What is under test

The public two-step forgot-password flow, end to end:

```
POST /api/v1/forgot-password          -> emails a 6-digit OTP (generic 200)
POST /api/v1/forgot-password/verify   -> verifies OTP, regenerates password,
                                          emails new credentials
POST /api/v1/login                    -> log in with the regenerated password
```

Source: `backend/internal/controllers/user_controller.go` (handlers),
`backend/internal/services/auth_service.go` (flow + throttle),
`backend/internal/services/email_smtp.go` (SMTP delivery).

## 2. Three constraints that dictate the test design

These come from the implementation and are the reason the tooling looks the way
it does. Change the code and this test's assumptions change with it.

### 2.1 The OTP never leaves over HTTP

`ForgotPassword` always returns an identical generic 200 with **no code** in the
body (enumeration-safe by design). The 6-digit OTP is delivered **only** by email
(`SendResetCode`). Therefore a real end-to-end driver must read the mailbox to
obtain the code — it cannot get it from the API response.

→ We run a mail sink with a queryable API and pull the OTP out of it.

### 2.2 Per-account throttle + short TTL

```
ForgotResetThrottle = 5 * time.Minute   // second request inside this = silent no-op
ResetOTPTTL         = 10 * time.Minute  // issued code validity
ResetOTPMaxAttempts = 5                  // verify tries per code
```

The throttle keys on `system_users.last_forgot_reset_at`. A single account cannot
be exercised more than once per 5 minutes.

→ **N samples require N distinct accounts, each hit exactly once.** 150 samples =
150 seeded accounts, each with a unique username **and** a unique, non-empty
email (the flow silently no-ops for accounts without an email).

### 2.3 Gmail SMTP is unfit for load; the send is async

`send()` opens a fresh STARTTLS + `PlainAuth` connection **per email** (no
pooling), fired from a **goroutine per request** (`go s.sendResetCode`). 150 of
those against real Gmail hits its concurrent-connection cap (~10-15) and daily
volume cap (~500/day free tier) → `421 4.7.0 try again later`, and it spams real
inboxes.

Because delivery is async, the forgot/verify **HTTP** latencies exclude the email
round-trip. The email path must be measured separately.

→ We point the backend at **Mailpit** (a catch-all SMTP sink with no limits) and
measure `otp-arrival` / `pw-arrival` as distinct metrics.

> **net/smtp gotcha.** Go's `net/smtp` refuses `PlainAuth` over a non-TLS
> connection unless the server host is `localhost`. Pointing the backend at
> Mailpit as a separate host (`mailpit:1025`) fails every send with
> `unencrypted connection` and no mail is delivered. The loadtest override
> therefore runs Mailpit in the backend's network namespace
> (`network_mode: service:backend`) and sets `EMAIL_SMTP_HOST=localhost`, which
> trips the localhost exemption — no TLS or certificates needed for the sink.
> Because Mailpit shares the namespace, its `1025`/`8025` host ports are declared
> on the **backend** service, not on Mailpit.

## 3. Architecture of the test

```
 ┌────────────┐  1. forgot/verify/login (HTTP)   ┌───────────┐
 │            │─────────────────────────────────▶│  backend  │
 │  driver    │                                   │  :8080    │
 │ (Go, -c    │  3. login w/ regenerated pw       └─────┬─────┘
 │  workers)  │◀───────────────────────────────         │ async SMTP :1025
 │            │                                          ▼
 │            │  2. poll for OTP / new password    ┌───────────┐
 │            │◀───────────────────────────────────│  Mailpit  │
 └────────────┘     GET /api/v1/search (REST)      │ :1025/8025│
                                                    └───────────┘
        seed program ──INSERT 150 accounts──▶ Postgres :5432
```

- **Mailpit** — catch-all SMTP on `:1025`, web UI + REST API on `:8025`. Accepts
  every message, no auth, no rate limits.
- **Driver** — bounded worker pool (`-c`). One worker per account runs the full
  five-step chain, reading the OTP and the regenerated password from Mailpit's
  REST API. Collects per-step latency percentiles.
- **Seed** — inserts the fixed account pool; idempotent; supports teardown.

Files:

| file | role |
|------|------|
| `docker-compose.loadtest.yml` | Mailpit + backend SMTP env + host ports for backend/db |
| `backend/loadtest/seed/main.go` | seed / teardown the account pool |
| `backend/loadtest/driver/main.go` | the load driver + metrics |
| `backend/loadtest/README.md` | terse quick-start |

## 4. Running it

All `go run` commands are from `backend/` (the module root). Replace `PASS` with
`DB_PASSWORD` from `.env`.

### Step 1 — bring up db + backend + mailpit (repo root)

```sh
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml up -d db backend mailpit
```

Confirm the backend is using Mailpit, not the no-op sender:

```sh
docker logs sql-quiz-backend | grep 'email:'
# expect: email: SMTP sender active (from=loadtest@example.com host=mailpit)
```

> The `EMAIL_SMTP_HOST` / `EMAIL_SMTP_PORT` vars are read by `NewSMTPEmailSender`
> but are **not** forwarded by the base `docker-compose.yml`; the loadtest
> override adds them. Without the override the backend would try real Gmail.

Mailpit UI: <http://localhost:8025>

### Step 2 — seed 150 accounts

```sh
DATABASE_URL='postgres://sqlquiz:PASS@localhost:5432/sqlquiz?sslmode=disable' \
  go run ./loadtest/seed -n 150
```

Accounts: `loadtest0001`…`loadtest0150`, emails `loadtestNNNN@example.com`
(reserved non-routable domain). The initial password is unused — the flow
regenerates it. Verify:

```sh
docker exec sql-quiz-db psql -U sqlquiz -d sqlquiz -c \
  "SELECT count(*) FROM app.system_users WHERE username LIKE 'loadtest%';"
```

### Step 3 — smoke test (n=1)

```sh
go run ./loadtest/driver -n 1
```

Watch <http://localhost:8025>: a "reset code" mail then a "credentials" mail
should appear; the run should report `succeeded: 1`. (Verified 2026-07-09: 1/1,
full chain ~490ms — forgot 3ms, otp-arrival 206ms, verify 13ms, pw-arrival
203ms, login 67ms.)

### Step 4 — full run

```sh
go run ./loadtest/driver -n 150 -c 20
```

Increase `-c` (30, 50, …) across runs to find the concurrency knee — the point
where `otp-arrival` p95 or failure counts start climbing.

Driver flags:

| flag | default | meaning |
|------|---------|---------|
| `-base` | `http://localhost:8080` | backend base URL |
| `-mailpit` | `http://localhost:8025` | Mailpit base URL |
| `-n` | 150 | number of samples |
| `-c` | 20 | max concurrent workers |
| `-mail-timeout` | 20s | per-email polling timeout |
| `-poll` | 200ms | Mailpit poll interval |
| `-purge` | true | clear Mailpit before the run |

## 5. Reading the output

```
===== email-auth load test =====
samples:      150
succeeded:    150
failed:       0
wall clock:   6.2s
throughput:   24.1 completed samples/sec

latency (successful samples only)
step             p50      p95      p99      min      max
forgot(HTTP)      12ms     31ms     44ms      6ms     58ms   <- step 1 API only (excludes send)
otp-arrival      210ms    480ms    900ms    180ms   1.2s    <- async SMTP path for the reset mail
verify(HTTP)      40ms     95ms    140ms     22ms    170ms   <- includes password regeneration (bcrypt)
pw-arrival       205ms    460ms    880ms    170ms   1.1s    <- async SMTP path for credentials mail
login(HTTP)       35ms     80ms    120ms     20ms    150ms
chain(total)     620ms    1.3s     1.8s     500ms    2.1s
```

- **`*-HTTP`** rows = server request handling. `verify` is the heaviest because it
  runs bcrypt to hash the regenerated password.
- **`*-arrival`** rows = the async email path (time from the HTTP 200 to the mail
  being visible in Mailpit). This is the real signal for SMTP-send capacity and
  is invisible to plain HTTP load tools.
- **`failures by step`** shows where dropped samples died:
  - `otp_email` / `cred_email` — mail never arrived in time (SMTP saturation, or
    the account was throttled from a prior run).
  - `otp_parse` / `pw_parse` — mail arrived but the code/password regex missed
    (template drift — check `email_smtp.go`).
  - `verify` / `login` — API rejected the request.

## 6. Re-running & the throttle

The throttle clock (`last_forgot_reset_at`) is stamped **before** the async send,
so even a run that errored on delivery leaves accounts throttled. Re-running
within 5 minutes **without reseeding** throttles every account: forgot still
returns 200, but no mail is sent, so every sample fails at `otp_email`. This is
expected and doubles as a throttle sanity check. To load-test again for real, do
one of:

```sh
# option A: wait 5 minutes, or
# option B: clear the throttle clock
docker exec sql-quiz-db psql -U sqlquiz -d sqlquiz -c \
  "UPDATE app.system_users SET last_forgot_reset_at = NULL WHERE username LIKE 'loadtest%';"
```

## 7. Teardown

```sh
DATABASE_URL='postgres://sqlquiz:PASS@localhost:5432/sqlquiz?sslmode=disable' \
  go run ./loadtest/seed -teardown
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml down     # add -v to wipe the DB volume
```

## 8. Scope & caveats

- Mailpit removes Gmail's real-world limits, so this measures **application +
  async-send throughput**, not production email deliverability. A separate
  small-N smoke test against real Gmail is the way to validate live delivery.
- The seed reuses one bcrypt hash for all rows (the initial password is never
  used), so seeding is fast; do not read anything into login timings from the
  initial password.
- The driver polls Mailpit rather than subscribing; `otp-arrival` includes up to
  one `-poll` interval of quantisation (200ms default). Lower `-poll` for finer
  resolution at the cost of more API calls.
