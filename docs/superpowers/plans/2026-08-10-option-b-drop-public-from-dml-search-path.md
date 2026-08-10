# Option B — Drop `public` from the DML `search_path` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop DML capture and grading from resolving unqualified table names through `public`, so a lecturer's SQL can no longer silently read or write tables shared by every category.

**Architecture:** Five `SET LOCAL search_path` statements drop their `, public` element. Four are production sites (two evaluation, two capture); the fifth is a test helper that hand-replicates the evaluation transaction and would otherwise stop exercising the real path. A read-only catalog audit gates the change: it enumerates, per category, every `public` table name absent from that category's own schema — the complete set of names a fallback could ever resolve. An empty report proves the change is a no-op for that database.

**Tech Stack:** Go 1.x, pgx/v5, PostgreSQL, psql for the audit script.

## Findings that changed this plan

The scope note (`docs/superpowers/specs/2026-08-10-option-b-drop-public-from-dml-search-path-scope.md`)
was written from the call sites alone. Reading the surrounding code corrected
it in three places. These corrections are the reason the tasks below look
different from the scope note's "proposed task shape":

1. **`target_table` is not in the blast radius at all.** Both capture paths
   resolve it through `ResolveTargetTable` *before* capture runs
   (`postgres_practice.go:394`, `postgres_questions.go:94`), and that function
   queries `WHERE n.nspname = $1` — the question's own schema only, never
   `public` (`postgres_target_table.go:54-62`). A DML question whose target
   exists solely in `public` is **already refused today** with
   `ErrTargetTableNotFound`. The scope note proposed testing exactly this case;
   such a test would pass before the change as well as after, pinning nothing.
2. **The real remaining blast radius is non-target tables** — tables named in
   `correct_sql` JOIN/FROM/subquery clauses and in `seed_sql` — which nothing
   resolves ahead of time. This is narrower than the scope note assumed but it
   is the whole of what the change affects.
3. **The scope note's evidence item 5 reached the right conclusion by the wrong
   route.** It claimed `postgres_core.go`'s `, public` sites are off the path
   because `evaluateDML` never calls `queryWithTx`. True for evaluation — but
   both *capture* sites do call `queryWithTxCols`, which re-sets
   `search_path TO <schema>, public` (`postgres_core.go:98`, `:132`), including
   for the DML post-image snapshot (`postgres_practice.go:573`). It is
   nonetheless harmless and must still be left alone: the post-image is
   `SELECT * FROM <target_table>`, and `target_table` is guaranteed by
   finding 1 to exist in the question's own schema, which `search_path` reaches
   first. The re-set can therefore never resolve the post-image through
   `public`. Task 2 pins this with a comment, not a code change.

## Global Constraints

- All five sites change in **one commit**. Capture and grading must agree on name resolution; splitting them lets a question capture green and fail at grade time — the shipped `target_table`-case bug's exact shape.
- `pg_temp` stays **last** at the two evaluation sites (ARCH-023). Naming it explicitly forces it out of first position, which is what stops `CREATE TEMP TABLE <target>` from shadowing the real table during post-image capture. Pinned by `TestPgTempForgery_TargetTableShadowing_Rejected`.
- **Do not modify `backend/internal/repository/postgres_core.go`.** Its four `, public` sites serve lecturer/admin paths. See finding 3 for why they are safe to leave.
- Backend tests are DB-global: always `go test ./... -p 1`. Plain `go test ./...` on a shared DB produces fake regressions.
- Audit scripts are **read-only**. House style guard, copied verbatim from `scripts/audit_dml_vacuous_questions.sql`: `BEGIN;` then `SET TRANSACTION READ ONLY;`. Never `SET default_transaction_read_only` — it is session-scoped and leaks into an operator's interactive psql.
- Never print, echo, or write the `DATABASE_URL` connection string or its password. Refer to it only as `$DATABASE_URL`.
- Integration tests run against the disposable probe container, never the dev database.

## Assumption stated, not guessed

