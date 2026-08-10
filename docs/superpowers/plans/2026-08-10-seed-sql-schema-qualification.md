# Seed SQL Schema-Qualification Refusal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refuse a lecturer seed that names another schema, closing the cross-schema half of the deadlock hazard the ARCH-058 schema gate deliberately left open.

**Architecture:** `validateSeedSQL` (`backend/internal/repository/dml_autoseed.go:852`) currently enforces INSERT-only but places no restriction on the *target* of that INSERT. The repository already owns a tested denylist for exactly this — `schemaQualifiedNameRe` plus `stripSQLNoise` (`postgres_schema.go:283`), used today to keep a category import inside its own schema. This plan applies that existing pair to seed SQL. No new parser, no catalog query, no signature change, no new package.

**Tech Stack:** Go 1.x, `pgx/v5`, PostgreSQL. Plain `testing` — no test framework beyond the standard library.

## Background — why this is worth doing

The ARCH-058…061 schema gate serialises DML per *execution schema*. Its Known limitations record what that does not cover:

> Two evaluations on *different* gated schemas whose seeds both write the same `public.*` table therefore still produce `40P01` in exactly the original shape.

Seed replay runs **inside** the grading transaction, after `SET LOCAL ROLE exam_student`, and `exam_student` holds DML on every real schema. So a seed that writes `public.t` takes locks that the gate key knows nothing about. Two categories graded at once, seeds touching the same two shared tables in opposite order, and the deadlock is back — reported to the student as a failed submission.

**Scope boundary — read this before starting.** This plan closes the *schema-qualified* case only (`INSERT INTO public.t …`). It does **not** close the case where an *unqualified* name falls through `search_path` to `public`, because an unqualified name gives the denylist nothing to match on. That second case needs `public` removed from the DML `search_path` (`postgres_evaluation.go:301`, `:597`, and the two capture sites that must move with them) and is deliberately **out of scope** — it is a shipped-behaviour change with its own blast radius. Do not attempt it here. Task 3 records that it remains open.

## Global Constraints

- Every seed refusal MUST wrap `ErrAutoSeedUnsupported`. `TestValidateSeedSQL_Refuses` asserts `errors.Is` on every case, and callers use it to return 400 rather than 500. Returning `ErrSchemaQualifiedName` raw would surface as a 500.
- Use `refusef` / `refuse` (`dml_autoseed.go:62,66`) to build refusals. Never `fmt.Errorf("%w", ErrAutoSeedUnsupported)` — the type comment at `:51` explains why.
- Do NOT change `schemaQualifiedNameRe` or `stripSQLNoise`. They are shared with `ImportSchemaIntoCategory` and `postgres_preview_schema.go:352`. This plan is a new *consumer* of them, not a modification.
- Do NOT touch any `search_path` statement. Out of scope (see above).
- Backend tests are DB-global: any full run needs `-p 1`. The tests in this plan are pure and need no database.
- `scripts/audit_*.sql` are READ-ONLY by convention and open with a guard that makes writes impossible. Follow that.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `backend/internal/repository/dml_autoseed.go` | Modify `validateSeedSQL` (~`:852`) | Add the qualification refusal to the existing allowlist |
| `backend/internal/repository/dml_seedsql_test.go` | Add tests | Refusal cases, plus the false-positive guards |
| `scripts/audit_seed_sql_qualified_names.sql` | Create | Read-only report of already-persisted seeds the new rule would refuse |
| `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md` | Modify *Known limitations* | Record which half closed and which remains |
| `feedback.md` | Modify | Register entry for the residual |
| `CHANGELOG.md` | Modify | Behaviour change affecting existing questions |

Task 1 must complete and be **read by a human** before Task 2 lands, because Task 2 changes behaviour for already-saved questions. Tasks 2 and 3 are ordinary sequential work.

---

### Task 1: Audit already-persisted seeds

**Files:**
- Create: `scripts/audit_seed_sql_qualified_names.sql`

**Interfaces:**
- Consumes: nothing.
- Produces: a row count that decides whether Task 2 can ship as written. There is no Go interface.

**Why first.** `validateSeedSQL` is called on the save path (`postgres_questions.go:30`, `postgres_practice.go:375`, `:710`) **and** on the replay path (`applyQuestionSeed`, `dml_autoseed.go:957`). Tightening it therefore also tightens grading for questions saved months ago. If any exist, they stop grading the moment this deploys. Measure before changing.

- [ ] **Step 1: Write the audit script**

Create `scripts/audit_seed_sql_qualified_names.sql`:

```sql
-- ============================================================================
-- Audit: seeds that name a schema (practice-dml-schema-gate residual)
-- ============================================================================
-- READ-ONLY. Performs NO writes. Lists every persisted lecturer seed whose SQL
-- references a system, shared, or foreign schema. After the qualification
-- refusal ships, each row below will refuse BOTH on save and at grade time,
-- so every one of them needs a lecturer to edit the seed before deploy.
--
-- Background: seed replay runs inside the grading transaction as exam_student,
-- which holds DML on every real schema. A seed writing public.t therefore takes
-- locks the ARCH-059 schema gate cannot see, because the gate keys on the
-- execution schema alone. Two categories graded concurrently whose seeds touch
-- the same shared tables in opposite order deadlock with SQLSTATE 40P01.
--
-- The pattern mirrors schemaQualifiedNameRe in
-- backend/internal/repository/postgres_schema.go. It is deliberately the same
-- denylist, so this report and the Go refusal agree.
--
-- KNOWN FALSE POSITIVE: this scans raw seed_sql, whereas the Go check strips
-- string literals first. A seed whose only match is inside a quoted value —
-- VALUES ('see public.foo') — is listed here but will NOT be refused. Read
-- each row before acting on it.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_seed_sql_qualified_names.sql
-- ============================================================================

-- Guarantee we can never write, even by accident.
SET default_transaction_read_only = on;
BEGIN;

\echo ''
\echo '== 1. Practice questions whose seed names a schema =='

SELECT
    pq.id            AS question_id,
    pq.category_id,
    pc.name          AS category_name,
    pc.schema_name,
    pq.target_table,
    pq.seed_sql
FROM app.practice_questions pq
LEFT JOIN app.practice_categories pc ON pc.id = pq.category_id
WHERE pq.seed_sql IS NOT NULL
  AND btrim(pq.seed_sql) <> ''
  AND pq.seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.'
ORDER BY pq.category_id NULLS FIRST, pq.id;

\echo ''
\echo '== 2. Exam questions whose seed names a schema =='

SELECT
    q.id             AS question_id,
    q.exam_id,
    q.category_id,
    q.target_table,
    q.seed_sql
FROM app.questions q
WHERE q.seed_sql IS NOT NULL
  AND btrim(q.seed_sql) <> ''
  AND q.seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.'
ORDER BY q.exam_id NULLS FIRST, q.id;

\echo ''
\echo '== 3. Totals =='

SELECT
    (SELECT count(*) FROM app.practice_questions
      WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> ''
        AND seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.')
      AS practice_questions_affected,
    (SELECT count(*) FROM app.questions
      WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> ''
        AND seed_sql ~* '\m(public|app|information_schema|pg_catalog|pg_toast|pg_temp[0-9]*|sandbox_[a-z0-9_]+|(practice_cat|curriculum_exam|exam_cat)_[0-9]+(_preview)?)\s*\.')
      AS exam_questions_affected,
    (SELECT count(*) FROM app.practice_questions WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> '')
      AS practice_seeds_total,
    (SELECT count(*) FROM app.questions WHERE seed_sql IS NOT NULL AND btrim(seed_sql) <> '')
      AS exam_seeds_total;

ROLLBACK;
```

- [ ] **Step 2: Verify the script parses and is genuinely read-only**

Run against the disposable probe database, not the dev database:

```bash
docker run -d --rm --name sqlquiz-seedaudit \
  -e POSTGRES_USER=probe -e POSTGRES_PASSWORD=probepw -e POSTGRES_DB=probe \
  -p 55434:5432 postgres:16
until docker exec sqlquiz-seedaudit pg_isready -U probe -d probe; do :; done
```

The probe has no `app` schema, so the expected result is a clean `ERROR: relation "app.practice_questions" does not exist` — that proves the SQL *parses* and the read-only guard fires. To exercise it properly, point it at a database that has the schema:

```bash
psql "$DATABASE_URL" -f scripts/audit_seed_sql_qualified_names.sql
```

Expected: three sections print; section 3 returns one row of four counts. No `ERROR: cannot execute ... in a read-only transaction` (there are no writes to trigger it).

- [ ] **Step 3: Record the result and STOP for a human decision**

Write the four counts into the task report. **Do not start Task 2 without them.**

- If `practice_questions_affected` and `exam_questions_affected` are both `0`: proceed to Task 2 as written. The refusal is a pure tightening with no existing casualties.
- If either is non-zero: **halt and report.** Shipping Task 2 unchanged would break grading for those questions. The decision (fix the seeds first, or enforce on save only and leave replay untouched) belongs to the user, not to the implementer.

- [ ] **Step 4: Commit**

