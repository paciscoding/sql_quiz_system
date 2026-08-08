# Lecturer-Authored Seed Rows for UPDATE/DELETE Questions

**Date:** 2026-08-08
**Status:** Approved, ready for implementation
**ARCH range:** ARCH-043 … ARCH-052
**Supersedes in part:** the 2026-08-07 DML auto-seed spec (ARCH-037 … ARCH-042),
whose derivation becomes a fallback rather than the only mechanism.

## Problem

ARCH-037/038 shipped a backend-derived auto-seed: when a DML model answer
affects 0 rows, the backend parses the answer's `WHERE` clause, builds a
one-row `INSERT`, and applies it inside the rolled-back evaluation savepoint.

Derivation understands a deliberately narrow grammar and refuses everything
else. Refusal is a designed outcome, but it leaves the lecturer with no way
forward for the cases it refuses:

- predicates outside `<col> = <literal> [AND …]` — `IN`, `>`, `OR`, `LIKE`;
- a pinned value whose foreign-key parent row does not exist, so the derived
  `INSERT` fails;
- questions that need more than one row, or rows in more than one table, to be
  meaningfully graded.

In all of these the lecturer knows exactly which rows should exist. The system
has no field in which to say so.

A second, smaller problem: the authoring dropdown offers only `QUERY` and
`DML`. The lecturer cannot state which DML kind the question is, so the UI
cannot show a seed field for exactly the kinds that need one.

## Goal

The lecturer types the seed rows explicitly, as SQL, in the authoring form for
UPDATE and DELETE questions. Those rows are inserted inside the same
rolled-back savepoint that already carries the derived seed, on all four
execution paths, so:

- the lecturer never edits the imported category or curriculum schema;
- the seeded rows are rolled back on every path, and the real schema is never
  modified;
- capture, grading, and the student's preview all observe the same seeded
  state, so grading discriminates;
- derivation still rescues questions where the lecturer wrote nothing, so no
  existing question regresses.

## Non-goals

- No relaxation of ARCH-008. A question whose model answer still affects 0 rows
  after seeding is still blocked at save time.
- No seed field for `QUERY` or `INSERT` questions (see ARCH-044).
- No backfill of `seed_sql`. Existing questions keep `NULL` and keep using
  derivation exactly as today.
- No general SQL rewriting. The seed is executed as written, not analysed
  beyond the INSERT-only gate.

## Design decisions

### ARCH-043 — Storage: two additive columns, `question_type` CHECK untouched

On both `app.questions` and `app.practice_questions`:

| column | type |
|---|---|
| `dml_kind` | `TEXT NULL CHECK (dml_kind IS NULL OR dml_kind IN ('INSERT','UPDATE','DELETE'))` |
| `seed_sql` | `TEXT NULL` |

`question_type` keeps its existing `CHECK (question_type IN ('QUERY','DML'))`
(`postgres_schema.go:907`). Widening it to four values would require rewriting
the constraint, `UPDATE`-ing every existing `'DML'` row, and auditing roughly
twenty `Type == "DML"` / `Type != "QUERY"` branches across
`controllers/`, `repository/`, and `services/` on a **deployed** database. The
four-way split is a presentation and validation concern, not a grading one, so
it is carried in a new column and every existing branch is left alone.

Both columns are added to the `CREATE TABLE` bodies **and** to the idempotent
`extraMigrations` slice in `backend/internal/repository/postgres_schema.go`,
matching the established `ADD COLUMN IF NOT EXISTS` pattern used by
`expected_rows_affected` and `auto_seed` (`:1086-1094`).

**Backfill.** Runs in Go, not SQL — a leading `--` or `/* */` comment before
the statement keyword cannot be stripped reliably by a Postgres regex, and
`dml_autoseed.go`'s `stripSQLComments` already does it correctly. A new
`migrateDMLKind(ctx)` in `postgres_schema.go` selects
`id, correct_sql` for rows `WHERE question_type = 'DML' AND dml_kind IS NULL`,
computes the kind with the shared scanner, and writes it back. The
`dml_kind IS NULL` guard makes it a no-op on every subsequent boot.

A row whose `correct_sql` matches none of the three keeps `dml_kind = NULL`;
the UI falls back to showing it as `DELETE`-less plain DML and the lecturer
re-picks the kind on next edit. `NULL` is never an error.

Go models gain `DMLKind string \`json:"dml_kind,omitempty"\`` and
`SeedSQL string \`json:"seed_sql,omitempty"\`` on `models.Question` and
`models.PracticeQuestion`, carried through `PracticeQuestion.AsQuestion()` and
persisted by the insert/upsert/update/read statements in `postgres_questions.go`
and `postgres_practice.go`.