The scope note left one question for the human: does a question captured before
this change get re-captured automatically, or does the lecturer re-save it?

**This plan chooses manual re-save**, matching what option A chose. Auto
re-capture would run arbitrary lecturer SQL unattended at deploy time, which is
a larger behaviour change than the four lines it would be protecting. The audit
output is therefore a worklist an operator works through. Task 3 records this
choice in `CHANGELOG.md` so the deployer knows it is theirs to action.

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `scripts/audit_public_fallback_questions.sql` | Read-only deploy gate: every `public` table name a category's DML could fall through to | Create |
| `backend/internal/repository/postgres_evaluation.go:301` | `evaluateDML` — student submit | Modify |
| `backend/internal/repository/postgres_evaluation.go:597` | `evaluateDMLPreview` — preview | Modify |
| `backend/internal/repository/postgres_questions.go:129` | exam capture | Modify |
| `backend/internal/repository/postgres_practice.go:461` | practice capture | Modify |
| `backend/internal/repository/postgres_practice_concurrent_dml_test.go:210` | `runSeedShapedTxn` — hand-rolled replica of the eval transaction | Modify |
| `backend/internal/repository/postgres_public_fallback_test.go` | Tests pinning the fallback's removal | Create |
| `CHANGELOG.md`, `feedback.md`, scope note, design spec | Docs | Modify |

---

### Task 1: Read-only audit gate

**Files:**
- Create: `scripts/audit_public_fallback_questions.sql`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: a gate result Task 2 depends on. Non-zero counts in sections 1 or 2 mean **stop and report to the human before changing behaviour**.

**Why catalog-only and not SQL text parsing:** the scope note suggested parsing
`correct_sql`. Do not. Extracting table references from arbitrary SQL text is
the same defect class that required four fix rounds on the option A branch
(literal detection disagreeing with scanners). This audit instead asks a
question the catalog answers exactly: *which names could a fallback resolve at
all?* A name absent from `public` can never be reached through it, and a name
present in the category's own schema is found before `public` is consulted. So
the set of `public` tables absent from a category's schema is a sound
over-approximation of the blast radius — it can over-report, never under-report.
An empty report is a proof of no-op, which is the only verdict the gate needs.

- [ ] **Step 1: Write the script**

