# HTTPS via self-signed certificate — stopgap design

Date: 2026-07-17
Status: approved, pending implementation plan

## Context

The system is deployed on an on-premise VPS that is reachable only from the
university LAN. Students reach it over the university VPN, by typing a bare IP
address into the browser. There is no public DNS name for the host, no inbound
route from the internet, and the box is not assumed to have outbound internet.

Today the stack is HTTP-only:

- `frontend/nginx.conf:2` — nginx listens on `:80` only.
- `docker-compose.yml:53` — compose publishes `80:80`.
- `docker-compose.yml:34` — the backend's `ALLOWED_ORIGINS` is hardcoded to
  `http://localhost,http://localhost:80`, which does not describe the real
  deployment.

## Threat model

The adversary is not an outsider. It is a third-year student on the same VPN as
the server and as every other student's session. The VPN authenticates them
*in*; it does not isolate them from each other or from the server.

Authentication is a JWT bearer token held in `localStorage`
(`frontend/src/services/practice.ts:118-122`). Over plaintext HTTP, an ARP-spoof
on the VPN subnet (bettercap/ettercap — commodity tooling) puts every other
user's requests through the attacker's machine, exposing:

- the login POST body (password), and
- the `Authorization: Bearer <jwt>` header on every subsequent request.

A captured lecturer or admin JWT yields exam questions ahead of the exam, every
submission, and grade modification. No application vulnerability is required;
the credentials are readable on the wire.

Additionally, the exam SSE stream passes the JWT in the URL query string,
because `EventSource` cannot set an `Authorization` header
(`frontend/src/composables/useExamStream.ts:78`):

```
/api/v1/quiz/exams/${id}/stream?access_token=${token}
```

nginx's default `combined` log format logs `$request`, which includes the query
string. Live JWTs are therefore being written to the frontend container's
access log on disk, and into browser history.

## Decision

Serve the application over TLS using a **self-signed certificate with an IP
SAN**, generated on the server. Students will see a browser certificate warning
and click through it once per browser.

This is explicitly a **stopgap**, chosen for speed over completeness.

### Why this is worth doing

It closes the passive-sniffing attack above, which is the likely attack. All
credentials and tokens become unreadable on the wire.

### What it does NOT do — accepted risks

- **It does not stop active MITM.** Once students are habituated to clicking
  through a certificate warning, the attacker can present his own self-signed
  certificate and be indistinguishable from the real server. This is the
  central limitation and the reason this is a stopgap rather than an end state.
- **The IP SAN is throwaway work.** No publicly-trusted CA issues certificates
  for private IP addresses. Moving to a trusted certificate later requires
  obtaining a DNS hostname, at which point this certificate is discarded.

### Rejected alternatives

- **Trusted certificate from university IT** (a hostname in the campus zone plus
  a cert from their CA service). Best outcome — zero client-side setup, no
  warning, no root CA handed to students. Rejected for now on time-to-deploy;
  remains the recommended end state.
- **Internal CA (`step-ca`) with name constraints.** Works air-gapped and yields
  no warnings, but requires installing a root CA on every student device. A root
  CA vouches for *any* site, so a leaked CA key would let its holder MITM every
  HTTPS site of everyone who installed it — and that key would sit on the box
  the adversary is most motivated to compromise. Rejected as disproportionate
  for a stopgap.

## Non-goals

- Obtaining a DNS hostname or a publicly-trusted certificate.
- Moving the SSE token out of the query string (see Follow-ups).
- Tightening the CSP's `'unsafe-inline'`.
- Any change to authentication, session lifetime, or the JWT itself.

## Design

### 1. `.gitignore` — first change

Add `certs/`.

This lands before any certificate exists. This repository has already committed
one live secret in plaintext (`cloudflare_tunneling.sh:1`, a Cloudflare Tunnel
token, present in git history from commit `c098a1f`); a committed TLS private
key would defeat the entire change.

### 2. New — `scripts/gen-selfsigned-cert.sh`

Run on the server. Signature: `./scripts/gen-selfsigned-cert.sh <server-ip> [--force]`.

Behaviour:

- Generates an EC P-256 private key and a self-signed certificate, 825-day
  validity.
- Sets `subjectAltName=IP:<server-ip>`. This is required, not cosmetic: modern
  browsers ignore the legacy CN field entirely, and a certificate without a
  matching SAN produces a hard failure with no click-through option.
- Sets `keyUsage=digitalSignature` and `extendedKeyUsage=serverAuth`. (Not
  `keyEncipherment` — it is meaningless for an EC key and some validators
  object.)
- Writes `certs/server.crt` and `certs/server.key`, with the key at mode 600.
- Exits nonzero without writing if `certs/server.crt` already exists, unless
  `--force` is passed. Regenerating silently would invalidate the exception
  every student has already stored in their browser.

### 3. `frontend/nginx.conf`

**a. Scrub tokens from the access log.** Define a log format that logs `$uri`
(path only) rather than `$request` (which carries the query string), and select
it. `log_format` is only valid in nginx's `http` context; this file is installed
to `/etc/nginx/conf.d/default.conf` (`frontend/Dockerfile:21`), which is
included from inside `http`, so the directive is valid here.