### ARCH-044 — Field scope: UPDATE and DELETE only

The seed field is offered for `dml_kind IN ('UPDATE','DELETE')`.

Both suffer the identical vacuous-0-rows failure: with no matching row present,
a correct answer and a statement that does nothing produce the same end state,
so the question cannot be graded. `INSERT` does not — an `INSERT` model answer
can only affect 0 rows via `INSERT … SELECT` over an empty source, which
seeding cannot fix, and which `isAutoSeedIneligibleInsert` already refuses by
name.

The API rejects a non-empty `seed_sql` with HTTP 400 when `type == "QUERY"` or
`dml_kind == "INSERT"`. The frontend clears the field when the kind changes
away from UPDATE/DELETE, so the rejection is a backstop, not a normal path.

### ARCH-045 — Precedence: lecturer wins, derivation is the fallback

At capture:

- `seed_sql` non-empty → apply it, then run `correct_sql`. **Derivation is not
  attempted at all**, and `auto_seed` is stored `NULL`.
- `seed_sql` empty → today's path runs unchanged, byte for byte, including the
  0-row derivation rescue and every ARCH-039 refusal message.

The two mechanisms are never both active for one question. `auto_seed` remains
backend-derived and response-only; it is never accepted from a client request
body.

**The lecturer's seed runs whenever it is present, not only on the 0-row
branch.** Conditional seeding would make capture and grading disagree about
whether the row exists: capture might skip the seed (model answer already
affected rows) while grading applied it, or vice versa after a category
re-import. Unconditional application keeps the four paths observing one state.

### ARCH-046 — Sandbox: three independent layers

New `validateSeedSQL(seedSQL string) ([]string, error)` in
`backend/internal/repository/dml_autoseed.go`, reusing that file's existing
scanner — `stripSQLComments`, `splitTopLevelSemicolons`, `matchKeywordAt` — so
comment stripping and quote handling cannot diverge between the two features.

1. **INSERT-only allowlist.** Comments are stripped, the input is split on
   top-level semicolons, empty segments are discarded, and every remaining
   statement must lead with the `INSERT` keyword. Anything else is refused with
   a message naming the offending leading keyword. Bounds: at most 20
   statements, at most 8192 bytes of input; both refuse rather than truncate.
   An all-whitespace or all-comment input is not an error — it is "no seed",
   and is normalised to the empty string before storage.
2. **Least privilege.** The seed executes *after* `SET LOCAL ROLE`, so it holds
   only the non-superuser student role's grants. The student login roles are
   `NOINHERIT` (`postgres_student_pool.go:133`), so hoisting the seed above the
   `SET LOCAL ROLE` would leave the connection with no privileges at all and
   the INSERT would fail outright.
3. **Always rolled back.** The seed executes inside the already-taken
   `SAVEPOINT student_eval` (capture uses its own transaction, discarded via
   `errCaptureRollback`). No path commits it.

Any one layer would be sufficient. All three are present so that no single
mistake leaks a write into an imported schema.

Statements are executed one at a time via `tx.Exec`, never as one multi-command
string — the QUERY path is pinned to the extended protocol
(`postgres.go:160`, `postgres_student_exec.go:27`), whose `Parse` accepts at
most one command per message.

### ARCH-047 — Execution: one helper, the four existing sites

```go
func applyQuestionSeed(ctx context.Context, tx pgx.Tx, seedSQL string, plan *AutoSeedPlan) error
```

Non-empty `seedSQL` → validate, then `tx.Exec` each statement in order.
Empty `seedSQL` → delegate to `applyAutoSeed(ctx, tx, plan)`, whose existing
nil/empty short-circuit is preserved, so a question with neither seed issues
zero extra statements.

It replaces the current `applyAutoSeed` call at each site, at the same point in
the sequence — after `SET LOCAL ROLE`, inside the savepoint, before the answer
statement:

| path | site | order |
|---|---|---|
| capture (practice) | `postgres_practice.go` `captureExpectedResult` | seed → run `correct_sql` → post-image |
| capture (exam) | `postgres_questions.go:117` | same |
| grading | `postgres_evaluation.go` `evaluateDML` | savepoint → set role → **seed** → student stmt → post-image |
| preview | `postgres_evaluation.go` `evaluateDMLPreview` | savepoint → set role → **seed** → student stmt → preview rows |

Applying the seed on *all four* paths is mandatory. If grading or preview
skipped it, a student's correct `DELETE` would affect 0 rows and be marked
wrong.