```sql
-- ============================================================================
-- Audit: DML questions that could resolve a table name through public
-- ============================================================================
-- READ-ONLY. Performs NO writes and NO automatic re-capture.
--
-- Run BEFORE deploying the change that drops `, public` from the DML
-- search_path (postgres_evaluation.go, postgres_questions.go,
-- postgres_practice.go). After that change, an unqualified table name that
-- exists only in `public` stops resolving: the lecturer's SQL fails with
-- "relation ... does not exist" instead of silently reading or writing a table
-- shared by every category.
--
-- What this reports: for every schema that owns DML questions, each `public`
-- table whose name is ABSENT from that schema. Those names — and only those —
-- are what the fallback can currently resolve. A name already present in the
-- owning schema is found there first and never reaches public; a name absent
-- from public cannot be reached through it at all.
--
-- This is a sound over-approximation: it lists names that COULD be reached,
-- not names any question actually references. Zero rows is a proof that the
-- change is a no-op for this database. Non-zero rows require a human to check
-- whether the listed DML questions actually reference the listed names.
--
-- NOT reported here, because it is already refused today: a question whose
-- target_table exists only in public. ResolveTargetTable
-- (postgres_target_table.go:54) resolves target_table against the owning
-- schema only, so such a question fails to save regardless of search_path.
-- Rows of that shape are reported by sections 2a/2b of
-- scripts/audit_dml_vacuous_questions.sql and are already broken.
--
-- Run:  psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql
-- ============================================================================

-- Guarantee we can never write, even by accident.
BEGIN;
SET TRANSACTION READ ONLY;

\echo ''
\echo '==================================================================='
\echo ' 1. Practice categories with DML questions: public table names'
\echo '    absent from the category schema (reachable via the fallback)'
\echo '==================================================================='
SELECT pc.id            AS category_id,
       pc.name          AS category_name,
       pc.schema_name   AS category_schema,
       p.relname        AS reachable_public_table,
       (SELECT count(*)
          FROM app.practice_questions q
         WHERE q.category_id = pc.id
           AND q.question_type = 'DML') AS dml_questions
FROM app.practice_categories pc
JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
JOIN pg_catalog.pg_class p
       ON p.relnamespace = pn.oid
      AND p.relkind IN ('r','p','v','m','f')
WHERE EXISTS (SELECT 1
                FROM app.practice_questions q
               WHERE q.category_id = pc.id
                 AND q.question_type = 'DML')
  AND pc.schema_name <> 'public'
  AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = pc.schema_name
          AND c.relkind IN ('r','p','v','m','f')
          AND lower(c.relname) = lower(p.relname)
      )
ORDER BY pc.id, p.relname;

\echo ''
\echo '==================================================================='
\echo ' 2. Exams with DML questions: public table names absent from the'
\echo '    exam curriculum schema (reachable via the fallback)'
\echo '==================================================================='
SELECT e.id              AS exam_id,
       e.title           AS exam_title,
       COALESCE(NULLIF(e.curriculum_schema, ''), 'public') AS curriculum_schema,
       p.relname         AS reachable_public_table,
       (SELECT count(*)
          FROM app.questions q
         WHERE q.exam_id = e.id
           AND q.question_type = 'DML') AS dml_questions
FROM app.examinations e
JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
JOIN pg_catalog.pg_class p
       ON p.relnamespace = pn.oid
      AND p.relkind IN ('r','p','v','m','f')
WHERE EXISTS (SELECT 1
                FROM app.questions q
               WHERE q.exam_id = e.id
                 AND q.question_type = 'DML')
  AND COALESCE(NULLIF(e.curriculum_schema, ''), 'public') <> 'public'
  AND NOT EXISTS (
        SELECT 1
        FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = COALESCE(NULLIF(e.curriculum_schema, ''), 'public')
          AND c.relkind IN ('r','p','v','m','f')
          AND lower(c.relname) = lower(p.relname)
      )
ORDER BY e.id, p.relname;

\echo ''
\echo '==================================================================='
\echo ' 3. Verdict'
\echo '==================================================================='
-- practice_dml_total / exam_dml_total exist to distinguish "the change is
-- safe here" from "this database has no DML questions, so the gate proved
-- nothing". A zero *_affected count alongside a zero *_total count is the
-- second case, and must be reported as such.
SELECT
  (SELECT count(*) FROM app.practice_questions WHERE question_type = 'DML')
      AS practice_dml_total,
  (SELECT count(*) FROM app.questions WHERE question_type = 'DML')
      AS exam_dml_total,
  (SELECT count(DISTINCT pc.id)
     FROM app.practice_categories pc
     JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
     JOIN pg_catalog.pg_class p
            ON p.relnamespace = pn.oid AND p.relkind IN ('r','p','v','m','f')
    WHERE EXISTS (SELECT 1 FROM app.practice_questions q
                   WHERE q.category_id = pc.id AND q.question_type = 'DML')
      AND pc.schema_name <> 'public'
      AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c
                      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                      WHERE n.nspname = pc.schema_name
                        AND c.relkind IN ('r','p','v','m','f')
                        AND lower(c.relname) = lower(p.relname)))
      AS practice_categories_affected,
  (SELECT count(DISTINCT e.id)
     FROM app.examinations e
     JOIN pg_catalog.pg_namespace pn ON pn.nspname = 'public'
     JOIN pg_catalog.pg_class p
            ON p.relnamespace = pn.oid AND p.relkind IN ('r','p','v','m','f')
    WHERE EXISTS (SELECT 1 FROM app.questions q
                   WHERE q.exam_id = e.id AND q.question_type = 'DML')
      AND COALESCE(NULLIF(e.curriculum_schema, ''), 'public') <> 'public'
      AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c
                      JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                      WHERE n.nspname = COALESCE(NULLIF(e.curriculum_schema, ''), 'public')
                        AND c.relkind IN ('r','p','v','m','f')
                        AND lower(c.relname) = lower(p.relname)))
      AS exams_affected;

ROLLBACK;
```

