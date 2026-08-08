# Structural Seed Invariants Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `seed_sql`/`auto_seed` structurally unable to reach a student or arrive from a client, make `dml_kind` agree with `correct_sql`, and repair the regression that made CTE-led DML questions unsavable.

**Architecture:** `json:"-"` on both model fields removes them from `encoding/json` in both directions. Lecturer reads opt back in via a view struct; the two write handlers recover the two fields with a second, narrow bind rather than a full request struct. `validateSeedFields` gains the `correct_sql` agreement check and stops demanding a kind it cannot itself derive.

**Tech Stack:** Go + Gin + pgx/v5, PostgreSQL.

**Spec:** `docs/superpowers/specs/2026-08-08-seed-invariants-structural-design.md` (ARCH-053 … ARCH-057).

## Global Constraints

- Backend tests touching the shared database MUST run with `go test -p 1`.
- No schema change. No column added, dropped, or altered. No migration.
- The lecturer-facing wire shape must not change: `auto_seed` and `seed_sql` keep appearing in lecturer authoring reads exactly as today, so **no frontend file is edited by this plan**.
- ARCH-053 and ARCH-055 must land in the SAME commit. `json:"-"` alone stops a lecturer's seed from being saved; the second bind alone does nothing. Splitting them ships a broken save path.
- `gofmt -l internal/` must report only the three pre-existing offenders (`internal/services/sse_hub_test.go`, `internal/services/student_import_service_test.go`). Never reformat those.
- A test database is available at `postgres://quiz:quizpw@127.0.0.1:55432/quiztest?sslmode=disable`. If it is not reachable, start one:
  `docker run -d --name sqlquiz-test-pg -e POSTGRES_USER=quiz -e POSTGRES_PASSWORD=quizpw -e POSTGRES_DB=quiztest -p 127.0.0.1:55432:5432 --tmpfs /var/lib/postgresql/data:rw postgres:15-alpine`
- Some tests in `internal/services` are DB-state sensitive. When a full-suite result matters, run it against a freshly created database.

---

### Task 1: Hide the fields, add the lecturer views, keep saving working

The whole ARCH-053 + ARCH-054 + ARCH-055 + ARCH-056 change, in one commit, because splitting it ships a broken save path.

**Files:**
- Modify: `backend/internal/models/submission.go` (the `Question` struct)
- Modify: `backend/internal/models/practice.go` (the `PracticeQuestion` struct)
- Create: `backend/internal/models/lecturer_view.go`
- Create: `backend/internal/models/lecturer_view_test.go`
- Modify: `backend/internal/controllers/practice_controller.go`
- Modify: `backend/internal/controllers/lecturer_controller.go`
- Modify: `backend/internal/controllers/quiz_controller.go`

**Interfaces:**
- Produces:
  - `models.LecturerQuestionView` with `AsLecturerQuestionView(Question) LecturerQuestionView` and `AsLecturerQuestionViews([]Question) []LecturerQuestionView`
  - `models.LecturerPracticeQuestionView` with `AsLecturerPracticeQuestionView(PracticeQuestion) LecturerPracticeQuestionView` and `AsLecturerPracticeQuestionViews([]PracticeQuestion) []LecturerPracticeQuestionView`

- [ ] **Step 1: Write the failing serialization tests**

Create `backend/internal/models/lecturer_view_test.go`:

