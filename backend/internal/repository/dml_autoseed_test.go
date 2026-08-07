package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// employeeCols is a representative column set: ssn and dno are NOT NULL with
// no default (so they must be pinned or refused), name/salary are nullable.
func employeeCols() []TableColumn {
	return []TableColumn{
		{Name: "ssn", IsNullable: false, HasDefault: false},
		{Name: "name", IsNullable: true, HasDefault: false},
		{Name: "salary", IsNullable: true, HasDefault: false},
		{Name: "dno", IsNullable: false, HasDefault: false},
	}
}

func TestAutoSeedDeriveDeleteSingleEquality(t *testing.T) {
	cols := []TableColumn{
		{Name: "ssn", IsNullable: false, HasDefault: false},
		{Name: "name", IsNullable: true, HasDefault: false},
		{Name: "dno", IsNullable: true, HasDefault: false},
	}
	plan, err := deriveAutoSeed(`DELETE FROM employee WHERE ssn = '999999999'`, "employee", cols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a plan, got nil")
	}
	if plan.Table != "employee" {
		t.Errorf("Table = %q, want %q", plan.Table, "employee")
	}
	if len(plan.Columns) != 1 {
		t.Fatalf("Columns = %v, want exactly 1", plan.Columns)
	}
	if plan.Columns[0].Name != "ssn" || plan.Columns[0].Value != "999999999" {
		t.Errorf("Columns[0] = %+v, want {ssn 999999999}", plan.Columns[0])
	}
}

func TestAutoSeedDeriveMultiPredicateAnd(t *testing.T) {
	cols := []TableColumn{
		{Name: "ssn", IsNullable: false, HasDefault: false},
		{Name: "dno", IsNullable: false, HasDefault: false},
		{Name: "name", IsNullable: true, HasDefault: false},
	}
	plan, err := deriveAutoSeed(`DELETE FROM employee WHERE ssn='1' AND dno=5`, "employee", cols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Columns) != 2 {
		t.Fatalf("Columns = %v, want exactly 2", plan.Columns)
	}
	if plan.Columns[0].Name != "ssn" || plan.Columns[0].Value != "1" {
		t.Errorf("Columns[0] = %+v, want {ssn 1}", plan.Columns[0])
	}
	if plan.Columns[1].Name != "dno" || plan.Columns[1].Value != "5" {
		t.Errorf("Columns[1] = %+v, want {dno 5}", plan.Columns[1])
	}
	for _, c := range plan.Columns {
		if c.Value != "1" && c.Value != "5" {
			t.Errorf("unexpected value %q", c.Value)
		}
	}
}

func TestAutoSeedDeriveUpdateOnlyUsesWhere(t *testing.T) {
	cols := []TableColumn{
		{Name: "ssn", IsNullable: false, HasDefault: false},
		{Name: "salary", IsNullable: true, HasDefault: false},
	}
	plan, err := deriveAutoSeed(`UPDATE employee SET salary=1 WHERE ssn='9'`, "employee", cols)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Columns) != 1 {
		t.Fatalf("Columns = %v, want exactly 1 (SET must not be treated as a predicate)", plan.Columns)
	}
	if plan.Columns[0].Name != "ssn" || plan.Columns[0].Value != "9" {
		t.Errorf("Columns[0] = %+v, want {ssn 9}", plan.Columns[0])
	}
}

func TestAutoSeedRefusalClasses(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		cols    []TableColumn
		wantSub string // substring the reason clause must contain
	}{
		{
			name:    "operator other than =",
			sql:     `DELETE FROM employee WHERE salary > '1000'`,
			cols:    employeeCols(),
			wantSub: `'>' on "salary"`,
		},
		{
			name:    "OR",
			sql:     `DELETE FROM employee WHERE ssn = '1' OR ssn = '2'`,
			cols:    employeeCols(),
			wantSub: "OR",
		},
		{
			name:    "IN",
			sql:     `DELETE FROM employee WHERE ssn IN ('1', '2')`,
			cols:    employeeCols(),
			wantSub: "IN (...)",
		},
		{
			name:    "subquery",
			sql:     `DELETE FROM employee WHERE ssn = (SELECT ssn FROM employee LIMIT 1)`,
			cols:    employeeCols(),
			wantSub: "subquery",
		},
		{
			name:    "BETWEEN",
			sql:     `DELETE FROM employee WHERE salary BETWEEN 1000 AND 2000`,
			cols:    employeeCols(),
			wantSub: "BETWEEN",
		},
		{
			name:    "IS NULL",
			sql:     `DELETE FROM employee WHERE salary IS NULL`,
			cols:    employeeCols(),
			wantSub: "IS NULL",
		},
		{
			name:    "two statements separated by semicolon",
			sql:     `DELETE FROM employee WHERE ssn='1'; DELETE FROM employee WHERE ssn='2'`,
			cols:    employeeCols(),
			wantSub: "more than one statement",
		},
		{
			name:    "INSERT statement",
			sql:     `INSERT INTO employee (ssn) VALUES ('1')`,
			cols:    employeeCols(),
			wantSub: "INSERT",
		},
		{
			name:    "unpinned NOT NULL no-default column",
			sql:     `DELETE FROM employee WHERE ssn = '1'`,
			cols:    employeeCols(), // dno is NOT NULL / no default and never pinned
			wantSub: `"dno"`,
		},
		{
			name:    "pinned column absent from cols",
			sql:     `DELETE FROM employee WHERE bogus = '1'`,
			cols:    employeeCols(),
			wantSub: `"bogus"`,
		},
		{
			name:    "UPDATE with FROM join",
			sql:     `UPDATE employee SET salary = 1 FROM dept WHERE dno = 5`,
			cols:    employeeCols(),
			wantSub: "FROM clause",
		},
		{
			name:    "UPDATE with parenthesised subquery in SET",
			sql:     `UPDATE employee SET salary = (SELECT max(x) FROM dept) WHERE ssn = '1'`,
			cols:    employeeCols(),
			wantSub: "parentheses",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := deriveAutoSeed(tc.sql, "employee", tc.cols)
			if err == nil {
				t.Fatalf("expected refusal, got plan %+v", plan)
			}
			if !errors.Is(err, ErrAutoSeedUnsupported) {
				t.Errorf("error %v does not wrap ErrAutoSeedUnsupported", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
			// Error text contract: lowercase reason clause, no trailing period.
			if strings.HasSuffix(err.Error(), ".") {
				t.Errorf("error %q ends with a trailing period", err.Error())
			}
		})
	}
}

