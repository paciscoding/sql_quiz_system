# Lecturer-Authored Seed Rows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a lecturer type the seed `INSERT` rows for an UPDATE/DELETE question directly in the authoring form, executed inside the existing rolled-back savepoint, with the shipped WHERE-clause derivation kept as the fallback when the field is empty.

**Architecture:** Two additive nullable columns (`dml_kind`, `seed_sql`) on `app.questions` and `app.practice_questions` — the `question_type IN ('QUERY','DML')` CHECK is deliberately left alone so no existing grading branch changes. One new repository helper, `applyQuestionSeed`, replaces the four existing `applyAutoSeed` call sites; it runs the lecturer's SQL when present and delegates to derivation when not. The frontend's four-way dropdown maps down to `question_type` + `dml_kind` on the wire.

**Tech Stack:** Go 1.x + pgx/v5 + Gin (backend), Vue 3 SFC + TypeScript + Vitest (frontend), PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-08-08-lecturer-seed-rows-design.md` (ARCH-043 … ARCH-052).

## Global Constraints

- Backend tests touching the shared database MUST run with `go test -p 1`. A plain `go test ./...` on a shared DB fakes regressions.
- `question_type`'s CHECK constraint stays `IN ('QUERY','DML')`. Never widen it.
- Every schema change is `ADD COLUMN IF NOT EXISTS` in the `extraMigrations` slice AND present in the `CREATE TABLE` body. The database is **deployed** — no destructive migration.
- The seed executes **after** `SET LOCAL ROLE` and **inside** `SAVEPOINT student_eval`. Never hoist it above the role switch: student login roles are `NOINHERIT`, so before the switch the connection holds no privileges at all.
- `ROLLBACK TO SAVEPOINT` always runs **before** `RESET ROLE` on every error exit path (ARCH-021 / ARCH-024b). Reversing them makes `RESET ROLE` itself fail in the aborted transaction.
- `seed_sql` and `auto_seed` must never appear in a student-facing HTTP response.
- Run `cd backend && go build ./...` and `gofmt -l .` before each commit; `gofmt -l .` must print nothing.
- Frontend: `cd frontend && npm test --` and `npx vue-tsc --noEmit` must pass before each frontend commit.

---

### Task 1: `validateSeedSQL` and `applyQuestionSeed`

Pure Go. No database. This is the security gate (ARCH-046) and the single execution entry point (ARCH-047) every later backend task calls.

**Files:**
- Modify: `backend/internal/repository/dml_autoseed.go` (append; do not restructure existing code)
- Test: `backend/internal/repository/dml_seedsql_test.go` (create)

**Interfaces:**
- Consumes: existing unexported helpers in `dml_autoseed.go` — `stripSQLComments(string) string`, `splitTopLevelSemicolons(string) []string`, `matchKeywordAt(s string, i int, keyword string) bool`, `refuse(string) error`, `refusef(string, ...any) error`, `ErrAutoSeedUnsupported`, `applyAutoSeed(ctx, tx, *AutoSeedPlan) error`.
- Produces:
  - `func normalizeSeedSQL(seedSQL string) string`
  - `func validateSeedSQL(seedSQL string) ([]string, error)` — returns the ordered, comment-stripped statements to execute. Empty input returns `(nil, nil)`. Refusals wrap `ErrAutoSeedUnsupported`.
  - `func applyQuestionSeed(ctx context.Context, tx pgx.Tx, seedSQL string, plan *AutoSeedPlan) error`
  - `const maxSeedStatements = 20`
  - `const maxSeedSQLBytes = 8192`

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/repository/dml_seedsql_test.go`:

```go
package repository

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateSeedSQL_Accepts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single statement",
			in:   "INSERT INTO employee (ssn) VALUES ('999999999')",
			want: []string{"INSERT INTO employee (ssn) VALUES ('999999999')"},
		},
		{
			name: "trailing semicolon",
			in:   "INSERT INTO employee (ssn) VALUES ('1');",
			want: []string{"INSERT INTO employee (ssn) VALUES ('1')"},
		},
		{
			name: "two statements, parent before child",
			in:   "INSERT INTO department (dnumber) VALUES (5);\nINSERT INTO employee (ssn, dno) VALUES ('1', 5);",
			want: []string{
				"INSERT INTO department (dnumber) VALUES (5)",
				"INSERT INTO employee (ssn, dno) VALUES ('1', 5)",
			},
		},
		{
			name: "leading line comment does not hide the keyword",
			in:   "-- seed the missing employee\nINSERT INTO employee (ssn) VALUES ('1')",
			want: []string{"INSERT INTO employee (ssn) VALUES ('1')"},
		},
		{
			name: "leading block comment does not hide the keyword",
			in:   "/* setup */ INSERT INTO employee (ssn) VALUES ('1')",
			want: []string{"INSERT INTO employee (ssn) VALUES ('1')"},
		},
		{
			name: "semicolon inside a string literal does not split",
			in:   "INSERT INTO employee (fname) VALUES ('a;b')",
			want: []string{"INSERT INTO employee (fname) VALUES ('a;b')"},
		},
		{
			name: "lowercase keyword",
			in:   "insert into employee (ssn) values ('1')",
			want: []string{"insert into employee (ssn) values ('1')"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateSeedSQL(tc.in)
			if err != nil {
				t.Fatalf("validateSeedSQL(%q) returned error: %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements %q, want %d %q", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if strings.TrimSpace(got[i]) != tc.want[i] {
					t.Errorf("statement %d: got %q, want %q", i, strings.TrimSpace(got[i]), tc.want[i])
				}
			}
		})
	}
}

func TestValidateSeedSQL_EmptyIsNotAnError(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t ", "-- nothing here\n", "/* nothing */"} {
		got, err := validateSeedSQL(in)
		if err != nil {
			t.Fatalf("validateSeedSQL(%q) returned error: %v", in, err)
		}
		if got != nil {
			t.Fatalf("validateSeedSQL(%q) = %q, want nil", in, got)
		}
	}
}

func TestValidateSeedSQL_Refuses(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantSubstr  string
	}{
		{"delete", "DELETE FROM employee WHERE ssn = '1'", "DELETE"},
		{"update", "UPDATE employee SET fname = 'x'", "UPDATE"},
		{"select", "SELECT * FROM employee", "SELECT"},
		{"ddl", "CREATE TEMP TABLE employee (ssn TEXT)", "CREATE"},
		{"truncate", "TRUNCATE employee", "TRUNCATE"},
		{"insert then delete", "INSERT INTO employee (ssn) VALUES ('1'); DELETE FROM employee", "DELETE"},
		{"unterminated quote", "INSERT INTO employee (fname) VALUES ('oops", "unterminated"},
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
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("refusal %q does not name %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestValidateSeedSQL_TooManyStatements(t *testing.T) {
	one := "INSERT INTO t (a) VALUES (1);"
	_, err := validateSeedSQL(strings.Repeat(one, maxSeedStatements+1))
	if err == nil {
		t.Fatal("accepted more than maxSeedStatements, want refusal")
	}
	if !strings.Contains(err.Error(), "at most 20 statements") {
		t.Errorf("refusal %q does not state the statement cap", err.Error())
	}
}

func TestValidateSeedSQL_TooLong(t *testing.T) {
	_, err := validateSeedSQL(strings.Repeat("x", maxSeedSQLBytes+1))
	if err == nil {
		t.Fatal("accepted over-length input, want refusal")
	}
	if !strings.Contains(err.Error(), "8192") {
		t.Errorf("refusal %q does not state the byte cap", err.Error())
	}
}

func TestNormalizeSeedSQL(t *testing.T) {
	if got := normalizeSeedSQL("  \n\t "); got != "" {
		t.Errorf("whitespace-only input normalized to %q, want empty", got)
	}
	if got := normalizeSeedSQL("  INSERT INTO t (a) VALUES (1)  "); got != "INSERT INTO t (a) VALUES (1)" {
		t.Errorf("normalizeSeedSQL trimmed to %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd backend && go test ./internal/repository/ -run 'TestValidateSeedSQL|TestNormalizeSeedSQL' -v`