- [ ] **Step 2: Verify the script is read-only and parses**

Run: `psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql`
Expected: four report blocks, no error, no write. The transaction ends in `ROLLBACK`.

- [ ] **Step 3: Record the gate result honestly**

Write the four verdict numbers into `.superpowers/sdd/progress.md` under Task 1.

**If `practice_dml_total` and `exam_dml_total` are both 0**, the gate has proved
the feature unused on this database, NOT the change safe. Say exactly that —
option A's audit made this mistake's twin and it had to be caught in review.
The local docker-compose database (`sql-quiz-db`) is known to hold only `app`
and `public` with no practice categories and no exams, so this is the expected
outcome here. It must be carried into Task 3's CHANGELOG entry as a
re-run-the-audit instruction for whoever deploys to a populated database.

- [ ] **Step 4: Commit**

```bash
git add scripts/audit_public_fallback_questions.sql
git commit -m "feat(scripts): audit public-fallback reachability for DML questions"
```

---

### Task 2: Drop the fallback at all five sites, test-first

**Files:**
- Create: `backend/internal/repository/postgres_public_fallback_test.go`
- Modify: `backend/internal/repository/postgres_evaluation.go:301`
- Modify: `backend/internal/repository/postgres_evaluation.go:597`
- Modify: `backend/internal/repository/postgres_questions.go:129`
- Modify: `backend/internal/repository/postgres_practice.go:461`
- Modify: `backend/internal/repository/postgres_practice_concurrent_dml_test.go:210`

**Interfaces:**
- Consumes: Task 1's gate result (must be clear, or the human must have approved proceeding).
- Produces: no new exported symbols. Behaviour change only.

**Test design note:** the one discriminating scenario is a DML question whose
`target_table` DOES resolve in its own schema (so `ResolveTargetTable` passes,
per finding 1) but whose `correct_sql` also references a second table that
exists only in `public`. Before the change that captures green while reading a
shared table; after, it fails at capture. A test built on `target_table` alone
would pass in both directions and pin nothing.

**Not tested, deliberately:** a concurrency test asserting two categories no
longer deadlock. With the fallback gone, two different schemas cannot reach a
common table by unqualified name at all, so the cross-schema lock is unreachable
by construction. A test asserting that two things which can no longer touch the
same table do not contend would pass for reasons unrelated to what it claims to
check. The mechanism tests in `postgres_practice_concurrent_dml_test.go` already
cover the schema-qualified route, which remains reachable.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/postgres_public_fallback_test.go`:

```go
package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// mkPublicOnlyTable creates a table in public that does NOT exist in the
// category schema, and registers cleanup. It returns the table name.
//
// The name is unique per test so parallel packages sharing this database do
// not collide, and so a crashed run cannot leave a fixture that silently
// satisfies a later run's NOT EXISTS check.
func mkPublicOnlyTable(t *testing.T, repo *PostgresRepository, ctx context.Context, prefix string) string {
	t.Helper()
	name := uniqueUsername(prefix) // lowercase, unique, identifier-safe
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"CREATE TABLE public.%s (id INT PRIMARY KEY, label TEXT)", name)); err != nil {
		t.Fatalf("create public.%s: %v", name, err)
	}
	if _, err := repo.pool.Exec(ctx, fmt.Sprintf(
		"INSERT INTO public.%s (id, label) VALUES (1, 'shared')", name)); err != nil {
		t.Fatalf("seed public.%s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(context.Background(),
			fmt.Sprintf("DROP TABLE IF EXISTS public.%s", name))
	})
	return name
}