The ARCH-021 / ARCH-024b ordering rule is unchanged: a failed seed leaves the
transaction aborted exactly like a failed student statement, and
`ROLLBACK TO SAVEPOINT` still runs **before** `RESET ROLE` on every exit path.

**Capture nests its own savepoint** around the seed, as the derivation path
already does with `SAVEPOINT autoseed`, so a failed lecturer seed does not
poison the capture transaction before the refusal message is recorded.

### ARCH-048 — Failure messages

| situation | outcome |
|---|---|
| `validateSeedSQL` refuses | save returns HTTP 400 naming the offending statement; nothing is written |
| seed INSERT fails at capture | `expected_result_status = 'failed'`, `capture_error = "Seed INSERT failed: <postgres error>."` |
| seed ran, `correct_sql` still affects 0 rows | `expected_result_status = 'failed'`, `capture_error = "Your seed rows were inserted, but the correct SQL still affected 0 rows. Check the WHERE clause of the correct SQL against the values you seeded."` |
| seed fails at grade or preview time | existing `ValidationResult{Status: "error"}` shape, feedback naming the question as misconfigured, **never** blaming the submission; subject to the ARCH-018 dual-schema feedback scrub like every other message on that path |

No new failure mode reaches the lecturer or the student as an HTTP 500.

### ARCH-049 — Sequence grants (defect found during design)

`GrantSchemaPermissions` (`postgres_schema.go:175`) grants
`SELECT, INSERT, UPDATE, DELETE ON ALL TABLES` plus matching default
privileges, but grants **no privilege on sequences**. Any seed `INSERT` into a
table with a `serial` or identity column omits that column, so Postgres calls
`nextval()` as the student role and fails with `permission denied for
sequence`.

This is a live defect in the already-shipped derived auto-seed — ARCH-038's
column reconciliation deliberately omits defaulted columns, and a `serial`
column is a defaulted column — not something this change introduces. It is
fixed here because the lecturer-authored seed would hit it constantly.

Fix, additive:

- `GrantSchemaPermissions`: add
  `GRANT USAGE ON ALL SEQUENCES IN SCHEMA <s> TO exam_student` and
  `ALTER DEFAULT PRIVILEGES IN SCHEMA <s> GRANT USAGE ON SEQUENCES TO exam_student`.
- `grantPreviewSchemaPermissions`: the same two, for `exam_student_preview`,
  **only** when `allowDML` is true. The read-only preview roles must not gain
  sequence usage.
- Existing schemas must be re-granted. The grant helpers are idempotent, so the
  boot path re-runs them over every existing category and sandbox schema.

### ARCH-050 — API surface

Practice and exam question create/update request structs gain:

```go
DMLKind string `json:"dml_kind"`
SeedSQL string `json:"seed_sql"`
```

Both lecturer preview endpoints — `PracticeController.LecturerPreview`
(`POST /lecturer/practice/preview`) and `LecturerController.PreviewCorrectSQL`
(`POST /lecturer/preview`) — also accept `seed_sql`, and apply it through
`applyQuestionSeed` before running the lecturer's SQL, so the lecturer can see
the seeded state before committing to a save.

`type` on the wire stays `QUERY | DML`; the existing
`"type must be QUERY or DML"` validation is unchanged. The frontend maps its
four-way dropdown down to `type` + `dml_kind`.

Validation on every write path: `dml_kind` must be empty when
`type == "QUERY"`, and one of `INSERT`/`UPDATE`/`DELETE` when `type == "DML"`;
`seed_sql` must be empty unless `dml_kind IN ('UPDATE','DELETE')`; a non-empty
`seed_sql` must pass `validateSeedSQL`. Each violation is an HTTP 400 with a
specific message.

### ARCH-051 — Authoring UI

`PracticeAuthoring.vue` and `lecturer/ExamQuestionsPanel.vue`:

- The type `<select>` becomes four options — **QUERY**, **INSERT**, **UPDATE**,
  **DELETE** — bound to a computed whose setter writes `question_type`
  (`'QUERY'` for QUERY, `'DML'` otherwise) and `dml_kind`, and whose getter
  reconstructs the four-way value from the pair. Existing questions loaded with
  `question_type = 'DML'` and a `NULL` `dml_kind` display as `DML` until the
  lecturer picks a kind; saving requires a kind.
- **Target Table** continues to show for all three DML kinds — same condition as
  today (`question_type === 'DML'`), just re-expressed.
- A new **"Seed rows (sandbox only)"** `<textarea>` renders directly beneath
  **Correct SQL**, only when `dml_kind` is `UPDATE` or `DELETE`. Helper text,
  verbatim: *"These INSERT statements run inside a transaction that is always
  rolled back. They give your UPDATE/DELETE something to act on. They never
  modify the imported database."*
