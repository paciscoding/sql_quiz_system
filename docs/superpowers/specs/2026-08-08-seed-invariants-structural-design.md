# Making the Seed Invariants Structural

**Date:** 2026-08-08
**Status:** Approved, ready for implementation
**ARCH range:** ARCH-053 … ARCH-057
**Builds on:** `2026-08-08-lecturer-seed-rows-design.md` (ARCH-043 … ARCH-052)

## Problem

The lecturer-seed feature shipped with two invariants that are currently held by
convention rather than by construction, plus one backward-compatibility defect
found while reviewing them.

### 1. The leak defence is opt-out

ARCH-052 keeps `seed_sql` and `auto_seed` away from students by assigning them
away at three hand-written sites:

- `practice_controller.go:106` (`ListForStudent`)
- `practice_controller.go:148` (`GetForStudent`)
- `quiz_controller.go:173` (`GetExamQuestions`)

All three are correct today. But the *default* for any student-facing endpoint
added later is to leak: the fields marshal unless someone remembers to clear
them. `seed_sql` is lecturer-written SQL containing the exact values a correct
answer must match, so a forgotten scrub hands out the answer.

### 2. `dml_kind` can silently disagree with `correct_sql`

`validateSeedFields` checks that `dml_kind` is one of `INSERT`/`UPDATE`/`DELETE`,
but never compares it to what `correct_sql` actually is. A lecturer can select
DELETE and write an `UPDATE`, and nothing objects. The worse direction is
selecting INSERT while writing a `DELETE`: the seed field is hidden and
`seed_sql` is rejected outright (ARCH-044), so the one question that needs a
seed cannot be given one.

`leadingDMLKind` already exists in `postgres_schema.go` and answers exactly this
question, but is wired only to the migration backfill.

### 3. Regression: CTE-led DML questions became uneditable

`validateSeedFields` requires a non-empty `dml_kind` for every `DML` question.
The `migrateDMLKind` backfill derives that kind from `correct_sql`'s leading
keyword — and leaves it `NULL` when the statement leads with none of the three.

A `WITH recent AS (…) DELETE FROM employee …` question is valid Postgres,
captures correctly, and has worked until now. After ARCH-050 it cannot be
re-saved: the backfill cannot name its kind, the dropdown shows a disabled
placeholder, and the save is refused with *"dml_kind is required for a DML
question"*. The lecturer is blocked on a question the system itself could not
classify.

This is a defect introduced by ARCH-050, not a pre-existing one.

## Goal

- `seed_sql` and `auto_seed` become structurally incapable of reaching a
  student, so a future endpoint cannot leak them by omission.
- A saved question's `dml_kind` always agrees with its `correct_sql`, whenever
  the system can determine what `correct_sql` is.
- No question that is editable today becomes uneditable.

## Non-goals

- No change to grading, capture, or seed execution. This spec touches
  serialization and request validation only.
- No change to the lecturer-facing wire shape. The authoring UI keeps receiving
  `auto_seed` and `seed_sql` exactly as it does now.
- No backfill or migration. No column is added, dropped, or altered.

## Design decisions

### ARCH-053 — The fields never marshal and never bind

On `models.Question` and `models.PracticeQuestion`:

```go
AutoSeed json.RawMessage `json:"-"`
SeedSQL  string          `json:"-"`
```

`json:"-"` removes both fields from `encoding/json` in **both** directions. They
cannot be written into a response, and they cannot be read out of a request
body.

The inbound half is a strengthening, not a side effect. ARCH-040 already
required that `auto_seed` "must never be accepted from a client request body",
enforced until now only by a doc comment. It is now enforced by the type.

**Consequence, and the reason this spec is larger than a tag change:** the two
write handlers currently bind the request body *directly* into the model, so
`json:"-"` would also stop a lecturer's seed from ever being saved. ARCH-055
addresses that; the two decisions must land together or saving breaks.

### ARCH-054 — Lecturer reads opt back in through a view struct

```go
// LecturerQuestionView is the lecturer-facing serialization of a Question. It
// re-exposes the two fields ARCH-053 hides from every other consumer. Nothing
// student-facing may use it.
type LecturerQuestionView struct {
	Question
	AutoSeed json.RawMessage `json:"auto_seed,omitempty"`
	SeedSQL  string          `json:"seed_sql,omitempty"`
}

func AsLecturerQuestionView(q Question) LecturerQuestionView
func AsLecturerQuestionViews(qs []Question) []LecturerQuestionView
```

and the `PracticeQuestion` twin, `LecturerPracticeQuestionView`, with the same
two constructors.

The embedded field's `json:"-"` excludes it outright, so the outer tagged field
is the only `auto_seed`/`seed_sql` `encoding/json` sees — there is no ambiguity
to resolve.

Applied at exactly two handlers, both of which are a bare
`c.JSON(http.StatusOK, questions)` today:

