# DML Auto-Seed for Vacuous DELETE/UPDATE Questions

**Date:** 2026-08-07
**Status:** Approved, ready for implementation
**ARCH range:** ARCH-037 … ARCH-042

## Problem

A lecturer authoring a DML question such as

```sql
DELETE FROM employee WHERE ssn = '999999999';
```

against a category database that contains no such row is hard-blocked at save
time. `captureExpectedResult` runs the model answer, sees `RowsAffected() == 0`,
and fails the capture with:

> The model answer affected 0 rows, so this question cannot tell a correct
> answer apart from a statement that does nothing. Fix the WHERE clause of the
> correct SQL, or fix the seed data in this category's database.

The block itself is correct (ARCH-008 / D3): with no row to delete, a correct
student answer and an empty statement produce identical end states, so the
question cannot be graded. But the only remedies offered both cost the lecturer
real work — rewrite the question, or re-import the category's database with the
row added. Adding the row permanently is also undesirable: it pollutes the
shared schema that every other question in the category reads.

Two block sites, one per question kind:

- `backend/internal/repository/postgres_practice.go:445` (practice questions)
- `backend/internal/repository/postgres_questions.go:117` (exam questions)

## Goal

When the model answer affects 0 rows, the system derives the missing row from
the answer's own `WHERE` clause and seeds it **inside the existing evaluation
savepoint**, so:

- the lecturer never edits the category schema;
- the seeded row is rolled back on every path and the real schema is unchanged;
- the model answer, the student's answer, and the student's preview all see the
  same seeded row, so grading discriminates again.

## Non-goals

- No general-purpose SQL rewriting. Derivation understands a deliberately narrow
  grammar and refuses everything else (see ARCH-038).
- No relaxation of ARCH-008. A question that still affects 0 rows after seeding
  is still blocked.
- No backfill. Questions already saved as `failed` are not re-captured; the
  lecturer re-saves them to pick up auto-seed.

## Design decisions

### ARCH-037 — Auto-seed is a rescue, not a default

Capture executes `correct_sql` exactly as it does today. Derivation runs **only**
when the statement is a DML question whose model answer returned
`RowsAffected() == 0`. Questions that already capture successfully take an
identical code path to today, byte for byte.

On the 0-row branch, capture takes a nested savepoint, runs the derived seed,
re-runs `correct_sql`, and accepts the question if the re-run affects > 0 rows.
`expected_result` and `expected_rows_affected` are then captured from the
post-seed state.

**Rationale:** every existing question keeps its current behaviour, and the
blast radius is confined to a branch that previously only produced an error.

### ARCH-038 — Restricted, refusing derivation

No SQL parser is vendored, and `pg_query_go` would introduce a cgo/C dependency.
Because the agreed fallback is *refuse with a specific reason*, a narrow
hand-rolled recogniser is sufficient: anything it does not fully understand is
refused, and refusal is a designed outcome rather than a gap.

New file: `backend/internal/repository/dml_autoseed.go`.

Accepted grammar:

```
DELETE FROM <table> WHERE <col> = <literal> [ AND <col> = <literal> ]*
UPDATE <table> SET <...> WHERE <col> = <literal> [ AND <col> = <literal> ]*
```

`<literal>` is a single-quoted string (`''` escape honoured), a numeric literal,
or `TRUE`/`FALSE`. Everything else is refused, including: `OR`, `NOT`,
parentheses in the predicate, any comparison other than `=`, `IN`, `LIKE`,
`BETWEEN`, `IS NULL`, subqueries, `USING`/`FROM` joins, function calls, column-
to-column comparisons, and multiple statements.

INSERT questions are never derived: an `INSERT` that affects 0 rows is an
`INSERT … SELECT` with an empty source, which seeding cannot fix.

Column reconciliation against `information_schema.columns` for
`question.target_table`:

| column | action |
|---|---|
| pinned by an `=` in the WHERE | included in the seed INSERT |
| not pinned, nullable | omitted |
| not pinned, has a default | omitted |
| not pinned, `NOT NULL`, no default | **refuse**, naming the column |
| pinned but not a real column of the table | **refuse**, naming the column |

### ARCH-039 — Refusal messages replace the one-size error

The single existing string at both block sites is replaced by a message that
states the 0-row fact *and* why auto-seed could not rescue it. Examples:

- `Model answer affected 0 rows. Auto-seed could not build a matching row: the WHERE clause uses '>' on "salary", which does not pin a single value. Use = comparisons, or fix the seed data in this category's database.`
- `Model answer affected 0 rows. Auto-seed could not build a matching row: column "hire_date" is NOT NULL with no default and your WHERE clause does not pin a value for it.`
- `Model answer affected 0 rows. Auto-seed built a row but inserting it failed: <postgres error>.`
- `Model answer affected 0 rows, and still affected 0 rows after auto-seeding a matching row. Check the WHERE clause of the correct SQL.`

Refusals set `expected_result_status = 'failed'` and populate `capture_error`,
exactly as the current block does. No new failure mode reaches the lecturer as
an HTTP 500.

### ARCH-040 — Storage: `auto_seed JSONB`, not rendered SQL

New nullable column `auto_seed JSONB` on `app.questions` and
`app.practice_questions`, added both to the `CREATE TABLE` bodies and to the
idempotent `extraMigrations` slice in
`backend/internal/repository/postgres_schema.go`, matching the established
pattern used by `expected_rows_affected` (`:1086-1087`).

Stored shape:

