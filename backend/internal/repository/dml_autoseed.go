package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AutoSeedColumn is one column/value pair of an AutoSeedPlan. Value is ALWAYS
// a Go string — never a JSON number or boolean — so that large integers and
// NUMERIC literals survive marshal/unmarshal without going through float64.
// applyAutoSeed binds Value as a text parameter and lets Postgres coerce it to
// the column's real type via the INSERT assignment cast.
type AutoSeedColumn struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AutoSeedPlan is the derived one-row INSERT that rescues a DML model answer
// which would otherwise affect 0 rows. It is schema-unqualified and relies on
// the caller's search_path.
type AutoSeedPlan struct {
	Table   string           `json:"table"`
	Columns []AutoSeedColumn `json:"columns"`
}

// TableColumn describes one column of the target table for the purposes of
// column reconciliation: whether an unpinned column can safely be omitted
// from the seed INSERT (nullable or defaulted) or must cause a refusal
// (NOT NULL with no default). fetchTableColumns treats identity/generated
// columns as HasDefault = true.
type TableColumn struct {
	Name       string
	IsNullable bool
	HasDefault bool
}

// ErrAutoSeedUnsupported is the sentinel wrapped by every derivation refusal.
// Callers use errors.Is(err, ErrAutoSeedUnsupported) to detect a refusal and
// build their own lecturer-facing message; the wrapping error's Error() is
// the reason clause only (see autoSeedRefusal below), with no prefix.
var ErrAutoSeedUnsupported = errors.New("auto-seed does not support this statement")

// autoSeedRefusal is a refusal error whose Error() is exactly the reason
// clause — lowercase, no trailing period — while still satisfying
// errors.Is(err, ErrAutoSeedUnsupported) via Unwrap. fmt.Errorf's %w verb
// cannot be used here because it always splices the wrapped error's own
// Error() text into the result, which would embed ErrAutoSeedUnsupported's
// generic message ahead of the reason clause.
type autoSeedRefusal struct {
	reason string
}

func (e *autoSeedRefusal) Error() string { return e.reason }
func (e *autoSeedRefusal) Unwrap() error { return ErrAutoSeedUnsupported }

func refuse(reason string) error {
	return &autoSeedRefusal{reason: reason}
}

func refusef(format string, args ...any) error {
	return refuse(fmt.Sprintf(format, args...))
}

// deriveAutoSeed parses correctSQL against the deliberately narrow accepted
// grammar:
//
//	DELETE FROM <table> WHERE <col> = <literal> [ AND <col> = <literal> ]*
//	UPDATE <table> SET <...> WHERE <col> = <literal> [ AND <col> = <literal> ]*
//
// and reconciles the pinned columns against cols. Anything it does not fully
// understand is refused with an error wrapping ErrAutoSeedUnsupported —
// refusal is a designed outcome, not a parsing gap. targetTable is used
// verbatim as AutoSeedPlan.Table (it is the caller's already-resolved
// canonical table name); the table name written in correctSQL itself is not
// otherwise significant beyond being present in the accepted shape.
func deriveAutoSeed(correctSQL, targetTable string, cols []TableColumn) (*AutoSeedPlan, error) {
	stmt, err := extractSingleStatement(correctSQL)
	if err != nil {
		return nil, err
	}

	kind, err := autoSeedStatementKind(stmt)
	if err != nil {
		return nil, err
	}

	whereIdx := findTopLevelKeyword(stmt, "WHERE")
	if whereIdx < 0 {
		return nil, refuse("the statement has no WHERE clause for auto-seed to derive a row from")
	}
	head := strings.TrimSpace(stmt[:whereIdx])
	predicate := strings.TrimSpace(stmt[whereIdx+len("WHERE"):])

	switch kind {
	case "DELETE":
		if err := validateAutoSeedDeleteHead(head); err != nil {
			return nil, err
		}
	case "UPDATE":
		if err := validateAutoSeedUpdateHead(head); err != nil {
			return nil, err
		}
	}

	terms, err := splitPredicateTerms(predicate)
	if err != nil {
		return nil, err
	}

	pinned := make(map[string]bool, len(terms))
	var planCols []AutoSeedColumn
	for _, term := range terms {
		colToken, quoted, value, err := parseEqualityTerm(term)
		if err != nil {
			return nil, err
		}
		tc, err := reconcileAutoSeedColumn(colToken, quoted, targetTable, cols)
		if err != nil {
			return nil, err
		}
		if pinned[tc.Name] {
			continue // same column pinned twice; keep the first occurrence
		}
		pinned[tc.Name] = true
		planCols = append(planCols, AutoSeedColumn{Name: tc.Name, Value: value})
	}

	for _, c := range cols {
		if pinned[c.Name] {
			continue
		}
		if !c.IsNullable && !c.HasDefault {
			return nil, refusef("column %q is NOT NULL with no default and your WHERE clause does not pin a value for it", c.Name)
		}
	}

	return &AutoSeedPlan{Table: targetTable, Columns: planCols}, nil
}

