package services

import (
	"encoding/json"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// These tests pin ARCH-043: COLUMN NAMES compare case-insensitively during
// grading (Postgres folds unquoted identifiers, so `AS "Fname"` vs `fname` is a
// quoting artefact, not a wrong answer), while CELL VALUES stay case-sensitive
// and a fold-collision inside one row is a deterministic non-match.
// All of them are pure — no database.

func TestCompareRows_StrictAttributes_ColumnNameCasing(t *testing.T) {
	svc := NewValidationService(nil)
	strict := models.StrictMode{Attributes: true}

	tests := []struct {
		name    string
		teacher map[string]interface{}
		student map[string]interface{}
		want    bool
	}{
		{
			name:    "exact case still matches",
			teacher: row("fname", "Smith", "dno", 5),
			student: row("fname", "Smith", "dno", 5),
			want:    true,
		},
		{
			name:    "case-differing alias now matches",
			teacher: row("fname", "Smith", "dno", 5),
			student: row("Fname", "Smith", "DNO", 5),
			want:    true,
		},
		{
			name:    "teacher captured with upper case, student lower",
			teacher: row("FNAME", "Smith"),
			student: row("fname", "Smith"),
			want:    true,
		},
		{
			name:    "wrong VALUE case still fails",
			teacher: row("fname", "Smith"),
			student: row("Fname", "smith"),
			want:    false,
		},
		{
			name:    "wrong column name still fails",
			teacher: row("fname", "Smith"),
			student: row("lname", "Smith"),
			want:    false,
		},
		{
			name:    "differing arity still fails",
			teacher: row("fname", "Smith"),
			student: row("Fname", "Smith", "dno", 5),
			want:    false,
		},
		{
			name: "student fold-collision cannot win points it did not deserve pre-ARCH-043",
			// `SELECT fname AS "x", lname AS "X"` — two real Postgres columns
			// that fold to the same name. Picking one would make the verdict
			// depend on Go's randomised map order, so we fall back to exact
			// keys (ARCH-044): the teacher's key "y" is simply absent.
			teacher: row("x", "Smith", "y", "John"),
			student: row("x", "Smith", "X", "Smith"),
			want:    false,
		},
		{
			name:    "teacher fold-collision vs a differently-keyed student is still a non-match",
			teacher: row("x", "Smith", "X", "Smith"),
			student: row("x", "Smith", "y", "Smith"),
			want:    false,
		},
		{
			// ARCH-044 regression: the lecturer's own model answer projects
			// case-distinct quoted names (`SELECT e.dno AS "Dno", d.dno AS
			// "DNO"`), so the TEACHER row collides under folding. Failing
			// closed here zeroed every student on the question, including one
			// submitting SQL byte-identical to the model answer.
			name:    "teacher fold-collision vs an identical student row MATCHES",
			teacher: row("Dno", 5, "DNO", 5),
			student: row("Dno", 5, "DNO", 5),
			want:    true,
		},
		{
			name:    "teacher fold-collision, student differs by exact key spelling",
			teacher: row("Dno", 5, "DNO", 5),
			student: row("dno", 5, "dno_other", 5),
			want:    false,
		},
		{
			name:    "teacher fold-collision, identical keys but wrong VALUE still fails",
			teacher: row("Dno", 5, "DNO", 5),
			student: row("Dno", 5, "DNO", 4),
			want:    false,
		},
		{
			name:    "teacher fold-collision, cell values stay case-SENSITIVE",
			teacher: row("Lname", "Smith", "LNAME", "Smith"),
			student: row("Lname", "Smith", "LNAME", "smith"),
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.compareRows(tc.teacher, tc.student, strict); got != tc.want {
				t.Fatalf("compareRows(%v, %v) = %v, want %v", tc.teacher, tc.student, got, tc.want)
			}
		})
	}
}

func TestEqualStringSlicesFold(t *testing.T) {
	tests := []struct {
		name    string
		teacher []string
		student []string
		want    bool
	}{
		{"identical", []string{"fname", "dno"}, []string{"fname", "dno"}, true},
		{"case-differing but same order", []string{"fname", "dno"}, []string{"Fname", "DNO"}, true},
		{"reordered is still rejected", []string{"fname", "dno"}, []string{"DNO", "Fname"}, false},
		{"different name rejected", []string{"fname"}, []string{"lname"}, false},
		{"different length rejected", []string{"fname"}, []string{"fname", "dno"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalStringSlicesFold(tc.teacher, tc.student); got != tc.want {
				t.Fatalf("equalStringSlicesFold(%v, %v) = %v, want %v", tc.teacher, tc.student, got, tc.want)
			}
		})
	}
}