// TestPracticeCapture_NoPublicFallback_ForJoinedTable pins the removal of the
// `, public` element from the practice capture search_path.
//
// The question's target_table lives in the category's own schema, so
// ResolveTargetTable (postgres_target_table.go:54) accepts it — that gate is
// NOT what this test exercises. The correct_sql additionally reads a table
// that exists only in public. Before the change that resolved through the
// fallback and captured green against a table shared by every category; after
// it must fail at capture, lecturer-facing, while it is still fixable.
func TestPracticeCapture_NoPublicFallback_ForJoinedTable(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid := mkPracticeCategory(t, repo, ctx, "pubfall")
	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		`CREATE TABLE emp (id INT PRIMARY KEY, name TEXT, label TEXT);
		 INSERT INTO emp (id, name) VALUES (1, 'Alice');`, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	shared := mkPublicOnlyTable(t, repo, ctx, "pubfall_shared")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		Title:       "reads a public-only table",
		Type:        "DML",
		TargetTable: "emp",
		CorrectSQL: fmt.Sprintf(
			"UPDATE emp SET label = s.label FROM %s s WHERE s.id = emp.id", shared),
		Requirement: "join a table that only exists in public",
		Points:      1,
	}
	if err := repo.CreatePracticeQuestion(ctx, q); err != nil {
		t.Fatalf("CreatePracticeQuestion returned a hard error: %v", err)
	}

	if q.ExpectedResultStatus != "failed" {
		t.Fatalf("expected capture to FAIL because %q resolves only in public; got status %q. "+
			"The public fallback is still present in the capture search_path.",
			shared, q.ExpectedResultStatus)
	}
	if !strings.Contains(strings.ToLower(q.CaptureError), "does not exist") {
		t.Errorf("capture_error should name the unresolvable relation, got: %q", q.CaptureError)
	}
}

// TestPracticeCapture_NoPublicFallback_LeavesSharedTableUntouched is the half
// that matters for the hazard itself: not merely that the question fails, but
// that the shared table was never written.
func TestPracticeCapture_NoPublicFallback_LeavesSharedTableUntouched(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	catID, uid := mkPracticeCategory(t, repo, ctx, "pubwrite")
	if err := repo.ImportSchemaIntoCategory(ctx, catID,
		`CREATE TABLE emp (id INT PRIMARY KEY, name TEXT);
		 INSERT INTO emp (id, name) VALUES (1, 'Alice');`, uid, false); err != nil {
		t.Fatalf("ImportSchemaIntoCategory: %v", err)
	}
	shared := mkPublicOnlyTable(t, repo, ctx, "pubwrite_shared")

	q := &models.PracticeQuestion{
		CreatedBy:   models.UserID(uid),
		CategoryID:  models.CategoryID(catID),
		Title:       "writes a public-only table via seed",
		Type:        "DML",
		TargetTable: "emp",
		CorrectSQL:  "UPDATE emp SET name = 'Bob' WHERE id = 1",
		SeedSQL:     fmt.Sprintf("INSERT INTO %s (id, label) VALUES (2, 'leaked');", shared),
		Requirement: "seed targets a table that only exists in public",
		Points:      1,
	}
	// A hard error is acceptable here (the seed refusal path), a green capture
	// is not. Either way the assertion below is what this test is for.
	_ = repo.CreatePracticeQuestion(ctx, q)

	var n int
	if err := repo.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT count(*) FROM public.%s", shared)).Scan(&n); err != nil {
		t.Fatalf("count public.%s: %v", shared, err)
	}
	if n != 1 {
		t.Fatalf("lecturer seed reached public.%s: row count %d, want 1. "+
			"A category's DML must not be able to write a table shared by every category.", shared, n)
	}
}