Expected: FAIL to compile — `undefined: validateSeedSQL`, `undefined: normalizeSeedSQL`, `undefined: maxSeedStatements`, `undefined: maxSeedSQLBytes`.

- [ ] **Step 3: Implement**

Append to `backend/internal/repository/dml_autoseed.go` (the file already imports `context`, `errors`, `fmt`, `strings`, and `github.com/jackc/pgx/v5`; no new imports are needed):

```go
// ---------------------------------------------------------------------------
// Lecturer-authored seed rows (ARCH-046 / ARCH-047)
// ---------------------------------------------------------------------------

// maxSeedStatements and maxSeedSQLBytes bound a lecturer's seed block. Both
// refuse rather than truncate: silently dropping statements would seed a
// different state than the lecturer read back in the form.
const (
	maxSeedStatements = 20
	maxSeedSQLBytes   = 8192
)

// normalizeSeedSQL is the single definition of "no seed". A field holding only
// whitespace is stored as the empty string, never as "   ", so the
// seedSQL != "" precedence test in applyQuestionSeed and in the capture paths
// can never be fooled by an accidental space.
func normalizeSeedSQL(seedSQL string) string {
	return strings.TrimSpace(seedSQL)
}

// validateSeedSQL is the ARCH-046 INSERT-only allowlist: the first of the
// three independent layers protecting the imported schema (the other two are
// the non-superuser student role and the always-rolled-back savepoint).
//
// It strips comments with the same scanner deriveAutoSeed uses, splits on
// top-level semicolons, discards empty segments, and requires every remaining
// statement to lead with the INSERT keyword. The returned slice is the ordered
// list of statements to execute one at a time — never as one multi-command
// string, because the pool is pinned to the extended protocol whose Parse
// message accepts at most one command.
//
// An empty, whitespace-only, or comment-only input is NOT an error: it means
// "no seed", and returns (nil, nil). Every refusal wraps
// ErrAutoSeedUnsupported so callers can turn it into a 400 rather than a 500.
func validateSeedSQL(seedSQL string) ([]string, error) {
	trimmed := normalizeSeedSQL(seedSQL)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > maxSeedSQLBytes {
		return nil, refusef("the seed SQL is %d bytes, which exceeds the %d byte limit", len(trimmed), maxSeedSQLBytes)
	}

	cleaned := stripSQLComments(trimmed)
	if err := checkSeedQuotesBalanced(cleaned); err != nil {
		return nil, err
	}

	var stmts []string
	for _, seg := range splitTopLevelSemicolons(cleaned) {
		s := strings.TrimSpace(seg)
		if s == "" {
			continue
		}
		if !matchKeywordAt(s, 0, "INSERT") {
			return nil, refusef("the seed SQL contains a %s statement; only INSERT statements are allowed, because the seed must add rows and nothing else", leadingSeedKeyword(s))
		}
		stmts = append(stmts, s)
	}
	if len(stmts) == 0 {
		return nil, nil
	}
	if len(stmts) > maxSeedStatements {
		return nil, refusef("the seed SQL has %d statements; at most %d statements are allowed", len(stmts), maxSeedStatements)
	}
	return stmts, nil
}

// checkSeedQuotesBalanced refuses a seed whose quoting does not close. Without
// it, an unterminated literal would swallow every following semicolon and turn
// several statements into one, defeating the per-statement INSERT check.
func checkSeedQuotesBalanced(s string) error {
	inSingle, inDouble := false, false
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		if inSingle {
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		}
	}
	if inSingle || inDouble {
		return refuse("the seed SQL has an unterminated quoted value")
	}
	return nil
}

// leadingSeedKeyword returns the uppercased leading word of stmt, for naming
// the offending statement in a refusal. Returns "unrecognised" when stmt does
// not start with an identifier-shaped token.
func leadingSeedKeyword(stmt string) string {
	end := 0
	for end < len(stmt) && isAutoSeedIdentByte(stmt[end]) {
		end++
	}
	if end == 0 {
		return "unrecognised"
	}
	return strings.ToUpper(stmt[:end])
}

// applyQuestionSeed is the single seeding entry point for all four DML paths
// (practice capture, exam capture, grading, preview). Precedence is ARCH-045:
// a non-empty lecturer seedSQL wins outright and the derived plan is ignored;
// an empty seedSQL delegates to applyAutoSeed, whose nil/empty short-circuit
// means a question with neither seed issues zero extra statements.
//
// MUST be called after SET LOCAL ROLE and inside the caller's savepoint. The
// student login roles are NOINHERIT, so before the role switch the connection
// holds no privileges and the INSERT would fail outright.
func applyQuestionSeed(ctx context.Context, tx pgx.Tx, seedSQL string, plan *AutoSeedPlan) error {
	if normalizeSeedSQL(seedSQL) == "" {
		return applyAutoSeed(ctx, tx, plan)
	}

	stmts, err := validateSeedSQL(seedSQL)
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, execErr := tx.Exec(ctx, stmt); execErr != nil {
			return execErr
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && gofmt -l internal/repository/ && go test ./internal/repository/ -run 'TestValidateSeedSQL|TestNormalizeSeedSQL' -v`

Expected: `gofmt -l` prints nothing; every test PASSes.

- [ ] **Step 5: Confirm nothing else broke**

Run: `cd backend && go build ./... && go test ./internal/repository/ -run 'TestDeriveAutoSeed|TestAutoSeed|TestBuildAutoSeedInsert' -p 1`

Expected: PASS. Existing derivation behaviour is untouched.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/repository/dml_autoseed.go backend/internal/repository/dml_seedsql_test.go
git commit -m "feat(seed): INSERT-only validator and applyQuestionSeed entry point (ARCH-046/047)"
```

---

### Task 2: Schema columns, Go-side backfill, and sequence grants

**Files:**
- Modify: `backend/internal/repository/postgres_schema.go`
- Modify: `backend/internal/repository/postgres_preview_schema.go`
- Test: `backend/internal/repository/seedsql_migration_test.go` (create)

**Interfaces:**
- Consumes: `stripSQLComments`, `matchKeywordAt` from Task 1's file (already present before Task 1).
- Produces: `func (r *PostgresRepository) migrateDMLKind(ctx context.Context) error`, and the columns `dml_kind` / `seed_sql` on both `app.questions` and `app.practice_questions`.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/seedsql_migration_test.go`. Follow the existing DB-test bootstrap in `backend/internal/repository/dml_autoseed_migration_test.go` — open it first and copy its `newTestRepo`/skip-if-no-DB preamble verbatim rather than inventing a new one.

