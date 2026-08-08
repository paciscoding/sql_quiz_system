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