// TestExamCapture_PublicCurriculumSchema_StillWorks pins the no-op claim for
// legacy exams. An exam with no per-exam import resolves curriculum_schema to
// 'public' via COALESCE(NULLIF(...), 'public'), so its search_path was
// `public, public, pg_temp` before the change and `public, pg_temp` after.
// Dropping the duplicate must change nothing.
func TestExamCapture_PublicCurriculumSchema_StillWorks(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	tbl := mkPublicOnlyTable(t, repo, ctx, "legacyexam")

	examID, uid := mkExamWithCurriculumSchema(t, repo, ctx, "legacy", "")

	qid, err := repo.CreateQuestion(ctx, &models.Question{
		ExamID:      models.ExamID(examID),
		CreatedBy:   models.UserID(uid),
		Type:        "DML",
		TargetTable: tbl,
		CorrectSQL:  fmt.Sprintf("UPDATE %s SET label = 'updated' WHERE id = 1", tbl),
		Requirement: "legacy exam, public curriculum schema",
		Points:      1,
	})
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}
	if qid == 0 {
		t.Fatal("CreateQuestion returned id 0")
	}

	got, err := repo.GetQuestionByID(ctx, qid)
	if err != nil {
		t.Fatalf("GetQuestionByID: %v", err)
	}
	if got.ExpectedResultStatus != "captured" {
		t.Fatalf("legacy exam on public curriculum schema must still capture; got %q (%s)",
			got.ExpectedResultStatus, got.CaptureError)
	}
}
```

**Note for the implementer:** `mkExamWithCurriculumSchema`, `uniqueUsername`,
`openTestRepo`, `mkPracticeCategory`, `GetQuestionByID`, and the
`ExpectedResultStatus` / `CaptureError` field names above are written from the
surrounding code's conventions but were not each individually verified. Before
writing the test, grep for the real helper and field names and use those. If a
helper for "exam with a given curriculum_schema" does not exist, write one in
this file rather than modifying a shared test file. **Do not** invent a helper
name and leave it undefined — the test must compile.

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
cd backend && go test ./internal/repository/ -run 'NoPublicFallback|PublicCurriculumSchema' -count=1 -p 1 -v
```
Expected: `TestPracticeCapture_NoPublicFallback_ForJoinedTable` FAILS with
status `captured` (the fallback still resolves the shared table).
`TestPracticeCapture_NoPublicFallback_LeavesSharedTableUntouched` FAILS with
row count 2 (the seed reached public).
`TestExamCapture_PublicCurriculumSchema_StillWorks` PASSES already — it is a
no-op guard, not a red test.

- [ ] **Step 3: Drop `, public` at the two evaluation sites**

`postgres_evaluation.go:301` — change:
```go
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, public, pg_temp", safeSchema)); err != nil {
```
to:
```go
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, pg_temp", safeSchema)); err != nil {
```

`postgres_evaluation.go:597` — change:
```go
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, public, pg_temp", safeSchema)); err != nil {
```
to:
```go
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path = %s, pg_temp", safeSchema)); err != nil {
```

(Note the second uses `=` rather than `TO`. Both are valid; preserve whichever
the line already has, and do not "tidy" it — an unrelated diff hunk in a
security-relevant line costs reviewer attention.)

Extend the ARCH-023 comment block above line 301 with one sentence, since it
describes the statement's shape and would otherwise go stale:

```go
	// ARCH-062: `public` is deliberately NOT in this list. With it present an
	// unqualified name absent from the question's own schema resolved to a
	// table shared by every category — a silent cross-category read or write,
	// and the cross-schema lock that the ARCH-058 per-schema gate cannot see
	// (the gate keys on the execution schema; the lock lands in another one).
	// The QUERY branch (line 209) has always run without it.
```

- [ ] **Step 4: Drop `, public` at the two capture sites**

`postgres_questions.go:129`:
```go
			if _, spErr := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safeCaptureSchema)); spErr != nil {
```

`postgres_practice.go:461`:
```go
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s", safeSchema)); err != nil {
```

Add above `postgres_practice.go:461`:

```go
		// ARCH-062: no `, public`. Capture must resolve names exactly as
		// grading does (postgres_evaluation.go:301), or a question captures
		// green and fails at grade time — the shape of the shipped
		// target_table case bug.
		//
		// The DML post-image below goes through queryWithTxCols, which re-sets
		// search_path to `<schema>, public` (postgres_core.go:132). That is
		// harmless and is left alone on purpose: the post-image is
		// `SELECT * FROM <target_table>`, and ResolveTargetTable
		// (postgres_target_table.go:54) has already proved target_table exists
		// in <schema>, which is searched first. The re-set can never resolve
		// the post-image through public.
```

- [ ] **Step 5: Update the test helper that replicates the eval transaction**

`postgres_practice_concurrent_dml_test.go:210` — `runSeedShapedTxn` hand-builds
the same transaction `evaluateDML` builds. If it keeps `, public` the
concurrency tests stop exercising the production path:

```go
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL search_path TO %s, pg_temp", safeSchema)); err != nil {
```

- [ ] **Step 6: Run the new tests to verify they pass**

Run:
```bash
cd backend && go test ./internal/repository/ -run 'NoPublicFallback|PublicCurriculumSchema' -count=1 -p 1 -v
```
Expected: all three PASS.

- [ ] **Step 7: Run the full backend suite**

Run:
```bash
cd backend && go build ./... && go vet ./... && go test ./... -p 1 -count=1
```
Expected: exit 0.

Two known places to look first if something fails:
`postgres_student_exec_test.go:297` and `:471` also hardcode
`SET LOCAL search_path TO %s, public` in probe setups. They are not replicas of
`evaluateDML` (they build arbitrary contexts to test what `exam_student` may
do), so they are **out of scope** and should be left as they are unless they
fail. If one does fail, report it rather than changing it — it means the probe
was relying on a public table, which is information the reviewer needs.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/repository/
git commit -m "fix(eval): drop the public fallback from the DML search_path

An unqualified table name absent from a question's own schema resolved
through public, letting one category's DML silently read or write a table
shared by every category. That is also a lock in a schema the ARCH-058
per-schema gate cannot see, so two categories could deadlock (40P01) and
the student was blamed.

All five sites move together — both evaluation sites, both capture sites,
and the concurrency test's replica of the eval transaction — because
capture and grading must agree on name resolution.

postgres_core.go's queryWithTx/queryWithTxCols keep their public element:
the only DML path reaching them is the post-image SELECT on target_table,
which ResolveTargetTable has already proved resolves in the owning schema."
```

---

### Task 3: Documentation

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `feedback.md` (OPEN-1)
- Modify: `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md` (Known limitations)
- Modify: `docs/superpowers/specs/2026-08-10-option-b-drop-public-from-dml-search-path-scope.md` (corrections)

**Interfaces:**
- Consumes: Task 1's recorded gate result; Task 2's commit hash.
- Produces: nothing consumed downstream.

- [ ] **Step 1: CHANGELOG entry**

Add under the current unreleased heading. It must carry the manual-re-save
decision and the re-run-the-audit instruction:

```markdown
### Fixed
- DML capture and grading no longer resolve unqualified table names through
  `public`. A lecturer's SQL that named a table absent from its own category
  schema previously fell through to a table shared by every category — a
  silent cross-category read or write, and a lock the per-schema DML gate
  cannot see, so two categories grading concurrently could deadlock (40P01)
  and the failure was reported to the student.

  **Before deploying:** run
  `psql "$DATABASE_URL" -f scripts/audit_public_fallback_questions.sql`.
  Non-zero `practice_categories_affected` / `exams_affected` means some DML
  questions may reference a table they only reached through `public`; those
  questions will fail at grade time until a lecturer re-saves them (re-saving
  re-runs capture against the corrected resolution). There is no automatic
  re-capture — that would run lecturer SQL unattended at deploy time.

  The audit run during development reported zero, but on a database with no
  practice categories and no exams, so it proved the feature unused rather
  than the change safe. Re-run it wherever real lecturer content lives.