```go
package repository

import (
	"context"
	"testing"
)

// TestSeedSQLColumnsExist asserts both additive columns landed on both tables,
// with seed_sql nullable and dml_kind constrained.
func TestSeedSQLColumnsExist(t *testing.T) {
	repo := newTestRepo(t) // same helper the other migration tests use
	ctx := context.Background()

	for _, table := range []string{"questions", "practice_questions"} {
		for _, col := range []string{"dml_kind", "seed_sql"} {
			var n int
			err := repo.pool.QueryRow(ctx, `
				SELECT count(*) FROM information_schema.columns
				WHERE table_schema = 'app' AND table_name = $1 AND column_name = $2`,
				table, col).Scan(&n)
			if err != nil {
				t.Fatalf("query information_schema: %v", err)
			}
			if n != 1 {
				t.Errorf("app.%s is missing column %s", table, col)
			}
		}
	}
}

// TestMigrateDMLKindIsIdempotentAndCorrect inserts DML rows with a NULL
// dml_kind, runs the backfill twice, and asserts the derived kind — including
// the case a SQL regex would get wrong, a statement hidden behind a comment.
func TestMigrateDMLKindIsIdempotentAndCorrect(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	cases := []struct {
		sql  string
		want string
	}{
		{"DELETE FROM employee WHERE ssn = '1'", "DELETE"},
		{"UPDATE employee SET fname = 'x' WHERE ssn = '1'", "UPDATE"},
		{"INSERT INTO employee (ssn) VALUES ('1')", "INSERT"},
		{"-- remove the row\nDELETE FROM employee WHERE ssn = '1'", "DELETE"},
		{"/* c */ update employee set fname='x' where ssn='1'", "UPDATE"},
	}

	var ids []int
	for _, tc := range cases {
		var id int
		err := repo.pool.QueryRow(ctx, `
			INSERT INTO app.practice_questions
				(created_by, target_table, correct_sql, question_type, requirement, dml_kind)
			SELECT id, 'employee', $1, 'DML', 'seed backfill fixture', NULL
			FROM app.system_users ORDER BY id LIMIT 1
			RETURNING id`, tc.sql).Scan(&id)
		if err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		ids = append(ids, id)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = repo.pool.Exec(ctx, `DELETE FROM app.practice_questions WHERE id = $1`, id)
		}
	})

	for run := 0; run < 2; run++ {
		if err := repo.migrateDMLKind(ctx); err != nil {
			t.Fatalf("migrateDMLKind run %d: %v", run, err)
		}
		for i, tc := range cases {
			var got *string
			if err := repo.pool.QueryRow(ctx,
				`SELECT dml_kind FROM app.practice_questions WHERE id = $1`, ids[i]).Scan(&got); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Errorf("run %d, sql %q: dml_kind = %v, want %q", run, tc.sql, got, tc.want)
			}
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/repository/ -run 'TestSeedSQLColumnsExist|TestMigrateDMLKind' -p 1 -v`

Expected: FAIL to compile (`repo.migrateDMLKind undefined`), or FAIL asserting the missing columns.

- [ ] **Step 3a: Add the columns to the `CREATE TABLE` bodies**

In `backend/internal/repository/postgres_schema.go`, in the `app.questions` DDL, immediately after the `auto_seed JSONB,` line (around `:866`):

```go
			auto_seed JSONB,
			dml_kind TEXT CHECK (dml_kind IS NULL OR dml_kind IN ('INSERT','UPDATE','DELETE')),
			seed_sql TEXT,
```

In the `app.practice_questions` DDL, immediately after its `auto_seed JSONB,` line (around `:917`):

```go
			auto_seed JSONB,
			dml_kind TEXT CHECK (dml_kind IS NULL OR dml_kind IN ('INSERT','UPDATE','DELETE')),
			seed_sql TEXT,
```

- [ ] **Step 3b: Add the migrations**

In the same file, in the `extraMigrations` slice, immediately after the two `auto_seed JSONB` entries (around `:1095`):

```go
		// Lecturer-authored seed rows (2026-08-08, ARCH-043). dml_kind splits the
		// authoring dropdown into INSERT/UPDATE/DELETE WITHOUT widening the
		// question_type CHECK — every existing `Type == "DML"` branch keeps
		// working. seed_sql holds the lecturer's INSERT-only seed block, applied
		// inside the rolled-back evaluation savepoint. Both NULL for QUERY
		// questions and for every pre-existing row; dml_kind is backfilled by
		// migrateDMLKind.
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS dml_kind TEXT",
		"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS dml_kind TEXT",
		"ALTER TABLE app.questions ADD COLUMN IF NOT EXISTS seed_sql TEXT",
		"ALTER TABLE app.practice_questions ADD COLUMN IF NOT EXISTS seed_sql TEXT",
		"ALTER TABLE app.questions DROP CONSTRAINT IF EXISTS questions_dml_kind_check",
		"ALTER TABLE app.questions ADD CONSTRAINT questions_dml_kind_check CHECK (dml_kind IS NULL OR dml_kind IN ('INSERT','UPDATE','DELETE'))",
		"ALTER TABLE app.practice_questions DROP CONSTRAINT IF EXISTS practice_questions_dml_kind_check",
		"ALTER TABLE app.practice_questions ADD CONSTRAINT practice_questions_dml_kind_check CHECK (dml_kind IS NULL OR dml_kind IN ('INSERT','UPDATE','DELETE'))",
```

- [ ] **Step 3c: Write the Go backfill**

Add to `backend/internal/repository/postgres_schema.go`:

```go
// migrateDMLKind backfills app.questions.dml_kind and
// app.practice_questions.dml_kind for rows written before the column existed
// (ARCH-043). The kind is read from correct_sql's leading statement keyword
// using the SAME comment-stripping scanner deriveAutoSeed uses — a Postgres
// regex cannot reliably see past a leading `--` or `/* */` comment, so this
// runs in Go rather than as an UPDATE ... CASE.
//
// Idempotent: the WHERE clause selects only rows whose dml_kind IS NULL, so a
// second boot matches nothing. A correct_sql that leads with none of the three
// keywords is left NULL rather than guessed; the lecturer picks the kind on
// next edit.
func (r *PostgresRepository) migrateDMLKind(ctx context.Context) error {
	for _, table := range []string{"app.questions", "app.practice_questions"} {
		rows, err := r.pool.Query(ctx, fmt.Sprintf(
			`SELECT id, correct_sql FROM %s WHERE question_type = 'DML' AND dml_kind IS NULL`, table))
		if err != nil {
			return fmt.Errorf("migrateDMLKind: select %s: %w", table, err)
		}

		type pending struct {
			id   int
			kind string
		}
		var updates []pending
		for rows.Next() {
			var id int
			var correctSQL string
			if err := rows.Scan(&id, &correctSQL); err != nil {
				rows.Close()
				return fmt.Errorf("migrateDMLKind: scan %s: %w", table, err)
			}
			if kind := leadingDMLKind(correctSQL); kind != "" {
				updates = append(updates, pending{id: id, kind: kind})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("migrateDMLKind: iterate %s: %w", table, err)
		}

		for _, u := range updates {
			if _, err := r.pool.Exec(ctx, fmt.Sprintf(
				`UPDATE %s SET dml_kind = $1 WHERE id = $2 AND dml_kind IS NULL`, table),
				u.kind, u.id); err != nil {
				return fmt.Errorf("migrateDMLKind: update %s id=%d: %w", table, u.id, err)
			}
		}
		if len(updates) > 0 {
			log.Printf("migrateDMLKind: backfilled dml_kind for %d row(s) in %s", len(updates), table)
		}
	}
	return nil
}

// leadingDMLKind returns "INSERT", "UPDATE", or "DELETE" for sql's leading
// statement keyword after comments are stripped, or "" when it is none of
// those. Shared by the backfill and by the write-path validation so they can
// never disagree about what kind a statement is.
func leadingDMLKind(sql string) string {
	trimmed := strings.TrimSpace(stripSQLComments(sql))
	switch {
	case matchKeywordAt(trimmed, 0, "INSERT"):
		return "INSERT"
	case matchKeywordAt(trimmed, 0, "UPDATE"):
		return "UPDATE"
	case matchKeywordAt(trimmed, 0, "DELETE"):
		return "DELETE"
	default:
		return ""
	}
}
```