```bash
git add scripts/audit_seed_sql_qualified_names.sql
git commit -m "chore(audit): report seeds that name a schema"
```

---

### Task 2: Refuse schema-qualified seed targets

**Files:**
- Modify: `backend/internal/repository/dml_autoseed.go:852` (`validateSeedSQL`)
- Test: `backend/internal/repository/dml_seedsql_test.go`

**Interfaces:**
- Consumes: `schemaQualifiedNameRe` and `stripSQLNoise` from `postgres_schema.go` (same package, no import needed); `refusef` from `dml_autoseed.go:66`.
- Produces: no signature change. `validateSeedSQL(seedSQL string) ([]string, error)` and its exported twin `ValidateSeedSQL` keep their shapes, so all five call sites compile untouched.

- [ ] **Step 1: Write the failing tests**

Append to `backend/internal/repository/dml_seedsql_test.go`:

```go
// TestValidateSeedSQL_RefusesSchemaQualifiedNames proves a seed may only touch
// the question's own schema. Seed replay runs inside the grading transaction as
// exam_student, which holds DML on every real schema, so a cross-schema seed
// takes locks the ARCH-059 gate cannot see — the gate keys on the execution
// schema alone. Two categories graded at once then deadlock (SQLSTATE 40P01).
func TestValidateSeedSQL_RefusesSchemaQualifiedNames(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantName string
	}{
		{"public", "INSERT INTO public.employee (ssn) VALUES ('1')", "public"},
		{"app", "INSERT INTO app.system_users (id) VALUES (1)", "app"},
		{"pg_catalog", "INSERT INTO pg_catalog.pg_class (relname) VALUES ('x')", "pg_catalog"},
		{"information_schema", "INSERT INTO information_schema.tables (x) VALUES (1)", "information_schema"},
		{"another practice category", "INSERT INTO practice_cat_3.employee (ssn) VALUES ('1')", "practice_cat_3"},
		{"a preview decoy", "INSERT INTO practice_cat_3_preview.employee (ssn) VALUES ('1')", "practice_cat_3_preview"},
		{"an exam sandbox", "INSERT INTO sandbox_exam_1.employee (ssn) VALUES ('1')", "sandbox_exam_1"},
		{"an exam category", "INSERT INTO exam_cat_2.employee (ssn) VALUES ('1')", "exam_cat_2"},
		{"a curriculum", "INSERT INTO curriculum_exam_4.employee (ssn) VALUES ('1')", "curriculum_exam_4"},
		{"pg_temp", "INSERT INTO pg_temp.employee (ssn) VALUES ('1')", "pg_temp"},
		{"uppercase", "INSERT INTO PUBLIC.employee (ssn) VALUES ('1')", "PUBLIC"},
		{"space before the dot", "INSERT INTO public . employee (ssn) VALUES ('1')", "public"},
		// The scan must cover every statement, not just the first: a seed that
		// opens innocently and escapes on statement two is the interesting case.
		{
			"second statement escapes",
			"INSERT INTO employee (ssn) VALUES ('1'); INSERT INTO public.employee (ssn) VALUES ('2')",
			"public",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateSeedSQL(tc.in)
			if err == nil {
				t.Fatalf("validateSeedSQL(%q) accepted, want refusal", tc.in)
			}
			if !errors.Is(err, ErrAutoSeedUnsupported) {
				t.Fatalf("refusal does not wrap ErrAutoSeedUnsupported: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantName) {
				t.Errorf("refusal %q does not name the offending schema %q", err.Error(), tc.wantName)
			}
		})
	}
}

// TestValidateSeedSQL_QualifiedNameInDataIsNotRefused guards the false positive
// that makes a denylist dangerous: a schema name appearing in a VALUES literal
// or a comment is data, not a reference, and refusing it would reject seeds
// that are entirely legitimate. The scan runs after stripSQLComments and over
// stripSQLNoise's output, which is what makes these safe.
func TestValidateSeedSQL_QualifiedNameInDataIsNotRefused(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"inside a string literal", "INSERT INTO note (body) VALUES ('see public.foo for details')"},
		{"inside a line comment", "-- do not touch public.foo\nINSERT INTO employee (ssn) VALUES ('1')"},
		{"inside a block comment", "/* public.foo is off limits */ INSERT INTO employee (ssn) VALUES ('1')"},
		// A table whose name merely starts with a denylisted word must survive:
		// the regex requires the dot, so "app_settings" is not "app.".
		{"table name prefixed with a keyword", "INSERT INTO app_settings (k) VALUES ('a')"},
		{"column qualified by its own table", "INSERT INTO employee (ssn) SELECT e.ssn FROM employee e"},
	}

	// NOTE: `INSERT INTO "public"."employee" ...` also lands here — accepted,
	// not because it is legitimate but because the shared denylist cannot see
	// through double-quoted identifiers. Deliberately NOT added as a case: a
	// test asserting that a deadlock vector is accepted would read as approval.
	// It is recorded under Known limitations and in the spec instead.

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateSeedSQL(tc.in)
			if err != nil {
				t.Fatalf("validateSeedSQL(%q) refused a legitimate seed: %v", tc.in, err)
			}
			if len(got) == 0 {
				t.Fatalf("validateSeedSQL(%q) returned no statements", tc.in)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd backend
go test ./internal/repository/ -run 'TestValidateSeedSQL_RefusesSchemaQualifiedNames|TestValidateSeedSQL_QualifiedNameInDataIsNotRefused' -count=1 -v
```