```

- [ ] **Step 2: Close the open finding in `feedback.md`**

Find OPEN-1 and mark the unqualified half CLOSED, dated 2026-08-10, naming the
commit from Task 2. Leave anything OPEN-1 records about the
**schema-qualified** half as it stands — option A closed that separately, and
the `"public"."employee"` double-quoted bypass remains open regardless.

- [ ] **Step 3: Update the design spec's Known limitations**

In `docs/superpowers/specs/2026-08-09-practice-dml-schema-gate-design.md`,
the unqualified-name hazard is listed as a known limitation of the schema gate.
Mark it closed by this change, and keep the explanation of *why* the gate alone
could not cover it — that reasoning is still what justifies the gate's design.

- [ ] **Step 4: Correct the scope note**

The scope note is committed and three of its claims are now known wrong. Append
a "Corrections" section rather than editing the body — the body is what was
reviewed and the delta is the useful record. Cover the three items from this
plan's *Findings that changed this plan* section: `target_table` is already
protected by `ResolveTargetTable`; the real blast radius is non-target tables;
and evidence item 5's conclusion was right but its reasoning missed that the
capture sites do call `queryWithTxCols`.

Also correct the scope note's proposed test — it suggested asserting that two
categories with unqualified seeds on a shared table "both grade success". After
this change they cannot reach a shared table at all, so that assertion
describes behaviour that no longer exists.

- [ ] **Step 5: Verify no other doc describes the old search_path**

Run:
```bash
grep -rn "public, pg_temp\|, public" ARCHITECTURE.md API_SPEC.md docs/ --include=*.md
```
Fix anything that states the DML search_path includes `public`. Option A's final
review caught two closed-list enumerations in `API_SPEC.md` and
`ARCHITECTURE.md` that had gone stale the same way; check those two files
specifically.

- [ ] **Step 6: Commit**

```bash
git add CHANGELOG.md feedback.md docs/
git commit -m "docs: record the DML search_path public-fallback removal"
```

---

## Out of scope, recorded so it is not silently missed

- **QUERY capture still resolves through `public`.** `captureExpectedResult`'s
  QUERY branch calls `queryWithTxCols`, whose `search_path` is
  `<schema>, public` (`postgres_core.go:132`), while QUERY *evaluation* runs
  `<schema>, pg_temp` (`postgres_evaluation.go:209`). A QUERY question reading a
  public-only table therefore captures green and fails at grade time. This is a
  pre-existing asymmetry, unrelated to the DML hazard, and closing it means
  touching `postgres_core.go`'s shared helpers — a much wider change. Raise it
  as its own finding; do not fix it here.
- **`"public"."employee"` double-quoted qualification** is still accepted by
  `validateSeedSQL` and would still execute a cross-schema write. Option A's
  known bound; unaffected by this change either way.
- **Exam-activation pre-flight** does not run `ValidateSeedSQL`, so a bad stored
  seed still fails per student rather than once, lecturer-facing. Recorded on
  the option A branch; still open.

## Self-review

**Spec coverage.** The scope note's four proposed tasks map to three here:
its Task 1 (audit) → Task 1, redesigned catalog-only for the reason given;
its Tasks 2+3 (test then change) → Task 2, merged because the test must fail
before the change and pass after, which is one TDD cycle and one review gate;
its Task 4 (docs) → Task 3. Its fifth site was missing and is added.

**Placeholder scan.** No TBDs. Every code step carries the code. The one
explicitly unverified area — helper and field names in the test file — is
flagged inline with an instruction to grep for the real names, rather than
being left to look verified.

**Type consistency.** `mkPublicOnlyTable` returns `string` and is consumed as a
table name in all three tests. `mkPracticeCategory` returns `(int, int)` per
`postgres_practice_title_test.go:25`. `CreatePracticeQuestion` takes
`*models.PracticeQuestion` and mutates it in place, which is why the tests read
`q.ExpectedResultStatus` after the call rather than re-fetching.
