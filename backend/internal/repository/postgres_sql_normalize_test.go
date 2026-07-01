package repository

import "testing"

func TestNormalizeSmartQuotes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "curly single quotes around a string literal",
			in:   "INSERT INTO works_on VALUES (‘111111100’, 62, 527)",
			want: "INSERT INTO works_on VALUES ('111111100', 62, 527)",
		},
		{
			name: "curly double quotes around an identifier",
			in:   `SELECT “Ssn” FROM employee`,
			want: `SELECT "Ssn" FROM employee`,
		},
		{
			name: "low-9 and prime variants",
			in:   "VALUES (‚x‛, ′y′, „z‟, ″w″)",
			want: `VALUES ('x', 'y', "z", "w")`,
		},
		{
			name: "ascii-only input is unchanged",
			in:   "INSERT INTO works_on VALUES ('111111100', 62, 527)",
			want: "INSERT INTO works_on VALUES ('111111100', 62, 527)",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSmartQuotes(tc.in); got != tc.want {
				t.Errorf("normalizeSmartQuotes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