| handler | file |
|---|---|
| `PracticeController.ListForLecturer` | `practice_controller.go:404` |
| `LecturerController.GetExamQuestions` | `lecturer_controller.go:467` |

This is required, not cosmetic. `PracticeAuthoring.vue:226` destructures
`auto_seed` off the loaded question to drive the read-only "Auto-seeded row"
panel, and `ExamQuestionsPanel.vue` does the same through `autoSeedPlan`. The
lecturer wire shape is unchanged, so no frontend edit is needed.

### ARCH-055 — Writes bind through explicit request structs

`PracticeController.Upsert` and `LecturerController.UpsertQuestion` currently
`ShouldBindJSON` straight into `models.PracticeQuestion` / `models.Question`.
Each gains an explicit request struct carrying every field a lecturer may set,
including:

```go
	DMLKind string `json:"dml_kind"`
	SeedSQL string `json:"seed_sql"`
```

The handler copies the struct's fields into the model, then proceeds exactly as
today. `auto_seed` is simply absent from the request struct: a client cannot
supply it, by construction rather than by comment.

The round-trip test in the Testing section exists specifically to catch a field
dropped during this transcription.

### ARCH-056 — The manual scrubs are deleted

With ARCH-053 in place the three `AutoSeed = nil` / `SeedSQL = ""` assignments
are dead code and are removed, along with their ARCH-052 comments.

The existing leak tests in `controllers/autoseed_leak_http_test.go` are **not**
touched. They must keep passing with the assignments gone — that is precisely
the evidence that the structural defence replaced the manual one rather than
merely sitting alongside it. A leak test that only passes because of the
assignments would prove nothing about ARCH-053.

### ARCH-057 — `dml_kind` must agree with `correct_sql`, when derivable

`leadingDMLKind` is exported as `repository.LeadingDMLKind(sql string) string`,
returning `"INSERT"`, `"UPDATE"`, `"DELETE"`, or `""`.

`validateSeedFields` gains a `correctSQL` parameter and two rules:

**Rule 1 — agreement.** When `LeadingDMLKind(correctSQL)` returns a non-empty
kind and the declared `dml_kind` differs, refuse:

> `You selected DELETE, but the correct SQL is an UPDATE statement. Pick the kind that matches your SQL.`

**Rule 2 — derivable-only requirement.** The existing "dml_kind is required for
a DML question" rule applies **only** when `LeadingDMLKind(correctSQL)` returns
a non-empty kind. When it returns `""` the system cannot classify the statement
either, so an empty `dml_kind` is accepted rather than demanded.

Rule 2 is what repairs the ARCH-050 regression. `WITH recent AS (…) DELETE …`
saves again, with `dml_kind` left empty and no seed field offered. A lecturer
who wants a seed on such a question can still set the kind by hand — Rule 1
does not fire, because there is nothing to disagree with.

The two rules share one principle: **unverifiable is not the same as wrong.**
The system refuses only what it can prove is inconsistent.

## Known limitations

1. **A hand-set kind on unclassifiable SQL is unchecked.** If
   `LeadingDMLKind` returns `""` and the lecturer picks `UPDATE`, nothing
   verifies it. Accepted: the alternative is blocking valid SQL.
2. **`LeadingDMLKind` is keyword-level, not a parser.** It strips comments and
   matches the leading keyword. It does not resolve a CTE to the statement the
   CTE feeds, which is why case 1 exists.
3. **The lecturer view is opt-in per handler.** A future *lecturer* endpoint
   that forgets to wrap will omit the two fields. That failure is safe — a
   missing authoring field is visible immediately, unlike a silent leak.

## Testing

**Structural serialization** (`models` package, no HTTP, no DB) — the core
guarantee, tested directly:

- Marshal a `models.Question` and a `models.PracticeQuestion` with both fields
  populated; assert the output contains neither `auto_seed` nor `seed_sql`.
- Marshal `LecturerQuestionView` and `LecturerPracticeQuestionView` built from
  the same values; assert both keys are present with the right values.
- Unmarshal a body containing `auto_seed` and `seed_sql` into the bare models;
  assert both fields remain zero — a client cannot inject either.

**Leak regression** — `controllers/autoseed_leak_http_test.go` passes unchanged
with the ARCH-056 assignments deleted.

**Round-trip** — save a question with a `seed_sql` through the real API, read it
back through the lecturer path, and assert the seed survived intact. This is the
guard against a field lost in the ARCH-055 transcription.

**Drift** (`validateSeedFields` table test):

- declared kind matches `correct_sql` → accepted, for each of the three kinds;
- each mismatched pair → rejected, message naming both the declared kind and the
  actual one;
- `WITH … DELETE` with an empty `dml_kind` → **accepted** (the ARCH-050
  regression test);
- `WITH … DELETE` with a hand-set `dml_kind` → accepted;
- `QUERY` with any `dml_kind` → still rejected, unchanged.

Backend tests touching the shared database run with `go test -p 1`.
