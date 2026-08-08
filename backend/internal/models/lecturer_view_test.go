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