// TestDecideStatus_ColumnOrderGate_Casing drives the gate through decideStatus,
// the real caller, so the wiring is covered and not just the helper.
func TestDecideStatus_ColumnOrderGate_Casing(t *testing.T) {
	svc := NewValidationService(nil)
	teacherData := []map[string]interface{}{row("fname", "Smith", "dno", 5)}
	teacherJSON, _ := json.Marshal(teacherData)
	teacherCols, _ := json.Marshal([]string{"fname", "dno"})

	q := models.Question{
		Type:            "QUERY",
		Points:          10,
		ExpectedResult:  teacherJSON,
		ExpectedColumns: teacherCols,
		StrictMode:      models.StrictMode{ColumnOrder: true, Attributes: true, Ordering: true},
	}

	tests := []struct {
		name          string
		studentCols   []string
		studentData   []map[string]interface{}
		wantIsCorrect bool
	}{
		{
			name:          "case-differing but order-identical columns are accepted",
			studentCols:   []string{"Fname", "DNO"},
			studentData:   []map[string]interface{}{row("Fname", "Smith", "DNO", 5)},
			wantIsCorrect: true,
		},
		{
			name:          "reordered columns are still rejected",
			studentCols:   []string{"DNO", "Fname"},
			studentData:   []map[string]interface{}{row("DNO", 5, "Fname", "Smith")},
			wantIsCorrect: false,
		},
		{
			name:          "value casing still decides the verdict",
			studentCols:   []string{"Fname", "DNO"},
			studentData:   []map[string]interface{}{row("Fname", "smith", "DNO", 5)},
			wantIsCorrect: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.decideStatus(models.ValidationResult{}, tc.studentData, tc.studentCols, q)
			if got.IsCorrect != tc.wantIsCorrect {
				t.Fatalf("decideStatus IsCorrect = %v (status %q, feedback %q), want %v",
					got.IsCorrect, got.Status, got.Feedback, tc.wantIsCorrect)
			}
		})
	}
}

func TestDiffRowMultiset_ColumnNameCasing(t *testing.T) {
	tests := []struct {
		name        string
		teacher     []map[string]interface{}
		student     []map[string]interface{}
		wantMissing int
		wantExtra   int
	}{
		{
			name:    "case-differing keys match as a multiset",
			teacher: []map[string]interface{}{row("pno", 1, "hours", 40), row("pno", 2, "hours", 20)},
			student: []map[string]interface{}{row("PNo", 2, "Hours", 20), row("PNO", 1, "HOURS", 40)},
		},
		{
			name:        "wrong VALUE case is still a mismatch",
			teacher:     []map[string]interface{}{row("ename", "Smith")},
			student:     []map[string]interface{}{row("ENAME", "smith")},
			wantMissing: 1,
			wantExtra:   1,
		},
		{
			name:        "wrong column name is still a mismatch",
			teacher:     []map[string]interface{}{row("ename", "Smith")},
			student:     []map[string]interface{}{row("lname", "Smith")},
			wantMissing: 1,
			wantExtra:   1,
		},
		{
			name:        "duplicate rows still behave as a multiset under folding",
			teacher:     []map[string]interface{}{row("pno", 1), row("pno", 1)},
			student:     []map[string]interface{}{row("PNO", 1)},
			wantMissing: 1,
		},
		{
			name:        "student fold-collision never grades correct",
			teacher:     []map[string]interface{}{row("x", 1, "y", 2)},
			student:     []map[string]interface{}{row("x", 1, "X", 2)},
			wantMissing: 1,
			wantExtra:   1,
		},
		{
			// ARCH-044 regression on the DML path: target table created with
			// quoted "Total" and "total" columns, so the teacher's own captured
			// post-image collides under folding. Failing closed made
			// diffRowMultiset report every teacher row missing regardless of
			// what the student wrote.
			name:    "teacher fold-collision vs an identical student snapshot is clean",
			teacher: []map[string]interface{}{row("Total", 10, "total", 20)},
			student: []map[string]interface{}{row("Total", 10, "total", 20)},
		},
		{
			name:        "teacher fold-collision, student keys differ exactly",
			teacher:     []map[string]interface{}{row("Total", 10, "total", 20)},
			student:     []map[string]interface{}{row("total", 20, "TOTAL", 10)},
			wantMissing: 1,
			wantExtra:   1,
		},
		{
			name:        "teacher fold-collision, cell values stay case-SENSITIVE",
			teacher:     []map[string]interface{}{row("Name", "Smith", "NAME", "Smith")},
			student:     []map[string]interface{}{row("Name", "Smith", "NAME", "smith")},
			wantMissing: 1,
			wantExtra:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			missing, extra := diffRowMultiset(tc.teacher, tc.student)
			if len(missing) != tc.wantMissing || len(extra) != tc.wantExtra {
				t.Fatalf("diffRowMultiset = missing %d / extra %d, want %d / %d (missing=%v extra=%v)",
					len(missing), len(extra), tc.wantMissing, tc.wantExtra, missing, extra)
			}
		})
	}
}

// TestFormatDMLRow_KeepsTeacherCasing pins requirement 5: matching folds, but
// the printed feedback keeps the teacher's original column spelling.
func TestFormatDMLRow_KeepsTeacherCasing(t *testing.T) {
	studentRow := row("PNO", 1, "Hours", 40)
	got := formatDMLRow(studentRow, []string{"pno", "hours"})
	if want := "{pno=1, hours=40}"; got != want {
		t.Fatalf("formatDMLRow = %q, want %q", got, want)
	}
}

func TestFormatDMLRow_FoldCollisionFallsBackToOwnKeys(t *testing.T) {
	// No usable folded index; every cell must still be rendered rather than
	// silently dropped from the feedback.
	got := formatDMLRow(row("x", 1, "X", 2), []string{"x"})
	if want := "{X=2, x=1}"; got != want {
		t.Fatalf("formatDMLRow = %q, want %q", got, want)
	}
}
