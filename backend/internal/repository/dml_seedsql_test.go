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
		name       string
		in         string
		wantSubstr string
	}{
		{"delete", "DELETE FROM employee WHERE ssn = '1'", "DELETE"},
		{"update", "UPDATE employee SET fname = 'x'", "UPDATE"},
		{"select", "SELECT * FROM employee", "SELECT"},
		{"ddl", "CREATE TEMP TABLE employee (ssn TEXT)", "CREATE"},
		{"truncate", "TRUNCATE employee", "TRUNCATE"},
		{"insert then delete", "INSERT INTO employee (ssn) VALUES ('1'); DELETE FROM employee", "DELETE"},
		{"unterminated quote", "INSERT INTO employee (fname) VALUES ('oops", "unterminated"},
		// Pins the ordering of the two checks: the schema-qualification scan
		// must run AFTER checkSeedQuotesBalanced, because literal-stripping is
		// unreliable across unbalanced quotes. If the qualification check ran
		// first here, it would see "public.foo" (inside the unterminated
		// literal) as a live reference and refuse with the schema message
		// instead of "unterminated" — which is the wrong reason to give a
		// lecturer whose real bug is a missing closing quote.
		{"unterminated quote holding a schema name", "INSERT INTO employee (fname) VALUES ('oops public.foo", "unterminated"},
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
		// A `--` inside a legitimate string literal is data, not a comment
		// marker — but stripSQLNoise's comment regexes are not quote-aware, so
		// if the qualification check re-strips comments from text that
		// stripSQLComments already cleaned, the `--` swallows the rest of the
		// seed (including the schema-qualified statement after it) and the
		// deadlock vector goes undetected.
		{
			"line comment marker inside a string literal hides a later cross-schema target",
			"INSERT INTO t (a) VALUES ('a--b'); INSERT INTO public.employee (ssn) VALUES ('1')",
			"public",
		},
		{
			"block comment marker inside a string literal hides a later cross-schema target",
			"INSERT INTO t (a) VALUES ('a/*b'); INSERT INTO public.employee (ssn) VALUES ('x*/y')",
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