`postgres_schema.go` already imports `fmt`, `log`, and `strings`. Verify with `head -20 backend/internal/repository/postgres_schema.go` and add any that are genuinely absent.

- [ ] **Step 3d: Call the backfill from the migration runner**

Find the function that executes `extraMigrations` (search: `grep -n "extraMigrations" backend/internal/repository/postgres_schema.go`). Immediately after its loop finishes successfully, and before it returns nil, add:

```go
	if err := r.migrateDMLKind(ctx); err != nil {
		return err
	}
```

- [ ] **Step 3e: Grant sequence usage (ARCH-049)**

In `backend/internal/repository/postgres_schema.go`, in `GrantSchemaPermissions` (`:175`), extend the `queries` slice:

```go
	queries := []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO exam_student", safeSchema),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO exam_student", safeSchema),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO exam_student", safeSchema),
		// ARCH-049: a seed INSERT that omits a serial/identity column calls
		// nextval() as exam_student. Without sequence USAGE that fails with
		// "permission denied for sequence" — a live defect in the shipped
		// derived auto-seed too, since ARCH-038 deliberately omits defaulted
		// columns and a serial column is a defaulted column.
		fmt.Sprintf("GRANT USAGE ON ALL SEQUENCES IN SCHEMA %s TO exam_student", safeSchema),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE ON SEQUENCES TO exam_student", safeSchema),
	}
```

In `backend/internal/repository/postgres_preview_schema.go`, in `grantPreviewSchemaPermissions` (`:51`), add the same two statements for `exam_student_preview` **only inside the `allowDML` branch**. The read-only preview role must not gain sequence usage. Read the function first and place them next to the existing `allowDML`-gated table grants:

```go
		fmt.Sprintf("GRANT USAGE ON ALL SEQUENCES IN SCHEMA %s TO exam_student_preview", safeSchema),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT USAGE ON SEQUENCES TO exam_student_preview", safeSchema),
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd backend && gofmt -l internal/repository/ && go build ./... && go test ./internal/repository/ -run 'TestSeedSQLColumnsExist|TestMigrateDMLKind' -p 1 -v`

