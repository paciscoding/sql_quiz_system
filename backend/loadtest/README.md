# Email-auth load test (~150 samples)

Drives the forgot-password → OTP → verify → login flow end to end. The OTP never
comes back over HTTP — it is only emailed — so we run **Mailpit** as a local SMTP
sink and read the code back out of its REST API.

## Why the shape it is

- **150 distinct accounts, hit once each.** A per-account 5-minute throttle
  (`ForgotResetThrottle`) silently drops a second forgot-password within the
  window, so you can't hammer one account. N samples ⇒ N accounts.
- **Mailpit, not Gmail.** `net/smtp.SendMail` opens a fresh STARTTLS+auth
  connection per email from a goroutine per request; 150 of those against Gmail
  hits concurrent-connection and daily-volume caps (and spams real inboxes).
  Mailpit is a catch-all with no limits.
- **Async send.** The email goes out in a goroutine, so the forgot/verify HTTP
  latencies exclude delivery. The `otp-arrival` / `pw-arrival` metrics measure
  the async SMTP path separately.

## Run order

All commands run from `backend/` (the Go module root).

### 1. Bring up db + backend + mailpit

From the repo root:

```sh
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml up -d db backend mailpit
```

> **Networking note.** Mailpit shares the backend container's network namespace
> (`network_mode: service:backend`) and the backend points at `localhost:1025`.
> This is required: Go's `net/smtp` refuses `PlainAuth` over a non-TLS connection
> unless the host is `localhost`. Talking to Mailpit as a separate host
> (`mailpit:1025`) fails with `unencrypted connection`. Sharing the namespace
> makes `localhost` reach Mailpit without any TLS/cert setup.

Confirm the backend picked up Mailpit (not the no-op sender):

```sh
docker logs sql-quiz-backend | grep 'email:'
# want: email: SMTP sender active (from=loadtest@example.com host=mailpit)
```

Mailpit web UI: http://localhost:8025

### 2. Seed the accounts

Uses the db port the override exposes. Replace `PASS` with your `DB_PASSWORD`.

```sh
DATABASE_URL='postgres://sqlquiz:PASS@localhost:5432/sqlquiz?sslmode=disable' \
  go run ./loadtest/seed -n 150
```

Verify:

```sh
docker exec sql-quiz-db psql -U sqlquiz -d sqlquiz -c \
  "SELECT count(*) FROM app.system_users WHERE username LIKE 'loadtest%';"
```

### 3. Smoke test (n=1)

```sh
go run ./loadtest/driver -n 1
```

Watch http://localhost:8025 — a "reset code" mail then a "credentials" mail
should appear, and the run should report 1 succeeded.

### 4. Full run

```sh
go run ./loadtest/driver -n 150 -c 20
```

Raise `-c` (30, 50, …) to find the concurrency knee. Flags:

| flag | default | meaning |
|------|---------|---------|
| `-base` | `http://localhost:8080` | backend URL |
| `-mailpit` | `http://localhost:8025` | Mailpit URL |
| `-n` | 150 | samples |
| `-c` | 20 | max concurrent workers |
| `-mail-timeout` | 20s | per-email poll timeout |
| `-purge` | true | clear Mailpit before the run |

### 5. Throttle sanity check

> **Throttle is stamped even when the send fails.** `beginForgotReset` stamps
> `last_forgot_reset_at` *before* the async send, so a run that errored on
> delivery still leaves accounts throttled. If a run fails at `otp_email` for
> infra reasons, clear the clock (below) before retrying.

Immediately re-run step 4 **without** reseeding: forgot still returns 200 but no
new emails arrive (all accounts throttled) — the driver will fail at `otp_email`
for every sample. That confirms the throttle path. To load-test again for real:

```sh
# option A: wait 5 minutes
# option B: clear the throttle clock
docker exec sql-quiz-db psql -U sqlquiz -d sqlquiz -c \
  "UPDATE app.system_users SET last_forgot_reset_at = NULL WHERE username LIKE 'loadtest%';"
```

### Teardown

```sh
DATABASE_URL='postgres://sqlquiz:PASS@localhost:5432/sqlquiz?sslmode=disable' \
  go run ./loadtest/seed -teardown
docker compose -f docker-compose.yml -f docker-compose.loadtest.yml down
```

## Interpreting output

```
step             p50      p95      p99      min      max
forgot(HTTP)     ...   <- API throughput of step 1 (excludes email send)
otp-arrival      ...   <- forgot-200 -> reset mail visible: the async SMTP path
verify(HTTP)     ...
pw-arrival       ...   <- verify-200 -> credentials mail visible
login(HTTP)      ...
chain(total)     ...   <- full per-sample wall time
```

`failures by step` breaks down where samples dropped (`otp_email` = OTP never
arrived in time — usually SMTP saturation or the throttle; `login` = bad
regenerated password / session issue; etc.).