```go
package models

import (
	"encoding/json"
	"strings"
	"testing"
)

const seedFixture = "INSERT INTO employee (ssn) VALUES ('999999999')"

// TestQuestionNeverMarshalsSeedFields is the ARCH-053 guarantee: no response
// built from a bare Question can carry either field, whatever the handler does.
func TestQuestionNeverMarshalsSeedFields(t *testing.T) {
	q := Question{
		Type:     "DML",
		DMLKind:  "DELETE",
		SeedSQL:  seedFixture,
		AutoSeed: json.RawMessage(`{"table":"employee","columns":[]}`),
	}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal Question: %v", err)
	}
	got := string(b)
	for _, key := range []string{"seed_sql", "auto_seed", seedFixture} {
		if strings.Contains(got, key) {
			t.Errorf("Question JSON leaked %q: %s", key, got)
		}
	}
	if !strings.Contains(got, "dml_kind") {
		t.Errorf("dml_kind is not secret and must still marshal: %s", got)
	}
}

func TestPracticeQuestionNeverMarshalsSeedFields(t *testing.T) {
	pq := PracticeQuestion{
		Type:     "DML",
		DMLKind:  "DELETE",
		SeedSQL:  seedFixture,
		AutoSeed: json.RawMessage(`{"table":"employee","columns":[]}`),
	}
	b, err := json.Marshal(pq)
	if err != nil {
		t.Fatalf("marshal PracticeQuestion: %v", err)
	}
	got := string(b)
	for _, key := range []string{"seed_sql", "auto_seed", seedFixture} {
		if strings.Contains(got, key) {
			t.Errorf("PracticeQuestion JSON leaked %q: %s", key, got)
		}
	}
}

// TestNeitherFieldCanBeClientSupplied is the inbound half of ARCH-053.
func TestNeitherFieldCanBeClientSupplied(t *testing.T) {
	body := `{"type":"DML","dml_kind":"DELETE","seed_sql":"INSERT INTO t (a) VALUES (1)","auto_seed":{"table":"t","columns":[]}}`

	var q Question
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("unmarshal Question: %v", err)
	}
	if q.SeedSQL != "" {
		t.Errorf("SeedSQL was client-supplied: %q", q.SeedSQL)
	}
	if len(q.AutoSeed) != 0 {
		t.Errorf("AutoSeed was client-supplied: %s", q.AutoSeed)
	}
	if q.DMLKind != "DELETE" {
		t.Errorf("dml_kind is not secret and must still bind, got %q", q.DMLKind)
	}

	var pq PracticeQuestion
	if err := json.Unmarshal([]byte(body), &pq); err != nil {
		t.Fatalf("unmarshal PracticeQuestion: %v", err)
	}
	if pq.SeedSQL != "" {
		t.Errorf("PracticeQuestion.SeedSQL was client-supplied: %q", pq.SeedSQL)
	}
	if len(pq.AutoSeed) != 0 {
		t.Errorf("PracticeQuestion.AutoSeed was client-supplied: %s", pq.AutoSeed)
	}
}

// TestLecturerViewsReExposeBothFields is ARCH-054: the authoring UI still needs
// them, so exactly one serialization path puts them back.
func TestLecturerViewsReExposeBothFields(t *testing.T) {
	autoSeed := json.RawMessage(`{"table":"employee","columns":[]}`)

	v := AsLecturerQuestionView(Question{
		Type: "DML", DMLKind: "DELETE", SeedSQL: seedFixture, AutoSeed: autoSeed,
	})
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal LecturerQuestionView: %v", err)
	}
	assertLecturerPayload(t, "LecturerQuestionView", string(b))

	pv := AsLecturerPracticeQuestionView(PracticeQuestion{
		Type: "DML", DMLKind: "DELETE", SeedSQL: seedFixture, AutoSeed: autoSeed,
	})
	pb, err := json.Marshal(pv)
	if err != nil {
		t.Fatalf("marshal LecturerPracticeQuestionView: %v", err)
	}
	assertLecturerPayload(t, "LecturerPracticeQuestionView", string(pb))
}

func assertLecturerPayload(t *testing.T, name, got string) {
	t.Helper()
	var round map[string]any
	if err := json.Unmarshal([]byte(got), &round); err != nil {
		t.Fatalf("%s: re-unmarshal: %v", name, err)
	}
	if round["seed_sql"] != seedFixture {
		t.Errorf("%s: seed_sql = %v, want %q", name, round["seed_sql"], seedFixture)
	}
	if round["auto_seed"] == nil {
		t.Errorf("%s: auto_seed missing: %s", name, got)
	}
	if round["dml_kind"] != "DELETE" {
		t.Errorf("%s: dml_kind = %v, want DELETE", name, round["dml_kind"])
	}
}

// TestLecturerViewSliceHelpers covers the list handlers' entry point.
func TestLecturerViewSliceHelpers(t *testing.T) {
	qs := AsLecturerQuestionViews([]Question{{SeedSQL: "a"}, {SeedSQL: "b"}})
	if len(qs) != 2 || qs[0].SeedSQL != "a" || qs[1].SeedSQL != "b" {
		t.Fatalf("AsLecturerQuestionViews lost data: %+v", qs)
	}
	pqs := AsLecturerPracticeQuestionViews([]PracticeQuestion{{SeedSQL: "a"}})
	if len(pqs) != 1 || pqs[0].SeedSQL != "a" {
		t.Fatalf("AsLecturerPracticeQuestionViews lost data: %+v", pqs)
	}
	// Empty input must produce an empty, non-nil slice so the handlers keep
	// emitting [] rather than null.
	if got := AsLecturerQuestionViews(nil); got == nil || len(got) != 0 {
		t.Errorf("AsLecturerQuestionViews(nil) = %v, want empty non-nil", got)
	}
	if got := AsLecturerPracticeQuestionViews(nil); got == nil || len(got) != 0 {
		t.Errorf("AsLecturerPracticeQuestionViews(nil) = %v, want empty non-nil", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/models/ -run 'NeverMarshals|ClientSupplied|LecturerView' -v`