Expected: `gofmt -l` prints nothing; both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_schema.go backend/internal/repository/postgres_preview_schema.go backend/internal/repository/seedsql_migration_test.go
git commit -m "feat(seed): dml_kind + seed_sql columns, Go backfill, sequence grants (ARCH-043/049)"
```

---

### Task 3: Model fields

**Files:**
- Modify: `backend/internal/models/submission.go` (the `Question` struct, around `:40-75`)
- Modify: `backend/internal/models/practice.go` (the `PracticeQuestion` struct and `AsQuestion`, around `:30-110`)
- Test: `backend/internal/models/practice_test.go` (extend the existing `AsQuestion` table test)

**Interfaces:**
- Produces: `models.Question.DMLKind string`, `models.Question.SeedSQL string`, `models.PracticeQuestion.DMLKind string`, `models.PracticeQuestion.SeedSQL string`, both carried by `PracticeQuestion.AsQuestion()`.

- [ ] **Step 1: Write the failing test**

In `backend/internal/models/practice_test.go`, add:

```go
func TestAsQuestionCarriesSeedFields(t *testing.T) {
	pq := PracticeQuestion{
		Type:        "DML",
		TargetTable: "employee",
		DMLKind:     "DELETE",
		SeedSQL:     "INSERT INTO employee (ssn) VALUES ('1')",
	}
	got := pq.AsQuestion()
	if got.DMLKind != "DELETE" {
		t.Errorf("DMLKind: got %q, want %q", got.DMLKind, "DELETE")
	}
	if got.SeedSQL != pq.SeedSQL {
		t.Errorf("SeedSQL: got %q, want %q", got.SeedSQL, pq.SeedSQL)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/models/ -run TestAsQuestionCarriesSeedFields -v`

Expected: FAIL to compile — `unknown field DMLKind in struct literal`.

- [ ] **Step 3: Implement**

In `backend/internal/models/submission.go`, inside `type Question struct`, immediately after the `AutoSeed json.RawMessage` field:

```go
	// DMLKind is the DML statement kind the lecturer selected: "INSERT",
	// "UPDATE", or "DELETE". Empty for QUERY questions and for DML questions
	// written before the column existed (ARCH-043). Presentation and
	// validation only — grading branches on Type, never on this.
	DMLKind string `json:"dml_kind,omitempty"`
	// SeedSQL is the lecturer's INSERT-only seed block, applied inside the
	// rolled-back evaluation savepoint before the answer statement so an
	// UPDATE/DELETE question has a row to act on (ARCH-045/046). When
	// non-empty it takes precedence over AutoSeed, which is then NULL.
	// MUST be scrubbed from every student-facing response (ARCH-052) — it
	// carries the exact values the correct answer must match.
	SeedSQL string `json:"seed_sql,omitempty"`
```

In `backend/internal/models/practice.go`, inside `type PracticeQuestion struct`, immediately after its `AutoSeed json.RawMessage` field, add the identical two fields with the same comments.

In the same file, inside `func (pq PracticeQuestion) AsQuestion() Question`, add to the returned struct literal (next to the existing `TargetTable:` line):

```go
		DMLKind:     pq.DMLKind,
		SeedSQL:     pq.SeedSQL,
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && gofmt -l internal/models/ && go test ./internal/models/ -v`

Expected: `gofmt -l` prints nothing; all model tests PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/models/submission.go backend/internal/models/practice.go backend/internal/models/practice_test.go
git commit -m "feat(seed): dml_kind and seed_sql on Question and PracticeQuestion (ARCH-043)"
```

---

### Task 4: Practice repository — persistence, capture, preview

**Depends on:** Tasks 1, 2, 3.

**Files:**
- Modify: `backend/internal/repository/postgres_practice.go`
- Test: `backend/internal/repository/postgres_practice_seedsql_test.go` (create)

**Interfaces:**
- Consumes: `applyQuestionSeed(ctx, tx, seedSQL string, plan *AutoSeedPlan) error`, `validateSeedSQL(string) ([]string, error)`, `normalizeSeedSQL(string) string`, `ErrAutoSeedUnsupported` (Task 1); `models.PracticeQuestion.DMLKind` / `.SeedSQL` (Task 3); the `dml_kind` / `seed_sql` columns (Task 2).
- Produces: `PreviewPracticeCorrectSQL` gains a trailing `seedSQL string` parameter — **new signature**, consumed by Task 7:
  `PreviewPracticeCorrectSQL(ctx context.Context, categoryID int, qType, targetTable, sqlText string, useDecoy bool, seedSQL string) ([]map[string]interface{}, []string, error)`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/postgres_practice_seedsql_test.go`. Copy the DB-test bootstrap and the category/question fixture helpers from the existing `backend/internal/repository/dml_autoseed_schema_isolation_test.go` — read that file first and reuse its helpers rather than writing new ones.

The test must cover, against a real category schema containing an `employee` table:

```go
// 1. A DELETE question whose row exists ONLY in seed_sql captures successfully.
//    Assert expected_result_status == "captured", expected_rows_affected == 1,
//    and auto_seed IS NULL (ARCH-045 precedence: derivation was never run).
//
// 2. SCHEMA ISOLATION, the core guarantee: after that capture, the real
//    category schema's employee table has exactly the row count it had before,
//    and no row with the seeded ssn exists.
//
// 3. A seed_sql that violates a constraint (duplicate primary key) saves with
//    expected_result_status == "failed" and a capture_error starting with
//    "Seed INSERT failed: ".
//
// 4. A seed_sql that inserts a row the correct_sql's WHERE clause does NOT
//    match saves failed with the capture_error
//    "Your seed rows were inserted, but the correct SQL still affected 0 rows.
//     Check the WHERE clause of the correct SQL against the values you seeded."
//
// 5. REGRESSION: a DELETE question with an empty seed_sql whose row is missing
//    still derives — auto_seed IS NOT NULL and status == "captured".
```

Write each as its own `t.Run` subtest with real assertions on values read back from `app.practice_questions`, not on log output.

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/repository/ -run TestPracticeSeedSQL -p 1 -v`

Expected: FAIL — `seed_sql` is never persisted, so case 1 reads back `expected_result_status = "failed"` with the ARCH-039 derivation refusal.

- [ ] **Step 3a: Normalize and validate on the write paths**

In `CreatePracticeQuestion` and `UpdatePracticeQuestion`, before capture runs:

```go
	q.SeedSQL = normalizeSeedSQL(q.SeedSQL)
	if q.SeedSQL != "" {
		if _, err := validateSeedSQL(q.SeedSQL); err != nil {
			return err
		}
	}
```

- [ ] **Step 3b: Wire the seed into `captureExpectedResult`**

In `captureExpectedResult` (`:432`), replace the entire `if tag.RowsAffected() == 0 { ... }` derivation block's **entry condition** so the lecturer's seed runs first and unconditionally. Restructure the DML branch to:

```go
			var tag pgconn.CommandTag
			if q.SeedSQL != "" {
				// ARCH-045: the lecturer's seed runs BEFORE the model answer and
				// whenever it is present — not only on the 0-row branch — so
				// capture, grading and preview all observe one state. Nested
				// savepoint (ARCH-047) so a failed seed cannot poison the outer
				// capture transaction before the refusal is recorded.
				if _, spErr := tx.Exec(ctx, "SAVEPOINT lecturer_seed"); spErr != nil {
					return spErr
				}
				if seedErr := applyQuestionSeed(ctx, tx, q.SeedSQL, nil); seedErr != nil {
					// ARCH-021 ordering: the tx is aborted, so ROLLBACK TO
					// SAVEPOINT must run before any other statement.
					if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT lecturer_seed"); rbErr != nil {
						return rbErr
					}
					*status = "failed"
					*captureErr = "Seed INSERT failed: " + seedErr.Error() + "."
					return errCaptureFailed
				}
				tag, err = tx.Exec(ctx, q.CorrectSQL)
				if err == nil && tag.RowsAffected() == 0 {
					*status = "failed"
					*captureErr = "Your seed rows were inserted, but the correct SQL still affected 0 rows. Check the WHERE clause of the correct SQL against the values you seeded."
					return errCaptureFailed
				}
				// auto_seed stays nil: ARCH-045 forbids both mechanisms at once.
			} else {
				tag, err = tx.Exec(ctx, q.CorrectSQL)
				if err == nil {
					// ... existing 0-row derivation block, unchanged ...
				}
			}
```

Keep the existing post-image capture (`rowsAffected`, `queryWithTxCols(... "SELECT * FROM %s" ...)`) shared after the branch, exactly as it is today. Do not duplicate it.

- [ ] **Step 3c: Persist the columns**

In `CreatePracticeQuestion`'s `INSERT`, add `dml_kind, seed_sql` to the column list, two placeholders (renumbering the trailing ones), and `q.DMLKind, q.SeedSQL` to the argument list — placed so the arguments still line up positionally. Do the same in `UpdatePracticeQuestion`'s `UPDATE ... SET`, and add `pq.dml_kind, COALESCE(pq.seed_sql, '')` plus `&q.DMLKind, &q.SeedSQL` to `ListPracticeQuestions` (`:678`) and `GetPracticeQuestion` (`:713`).

`dml_kind` is nullable, so scan it as `COALESCE(pq.dml_kind, '')` into the `string` field rather than adding a pointer.

- [ ] **Step 3d: Seed the practice preview endpoint**

Change `PreviewPracticeCorrectSQL` (`:553`) to take a trailing `seedSQL string`, and inside its transaction — after the role switch and inside its savepoint, before the lecturer's SQL runs — call:

```go
	if seedSQL != "" {
		if seedErr := applyQuestionSeed(ctx, tx, seedSQL, nil); seedErr != nil {
			return nil, nil, seedErr
		}
	}
```

Update the one existing caller in `backend/internal/controllers/practice_controller.go` to pass `""` for now; Task 7 replaces it with the request field. Without this the build breaks.

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && gofmt -l internal/repository/ && go build ./... && go test ./internal/repository/ -run 'TestPracticeSeedSQL|TestDMLAutoSeed' -p 1 -v`

Expected: `gofmt -l` prints nothing; the new subtests PASS and every existing auto-seed test still PASSes.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_practice.go backend/internal/repository/postgres_practice_seedsql_test.go backend/internal/controllers/practice_controller.go
git commit -m "feat(seed): practice capture, persistence and preview honour seed_sql (ARCH-045/047)"
```

---

### Task 5: Exam repository — persistence, capture, preview

**Depends on:** Tasks 1, 2, 3.

**Files:**
- Modify: `backend/internal/repository/postgres_questions.go`
- Test: `backend/internal/repository/postgres_questions_seedsql_test.go` (create)

**Interfaces:**
- Consumes: same as Task 4.
- Produces: `PreviewCorrectSQLInSchema` gains a trailing `seedSQL string` parameter — **new signature**, consumed by Task 7:
  `PreviewCorrectSQLInSchema(ctx context.Context, qType, targetTable, sqlText string, examID int, categoryID int, useDecoy bool, seedSQL string) ([]map[string]interface{}, []string, error)`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/postgres_questions_seedsql_test.go`, mirroring Task 4's five cases against `app.questions` instead of `app.practice_questions`. Copy the exam fixture bootstrap from `backend/internal/repository/postgres_questions_autoseed_test.go` — read it first and reuse its helpers.

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/repository/ -run TestExamSeedSQL -p 1 -v`

Expected: FAIL for the same reason as Task 4.

- [ ] **Step 3a: Normalize and validate**

In the exam question save function (the one containing the `insertQ`/`upsertQ` statements at `:226-270`), before capture runs:

```go
	q.SeedSQL = normalizeSeedSQL(q.SeedSQL)
	if q.SeedSQL != "" {
		if _, err := validateSeedSQL(q.SeedSQL); err != nil {
			return 0, err
		}
	}
```

- [ ] **Step 3b: Wire the seed into capture**

In the capture closure (`:95-190`), apply the identical restructuring described in Task 4 Step 3b — lecturer seed first inside a `SAVEPOINT lecturer_seed`, else the existing derivation block untouched — adapted to this file's local variable names (`status`, `captureErr`, `autoSeed` are plain locals here, not pointers).

- [ ] **Step 3c: Persist the columns**

Add `dml_kind, seed_sql` to both `insertQ` and `upsertQ` column lists with new placeholders, add `dml_kind = EXCLUDED.dml_kind,` and `seed_sql = EXCLUDED.seed_sql,` to the `ON CONFLICT DO UPDATE SET` list, and append `q.DMLKind, q.SeedSQL` to both argument lists. Add `q.dml_kind`, `COALESCE(q.seed_sql, '')` to the two read queries at `:467` and `:500` and `&q.DMLKind, &q.SeedSQL` to their `Scan` calls at `:482` and `:522`. Scan `dml_kind` as `COALESCE(q.dml_kind, '')`.

- [ ] **Step 3d: Seed the exam preview endpoints**

Give `PreviewCorrectSQLInSchema` (`:354`) a trailing `seedSQL string`, applying it inside the transaction after the role switch exactly as in Task 4 Step 3d. Leave `PreviewCorrectSQL` (`:317`) alone — it is the no-exam legacy path with no seed field.

Update the caller in `backend/internal/controllers/lecturer_controller.go:533` to pass `""` for now; Task 7 replaces it.

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && gofmt -l internal/repository/ && go build ./... && go test ./internal/repository/ -run 'TestExamSeedSQL|TestQuestionsAutoSeed' -p 1 -v`

Expected: `gofmt -l` prints nothing; new subtests PASS, existing auto-seed tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_questions.go backend/internal/repository/postgres_questions_seedsql_test.go backend/internal/controllers/lecturer_controller.go
git commit -m "feat(seed): exam capture, persistence and preview honour seed_sql (ARCH-045/047)"
```

---

### Task 6: Grading and student-preview execution

**Depends on:** Tasks 1, 2, 3. Independent of Tasks 4 and 5 (different file).

**Files:**
- Modify: `backend/internal/repository/postgres_evaluation.go`
- Test: `backend/internal/repository/postgres_seedsql_isolation_test.go` (create)

**Interfaces:**
- Consumes: `applyQuestionSeed` (Task 1), `models.Question.SeedSQL` (Task 3).

- [ ] **Step 1: Write the failing test**

Create `backend/internal/repository/postgres_seedsql_isolation_test.go`, modelled on the existing `backend/internal/repository/dml_autoseed_schema_isolation_test.go` (read it first, reuse its fixtures). Cover:

```go
// 1. GRADING DISCRIMINATION: a DELETE question seeded via seed_sql grades the
//    matching DELETE as correct, and grades an empty/no-op statement as
//    incorrect. This is the property ARCH-008 exists to protect.
//
// 2. SCHEMA ISOLATION: after grading AND previewing that question, the real
//    schema's row count for the seeded table is unchanged and no seeded row
//    remains.
//
// 3. PREVIEW: evaluateDMLPreview on the same question returns the seeded row
//    in its result set, so the student sees what grading will see.
//
// 4. SEED FAILURE IS NOT THE STUDENT'S FAULT: a question whose seed_sql
//    references a dropped column returns Status == "error" with
//    AutoSeedFailureFeedback, never a wrong-answer verdict, and the pool
//    connection is reusable afterwards (issue a trivial follow-up query on it).
//
// 5. SEQUENCE GRANTS (ARCH-049): a seed INSERT into a table whose primary key
//    is `serial`, omitting that column, succeeds. This FAILS before Task 2's
//    grant fix with "permission denied for sequence".
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/repository/ -run TestSeedSQLEvaluation -p 1 -v`

Expected: FAIL — grading never applies `seed_sql`, so the correct DELETE affects 0 rows and is graded incorrect.

- [ ] **Step 3: Implement**

In `evaluateDML` (`:252`), replace the existing block:

```go
	if plan, seedErr := unmarshalAutoSeed(question.AutoSeed); seedErr != nil || plan != nil {
		if seedErr == nil {
			seedErr = applyAutoSeed(ctx, tx, plan)
		}
```

with:

```go
	// ARCH-045/047: one seeding entry point. A non-empty lecturer seed_sql
	// wins and the derived plan is ignored; an empty one delegates to the
	// derived plan, whose nil short-circuit means zero extra statements for
	// the overwhelmingly common question that needs no seed. MUST stay AFTER
	// SET LOCAL ROLE above — the student login roles are NOINHERIT, so before
	// the switch the connection holds no privileges and the INSERT fails.
	if plan, seedErr := unmarshalAutoSeed(question.AutoSeed); seedErr != nil || plan != nil || question.SeedSQL != "" {
		if seedErr == nil {
			seedErr = applyQuestionSeed(ctx, tx, question.SeedSQL, plan)
		}
```

Leave the entire error-handling body below it untouched — the `ROLLBACK TO SAVEPOINT` → `RESET ROLE` ordering and `AutoSeedFailureFeedback` are already correct and already cover a failed lecturer seed.

Apply the identical change to `evaluateDMLPreview` (`:530`).

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && gofmt -l internal/repository/ && go build ./... && go test ./internal/repository/ -p 1`

Expected: `gofmt -l` prints nothing; the whole repository package PASSes, including every pre-existing auto-seed, temp-table-forgery, and dual-schema test.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_evaluation.go backend/internal/repository/postgres_seedsql_isolation_test.go
git commit -m "feat(seed): grading and student preview replay seed_sql (ARCH-047)"
```

---

### Task 7: Controllers — request fields, validation, student scrub

**Depends on:** Tasks 3, 4, 5.

**Files:**
- Modify: `backend/internal/controllers/practice_controller.go`
- Modify: `backend/internal/controllers/lecturer_controller.go`
- Modify: `backend/internal/controllers/quiz_controller.go`
- Test: `backend/internal/controllers/autoseed_leak_http_test.go` (extend)

**Interfaces:**
- Consumes: the new `PreviewPracticeCorrectSQL(..., seedSQL string)` (Task 4) and `PreviewCorrectSQLInSchema(..., seedSQL string)` (Task 5) signatures; `models.Question.DMLKind` / `.SeedSQL` (Task 3).
- Produces: `func validateSeedFields(qType, dmlKind, seedSQL string) error` in `practice_controller.go`, exported to the other controllers via the shared package.

- [ ] **Step 1: Write the failing test**

Extend `backend/internal/controllers/autoseed_leak_http_test.go`. It already builds a fixture question with a non-null `auto_seed` and asserts the student list/get responses carry no `auto_seed` key. Add a parallel fixture whose `seed_sql` is non-empty, and extend each existing assertion loop:

```go
		if v, ok := q["seed_sql"]; ok && v != nil {
			t.Fatalf("SECURITY REGRESSION: student response leaked seed_sql (question id=%v): %v", q["id"], v)
		}
```

Add it at every place the file currently checks `auto_seed` — the practice list assertion, the practice get assertion, and the exam questions assertion.

Also add a table test for the new validator:

```go
func TestValidateSeedFields(t *testing.T) {
	cases := []struct {
		name    string
		qType   string
		kind    string
		seed    string
		wantErr bool
	}{
		{"query with no kind or seed", "QUERY", "", "", false},
		{"query with a kind", "QUERY", "DELETE", "", true},
		{"query with a seed", "QUERY", "", "INSERT INTO t (a) VALUES (1)", true},
		{"dml with no kind", "DML", "", "", true},
		{"dml delete with seed", "DML", "DELETE", "INSERT INTO t (a) VALUES (1)", false},
		{"dml update with seed", "DML", "UPDATE", "INSERT INTO t (a) VALUES (1)", false},
		{"dml insert with seed", "DML", "INSERT", "INSERT INTO t (a) VALUES (1)", true},
		{"dml insert without seed", "DML", "INSERT", "", false},
		{"dml with bogus kind", "DML", "MERGE", "", true},
		{"dml delete with non-insert seed", "DML", "DELETE", "DELETE FROM t", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSeedFields(tc.qType, tc.kind, tc.seed)
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected with %v, want accept", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/controllers/ -run 'TestValidateSeedFields|AutoSeedLeak' -p 1 -v`

Expected: FAIL to compile — `undefined: validateSeedFields`.

- [ ] **Step 3a: The shared validator**

Add to `backend/internal/controllers/practice_controller.go`:

```go
// validateSeedFields enforces ARCH-044 and ARCH-050 on every question write
// path. Returns a lecturer-facing error suitable for an HTTP 400 body; the
// caller never turns any of these into a 500.
func validateSeedFields(qType, dmlKind, seedSQL string) error {
	seedSQL = strings.TrimSpace(seedSQL)

	if qType == "QUERY" {
		if dmlKind != "" {
			return errors.New("dml_kind must be empty for a QUERY question")
		}
		if seedSQL != "" {
			return errors.New("seed_sql must be empty for a QUERY question")
		}
		return nil
	}

	switch dmlKind {
	case "INSERT", "UPDATE", "DELETE":
	case "":
		return errors.New("dml_kind is required for a DML question and must be INSERT, UPDATE, or DELETE")
	default:
		return fmt.Errorf("dml_kind must be INSERT, UPDATE, or DELETE, got %q", dmlKind)
	}

	if seedSQL == "" {
		return nil
	}
	if dmlKind == "INSERT" {
		// ARCH-044: an INSERT model answer can only affect 0 rows via
		// INSERT ... SELECT over an empty source, which seeding cannot fix.
		return errors.New("seed_sql applies only to UPDATE and DELETE questions")
	}
	if _, err := repository.ValidateSeedSQL(seedSQL); err != nil {
		return fmt.Errorf("seed_sql is not valid: %w", err)
	}
	return nil
}
```

This needs `validateSeedSQL` reachable from the controllers package. In `backend/internal/repository/dml_autoseed.go`, add the exported wrapper next to the unexported one:

```go
// ValidateSeedSQL is the exported form of validateSeedSQL, for the controller
// layer's request validation. The repository re-validates on its own write
// path regardless — this is a fast 400 for the lecturer, not the security
// boundary.
func ValidateSeedSQL(seedSQL string) ([]string, error) { return validateSeedSQL(seedSQL) }
```

- [ ] **Step 3b: Accept the fields on every write path**

In the practice question create/update request structs in `practice_controller.go`, and the exam question request struct in `lecturer_controller.go`, add:

```go
		DMLKind string `json:"dml_kind"`
		SeedSQL string `json:"seed_sql"`
```

After each `ShouldBindJSON` succeeds and the existing type check passes, add:

```go
	if err := validateSeedFields(req.Type, req.DMLKind, req.SeedSQL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
```

and carry `req.DMLKind` / `req.SeedSQL` into the `models.PracticeQuestion` / `models.Question` the handler builds.

- [ ] **Step 3c: Accept `seed_sql` on the two preview endpoints**

In `practice_controller.go`'s `LecturerPreview` request struct (`:382`) and `lecturer_controller.go`'s `PreviewCorrectSQL` request struct (`:511`), add `SeedSQL string \`json:"seed_sql"\``. Validate it with `repository.ValidateSeedSQL` when non-empty (400 on refusal), then pass it as the new trailing argument to `PreviewPracticeCorrectSQL` / `PreviewCorrectSQLInSchema`, replacing the `""` placeholders Tasks 4 and 5 left.

- [ ] **Step 3d: Scrub `seed_sql` from student responses (ARCH-052)**

At all three existing `AutoSeed = nil` scrub sites — `quiz_controller.go:170`, `practice_controller.go:65`, `practice_controller.go:104` — add the matching line directly beneath:

```go
		questions[i].AutoSeed = nil
		// ARCH-052: seed_sql leaks strictly more than auto_seed — it is
		// lecturer-written SQL holding the exact values the answer must match.
		questions[i].SeedSQL = ""
```

(and `question.SeedSQL = ""` at the single-question site).

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && gofmt -l internal/ && go build ./... && go test ./internal/controllers/ -p 1 -v`

Expected: `gofmt -l` prints nothing; `TestValidateSeedFields` and every leak test PASS.

- [ ] **Step 5: Full backend suite**

Run: `cd backend && go test ./... -p 1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/controllers/ backend/internal/repository/dml_autoseed.go
git commit -m "feat(seed): API fields, write validation, and student-response scrub (ARCH-050/052)"
```

---

### Task 8: Frontend types

**Depends on:** nothing. Can run alongside Tasks 1-7.

**Files:**
- Modify: `frontend/src/services/practice.ts` (the `PracticeQuestion` interface, around `:73-101`)
- Modify: `frontend/src/components/lecturer/types.ts` (the exam question interface)

**Interfaces:**
- Produces: `dml_kind?: string` and `seed_sql?: string` on both question interfaces, consumed by Tasks 9 and 10.

- [ ] **Step 1: Add the fields**

In `frontend/src/services/practice.ts`, inside `export interface PracticeQuestion`, next to `auto_seed`:

```ts
  // DML statement kind the lecturer selected: 'INSERT' | 'UPDATE' | 'DELETE'.
  // Absent for QUERY questions and for DML questions authored before the
  // column existed. Presentation and validation only.
  dml_kind?: string
  // Lecturer-authored INSERT-only seed block, run inside a rolled-back
  // transaction so an UPDATE/DELETE question has a row to act on. Never
  // present in a student-facing response.
  seed_sql?: string
```

Add the identical two fields to the exam question interface in `frontend/src/components/lecturer/types.ts`, next to its `auto_seed` field.

- [ ] **Step 2: Verify types still compile**

Run: `cd frontend && npx vue-tsc --noEmit`

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/services/practice.ts frontend/src/components/lecturer/types.ts
git commit -m "feat(seed): dml_kind and seed_sql on frontend question types"
```

---

### Task 9: `PracticeAuthoring.vue` — four-way type select and seed field

**Depends on:** Task 8.

**Files:**
- Modify: `frontend/src/components/PracticeAuthoring.vue`
- Test: `frontend/src/__tests__/components/practice_authoring_seed.test.ts` (create)

**Interfaces:**
- Consumes: `PracticeQuestion.dml_kind`, `PracticeQuestion.seed_sql` (Task 8).

- [ ] **Step 1: Write the failing test**

Create `frontend/src/__tests__/components/practice_authoring_seed.test.ts`. Read an existing component test in that directory first (e.g. `lecturer_sort_filter.test.ts`) and copy its mount/stub setup. Assert:

```ts
// 1. The type <select> offers exactly QUERY, INSERT, UPDATE, DELETE.
// 2. Choosing DELETE sets form.question_type === 'DML' and form.dml_kind === 'DELETE'.
// 3. Choosing QUERY sets form.question_type === 'QUERY' and form.dml_kind === ''.
// 4. The seed textarea is absent for QUERY and for INSERT, present for UPDATE and DELETE.
// 5. Typing a seed then switching the kind to INSERT clears form.seed_sql.
// 6. Loading a question with question_type 'DML' and no dml_kind selects nothing
//    invalid — the select falls back to a disabled 'DML' placeholder option.
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend && npm test -- -- practice_authoring_seed`

Expected: FAIL — the select still offers only two options.

- [ ] **Step 3a: The four-way computed**

In the component's `<script setup>`, add:

```ts
// The wire format keeps question_type as 'QUERY' | 'DML' (the database CHECK
// constraint is deliberately unchanged, ARCH-043); the four-way authoring
// choice is question_type + dml_kind. This computed is the single place the
// two representations are converted, in both directions.
const questionKind = computed<string>({
  get() {
    if (form.question_type === 'QUERY') return 'QUERY'
    return form.dml_kind || 'DML'
  },
  set(v: string) {
    if (v === 'QUERY') {
      form.question_type = 'QUERY'
      form.dml_kind = ''
      form.seed_sql = ''
      return
    }
    form.question_type = 'DML'
    form.dml_kind = v
    // ARCH-044: the seed field only exists for UPDATE and DELETE. Clearing it
    // here means a hidden field can never be silently persisted.
    if (v !== 'UPDATE' && v !== 'DELETE') form.seed_sql = ''
  },
})

const seedApplies = computed(() => form.dml_kind === 'UPDATE' || form.dml_kind === 'DELETE')
```

Add `dml_kind: ''` and `seed_sql: ''` to the form's initial state and to whatever `resetForm`/`loadQuestion` functions the component already has, so an edit round-trip carries them.

- [ ] **Step 3b: The template**

Replace the type select (`:612-617`):

```html
          <div class="field">
            <label>Question Type</label>
            <select v-model="questionKind" class="input">
              <option value="QUERY">QUERY</option>
              <option value="INSERT">INSERT</option>
              <option value="UPDATE">UPDATE</option>
              <option value="DELETE">DELETE</option>
              <option value="DML" disabled>DML (pick a kind)</option>
            </select>
          </div>
```

Change the Target Table field's condition from `form.question_type == 'DML'` to the same `form.question_type === 'DML'` (behaviour unchanged; just normalise the comparison), and keep its label.

Directly beneath the **Correct SQL** field, add:

```html
          <div class="field" style="grid-column: span 2;" v-if="seedApplies">
            <label>Seed rows (sandbox only)</label>
            <textarea v-model="form.seed_sql" class="input" rows="4"
              placeholder="INSERT INTO employee (ssn, fname) VALUES ('999999999', 'Test');"></textarea>
            <small class="muted">
              These INSERT statements run inside a transaction that is always rolled back.
              They give your UPDATE/DELETE something to act on. They never modify the imported database.
            </small>
          </div>
```

- [ ] **Step 3c: Gate the read-only auto-seed panel**

Find the existing "Auto-seeded row" panel and change its `v-if` so it renders only when there is no lecturer seed — the two are mutually exclusive by ARCH-045 and must never both show:

```html
v-if="!form.seed_sql && loadedQuestion?.auto_seed"
```

Use whatever the component already names the loaded question object; do not introduce a new one.

- [ ] **Step 4: Run to verify it passes**

Run: `cd frontend && npm test -- -- practice_authoring_seed && npx vue-tsc --noEmit`

Expected: all new assertions PASS; no type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/PracticeAuthoring.vue frontend/src/__tests__/components/practice_authoring_seed.test.ts
git commit -m "feat(seed): four-way type select and seed field in practice authoring (ARCH-051)"
```

---

### Task 10: `ExamQuestionsPanel.vue` — four-way type select and seed field

**Depends on:** Task 8. Independent of Task 9 (different file).

**Files:**
- Modify: `frontend/src/components/lecturer/ExamQuestionsPanel.vue`
- Test: `frontend/src/__tests__/components/exam_questions_seed.test.ts` (create)

**Interfaces:**
- Consumes: the exam question interface's `dml_kind` / `seed_sql` (Task 8).

- [ ] **Step 1: Write the failing test**

Create `frontend/src/__tests__/components/exam_questions_seed.test.ts` asserting the same six properties as Task 9 Step 1, against `question.type` / `question.dml_kind` / `question.seed_sql` (this component's state object is `question`, not `form`).

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend && npm test -- -- exam_questions_seed`

Expected: FAIL — the select still offers only two options.

- [ ] **Step 3a: The computed**

Add to `<script setup>`, mirroring Task 9 but bound to this component's `question` ref:

```ts
const questionKind = computed<string>({
  get() {
    if (question.value.type === 'QUERY') return 'QUERY'
    return question.value.dml_kind || 'DML'
  },
  set(v: string) {
    if (v === 'QUERY') {
      question.value.type = 'QUERY'
      question.value.dml_kind = ''
      question.value.seed_sql = ''
      return
    }
    question.value.type = 'DML'
    question.value.dml_kind = v
    if (v !== 'UPDATE' && v !== 'DELETE') question.value.seed_sql = ''
  },
})

const seedApplies = computed(
  () => question.value.dml_kind === 'UPDATE' || question.value.dml_kind === 'DELETE',
)
```

Add `dml_kind: ''` and `seed_sql: ''` to the blank-question factory this component uses when starting a new question, and make sure the save payload includes both.

- [ ] **Step 3b: The template**

Replace the type select (`:853-858`):

```html
          <select id="exam-question-type" v-model="questionKind">
            <option value="QUERY">Query (SELECT)</option>
            <option value="INSERT">INSERT</option>
            <option value="UPDATE">UPDATE</option>
            <option value="DELETE">DELETE</option>
            <option value="DML" disabled>DML (pick a kind)</option>
          </select>
```

Directly beneath the Correct SQL input, add:

```html
        <div class="input-group" v-if="seedApplies">
          <label for="exam-question-seed-sql">Seed rows (sandbox only):</label>
          <textarea id="exam-question-seed-sql" v-model="question.seed_sql" rows="4"
            placeholder="INSERT INTO employee (ssn, fname) VALUES ('999999999', 'Test');"></textarea>
          <small class="muted">
            These INSERT statements run inside a transaction that is always rolled back.
            They give your UPDATE/DELETE something to act on. They never modify the imported database.
          </small>
        </div>
```

Gate the existing read-only auto-seed panel (`:107` region) on `!question.seed_sql && question.auto_seed`, as in Task 9 Step 3c.

Leave the two `question.type === 'DML'` target-table guards at `:526` and `:571` exactly as they are — `type` is still `'DML'` for all three kinds, so they keep working unchanged.

- [ ] **Step 4: Run to verify it passes**

Run: `cd frontend && npm test -- && npx vue-tsc --noEmit`

Expected: the whole frontend suite PASSes; no type errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/lecturer/ExamQuestionsPanel.vue frontend/src/__tests__/components/exam_questions_seed.test.ts
git commit -m "feat(seed): four-way type select and seed field in exam authoring (ARCH-051)"
```

---

### Task 11: End-to-end verification

**Depends on:** all prior tasks.

**Files:** none modified — this task only runs and reports.

- [ ] **Step 1: Full backend suite**

Run: `cd backend && go build ./... && gofmt -l . && go test ./... -p 1`

Expected: build succeeds, `gofmt -l` prints nothing, all tests PASS.

- [ ] **Step 2: Full frontend suite**

Run: `cd frontend && npx vue-tsc --noEmit && npm test -- && npm run build`

Expected: no type errors, all tests PASS, build succeeds.

- [ ] **Step 3: Confirm the CHECK constraint was never widened**

Run:
```bash
grep -n "question_type IN" backend/internal/repository/postgres_schema.go
```
Expected: every hit still reads `question_type IN ('QUERY','DML')`.

- [ ] **Step 4: Confirm no student path can leak the seed**

Run:
```bash
grep -n "SeedSQL = \"\"" backend/internal/controllers/*.go
```
Expected: exactly three hits — `quiz_controller.go`, and two in `practice_controller.go`.

- [ ] **Step 5: Report**

State plainly which suites ran, their results, and anything that was skipped (for example, DB-backed tests skipped because no test database was reachable). Do not claim completion for a suite that did not run.
