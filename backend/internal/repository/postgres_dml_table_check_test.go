package repository

// Unit tests for ARCH-017/TODO-018: DMLCorrectSQLTargetTableWarning
// (postgres_dml_table_check.go). Pure regex/string logic, no database
// required — do NOT gate these behind TEST_DATABASE_URL, so `go test ./...`
// exercises them on every run, with or without a real Postgres.
//
// Run: go test ./internal/repository/... -run TestDMLCorrectSQLTargetTableWarning

import (
	"strings"
	"testing"
)

func TestDMLCorrectSQLTargetTableWarning(t *testing.T) {
	cases := []struct {
		name        string
		qType       string
		correctSQL  string
		targetTable string
		wantWarning bool // true => non-empty warning expected
	}{
		{
			name:        "mentioned target table returns no warning",
			qType:       "DML",
			correctSQL:  "UPDATE employees SET salary = salary * 1.1",
			targetTable: "employees",
			wantWarning: false,
		},
		{
			name:        "unmentioned target table returns a warning",
			qType:       "DML",
			correctSQL:  "UPDATE departments SET budget = budget + 100",
			targetTable: "employees",
			wantWarning: true,
		},
		{
			name:        "case-insensitive match: EMPLOYEES satisfies employees",
			qType:       "DML",
			correctSQL:  "UPDATE EMPLOYEES SET salary = 1",
			targetTable: "employees",
			wantWarning: false,
		},
		{
			name:        "case-insensitive match: lowercase mention satisfies uppercase target",
			qType:       "DML",
			correctSQL:  "update employees set salary = 1",
			targetTable: "EMPLOYEES",
			wantWarning: false,
		},
		{
			name:        "word boundary: mentioning 'employee' does NOT satisfy target table 'employees'",
			qType:       "DML",
			correctSQL:  "UPDATE employee SET salary = 1", // singular, target is plural
			targetTable: "employees",
			wantWarning: true,
		},
		{
			name:        "word boundary: mentioning 'employees' does NOT satisfy target table 'employee'",
			qType:       "DML",
			correctSQL:  "UPDATE employees SET salary = 1", // plural, target is singular
			targetTable: "employee",
			wantWarning: true,
		},
		{
			name:        "target table mentioned only inside a single-quoted string literal is not a reference",
			qType:       "DML",
			correctSQL:  "UPDATE departments SET note = 'employees table used to live here'",
			targetTable: "employees",
			wantWarning: true,
		},
		{
			name:        "target table mentioned only inside a line comment is not a reference",
			qType:       "DML",
			correctSQL:  "-- employees\nUPDATE departments SET budget = 1",
			targetTable: "employees",
			wantWarning: true,
		},
		{
			name:        "target table mentioned only inside a block comment is not a reference",
			qType:       "DML",
			correctSQL:  "/* employees */ UPDATE departments SET budget = 1",
			targetTable: "employees",
			wantWarning: true,
		},
		{
			name:        "target table mentioned in real SQL alongside a decoy literal still counts as mentioned",
			qType:       "DML",
			correctSQL:  "UPDATE employees SET note = 'this is not the employees table, right?' WHERE id = 1",
			targetTable: "employees",
			wantWarning: false,
		},
		{
			name:        "QUERY type always returns no warning even when the table is never mentioned",
			qType:       "QUERY",
			correctSQL:  "SELECT * FROM departments",
			targetTable: "employees",
			wantWarning: false,
		},
		{
			name:        "QUERY type returns no warning even with empty correctSQL",
			qType:       "QUERY",
			correctSQL:  "",
			targetTable: "employees",
			wantWarning: false,
		},
		{
			name:        "empty target table (DML) always returns no warning",
			qType:       "DML",
			correctSQL:  "UPDATE departments SET budget = 1",
			targetTable: "",
			wantWarning: false,
		},
		{
			name:        "empty target table (QUERY) always returns no warning",
			qType:       "QUERY",
			correctSQL:  "SELECT * FROM departments",
			targetTable: "",
			wantWarning: false,
		},
		{
			name:        "whitespace-only target table is treated as empty -> no warning",
			qType:       "DML",
			correctSQL:  "UPDATE departments SET budget = 1",
			targetTable: "   ",
			wantWarning: false,
		},
		{
			name:        "regex metacharacter in target table ('.') matched literally when present verbatim",
			qType:       "DML",
			correctSQL:  "UPDATE a.b SET x = 1",
			targetTable: "a.b",
			wantWarning: false,
		},
		{
			name: "regex metacharacter in target table ('.') is NOT treated as a wildcard " +
				"(regexp.QuoteMeta proof): a stand-in character in the dot's position must not count as a match",
			qType:       "DML",
			correctSQL:  "UPDATE aXb SET x = 1", // if '.' were an unescaped wildcard, this would wrongly match
			targetTable: "a.b",
			wantWarning: true,
		},
		{
			name:        "regex metacharacter in target table ('+') matched literally when present verbatim",
			qType:       "DML",
			correctSQL:  "UPDATE t+1 SET x = 1",
			targetTable: "t+1",
			wantWarning: false,
		},
		{
			name: "regex metacharacter in target table ('+') is NOT treated as a quantifier " +
				"(regexp.QuoteMeta proof): 'ttt1' would satisfy an unescaped 't+1' pattern " +
				"(one-or-more 't' then '1') but must NOT satisfy the literal table name 't+1'",
			qType:       "DML",
			correctSQL:  "UPDATE ttt1 SET x = 1",
			targetTable: "t+1",
			wantWarning: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := DMLCorrectSQLTargetTableWarning(tc.qType, tc.correctSQL, tc.targetTable)
			if tc.wantWarning {
				if got == "" {
					t.Errorf("expected a non-empty warning, got empty string")
				} else if !strings.Contains(got, tc.targetTable) && strings.TrimSpace(tc.targetTable) != "" {
					t.Errorf("expected warning to mention target table %q, got %q", tc.targetTable, got)
				}
			} else {
				if got != "" {
					t.Errorf("expected no warning, got %q", got)
				}
			}
		})
	}
}