// fetchTableColumns loads column metadata for schema.table from
// information_schema.columns, treating identity and generated columns as
// HasDefault = true (per ARCH-038's column-reconciliation table, a lecturer's
// WHERE clause pinning a GENERATED ALWAYS column is refused later, by the
// INSERT itself, not here). schema and table are bound as data parameters,
// never interpolated into SQL.
func fetchTableColumns(ctx context.Context, tx pgx.Tx, schema, table string) ([]TableColumn, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			column_name,
			(is_nullable = 'YES') AS is_nullable,
			(column_default IS NOT NULL
				OR identity_generation IS NOT NULL
				OR is_generated <> 'NEVER') AS has_default
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := []TableColumn{}
	for rows.Next() {
		var c TableColumn
		if err := rows.Scan(&c.Name, &c.IsNullable, &c.HasDefault); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cols, nil
}

// applyAutoSeed executes plan's derived one-row INSERT inside the caller's
// transaction/savepoint. A nil plan, or a plan with no columns, is a no-op
// that returns nil without touching tx — this is what makes it safe to call
// unconditionally on the "already affects > 0 rows" path where no plan was
// derived. Table and column identifiers are sanitized via pgx.Identifier;
// values are passed as bind parameters ($1..$N), never interpolated into the
// generated SQL text.
func applyAutoSeed(ctx context.Context, tx pgx.Tx, plan *AutoSeedPlan) error {
	if plan == nil || len(plan.Columns) == 0 {
		return nil
	}

	query, values := buildAutoSeedInsert(plan)

	_, err := tx.Exec(ctx, query, values...)
	return err
}

// buildAutoSeedInsert builds the INSERT statement text and its bind
// parameters for a non-nil, non-empty plan. It is factored out of
// applyAutoSeed purely so the generated SQL text can be asserted against in
// tests without a live database connection; applyAutoSeed's exported
// behaviour is unchanged. Callers with a nil or empty plan must not call
// this — that no-op short-circuit lives in applyAutoSeed.
func buildAutoSeedInsert(plan *AutoSeedPlan) (query string, values []any) {
	colList := make([]string, len(plan.Columns))
	placeholders := make([]string, len(plan.Columns))
	values = make([]any, len(plan.Columns))
	for i, c := range plan.Columns {
		colList[i] = pgx.Identifier{c.Name}.Sanitize()
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		values[i] = c.Value
	}

	query = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		pgx.Identifier{plan.Table}.Sanitize(),
		strings.Join(colList, ", "),
		strings.Join(placeholders, ", "))

	return query, values
}

// marshalAutoSeed serializes plan to JSON for storage in the auto_seed JSONB
// column. A nil plan marshals to (nil, nil) so callers can store SQL NULL
// rather than the JSON literal null. AutoSeedColumn.Value being a Go string
// guarantees every value round-trips as a JSON string (e.g. "value":"5"),
// never a JSON number or boolean.
func marshalAutoSeed(plan *AutoSeedPlan) ([]byte, error) {
	if plan == nil {
		return nil, nil
	}
	return json.Marshal(plan)
}

