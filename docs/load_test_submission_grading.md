# Load Test: Submission Grading (`/api/v1/quiz/submit`)

Measures the throughput and latency of the **exam submission-grading** path — the
one that actually sizes the server — under a closed-loop concurrency sweep.
Tooling: `backend/loadtest/gradeseed` (fixture) + `backend/loadtest/gradedriver`
(load).

## Why this test, not the email one

`docs/load_test_email_auth.md` exercises the forgot-password flow. It does **not**
touch grading, which has a fundamentally different bottleneck:

- **Exam DML (INSERT/UPDATE/DELETE)** is serialised by an in-process
  **single-worker FIFO queue** (`services.NewSubmissionQueue(256, 30s)`): one DML
  eval at a time, per backend. Throughput is `1 / per-op-time`, independent of
  concurrency; extra concurrency only grows the queue (latency).
- **Exam SELECT** runs lock-free under the read-only role: parallel, bounded by
  the pgx pool (`MaxConns=50`) and Postgres CPU.

So DML and SELECT must be measured separately, and DML latency is expected to
climb linearly with concurrency (Little's Law: `latency ≈ concurrency / throughput`).

## How to run

Against the loadtest compose override (exposes db:5432, backend:8080), or any
running backend + reachable DB:

```bash
# 1. Seed: lecturer + N students + one ACTIVE exam with a SELECT and a DML question
DATABASE_URL='postgres://USER:PASS@localhost:5432/DB?sslmode=disable' \
  go run ./loadtest/gradeseed -base http://localhost:8080 -n 100 -out loadtest/grade_manifest.json

# 2. Sweep concurrency, hold each level 6s (1s warmup discarded)
go run ./loadtest/gradedriver -manifest loadtest/grade_manifest.json -c 1,2,4,8,16,32,64 -d 6s

# 3. Teardown (drops the gradeload exams + accounts + sandboxes)
DATABASE_URL=... go run ./loadtest/gradeseed -teardown
```

The DML question defaults to `UPDATE works_on SET hours = 40` and SELECT to
`SELECT ssn, fname, lname FROM employee ORDER BY ssn` (both against the default
`public` company schema). Override with `-dml-sql` / `-select-sql` / `-dml-table`
to test a heavier query — **per-op time is the whole ballgame for DML.**

## Measured baseline (2026-07-13)

Test box: **8 logical cores (i5-1135G7), 7.5 GB RAM, local disk.** Driver +
backend + Postgres co-located (no network latency — real remote clients add RTT).
Warm sandbox, small tables (`works_on` = 48 rows). Single backend, single Postgres.

```
===== SELECT workload =====             ===== DML workload =====
conc     req/s   p50   p95   p99        conc     req/s   p50    p95    p99
1        268.0   3ms   4ms   5ms        1        163.3   5ms    7ms    8ms
2        473.9   4ms   5ms   6ms        2        293.3   6ms    8ms    12ms
4        707.8   5ms   7ms   11ms       4        265.3   13ms   22ms   43ms
8        995.9   7ms   10ms  14ms       8        265.5   26ms   52ms   96ms
16       1203.2  12ms  17ms  22ms       16       266.0   55ms   97ms   127ms
32       1217.5  25ms  35ms  42ms       32       278.7   107ms  171ms  257ms
64       1299.3  48ms  63ms  74ms       64       262.3   243ms  336ms  456ms
```
Zero errors at every level.

### Reading it

- **DML throughput is flat at ~265 req/s from c≥4** — the single worker is
  saturated; per-op ≈ 3.8 ms on this box/data. Adding concurrency does **not**
  raise throughput; it only adds queue wait. p95 grows linearly: `p95 ≈ c / 265`.
  **The knee is ~c=4.**
- **SELECT scales** to ~1300 req/s (plateaus near c=16 as Postgres/CPU saturates),
  p95 stays low.
- More CPU cores raise SELECT and capture, **not** DML throughput. The DML lever
  is **per-op time** (table size + disk + sandbox cache), i.e. faster Postgres/disk
  and enough `shared_buffers` to keep the sandbox hot — not core count.

## Sizing implications

Exam reality: students submit *occasionally* (think + type), so the sustained DML
rate is far below the ceiling; the risk is the **deadline burst**, which the
256-deep queue absorbs (beyond 256 queued DML → `503 queue full`).

For a single DML burst of K students, expected p95 ≈ `K / 265 s` on comparable
hardware: 50→~0.2s, 150→~0.6s, 256→~1s (then the cap bites).

| Scenario | Recommended | Notes |
|----------|-------------|-------|
| ≤150 concurrent (typical exam room) | **4 vCPU / 8 GB / SSD**, single node | Already exceeded by the 8-core test box; comfortable p95. |
| 150–400, or heavy DML (big tables/slow queries) | **8 vCPU / 16 GB / NVMe**, Postgres on its own node, `shared_buffers` ≈ 4 GB | Per-op grows with table size → ceiling drops ∝ `1/per-op`; fast disk matters most. |
| >256 simultaneous DML submitters per exam | Raise `NewSubmissionQueue` capacity (cmd/main.go), else 503s | Or stagger deadlines. |

**Do not horizontally scale the backend to speed up DML for one exam:** the queue
is in-process per instance, so two replicas run two workers against the same
exam's shared sandbox → savepoint interference. Scale DML **vertically** (faster
core + faster Postgres/disk), or replace the in-process queue with a per-exam
Postgres advisory lock (the pattern already exists for practice-reset) before
adding replicas.

## Caveats

- Numbers are for warm cache + tiny tables + co-located, zero-network. Re-run
  against production-like data, table sizes, disk, and a remote driver for exact
  ceilings — especially the DML per-op time, which sets the whole DML ceiling.
- 8-thread laptop CPU thermally throttles; a real server does better on SELECT.
