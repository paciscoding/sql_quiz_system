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
		// The schema-qualification scan must run AFTER checkSeedQuotesBalanced,
		// because literal-stripping is unreliable across unbalanced quotes: a
		// lecturer whose real bug is a missing closing quote must be told that,
		// not handed a schema message about text swallowed by the runaway
		// literal. This case documents that contract but does not on its own
		// prove it — stripSQLLiterals blanks an unterminated single-quoted body
		// to end of input, so "public.foo" is invisible to the scan from either
		// position. The ordering itself is argued on the scan in
		// dml_autoseed.go.
		{"unterminated quote holding a schema name", "INSERT INTO employee (fname) VALUES ('oops public.foo", "unterminated"},
		// Pins the other end of that ordering: the schema-qualification scan
		// must run AFTER the INSERT-only loop. This statement is refusable on
		// both counts, and the keyword refusal is the one the lecturer can act
		// on — rewriting the schema name would still leave a DELETE.
		{"delete naming a schema", "DELETE FROM public.employee WHERE ssn = '1'", "DELETE"},
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
		// The three cases below are all the same defect: a regex literal-stripper
		// disagrees with the character scanners around it (stripSQLComments,
		// checkSeedQuotesBalanced, splitTopLevelSemicolons) about where a literal
		// starts and ends, and every byte of disagreement is somewhere a
		// schema-qualified target can hide. Each one is accepted by
		// checkSeedQuotesBalanced and split into two real statements, so the
		// second statement genuinely executes — this is an escape, not a
		// degradation into a parse error.

		// A single quote inside a double-quoted IDENTIFIER is not a literal
		// delimiter. A regex that does not know identifiers from literals pairs
		// it with the next real quote and blanks the qualified target between.
		{
			"apostrophe inside a double-quoted identifier hides a later cross-schema target",
			`INSERT INTO "we'ird" (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES ('1')`,
			"public",
		},
		// A tagged dollar-quote body is a literal too. A stripper that only knows
		// the anonymous $$...$$ form leaves the tags in place, so the apostrophes
		// inside the two bodies pair with each other and blank the qualified
		// target between them.
		{
			"tagged dollar-quoted bodies hide a later cross-schema target",
			"INSERT INTO t (a) VALUES ($q$'$q$); INSERT INTO public.employee (ssn) VALUES ($q$'$q$)",
			"public",
		},
		// A dollar-quote only opens at a token boundary: '$' is a legal
		// identifier-continuation byte, so `t$$`, `t$x$` and `a$x$b` are ordinary
		// identifiers to the server. A stripper that opens a literal there scans
		// forward to the next matching tag and blanks everything in between —
		// across the ';' as well, because splitTopLevelSemicolons is '$'-blind and
		// still splits the seed normally. Every statement therefore executes, and
		// the cross-schema write in the middle is hidden.
		{
			"identifier ending in $$ does not open a dollar-quote",
			"INSERT INTO t$$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ($$hi$$)",
			"public",
		},
		{
			"identifier ending in a tagged $x$ does not open a dollar-quote",
			"INSERT INTO t$x$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ($x$hi$x$)",
			"public",
		},
		{
			"$x$ inside an identifier does not open a dollar-quote",
			"INSERT INTO t (a$x$b) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ($x$hi$x$)",
			"public",
		},
		// A '$' preceded by a non-ASCII identifier byte (PostgreSQL admits
		// non-ASCII letters in identifiers) is identifier continuation too, not
		// a tag opener — the token-boundary guard above must not be ASCII-only.
		{
			"identifier ending in a non-ASCII byte before $$ does not open a dollar-quote",
			"INSERT INTO té$$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ($$hi$$)",
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
// is data, not a reference, and refusing it would reject seeds that are
// entirely legitimate.
//
// Only the plain and dollar-quoted literal cases constrain the literal scanner
// (stripSQLLiterals) that the schema-qualification check reads: they are the
// counterweight to TestValidateSeedSQL_RefusesSchemaQualifiedNames, pinning
// those two forms as data.
//
// The doubled-” case does NOT pin the ” branch of stripSQLLiterals, and is
// kept only as a shape guard on the pipeline. Deleting that branch leaves this
// test green, because the escape is parity-neutral: a naive close-then-reopen
// ends the literal on the first quote and starts a new one on the second, so
// the set of bytes left OUTSIDE literals is identical either way and only the
// number of blanking spaces differs. A brute-force search over token sequences
// built from ', ", $$, $q$, ;, a schema-qualified name and filler — every input
// containing ” that checkSeedQuotesBalanced accepts — found no input whose
// accept/refuse outcome changes. The branch stays in the implementation because
// it matches PostgreSQL semantics and the sibling scanner
// checkSeedQuotesBalanced; it is simply not test-pinned, and no test here
// should claim otherwise.
//
// Of the rest, the two comment cases never reach the check at all —
// stripSQLComments removes them first — and "app_settings"/"e.ssn" do not match
// schemaQualifiedNameRe in the first place. Those are shape guards on the
// surrounding pipeline too, kept so a future change to it cannot start
// rejecting them unnoticed.
func TestValidateSeedSQL_QualifiedNameInDataIsNotRefused(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"inside a string literal", "INSERT INTO note (body) VALUES ('see public.foo for details')"},
		// Shape guard only — see the note above: this case does not pin the ''
		// branch of stripSQLLiterals, it passes with or without it.
		{"inside a literal that uses the doubled-quote escape", "INSERT INTO note (body) VALUES ('it''s public.foo')"},
		{"inside an anonymous dollar-quoted literal", "INSERT INTO note (body) VALUES ($$public.foo$$)"},
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

// TestStripSQLLiterals pins the scanner itself rather than only its one caller.
// Everything the schema-qualification check can see is whatever this function
// leaves outside a literal, so each case asserts the exact returned string: a
// byte that survives when it should have been blanked is a false refusal, and a
// byte that is blanked when it should have survived is an escape.
func TestStripSQLLiterals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"a single-quoted body is blanked to one space",
			"INSERT INTO t (a) VALUES ('hi')",
			"INSERT INTO t (a) VALUES ( )",
		},
		{
			"a doubled '' does not end the literal",
			"INSERT INTO t (a) VALUES ('it''s')",
			"INSERT INTO t (a) VALUES ( )",
		},
		{
			"a double-quoted identifier is copied through, apostrophe included",
			`INSERT INTO "we'ird" (a) VALUES (1)`,
			`INSERT INTO "we'ird" (a) VALUES (1)`,
		},
		{
			"an anonymous dollar-quoted body is blanked",
			"INSERT INTO t (a) VALUES ($$hi$$)",
			"INSERT INTO t (a) VALUES ( )",
		},
		{
			"a tagged dollar-quoted body is blanked",
			"INSERT INTO t (a) VALUES ($q$hi$q$)",
			"INSERT INTO t (a) VALUES ( )",
		},
		{
			"an unterminated single quote blanks to end of input",
			"INSERT INTO t (a) VALUES ('oops",
			"INSERT INTO t (a) VALUES ( ",
		},
		{
			"an unterminated double quote is copied through",
			`INSERT INTO "oops (a) VALUES (1)`,
			`INSERT INTO "oops (a) VALUES (1)`,
		},
		// An unterminated dollar-quote is copied VERBATIM rather than blanked to
		// end of input: its extent cannot be trusted, and blanking would let it
		// swallow a qualified target that follows. validateSeedSQL never reaches
		// this state for single quotes (checkSeedQuotesBalanced refuses first),
		// but it is '$'-blind, so this input does reach the scanner.
		{
			"an unterminated dollar-quote is copied verbatim, not blanked",
			"INSERT INTO t (a) VALUES ($q$oops",
			"INSERT INTO t (a) VALUES ($q$oops",
		},
		// $1$x$1$ is not two positional parameters bracketing a tag: the digit
		// after the first '$' means isDollarTagStartByte must refuse it as an
		// opener, so nothing here is blanked. If the digit rule regressed to
		// accept digits, "x" would read as a tag and the body would blank to a
		// single space — unlike the old $1-only case, this one actually pins the
		// digit rule rather than passing regardless of it.
		{
			"a bind placeholder followed by an unrelated $x$ is not a dollar-quote tag",
			"INSERT INTO t (a) VALUES ($1$x$1$)",
			"INSERT INTO t (a) VALUES ($1$x$1$)",
		},
		// A '$' preceded by an identifier byte is identifier continuation, not a
		// tag opener — PostgreSQL only begins dollar-quoting at a token boundary.
		// Treating `t$$` as an opener made the scanner blank forward to the next
		// '$$', erasing a real cross-schema statement lying between them.
		{
			"an identifier ending in $$ does not open a dollar-quote",
			"INSERT INTO t$$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ($$hi$$)",
			"INSERT INTO t$$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ( )",
		},
		// PostgreSQL identifiers admit non-ASCII letters, so a '$' preceded by one
		// is identifier continuation too, not a tag opener. The token-boundary
		// guard's preceding-byte check must reject any byte >= 0x80, not just the
		// ASCII identifier bytes isAutoSeedIdentByte recognises.
		{
			"an identifier ending in a non-ASCII byte before $$ does not open a dollar-quote",
			"INSERT INTO té$$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ($$hi$$)",
			"INSERT INTO té$$ (a) VALUES (1); INSERT INTO public.employee (ssn) VALUES (2); INSERT INTO u (b) VALUES ( )",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripSQLLiterals(tc.in); got != tc.want {
				t.Errorf("stripSQLLiterals(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}