Expected: FAIL to compile — `undefined: AsLecturerQuestionView`. (The two "never marshals" tests would also fail on their assertions, since the fields still carry `json:"auto_seed,omitempty"` today.)

- [ ] **Step 3a: Hide the fields**

In `backend/internal/models/submission.go`, in `type Question struct`, change the two tags (keep every existing doc comment, and add the ARCH-053 sentence):

```go
	// ... existing AutoSeed comment ...
	// ARCH-053: `json:"-"` in BOTH directions. It can never be marshalled into
	// a response (no student endpoint can leak it by omission) and can never be
	// bound from a request body (a client cannot supply it). Lecturer authoring
	// reads opt back in via models.LecturerQuestionView.
	AutoSeed json.RawMessage `json:"-"`
	// ... existing SeedSQL comment ...
	// ARCH-053: `json:"-"`, same reasoning as AutoSeed above — and stronger,
	// since seed_sql carries the exact values the correct answer must match.
	SeedSQL string `json:"-"`
```

Apply the identical tag change to `AutoSeed` and `SeedSQL` in
`type PracticeQuestion struct` in `backend/internal/models/practice.go`.

Leave `DMLKind` alone — it is not secret and must keep marshalling and binding.

- [ ] **Step 3b: Add the lecturer views**

Create `backend/internal/models/lecturer_view.go`:

```go
package models

import "encoding/json"

// LecturerQuestionView is the ONLY serialization of a Question that carries
// auto_seed and seed_sql. ARCH-053 hides both on the model itself, so a
// response can expose them only by deliberately going through this type.
//
// The embedded Question's own AutoSeed/SeedSQL fields are tagged `json:"-"`
// and so are skipped entirely by encoding/json; the tagged fields below are
// the only ones it sees. Nothing student-facing may use this type.
type LecturerQuestionView struct {
	Question
	AutoSeed json.RawMessage `json:"auto_seed,omitempty"`
	SeedSQL  string          `json:"seed_sql,omitempty"`
}

// AsLecturerQuestionView lifts q into its lecturer-facing serialization.
func AsLecturerQuestionView(q Question) LecturerQuestionView {
	return LecturerQuestionView{Question: q, AutoSeed: q.AutoSeed, SeedSQL: q.SeedSQL}
}

// AsLecturerQuestionViews maps AsLecturerQuestionView over qs. It always
// returns a non-nil slice so a handler emits [] rather than null for an exam
// with no questions.
func AsLecturerQuestionViews(qs []Question) []LecturerQuestionView {
	out := make([]LecturerQuestionView, 0, len(qs))
	for _, q := range qs {
		out = append(out, AsLecturerQuestionView(q))
	}
	return out
}

// LecturerPracticeQuestionView is the PracticeQuestion twin of
// LecturerQuestionView. See that type for the rationale.
type LecturerPracticeQuestionView struct {
	PracticeQuestion
	AutoSeed json.RawMessage `json:"auto_seed,omitempty"`
	SeedSQL  string          `json:"seed_sql,omitempty"`
}

// AsLecturerPracticeQuestionView lifts pq into its lecturer-facing form.
func AsLecturerPracticeQuestionView(pq PracticeQuestion) LecturerPracticeQuestionView {
	return LecturerPracticeQuestionView{PracticeQuestion: pq, AutoSeed: pq.AutoSeed, SeedSQL: pq.SeedSQL}
}

// AsLecturerPracticeQuestionViews maps AsLecturerPracticeQuestionView over pqs,
// always returning a non-nil slice.
func AsLecturerPracticeQuestionViews(pqs []PracticeQuestion) []LecturerPracticeQuestionView {
	out := make([]LecturerPracticeQuestionView, 0, len(pqs))
	for _, pq := range pqs {
		out = append(out, AsLecturerPracticeQuestionView(pq))
	}
	return out
}
```