Expected: `TestValidateSeedSQL_RefusesSchemaQualifiedNames` FAILS on every subtest with `accepted, want refusal`. `TestValidateSeedSQL_QualifiedNameInDataIsNotRefused` already PASSES — it is a guard against the fix over-reaching, so it is green before and must stay green after.

No database is required; `TestMain` does not demand `TEST_DATABASE_URL` for pure tests.

- [ ] **Step 3: Add the refusal**

In `backend/internal/repository/dml_autoseed.go`, inside `validateSeedSQL`, insert immediately after the `checkSeedQuotesBalanced` block and before the `splitTopLevelSemicolons` loop:

```go
	cleaned := stripSQLComments(trimmed)
	if err := checkSeedQuotesBalanced(cleaned); err != nil {
		return nil, err
	}

	// A seed may only touch the question's own schema. Replay runs inside the
	// grading transaction after SET LOCAL ROLE exam_student, and that role
	// holds DML on every real schema, so a schema-qualified target takes locks
	// the ARCH-059 gate cannot see — the gate keys on the execution schema
	// alone. Two categories graded at once whose seeds write the same shared
	// tables in opposite order then deadlock with SQLSTATE 40P01, and the
	// student is blamed for it.
	//
	// This reuses the denylist that already keeps a category import inside its
	// own schema (postgres_schema.go). It runs AFTER checkSeedQuotesBalanced
	// because stripSQLNoise cannot strip literals reliably out of unbalanced
	// quotes, and it scans stripSQLNoise(cleaned) rather than cleaned so that a
	// schema name inside a VALUES literal stays data rather than a match.
	//
	// The refusal is rebuilt with refusef rather than returned from
	// validateNoSchemaQualifiedNames directly: every seed refusal must wrap
	// ErrAutoSeedUnsupported so callers answer 400, and ErrSchemaQualifiedName
	// would fall through to a 500.
	if m := schemaQualifiedNameRe.FindStringSubmatch(stripSQLNoise(cleaned)); m != nil {
		return nil, refusef(
			"the seed SQL references the %q schema; a seed may only write to the question's own schema, because a seed that writes a shared table can deadlock against another category being graded at the same time",
			m[1],
		)
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/repository/ -run 'TestValidateSeedSQL' -count=1 -v
```

Expected: PASS, including the pre-existing `TestValidateSeedSQL_Accepts`, `_Refuses`, `_TooManyStatements`, `_TooLong`. If `_Accepts` broke, the scan is over-reaching — that is the failure mode this ordering exists to prevent.

- [ ] **Step 5: Run the full repository package**

```bash
export TEST_DATABASE_URL='postgres://probe:probepw@localhost:55433/probe?sslmode=disable'
go build ./... && go vet ./...
go test ./internal/repository/ -p 1 -count=1
```

Expected: `ok`. Watch for fixtures elsewhere in the package that seed with a qualified name and would now refuse.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/repository/dml_autoseed.go backend/internal/repository/dml_seedsql_test.go
git commit -m "fix(seed): refuse a seed that names another schema

Seed replay runs inside the grading transaction as exam_student, which
holds DML on every real schema, so a schema-qualified seed target takes
locks the ARCH-059 schema gate cannot see — the gate keys on the
execution schema alone. Two categories graded concurrently whose seeds
write the same shared tables in opposite order deadlock with SQLSTATE
40P01, surfacing to the student as a failed submission.

Reuses the denylist that already keeps a category import inside its own
schema, scanning after comment stripping and over stripSQLNoise output so
a schema name inside a VALUES literal stays data. The refusal is rebuilt
with refusef because every seed refusal must wrap ErrAutoSeedUnsupported
to answer 400 rather than 500.