func TestAutoSeedValueEncodingIsAlwaysString(t *testing.T) {
	plan := &AutoSeedPlan{
		Table: "employee",
		Columns: []AutoSeedColumn{
			{Name: "dno", Value: "5"},
		},
	}
	raw, err := marshalAutoSeed(plan)
	if err != nil {
		t.Fatalf("marshalAutoSeed error: %v", err)
	}
	if !strings.Contains(string(raw), `"value":"5"`) {
		t.Errorf("marshalAutoSeed output %s does not contain \"value\":\"5\"", raw)
	}
	if strings.Contains(string(raw), `"value":5`) {
		t.Errorf("marshalAutoSeed output %s wrongly contains a JSON number for value", raw)
	}
}

func TestAutoSeedMarshalNilPlan(t *testing.T) {
	raw, err := marshalAutoSeed(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw != nil {
		t.Errorf("marshalAutoSeed(nil) = %v, want nil", raw)
	}
}

func TestAutoSeedUnmarshalRoundTrip(t *testing.T) {
	plan := &AutoSeedPlan{
		Table:   "employee",
		Columns: []AutoSeedColumn{{Name: "ssn", Value: "999999999"}},
	}
	raw, err := marshalAutoSeed(plan)
	if err != nil {
		t.Fatalf("marshalAutoSeed error: %v", err)
	}
	got, err := unmarshalAutoSeed(raw)
	if err != nil {
		t.Fatalf("unmarshalAutoSeed error: %v", err)
	}
	if got == nil || got.Table != "employee" || len(got.Columns) != 1 || got.Columns[0].Value != "999999999" {
		t.Errorf("round trip mismatch: got %+v", got)
	}
}

func TestAutoSeedUnmarshalEmptyOrNull(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("null"), []byte("  null  ")} {
		got, err := unmarshalAutoSeed(raw)
		if err != nil {
			t.Fatalf("unmarshalAutoSeed(%q) error: %v", raw, err)
		}
		if got != nil {
			t.Errorf("unmarshalAutoSeed(%q) = %+v, want nil", raw, got)
		}
	}
}

func TestAutoSeedApplyBindsValuesAsParameters(t *testing.T) {
	plan := &AutoSeedPlan{
		Table: "employee",
		Columns: []AutoSeedColumn{
			{Name: "ssn", Value: "999999999"},
			{Name: "name", Value: "'; DROP TABLE employee; --"},
			{Name: "dno", Value: "5"},
		},
	}
	query, values := buildAutoSeedInsert(plan)

	// ARCH-038 criterion #8: the generated SQL text must reference $1..$N
	// placeholders and must not contain any of the literal values —
	// values are bound as parameters, never interpolated into the SQL.
	for i := range plan.Columns {
		placeholder := fmt.Sprintf("$%d", i+1)
		if !strings.Contains(query, placeholder) {
			t.Errorf("query %q does not contain placeholder %q", query, placeholder)
		}
	}
	for _, c := range plan.Columns {
		if strings.Contains(query, c.Value) {
			t.Errorf("query %q wrongly contains literal value %q", query, c.Value)
		}
	}
	if len(values) != len(plan.Columns) {
		t.Fatalf("values = %v, want %d entries", values, len(plan.Columns))
	}
	for i, c := range plan.Columns {
		if values[i] != c.Value {
			t.Errorf("values[%d] = %v, want %q", i, values[i], c.Value)
		}
	}
}

func TestAutoSeedApplyNilPlanIsNoOp(t *testing.T) {
	if err := applyAutoSeed(context.Background(), nil, nil); err != nil {
		t.Errorf("applyAutoSeed(ctx, nil, nil) = %v, want nil", err)
	}
	empty := &AutoSeedPlan{Table: "employee"}
	if err := applyAutoSeed(context.Background(), nil, empty); err != nil {
		t.Errorf("applyAutoSeed with empty Columns = %v, want nil (no-op)", err)
	}
}