**b. Redirect `:80`.** The existing `:80` server block reduces to
`return 301 https://$host$request_uri`. Port 80 stays published so existing
bookmarks and typed URLs keep working.

**c. New `:443 ssl` server block** carrying the current content (static root, SSE
passthrough, `/api/` proxy — all unchanged), plus:

- `ssl_certificate /etc/nginx/certs/server.crt;`
  `ssl_certificate_key /etc/nginx/certs/server.key;`
- `ssl_protocols TLSv1.2 TLSv1.3;`
- session cache enabled, session tickets off.

The two proxy blocks already set `X-Forwarded-Proto $scheme`, which now
correctly reports `https` with no change needed.

**d. CSP.** Drop the `http:` source from `default-src`. All origins are
same-origin through this proxy, so nothing legitimate depends on it.

**e. Fix the `add_header` inheritance bug (pre-existing).** nginx does not merge
`add_header` across levels: a block that declares any `add_header` discards
every one inherited from its parent. The static-asset location
(`nginx.conf:26-29`) declares `add_header Cache-Control`, so **the five
security headers at `nginx.conf:14-18` are currently not sent on any `.js`,
`.css`, or image response.** This is live today and independent of TLS. Fix by
removing the redundant `add_header Cache-Control` and letting the existing
`expires 30d` emit `Cache-Control` on its own, which leaves no `add_header` in
that block and restores inheritance.

**No HSTS.** RFC 6797 requires that a user agent not record an HSTS policy for a
host addressed by IP literal, so browsers will not honour it here. It is also ignored over an untrusted
certificate. HSTS becomes appropriate on the move to a hostname with a trusted
certificate, and should be introduced then with a short `max-age` first.

### 4. `frontend/Dockerfile`

`EXPOSE 443` alongside the existing `EXPOSE 80` (documentation only; does not
affect published ports).

### 5. `docker-compose.yml`

- `frontend.ports`: add `"443:443"`, keep `"80:80"`.
- `frontend.volumes`: add `./certs:/etc/nginx/certs:ro`.
- `backend.environment`: replace the hardcoded `ALLOWED_ORIGINS` with
  `${ALLOWED_ORIGINS:-https://localhost}`, sourced from `.env`. This removes the
  server's IP from version control and makes the value correct for the real
  deployment.

### 6. `.env.example`

Document `ALLOWED_ORIGINS`, e.g. `ALLOWED_ORIGINS=https://10.20.30.40`, noting
it must match the scheme and host students actually use.

### 7. `scripts/deploy.sh`

Add a preflight check, next to the existing `.env` guard (`scripts/deploy.sh:21-24`)
and following its style: if `certs/server.crt` or `certs/server.key` is missing,
print a message naming `gen-selfsigned-cert.sh` and exit nonzero.

Without this, deploying to a box with no certificate starts an nginx that exits
immediately on the missing file. The stack goes down and it presents as a broken
deploy rather than a missing certificate.

### 8. `MANUAL.md`

A short section for students: the certificate warning is expected, it is not a
sign of compromise, and here is how to proceed past it in Chrome and Firefox.
State plainly that this is a temporary measure.

## Verification

Run on the server after deploy:

1. `curl -kI http://<ip>/` → `301`, `Location: https://<ip>/`.
2. `curl -k https://<ip>/` → serves `index.html`.
3. `curl -k https://<ip>/api/v1/...` → backend responds through the proxy.
4. `openssl s_client -connect <ip>:443 </dev/null 2>/dev/null | openssl x509 -noout -text | grep -A1 'Subject Alternative Name'`
   → shows `IP Address:<ip>`.
5. `curl -kI https://<ip>/assets/<some>.js` → security headers from
   `nginx.conf:14-18` are present (regression test for the `add_header` fix).
6. In a browser: log in, start an exam, confirm the SSE stream connects and
   exam state transitions arrive.
7. `docker compose logs frontend | grep access_token` → no matches (regression
   test for the log scrub).

## Rollback

Revert `docker-compose.yml` and `frontend/nginx.conf`, then redeploy. Port 80
remains published throughout, so reverting restores plain HTTP with no DNS or
client-side change. The certificate files are inert once nginx no longer
references them.

## Follow-ups (each its own spec)

1. **Rotate the leaked Cloudflare Tunnel token.** `cloudflare_tunneling.sh:1`
   contains a live token, committed and present in git history. Anyone with
   repository read access can run that tunnel. Revoke it in the Cloudflare
   dashboard and move the replacement to `.env`. Unrelated to TLS; do it
   regardless.
2. **Move the SSE token out of the query string.** Have the backend issue a
   short-lived, single-use ticket for the stream and have `EventSource` send
   `?ticket=` instead of the JWT. This addresses browser history and `Referer`
   leakage, which the log scrub does not. Touches Go backend, frontend, and
   nginx.
3. **Obtain a hostname and a trusted certificate.** The end state. Removes the
   click-through warning and closes the active-MITM gap this design accepts.
   Ask university IT for a name in the campus zone plus a certificate from their
   CA service. At that point: re-issue with a DNS SAN, add HSTS, and drop this
   design's IP SAN.