- [ ] **Step 4: Run the model tests to verify they pass**

Run: `cd backend && gofmt -l internal/models/ && go test ./internal/models/ -v`

Expected: `gofmt -l` prints nothing; every model test PASSes.

- [ ] **Step 5: Commit the model half**

```bash
git add backend/internal/models/
git commit -m "feat(seed): seed_sql and auto_seed never marshal or bind (ARCH-053/054)"
```

- [ ] **Step 6: Wire the lecturer reads**

In `backend/internal/controllers/practice_controller.go`, `ListForLecturer` (`:404`), change the response line:

```go
	c.JSON(http.StatusOK, models.AsLecturerPracticeQuestionViews(questions))
```

In `backend/internal/controllers/lecturer_controller.go`, `GetExamQuestions` (`:467`), change the response line:

```go
	c.JSON(http.StatusOK, models.AsLecturerQuestionViews(questions))
```

Change nothing else in either handler.

- [ ] **Step 7: Fix the write paths (ARCH-055)**

In `backend/internal/controllers/practice_controller.go`, `Upsert` (`:320`), replace the bind:

```go
	var q models.PracticeQuestion
	if err := c.ShouldBindBodyWithJSON(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// ARCH-055: ARCH-053 gives DMLKind/SeedSQL `json:"-"`, so the bind above
	// cannot populate them. Recover exactly those two from the same body — and
	// only those two, which is why auto_seed still cannot be client-supplied.
	// ShouldBindBodyWithJSON caches the body, so binding it twice is safe;
	// ShouldBindJSON would consume the reader and make this second bind fail.
	var seed struct {
		DMLKind string `json:"dml_kind"`
		SeedSQL string `json:"seed_sql"`
	}
	if err := c.ShouldBindBodyWithJSON(&seed); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	q.DMLKind, q.SeedSQL = seed.DMLKind, seed.SeedSQL
```

Apply the identical change to `LecturerController.UpsertQuestion` (`lecturer_controller.go:377`), binding into `models.Question`.

`ShouldBindBodyWithJSON` exists in the vendored gin v1.12.0
(`vendor/github.com/gin-gonic/gin/context.go:946`) and needs no extra import —
do NOT use the older `ShouldBindBodyWith(obj, binding.JSON)` form, which would
pull in a `binding` import for no benefit.

**Then check every other `ShouldBindJSON` in these two handlers** — if a handler binds the body twice, BOTH calls must be `ShouldBindBodyWithJSON`, or the second reads a consumed reader. Search each handler body before moving on.

- [ ] **Step 8: Delete the dead scrubs (ARCH-056)**

Remove these three assignments and their ARCH-052 comments, now that the fields cannot marshal at all:

- `backend/internal/controllers/practice_controller.go:106` — `questions[i].SeedSQL = ""` and the `questions[i].AutoSeed = nil` above it
- `backend/internal/controllers/practice_controller.go:148` — `question.SeedSQL = ""` and `question.AutoSeed = nil`
- `backend/internal/controllers/quiz_controller.go:173` — `questions[i].SeedSQL = ""` and `questions[i].AutoSeed = nil`

Leave a one-line comment at each site recording why nothing is needed:

```go
	// ARCH-053: auto_seed and seed_sql are `json:"-"` on the model, so there is
	// nothing to scrub here — they cannot reach a student by construction.
```

Do **not** modify `backend/internal/controllers/autoseed_leak_http_test.go`. Those tests must pass unchanged with the assignments gone; that is the evidence the structural defence works.

- [ ] **Step 9: Run the controller suite**

Run:
```
cd backend && gofmt -l internal/ && go build ./... && \
TEST_DATABASE_URL='postgres://quiz:quizpw@127.0.0.1:55432/quiztest?sslmode=disable' \
go test ./internal/controllers/ -p 1 -count=1
```

Expected: `gofmt -l` prints only the two pre-existing `internal/services/` offenders; build clean; every controller test PASSes — **including the untouched leak tests**.

If a leak test fails here, do not edit the test. It means `json:"-"` is not actually suppressing the field; fix the model.

- [ ] **Step 10: Commit**

```bash
git add backend/internal/controllers/
git commit -m "feat(seed): lecturer views on reads, narrow rebind on writes, drop dead scrubs (ARCH-054/055/056)"
```

---

### Task 2: `dml_kind` must agree with `correct_sql`, when derivable

**Depends on:** Task 1 (it changes `validateSeedFields`, which Task 1's handlers call).

**Files:**
- Modify: `backend/internal/repository/postgres_schema.go` (export the helper)
- Modify: `backend/internal/controllers/practice_controller.go` (`validateSeedFields`, and its call site at `:331`)
- Modify: `backend/internal/controllers/lecturer_controller.go` (its call site at `:385`)
- Modify: `backend/internal/controllers/autoseed_leak_http_test.go` (extend `TestValidateSeedFields`)

**Interfaces:**
- Consumes: `leadingDMLKind(sql string) string` (unexported, `postgres_schema.go:1267`).
- Produces: `repository.LeadingDMLKind(sql string) string`; `validateSeedFields(qType, dmlKind, seedSQL, correctSQL string) error` — **note the new fourth parameter**.

- [ ] **Step 1: Write the failing test**

In `backend/internal/controllers/autoseed_leak_http_test.go`, replace the existing `TestValidateSeedFields` table with one carrying `correctSQL`, and add the agreement and regression cases:

```go
func TestValidateSeedFields(t *testing.T) {
	const del = "DELETE FROM employee WHERE ssn = '1'"
	const upd = "UPDATE employee SET fname = 'x' WHERE ssn = '1'"
	const ins = "INSERT INTO employee (ssn) VALUES ('1')"
	const cte = "WITH recent AS (SELECT ssn FROM employee) DELETE FROM employee WHERE ssn IN (SELECT ssn FROM recent)"
	const sel = "SELECT * FROM employee"
	const seed = "INSERT INTO employee (ssn) VALUES ('1')"

	cases := []struct {
		name       string
		qType      string
		kind       string
		seedSQL    string
		correctSQL string
		wantErr    bool
		errSubstr  string
	}{
		// Unchanged QUERY rules.
		{name: "query clean", qType: "QUERY", correctSQL: sel},
		{name: "query with a kind", qType: "QUERY", kind: "DELETE", correctSQL: sel, wantErr: true},
		{name: "query with a seed", qType: "QUERY", seedSQL: seed, correctSQL: sel, wantErr: true},

		// Agreement (ARCH-057 rule 1).
		{name: "delete matches", qType: "DML", kind: "DELETE", correctSQL: del},
		{name: "update matches", qType: "DML", kind: "UPDATE", correctSQL: upd},
		{name: "insert matches", qType: "DML", kind: "INSERT", correctSQL: ins},
		{name: "declared DELETE but SQL is UPDATE", qType: "DML", kind: "DELETE", correctSQL: upd, wantErr: true, errSubstr: "UPDATE"},
		{name: "declared UPDATE but SQL is DELETE", qType: "DML", kind: "UPDATE", correctSQL: del, wantErr: true, errSubstr: "DELETE"},
		{name: "declared INSERT but SQL is DELETE", qType: "DML", kind: "INSERT", correctSQL: del, wantErr: true, errSubstr: "DELETE"},
		{name: "declared DELETE but SQL is INSERT", qType: "DML", kind: "DELETE", correctSQL: ins, wantErr: true, errSubstr: "INSERT"},

		// Derivable-only requirement (ARCH-057 rule 2) — the ARCH-050 regression.
		{name: "CTE-led DML with no kind is allowed", qType: "DML", kind: "", correctSQL: cte},
		{name: "CTE-led DML with a hand-set kind is allowed", qType: "DML", kind: "DELETE", correctSQL: cte},
		{name: "plain DELETE with no kind is still rejected", qType: "DML", kind: "", correctSQL: del, wantErr: true},
		{name: "bogus kind", qType: "DML", kind: "MERGE", correctSQL: del, wantErr: true},

		// Seed rules, unchanged.
		{name: "delete with seed", qType: "DML", kind: "DELETE", seedSQL: seed, correctSQL: del},
		{name: "update with seed", qType: "DML", kind: "UPDATE", seedSQL: seed, correctSQL: upd},
		{name: "insert with seed rejected", qType: "DML", kind: "INSERT", seedSQL: seed, correctSQL: ins, wantErr: true},
		{name: "non-insert seed rejected", qType: "DML", kind: "DELETE", seedSQL: "DELETE FROM t", correctSQL: del, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSeedFields(tc.qType, tc.kind, tc.seedSQL, tc.correctSQL)
			if tc.wantErr && err == nil {
				t.Fatal("accepted, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected with %v, want accept", err)
			}
			if tc.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tc.errSubstr)) {
				t.Errorf("error %v does not name %q", err, tc.errSubstr)
			}
		})
	}
}
```

Ensure `strings` is imported in that test file.

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/controllers/ -run TestValidateSeedFields -v`

Expected: FAIL to compile — `too many arguments in call to validateSeedFields`.

- [ ] **Step 3a: Export the helper**

In `backend/internal/repository/postgres_schema.go`, next to `leadingDMLKind`:

```go
// LeadingDMLKind is the exported form of leadingDMLKind, for the controller
// layer's ARCH-057 agreement check. Returns "INSERT", "UPDATE", "DELETE", or
// "" when the statement leads with none of them — a CTE-led statement, for
// example, which the controller treats as unverifiable rather than wrong.
func LeadingDMLKind(sql string) string { return leadingDMLKind(sql) }
```

- [ ] **Step 3b: Add the two rules**

In `backend/internal/controllers/practice_controller.go`, change `validateSeedFields`'s signature to take `correctSQL string` as a fourth parameter, and replace its DML `switch` with:

```go
	// ARCH-057: correct_sql is the authority on what kind of statement this is.
	// The declared kind must agree with it — but only when the system can
	// actually derive it. A CTE-led statement (WITH ... DELETE ...) returns ""
	// here: unverifiable, which is not the same as wrong.
	actual := repository.LeadingDMLKind(correctSQL)

	switch dmlKind {
	case "INSERT", "UPDATE", "DELETE":
		if actual != "" && actual != dmlKind {
			return fmt.Errorf("you selected %s, but the correct SQL is a %s statement. Pick the kind that matches your SQL", dmlKind, actual)
		}
	case "":
		// ARCH-057 rule 2: demand a kind only when one is derivable. Requiring
		// it unconditionally made CTE-led DML questions unsavable (the ARCH-050
		// regression) — the lecturer was blocked on a classification the system
		// could not make either.
		if actual != "" {
			return errors.New("dml_kind is required for a DML question and must be INSERT, UPDATE, or DELETE")
		}
	default:
		return fmt.Errorf("dml_kind must be INSERT, UPDATE, or DELETE, got %q", dmlKind)
	}
```

Keep the rest of the function — the QUERY branch and the seed rules below — exactly as it is. Note the seed rules run after this block and already handle `dmlKind == "INSERT"`; an empty `dmlKind` with a non-empty `seedSQL` falls through to `repository.ValidateSeedSQL`, which is correct.

- [ ] **Step 3c: Update both call sites**

`practice_controller.go:331`:
```go
	if err := validateSeedFields(q.Type, q.DMLKind, q.SeedSQL, q.CorrectSQL); err != nil {
```

`lecturer_controller.go:385`:
```go
	if err := validateSeedFields(q.Type, q.DMLKind, q.SeedSQL, q.CorrectSQL); err != nil {
```

- [ ] **Step 4: Run to verify it passes**

Run:
```
cd backend && gofmt -l internal/ && go build ./... && \
TEST_DATABASE_URL='postgres://quiz:quizpw@127.0.0.1:55432/quiztest?sslmode=disable' \
go test ./internal/controllers/ -p 1 -count=1 -v -run 'TestValidateSeedFields'
```

Expected: all subtests PASS, including `CTE-led DML with no kind is allowed`.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/repository/postgres_schema.go backend/internal/controllers/
git commit -m "feat(seed): dml_kind must match correct_sql when derivable (ARCH-057)"
```

---

### Task 3: Round-trip and full verification

**Depends on:** Tasks 1 and 2.

**Files:**
- Modify: `backend/internal/controllers/autoseed_leak_http_test.go` (add the round-trip test)

- [ ] **Step 1: Write the round-trip test**

This is the guard against ARCH-055's double bind silently dropping a field. Read the existing real-controller test setup in that file (the ones that build a router and hit real handlers) and reuse it. Assert:

```go
// 1. POST a practice question with a non-empty seed_sql and dml_kind through
//    the real Upsert handler. Read the row back from the database directly and
//    assert BOTH survived — this fails if the second bind was dropped.
//
// 2. Read the same question back through the real ListForLecturer handler and
//    assert the JSON body carries seed_sql and auto_seed — proving the lecturer
//    view is wired and the lecturer UI did not silently lose its fields.
//
// 3. POST a body that ALSO contains an "auto_seed" key. Assert the saved row's
//    auto_seed is whatever the backend derived (or NULL), never the client's
//    value — the inbound half of ARCH-053.
//
// 4. POST a question whose dml_kind disagrees with correct_sql. Assert HTTP 400
//    and that the message names both kinds.
```

- [ ] **Step 2: Run it**

Run:
```
cd backend && TEST_DATABASE_URL='postgres://quiz:quizpw@127.0.0.1:55432/quiztest?sslmode=disable' \
go test ./internal/controllers/ -p 1 -count=1 -run RoundTrip -v
```

Expected: PASS.

- [ ] **Step 3: Full backend suite on a fresh database**

Some `internal/services` tests are DB-state sensitive, so use a database no other run has touched:

```bash
docker exec sqlquiz-test-pg psql -U quiz -d postgres -c "DROP DATABASE IF EXISTS quizverify;" -c "CREATE DATABASE quizverify;"
cd backend && TEST_DATABASE_URL='postgres://quiz:quizpw@127.0.0.1:55432/quizverify?sslmode=disable' go test ./... -p 1 -count=1
```

Expected: every package `ok`.

- [ ] **Step 4: Confirm the frontend was not disturbed**

The lecturer wire shape is unchanged, so no frontend file should have been edited and the suite must still pass untouched:

```bash
git diff --name-only HEAD~4 -- frontend/    # expect: no output
cd frontend && npx vue-tsc --noEmit && npm test
```

Expected: no frontend files changed; 44 files / 464 tests pass.

- [ ] **Step 5: Confirm the structural claim by grep**

```bash
grep -n 'SeedSQL  *string  *`json:"-"`' backend/internal/models/*.go        # expect 2 hits
grep -rn 'SeedSQL = ""' backend/internal/controllers/                        # expect 0 hits
grep -c 'LecturerQuestionView\|LecturerPracticeQuestionView' backend/internal/controllers/*.go
```

Expected: both fields hidden on both models; zero manual scrubs remaining; the two lecturer handlers reference the view types.

- [ ] **Step 6: Commit and report**

```bash
git add backend/internal/controllers/autoseed_leak_http_test.go
git commit -m "test(seed): round-trip coverage for the structural seed invariants"
```

Report plainly which suites ran and their results. If any DB-backed test was skipped because no database was reachable, say so — do not report it as passing.