```json
{ "table": "employee",
  "columns": [ { "name": "ssn", "value": "999999999" } ] }
```

Values are always stored as **strings**, never as JSON numbers or booleans, and
are bound as text parameters; Postgres coerces each to its column's type. This
avoids the float64 round-trip that would silently corrupt large integers and
`NUMERIC` values.

At execution time the statement is rebuilt as
`INSERT INTO <sanitized table> (c1, c2) VALUES ($1, $2)` with the values passed
as **bind parameters**. Literals are never string-interpolated into SQL, so the
seed carries no injection surface, and grading replays exactly what capture
used rather than re-deriving from text.

`AutoSeed json.RawMessage` is added to `models.Question` and
`models.PracticeQuestion`, carried through `PracticeQuestion.AsQuestion()`, and
persisted by the insert/upsert/update/read statements in
`postgres_questions.go` and `postgres_practice.go`.

### ARCH-041 — Execution: after `SET LOCAL ROLE`, inside the existing savepoint

The seed runs on all three DML paths, at one consistent point: **after**
`SET LOCAL ROLE`, **inside** the already-taken `SAVEPOINT student_eval`, and
**before** the answer statement.

Placement after `SET LOCAL ROLE` is load-bearing. The student login roles are
created `NOINHERIT` (`postgres_student_pool.go:133`), so before `SET LOCAL ROLE`
the connection holds no privileges at all and the INSERT would fail. The sandbox
roles do hold `INSERT` on category schemas (`postgres_schema.go:179`), and
`exam_student_preview` holds full DML on decoy schemas
(`postgres_preview_schema.go:150`).

| path | site | order |
|---|---|---|
| capture (practice) | `postgres_practice.go:445` | nested savepoint → seed → re-run `correct_sql` → post-image |
| capture (exam) | `postgres_questions.go:117` | same |
| grading | `postgres_evaluation.go:211` (`evaluateDML`) | savepoint → set role → **seed** → student stmt → post-image |
| preview | `postgres_evaluation.go:442` (`evaluateDMLPreview`) | savepoint → set role → **seed** → student stmt → preview rows |

All three roll back through the existing `ROLLBACK TO SAVEPOINT student_eval`,
so the real schema is never modified. The ARCH-021 / ARCH-024b ordering rule is
unchanged: `ROLLBACK TO SAVEPOINT` still runs before `RESET ROLE` on every exit
path, and a failed seed leaves the transaction aborted exactly like a failed
student statement, recovered by the same code.

Applying the seed to *all three* paths is mandatory, not optional. If grading or
preview skipped it, the student's correct `DELETE` would affect 0 rows and be
marked wrong.

A seed failure at grade time is not a student error. It returns the existing
`ValidationResult{Status: "error"}` shape with feedback that names the question
as misconfigured rather than blaming the submission, and is subject to the same
ARCH-018 dual-schema feedback scrub as every other message on that path.

### ARCH-042 — Lecturer-visible, read-only

`PracticeAuthoring.vue` and the exam authoring form render a read-only
"Auto-seeded row" panel beneath **Correct SQL** whenever the loaded question has
a non-null `auto_seed`, showing the derived INSERT in human-readable form.

The lecturer does not edit it — per the approved approach the system derives it —
but data invented on the lecturer's behalf must be visible, not silent.

## Known limitations

Stated explicitly rather than hidden:

1. **Dual-schema questions.** Grading runs on the real schema and preview on the
   decoy. The seed runs on both. If the decoy's column set for `target_table`
   differs from the real schema's, the preview seed fails and the student sees a
   clear "question is misconfigured" message rather than a wrong grade.
2. **Foreign keys.** A pinned value whose parent row does not exist makes the
   INSERT fail. This surfaces at save time as an ARCH-039 refusal carrying the
   Postgres error, so the lecturer learns it before students do.
3. **Category re-import.** Re-importing a category's database does not
   re-validate stored `auto_seed` values. A seed that references a dropped
   column fails at grade time as a misconfiguration message. Re-saving the
   question re-derives it.
4. **Generated / identity columns.** Treated as "has a default" and omitted. A
   WHERE clause that pins a `GENERATED ALWAYS` column is refused by the INSERT
   itself and reported per ARCH-039.

## Testing

- **Derivation unit tests** (`dml_autoseed_test.go`, no DB): accepted grammar
  produces the expected column/value set; each refusal class returns its named
  reason — non-equality operator, `OR`, subquery, `IN`, multiple statements,
  unknown column, unpinned `NOT NULL` column.
- **Capture integration**: a DELETE against a non-existent row saves
  successfully with `expected_result_status = 'captured'`, a non-null
  `auto_seed`, and `expected_rows_affected = 1`; an unparseable WHERE still
  fails with the ARCH-039 message.
- **Schema isolation** (the core guarantee): after a capture, a grade, and a
  preview of an auto-seeded question, `SELECT count(*)` on the target table in
  the real category schema is unchanged, and the seeded row is absent.
- **Grading correctness**: the same DELETE submitted by a student is graded
  correct; an empty/no-op statement is graded incorrect — the discrimination
  ARCH-008 exists to protect.
- **Regression**: questions whose model answer already affects > 0 rows produce
  `auto_seed = NULL` and byte-identical `expected_result` to before the change.
- **Migration idempotency**: a second boot re-runs the `ADD COLUMN IF NOT
  EXISTS` without error; pre-existing rows read back `auto_seed = NULL`.

Backend tests touching the shared DB must run with `go test -p 1` (see
`docs/`/project convention).