// unmarshalAutoSeed is the inverse of marshalAutoSeed. Both a zero-length
// input and a JSON null literal deserialize to (nil, nil).
func unmarshalAutoSeed(raw []byte) (*AutoSeedPlan, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var plan AutoSeedPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// ---------------------------------------------------------------------------
// Statement recognition
// ---------------------------------------------------------------------------

// extractSingleStatement strips comments from sql and returns the single
// non-empty statement it contains (an optional trailing ';' is allowed).
// More than one non-empty statement, separated by a top-level ';', is a
// refusal rather than an attempt to guess which one matters.
func extractSingleStatement(sql string) (string, error) {
	cleaned := stripSQLComments(sql)

	var nonEmpty []string
	for _, seg := range splitTopLevelSemicolons(cleaned) {
		if t := strings.TrimSpace(seg); t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	if len(nonEmpty) == 0 {
		return "", refuse("the correct SQL is empty")
	}
	if len(nonEmpty) > 1 {
		return "", refuse("the input contains more than one statement separated by ';', which auto-seed does not support")
	}
	return nonEmpty[0], nil
}

// autoSeedStatementKind identifies the leading keyword of stmt. INSERT gets
// its own refusal reason (a 0-row INSERT is a SELECT source with no rows,
// which seeding cannot fix); every other keyword is refused generically.
func autoSeedStatementKind(stmt string) (string, error) {
	trimmed := strings.TrimSpace(stmt)
	switch {
	case matchKeywordAt(trimmed, 0, "DELETE"):
		return "DELETE", nil
	case matchKeywordAt(trimmed, 0, "UPDATE"):
		return "UPDATE", nil
	case matchKeywordAt(trimmed, 0, "INSERT"):
		return "", refuse("the statement is an INSERT; a 0-row INSERT has an empty source, which auto-seed cannot fix")
	default:
		return "", refuse("only DELETE and UPDATE statements can be auto-seeded")
	}
}

// isAutoSeedIneligibleInsert reports whether sql's leading statement keyword
// is INSERT, after stripping comments. Question.Type on models.Question is
// only ever "QUERY" or "DML" — it never distinguishes INSERT from
// UPDATE/DELETE — and a 0-row INSERT is an INSERT … SELECT over an empty
// source, which seeding cannot fix, so callers use this to skip auto-seed
// derivation for INSERT questions before ever calling deriveAutoSeed. Shared
// by both call sites (practice and exam) so they can never disagree about
// what counts as "an INSERT".
func isAutoSeedIneligibleInsert(sql string) bool {
	trimmed := strings.TrimSpace(stripSQLComments(sql))
	return matchKeywordAt(trimmed, 0, "INSERT")
}

var autoSeedDeleteHeadRe = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_]*)\s*$`)

// validateAutoSeedDeleteHead checks the portion of a DELETE statement before
// the top-level WHERE. Only `DELETE FROM <table>` is accepted; a USING
// clause, multiple tables, or anything else between the table name and WHERE
// is refused.
func validateAutoSeedDeleteHead(head string) error {
	if autoSeedDeleteHeadRe.MatchString(head) {
		return nil
	}
	upper := strings.ToUpper(head)
	if strings.Contains(upper, "USING") {
		return refuse("the DELETE statement uses a USING clause, which auto-seed does not support")
	}
	if strings.Contains(head, ",") {
		return refuse("the DELETE statement targets more than one table, which auto-seed does not support")
	}
	return refuse("the DELETE statement has content between the table name and WHERE that auto-seed does not understand")
}

var autoSeedUpdateHeadRe = regexp.MustCompile(`(?is)^UPDATE\s+("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_]*)\s+SET\s+.+$`)

// validateAutoSeedUpdateHead checks the portion of an UPDATE statement before
// the top-level WHERE. The SET list itself is never parsed beyond a top-level
// FROM/parenthesis check — because only the tail after WHERE is analysed —
// but a FROM join (top-level, mirroring DELETE ... USING) or a parenthesised
// subquery/function call anywhere in the SET clause is refused rather than
// silently accepted, since neither can be safely reasoned about here.
func validateAutoSeedUpdateHead(head string) error {
	if findTopLevelKeyword(head, "FROM") >= 0 {
		return refuse("the UPDATE statement uses a FROM clause, which auto-seed does not support")
	}
	if findUnquotedByte(head, '(') >= 0 {
		return refuse("the UPDATE statement's SET clause contains parentheses, which auto-seed does not support")
	}
	if autoSeedUpdateHeadRe.MatchString(head) {
		return nil
	}
	return refuse("the UPDATE statement is missing a SET clause that auto-seed can locate")
}

// ---------------------------------------------------------------------------
// Predicate scanning
// ---------------------------------------------------------------------------

// splitPredicateTerms scans predicate once, quote- and paren-aware, refusing
// every construct outside the accepted grammar (OR, NOT, IN, LIKE/ILIKE,
// BETWEEN, IS [NOT] NULL, any parenthesis) the instant it is seen, and
// otherwise splitting the predicate into its top-level AND-joined terms.
func splitPredicateTerms(predicate string) ([]string, error) {
	s := strings.TrimSpace(predicate)
	if s == "" {
		return nil, refuse("the WHERE clause is empty, so auto-seed has no predicate to derive a row from")
	}

	var splits []int
	inSingle, inDouble := false, false
	n := len(s)
	i := 0
	for i < n {
		c := s[i]

		if inSingle {
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			i++
			continue
		}
		if c == '"' {
			inDouble = true
			i++
			continue
		}
		if c == '(' {
			return nil, refusalForParen(s, i)
		}

		switch {
		case matchKeywordAt(s, i, "OR"):
			return nil, refuse("the WHERE clause uses OR, which auto-seed only supports AND-joined equality predicates")
		case matchKeywordAt(s, i, "NOT"):
			return nil, refuse("the WHERE clause uses NOT, which auto-seed does not support")
		case matchKeywordAt(s, i, "BETWEEN"):
			return nil, refuse("the WHERE clause uses BETWEEN, which does not pin a single value")
		case matchKeywordAt(s, i, "ILIKE"):
			return nil, refuse("the WHERE clause uses ILIKE, which does not pin a single value")
		case matchKeywordAt(s, i, "LIKE"):
			return nil, refuse("the WHERE clause uses LIKE, which does not pin a single value")
		case matchKeywordAt(s, i, "IN"):
			return nil, refuse("the WHERE clause uses IN (...), which does not pin a single value")
		case matchKeywordAt(s, i, "IS"):
			return nil, refuse("the WHERE clause uses IS NULL or IS NOT NULL, which does not pin a value")
		case matchKeywordAt(s, i, "AND"):
			splits = append(splits, i)
		}
		i++
	}
	if inSingle || inDouble {
		return nil, refuse("the WHERE clause has an unterminated quoted value")
	}

	terms := make([]string, 0, len(splits)+1)
	start := 0
	for _, idx := range splits {
		terms = append(terms, s[start:idx])
		start = idx + len("AND")
	}
	terms = append(terms, s[start:])
	return terms, nil
}

// refusalForParen builds the refusal reason for a '(' found at s[i]. An
// identifier immediately preceding it is a function call; otherwise, if the
// parenthesised content starts with SELECT it is a subquery; otherwise it is
// bare grouping — all three are outside the accepted grammar.
func refusalForParen(s string, i int) error {
	j := i
	for j > 0 && isAutoSeedIdentByte(s[j-1]) {
		j--
	}
	if j < i {
		return refusef("the WHERE clause calls %s(...), which auto-seed does not support", s[j:i])
	}
	inner := strings.TrimSpace(s[i+1:])
	if matchKeywordAt(inner, 0, "SELECT") {
		return refuse("the WHERE clause contains a subquery, which auto-seed does not support")
	}
	return refuse("the WHERE clause uses parentheses, which auto-seed does not support")
}

var (
	autoSeedBareIdentPrefixRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)
	autoSeedOperatorRe        = regexp.MustCompile(`^[=<>!]+`)
	autoSeedStringLiteralRe   = regexp.MustCompile(`^'(?:[^']|'')*'$`)
	autoSeedNumericLiteralRe  = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)
	autoSeedBoolLiteralRe     = regexp.MustCompile(`(?i)^(?:TRUE|FALSE)$`)
	autoSeedBareIdentFullRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	autoSeedQuotedIdentFullRe = regexp.MustCompile(`^"(?:[^"]|"")+"$`)
)

