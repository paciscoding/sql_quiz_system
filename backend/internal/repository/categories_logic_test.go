package repository

import (
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestPracticeCategorySchemaName pins the deterministic schema-name convention.
func TestPracticeCategorySchemaName(t *testing.T) {
	if got := PracticeCategorySchemaName(3); got != "practice_cat_3" {
		t.Errorf("PracticeCategorySchemaName(3) = %q; want %q", got, "practice_cat_3")
	}
}

// TestQuerySchemaFor guards the ARCH-019 invariant: exam-scoped SELECT questions
// always read public (byte-for-byte historic behavior), while practice questions
// (ExamID == nil) read their category schema, with public as the empty fallback.
func TestQuerySchemaFor(t *testing.T) {
	examID := 7
	cases := []struct {
		name    string
		examID  *int
		sandbox string
		want    string
	}{
		{"exam question ignores sandbox, reads public", &examID, "sandbox_exam_7", "public"},
		{"exam question with empty sandbox reads public", &examID, "", "public"},
		{"practice question reads its category schema", nil, "practice_cat_4", "practice_cat_4"},
		{"practice question with empty schema falls back to public", nil, "", "public"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := models.Question{Type: "QUERY", ExamID: tc.examID}
			if got := querySchemaFor(q, tc.sandbox); got != tc.want {
				t.Errorf("querySchemaFor(examID=%v, sandbox=%q) = %q; want %q", tc.examID, tc.sandbox, got, tc.want)
			}
		})
	}
}

// TestValidateNoSchemaQualifiedNames covers the OQ-2 import-safety gate: scripts
// referencing system/shared schemas are rejected, unqualified DDL passes, and
// qualifiers buried in string literals or comments are not false positives.
func TestValidateNoSchemaQualifiedNames(t *testing.T) {
	rejected := []string{
		"CREATE TABLE public.foo (id int);",
		"INSERT INTO app.bar VALUES (1);",
		"select * from PUBLIC.baz;",
		"CREATE TABLE practice_cat_9.x (id int);",
		"SELECT * FROM sandbox_exam_2.t;",
		"SELECT * FROM information_schema.tables;",
	}
	for _, sql := range rejected {
		if err := validateNoSchemaQualifiedNames(sql); err == nil {
			t.Errorf("expected rejection for %q, got nil", sql)
		}
	}

	accepted := []string{
		"CREATE TABLE employee (id int, name text);",
		"INSERT INTO employee (id, name) VALUES (1, 'a.b');",          // qualifier-looking value in a literal
		"SELECT e.name FROM employee e JOIN dept d ON e.dept = d.id;", // alias.column, not schema.table
		"-- public.foo in a comment\nCREATE TABLE t (id int);",
		"INSERT INTO t VALUES ('see app.bar in text');",
	}
	for _, sql := range accepted {
		if err := validateNoSchemaQualifiedNames(sql); err != nil {
			t.Errorf("expected acceptance for %q, got %v", sql, err)
		}
	}
}