- Changing the kind away from UPDATE/DELETE clears `seed_sql` before submit, so
  a hidden field can never be silently persisted.
- The existing read-only **"Auto-seeded row"** panel (ARCH-042) stays, now shown
  only when `seed_sql` is empty **and** `auto_seed` is non-null — the two are
  mutually exclusive by ARCH-045, so both must never render at once.
- `frontend/src/services/practice.ts` and
  `frontend/src/components/lecturer/types.ts` gain `dml_kind?: string` and
  `seed_sql?: string` on their question interfaces.

### ARCH-052 — `seed_sql` never reaches a student

`auto_seed` is stripped from every student-facing response because it carries
the literal values the model answer's `WHERE` clause pins — handing a student
the answer. The scrub sites are `quiz_controller.go:170`,
`practice_controller.go:65` (list) and `practice_controller.go:104` (get), and
`controllers/autoseed_leak_http_test.go` asserts the responses contain no
`auto_seed` field at all.

`seed_sql` leaks strictly more: it is lecturer-written SQL containing the exact
values the answer must match. It is scrubbed at the same three sites, in the
same place, by setting the field to `""` so `json:"seed_sql,omitempty"` drops
it from the payload entirely — and the existing leak test is extended to assert
its absence alongside `auto_seed`.

`dml_kind` is **not** secret and is not scrubbed; a student can already read the
question type.

## Known limitations

Stated explicitly rather than hidden:

1. **Dual-schema questions.** Grading runs on the real schema, preview on the
   decoy. The seed runs on both. If the decoy's shape for the seeded tables
   differs from the real schema's, the preview seed fails and the student sees
   a "question is misconfigured" message rather than a wrong grade. Same
   trade-off as ARCH-042's limitation 1.
2. **Category re-import.** Re-importing a category's database does not
   re-validate a stored `seed_sql`. A seed referencing a dropped column fails at
   grade time as a misconfiguration message. Re-saving the question surfaces it
   to the lecturer.
3. **Ordering within the seed.** Statements execute in written order. A
   lecturer who inserts a child row before its foreign-key parent gets the
   Postgres error at save time, unmodified.
4. **No deduplication against existing rows.** A seed that inserts a row whose
   primary key already exists fails with a unique-violation at save time. This
   is deliberate: silently skipping it would hide a mistaken assumption about
   the category data.

## Testing

- **`validateSeedSQL` unit tests** (no DB): every refusal class — a leading
  `UPDATE`/`DELETE`/`SELECT`/`CREATE`, more than 20 statements, over-length
  input, an unterminated quote; and every accept — one statement, several
  statements, trailing semicolon, comments interleaved, an `INSERT` whose
  values contain a semicolon inside a string literal.
- **Precedence**: a question saved with `seed_sql` stores `auto_seed = NULL` and
  never calls `deriveAutoSeed`; a question saved without one still derives
  exactly as today.
- **Capture integration**: a `DELETE` against a row that exists only in
  `seed_sql` saves with `expected_result_status = 'captured'` and
  `expected_rows_affected = 1`; a seed that violates a constraint fails with the
  ARCH-048 message.
- **Schema isolation** (the core guarantee): after a capture, a grade, and a
  preview of a seeded question, `SELECT count(*)` on every table the seed
  touched, in the real category schema, is unchanged, and no seeded row is
  present.
- **Grading correctness**: the same `DELETE` submitted by a student is graded
  correct; an empty or no-op statement is graded incorrect — the discrimination
  ARCH-008 exists to protect.
- **Sequence grants** (ARCH-049): a seed `INSERT` into a table with a `serial`
  primary key succeeds at capture, grade, and preview. This test fails before
  the grant fix and passes after.
- **Migration idempotency**: a second boot re-runs both `ADD COLUMN IF NOT
  EXISTS` statements and the guarded backfill without error and without
  changing any row; pre-existing rows read back `seed_sql = NULL` and a
  `dml_kind` matching their `correct_sql`'s leading keyword.
- **Regression**: questions with neither `seed_sql` nor a derived plan produce
  byte-identical `expected_result` to before the change.
- **Leak** (ARCH-052): `controllers/autoseed_leak_http_test.go` extended so the
  student list/get responses for practice and exam contain no `seed_sql` field,
  with a fixture question whose `seed_sql` is non-empty in the database.

Backend tests touching the shared database must run with `go test -p 1` — a
plain `go test ./...` on a shared DB fakes regressions.