// parseEqualityTerm parses one AND-joined term of the form
// <col> = <literal>. col is returned unquoted; quoted reports whether it was
// written as a double-quoted identifier (case-sensitive match) or bare
// (case-insensitive match). Anything other than exactly one '=' between a
// column and a single literal is refused, naming the offending operator or
// construct.
func parseEqualityTerm(term string) (col string, quoted bool, value string, err error) {
	t := strings.TrimSpace(term)
	if t == "" {
		return "", false, "", refuse("the WHERE clause has an empty predicate that auto-seed does not understand")
	}

	var colToken string
	if strings.HasPrefix(t, `"`) {
		end := findClosingDoubleQuote(t)
		if end < 0 {
			return "", false, "", refuse("the WHERE clause has an unterminated quoted identifier")
		}
		colToken = t[:end+1]
		col = unescapeDoubleQuoted(colToken)
		quoted = true
		t = t[end+1:]
	} else {
		colToken = autoSeedBareIdentPrefixRe.FindString(t)
		if colToken == "" {
			return "", false, "", refuse("the WHERE clause has a predicate auto-seed does not understand")
		}
		col = colToken
		t = t[len(colToken):]
	}

	t = strings.TrimSpace(t)
	opMatch := autoSeedOperatorRe.FindString(t)
	if opMatch == "" {
		return "", false, "", refusef("the WHERE clause on %q is missing a comparison operator", col)
	}
	if opMatch != "=" {
		return "", false, "", refusef("the WHERE clause uses '%s' on %q, which does not pin a single value", opMatch, col)
	}

	rest := strings.TrimSpace(t[len(opMatch):])
	switch {
	case autoSeedStringLiteralRe.MatchString(rest):
		value = unescapeStringLiteral(rest)
	case autoSeedBoolLiteralRe.MatchString(rest):
		value = rest
	case autoSeedNumericLiteralRe.MatchString(rest):
		value = rest
	case autoSeedBareIdentFullRe.MatchString(rest):
		return "", false, "", refusef("the WHERE clause compares %q to another column (%q), which auto-seed does not support", col, rest)
	case autoSeedQuotedIdentFullRe.MatchString(rest):
		return "", false, "", refusef("the WHERE clause compares %q to another column (%q), which auto-seed does not support", col, unescapeDoubleQuoted(rest))
	default:
		return "", false, "", refusef("the WHERE clause on %q does not pin a literal value that auto-seed understands", col)
	}
	return col, quoted, value, nil
}