Does not close the unqualified case, where a bare table name falls
through search_path to public. That needs public removed from the DML
search_path and is tracked separately."
```

---

### Task 3: Reconcile the documentation

**Files:**
- Modify: `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md` (*Known limitations*, `:354`)
- Modify: `feedback.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: the Task 1 counts and the Task 2 commit hash.
- Produces: nothing consumed by code.

The spec currently states the limitation as one item and proposes a fix this plan did not use. Half of it is now closed and the framing was wrong, so leaving it unedited would misdescribe shipped behaviour — the same failure the previous branch spent a whole review round correcting.

- [ ] **Step 1: Split the spec's Known-limitations entry**

In `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`, replace the **Cross-schema seeds can still deadlock** bullet with two bullets: one recording that the schema-qualified half is closed (naming the commit and that it reuses `schemaQualifiedNameRe`), and one that keeps the unqualified/`search_path` half open with its real cost.

The closed bullet must correct the original's proposed remedy. It said *"restricting `validateSeedSQL` to the question's own schema … or keying the gate on the set of tables a seed touches — both larger changes than this spec."* Neither was needed for this half; an existing validator covered it. Say so plainly, so the next reader does not re-cost the work from a stale estimate.

The still-open bullet must keep the sub-point at `:366` — that within one category, submit gates on `practice_cat_N` while preview gates on `practice_cat_N_preview`, so an unqualified name resolving to `public` is ungated between them — and name the four `search_path` sites that would have to move together.

- [ ] **Step 2: Update `feedback.md`**

Extend the OPEN-1 residual paragraph so it distinguishes the closed half from the open one. If the register has grown a finding for the unqualified case, update it in place rather than adding a second entry for the same hazard.

- [ ] **Step 3: Add a CHANGELOG entry**

This changes behaviour for lecturers, not only internals: a seed that saved yesterday may refuse today. The entry must say so, and must reference `scripts/audit_seed_sql_qualified_names.sql` as the way to find affected questions before deploying.

- [ ] **Step 4: Verify no stale cross-references**

```bash
grep -rn "Cross-schema seeds" docs/ feedback.md CHANGELOG.md
grep -n "validateSeedSQL" docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md
```

Expected: every hit describes current behaviour. Line references in this codebase are precise and are expected to stay that way; `validateSeedSQL` moved no lines, but the surrounding bullets did.

- [ ] **Step 5: Commit**

```bash
git add docs/ feedback.md CHANGELOG.md
git commit -m "docs: record which half of the cross-schema seed hazard closed"
```

---

## Verification Checklist

Run after Task 3.

- [ ] `cd backend && go build ./... && go vet ./...` — clean
- [ ] `go test ./... -p 1 -count=1` with `TEST_DATABASE_URL` set — green
- [ ] Run it a **second** time against the same database — still green (see `feedback.md` OPEN-8 for why this is checked rather than assumed)
- [ ] `git diff main --stat` — touches only the six files in the File Structure table
- [ ] `git diff main -- backend/internal/repository/postgres_evaluation.go` — **empty**. Any `search_path` change means the out-of-scope work leaked in.
- [ ] `grep -rn "search_path" backend/internal/repository/dml_autoseed.go` — no new hits
- [ ] The audit script's counts from Task 1 appear in the final report

## Known limitations of this plan

- **Denylist, not allowlist.** A schema outside the listed families — a lecturer-created `scratch.t` — is not refused. This inherits the existing trade-off deliberately: diverging from `schemaQualifiedNameRe` would mean two rules that must be kept in agreement, which is worse than one shared rule with a known bound. `ImportSchemaIntoCategory` has accepted this bound since ARCH-027.
- **Quoted identifiers bypass the denylist.** `INSERT INTO "public"."employee" …` is **not** refused, verified against the live regex: the pattern needs `public` followed by optional whitespace and a dot, and the closing `"` is neither. `stripSQLNoise` strips single-quoted literals and anonymous dollar-quoted bodies, not double-quoted identifiers, so nothing upstream normalises this away. The same bypass exists today in `ImportSchemaIntoCategory`, so this plan does not widen the hole — but the refusal must not be described anywhere as complete. Closing it means either extending the regex to tolerate an optional quote on each side of the dot, or parsing the target properly; both belong with the unqualified-case work, not here. State this in the spec bullet and the CHANGELOG entry from Task 3.
- **The unqualified case stays open**, and it is the more likely one in practice — a lecturer writes `INSERT INTO employee …`, not `INSERT INTO public.employee …`. This plan is the cheap half, not the whole fix. Do not let the CHANGELOG entry imply otherwise.
- **Replay-path enforcement is a behaviour change for existing data.** Task 1 exists to bound it. If the counts are non-zero, this plan needs revisiting before it ships.