// reconcileAutoSeedColumn resolves colToken against cols: case-sensitively if
// it was written as a double-quoted identifier, case-insensitively if bare.
// A pinned name absent from the table is a refusal naming it.
func reconcileAutoSeedColumn(colToken string, quoted bool, table string, cols []TableColumn) (TableColumn, error) {
	for _, c := range cols {
		if quoted {
			if c.Name == colToken {
				return c, nil
			}
		} else if strings.EqualFold(c.Name, colToken) {
			return c, nil
		}
	}
	return TableColumn{}, refusef("column %q pinned in the WHERE clause is not a column of table %q", colToken, table)
}

// ---------------------------------------------------------------------------
// Low-level scanning helpers
// ---------------------------------------------------------------------------

// stripSQLComments replaces '--' line comments and '/* ... */' block
// comments with a single space each, leaving everything inside single- and
// double-quoted regions untouched.
func stripSQLComments(sql string) string {
	var b strings.Builder
	inSingle, inDouble := false, false
	n := len(sql)
	i := 0
	for i < n {
		c := sql[i]

		if inSingle {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < n && sql[i+1] == '\'' {
					b.WriteByte(sql[i+1])
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if inDouble {
			b.WriteByte(c)
			if c == '"' {
				inDouble = false
			}
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '"' {
			inDouble = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '-' && i+1 < n && sql[i+1] == '-' {
			j := i
			for j < n && sql[j] != '\n' {
				j++
			}
			b.WriteByte(' ')
			i = j
			continue
		}
		if c == '/' && i+1 < n && sql[i+1] == '*' {
			j := i + 2
			for j+1 < n && !(sql[j] == '*' && sql[j+1] == '/') {
				j++
			}
			if j+1 < n {
				j += 2
			} else {
				j = n
			}
			b.WriteByte(' ')
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// splitTopLevelSemicolons splits s on ';' characters that are outside quotes
// and outside parentheses.
func splitTopLevelSemicolons(s string) []string {
	var parts []string
	depth := 0
	inSingle, inDouble := false, false
	n := len(s)
	start := 0
	i := 0
	for i < n {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
		i++
	}
	parts = append(parts, s[start:])
	return parts
}

// findTopLevelKeyword returns the index of the first occurrence of keyword as
// a standalone word, outside quotes and outside parentheses, or -1.
func findTopLevelKeyword(s, keyword string) int {
	depth := 0
	inSingle, inDouble := false, false
	n := len(s)
	i := 0
	for i < n {
		c := s[i]

		if inSingle {
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			i++
			continue
		}
		if c == '"' {
			inDouble = true
			i++
			continue
		}
		if c == '(' {
			depth++
			i++
			continue
		}
		if c == ')' {
			if depth > 0 {
				depth--
			}
			i++
			continue
		}

		if depth == 0 && matchKeywordAt(s, i, keyword) {
			return i
		}
		i++
	}
	return -1
}

// matchKeywordAt reports whether s[i:] starts with keyword as a
// case-insensitive whole word (not a substring of a longer identifier).
func matchKeywordAt(s string, i int, keyword string) bool {
	n := len(keyword)
	if i < 0 || i+n > len(s) {
		return false
	}
	if !strings.EqualFold(s[i:i+n], keyword) {
		return false
	}
	if i > 0 && isAutoSeedIdentByte(s[i-1]) {
		return false
	}
	if i+n < len(s) && isAutoSeedIdentByte(s[i+n]) {
		return false
	}
	return true
}

// findUnquotedByte returns the index of the first occurrence of ch outside
// single- and double-quoted regions, or -1. Unlike findTopLevelKeyword, it
// does not track parenthesis depth — it is used to detect the presence of
// ch (typically '(') at any nesting level.
func findUnquotedByte(s string, ch byte) int {
	inSingle, inDouble := false, false
	n := len(s)
	i := 0
	for i < n {
		c := s[i]

		if inSingle {
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i += 2
					continue
				}
				inSingle = false
			}
			i++
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			i++
			continue
		}
		if c == '\'' {
			inSingle = true
			i++
			continue
		}
		if c == '"' {
			inDouble = true
			i++
			continue
		}
		if c == ch {
			return i
		}
		i++
	}
	return -1
}

func isAutoSeedIdentByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

func findClosingDoubleQuote(s string) int {
	i := 1
	n := len(s)
	for i < n {
		if s[i] == '"' {
			if i+1 < n && s[i+1] == '"' {
				i += 2
				continue
			}
			return i
		}
		i++
	}
	return -1
}

func unescapeStringLiteral(s string) string {
	inner := s[1 : len(s)-1]
	return strings.ReplaceAll(inner, "''", "'")
}

func unescapeDoubleQuoted(s string) string {
	inner := s[1 : len(s)-1]
	return strings.ReplaceAll(inner, `""`, `"`)
}

// ---------------------------------------------------------------------------
// Lecturer-authored seed rows (ARCH-046 / ARCH-047)
// ---------------------------------------------------------------------------

// maxSeedStatements and maxSeedSQLBytes bound a lecturer's seed block. Both
// refuse rather than truncate: silently dropping statements would seed a
// different state than the lecturer read back in the form.
const (
	maxSeedStatements = 20
	maxSeedSQLBytes   = 8192
)

// normalizeSeedSQL is the single definition of "no seed". A field holding only
// whitespace is stored as the empty string, never as "   ", so the
// seedSQL != "" precedence test in applyQuestionSeed and in the capture paths
// can never be fooled by an accidental space.
func normalizeSeedSQL(seedSQL string) string {
	return strings.TrimSpace(seedSQL)
}

// validateSeedSQL is the ARCH-046 INSERT-only allowlist: the first of the
// three independent layers protecting the imported schema (the other two are
// the non-superuser student role and the always-rolled-back savepoint).
//
// It strips comments with the same scanner deriveAutoSeed uses, splits on
// top-level semicolons, discards empty segments, and requires every remaining
// statement to lead with the INSERT keyword. The returned slice is the ordered
// list of statements to execute one at a time — never as one multi-command
// string, because the pool is pinned to the extended protocol whose Parse
// message accepts at most one command.
//
// An empty, whitespace-only, or comment-only input is NOT an error: it means
// "no seed", and returns (nil, nil). Every refusal wraps
// ErrAutoSeedUnsupported so callers can turn it into a 400 rather than a 500.
func validateSeedSQL(seedSQL string) ([]string, error) {
	trimmed := normalizeSeedSQL(seedSQL)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > maxSeedSQLBytes {
		return nil, refusef("the seed SQL is %d bytes, which exceeds the %d byte limit", len(trimmed), maxSeedSQLBytes)
	}

	cleaned := stripSQLComments(trimmed)
	if err := checkSeedQuotesBalanced(cleaned); err != nil {
		return nil, err
	}

	// A seed may only touch the question's own schema. Replay runs inside the
	// grading transaction after SET LOCAL ROLE exam_student, and that role
	// holds DML on every real schema, so a schema-qualified target takes locks
	// the ARCH-059 gate cannot see — the gate keys on the execution schema
	// alone. Two categories graded at once whose seeds write the same shared
	// tables in opposite order then deadlock with SQLSTATE 40P01, and the
	// student is blamed for it.
	//
	// This reuses the denylist that already keeps a category import inside its
	// own schema (postgres_schema.go). It runs AFTER checkSeedQuotesBalanced
	// because stripSQLNoise cannot strip literals reliably out of unbalanced
	// quotes, and it scans stripSQLNoise(cleaned) rather than cleaned so that a
	// schema name inside a VALUES literal stays data rather than a match.
	//
	// The refusal is rebuilt with refusef rather than returned from
	// validateNoSchemaQualifiedNames directly: every seed refusal must wrap
	// ErrAutoSeedUnsupported so callers answer 400, and ErrSchemaQualifiedName
	// would fall through to a 500.
	if m := schemaQualifiedNameRe.FindStringSubmatch(stripSQLNoise(cleaned)); m != nil {
		return nil, refusef(
			"the seed SQL references the %q schema; a seed may only write to the question's own schema, because a seed that writes a shared table can deadlock against another category being graded at the same time",
			m[1],
		)
	}

	var stmts []string
	for _, seg := range splitTopLevelSemicolons(cleaned) {
		s := strings.TrimSpace(seg)
		if s == "" {
			continue
		}
		if !matchKeywordAt(s, 0, "INSERT") {
			return nil, refusef("the seed SQL contains a %s statement; only INSERT statements are allowed, because the seed must add rows and nothing else", leadingSeedKeyword(s))
		}
		stmts = append(stmts, s)
	}
	if len(stmts) == 0 {
		return nil, nil
	}
	if len(stmts) > maxSeedStatements {
		return nil, refusef("the seed SQL has %d statements; at most %d statements are allowed", len(stmts), maxSeedStatements)
	}
	return stmts, nil
}

// checkSeedQuotesBalanced refuses a seed whose quoting does not close. Without
// it, an unterminated literal would swallow every following semicolon and turn
// several statements into one, defeating the per-statement INSERT check.
func checkSeedQuotesBalanced(s string) error {
	inSingle, inDouble := false, false
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		if inSingle {
			if c == '\'' {
				if i+1 < n && s[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		}
	}
	if inSingle || inDouble {
		return refuse("the seed SQL has an unterminated quoted value")
	}
	return nil
}

// leadingSeedKeyword returns the uppercased leading word of stmt, for naming
// the offending statement in a refusal. Returns "unrecognised" when stmt does
// not start with an identifier-shaped token.
func leadingSeedKeyword(stmt string) string {
	end := 0
	for end < len(stmt) && isAutoSeedIdentByte(stmt[end]) {
		end++
	}
	if end == 0 {
		return "unrecognised"
	}
	return strings.ToUpper(stmt[:end])
}

// ValidateSeedSQL is the exported form of validateSeedSQL, for the controller
// layer's request validation. The repository re-validates on its own write
// path regardless — this is a fast 400 for the lecturer, not the security
// boundary.
func ValidateSeedSQL(seedSQL string) ([]string, error) { return validateSeedSQL(seedSQL) }

// applyQuestionSeed is the single seeding entry point for all four DML paths
// (practice capture, exam capture, grading, preview). Precedence is ARCH-045:
// a non-empty lecturer seedSQL wins outright and the derived plan is ignored;
// an empty seedSQL delegates to applyAutoSeed, whose nil/empty short-circuit
// means a question with neither seed issues zero extra statements.
//
// MUST be called after SET LOCAL ROLE and inside the caller's savepoint. The
// student login roles are NOINHERIT, so before the role switch the connection
// holds no privileges and the INSERT would fail outright.
func applyQuestionSeed(ctx context.Context, tx pgx.Tx, seedSQL string, plan *AutoSeedPlan) error {
	if normalizeSeedSQL(seedSQL) == "" {
		return applyAutoSeed(ctx, tx, plan)
	}

	stmts, err := validateSeedSQL(seedSQL)
	if err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, execErr := tx.Exec(ctx, stmt); execErr != nil {
			return execErr
		}
	}
	return nil
}
