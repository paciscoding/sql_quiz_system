package repository

import (
	"fmt"
	"regexp"
	"strings"
)

// DMLCorrectSQLTargetTableWarning is a warn-only sanity check that a DML
// question's correct_sql actually references its declared target_table. It
// never returns an error and never blocks a save — callers (practice and
// exam controllers) run it after a successful save and surface the result as
// an advisory table_warning field for the lecturer. It has nothing to do
// with dual_schema and does not read or write any dual-schema column.
//
// The check is deliberately loose: correctSQL is stripped of comments and
// string literals via stripSQLNoise (postgres_schema.go:296) before matching,
// so a target table mentioned only inside a comment or a quoted literal does
// not count as a reference. The match itself is case-insensitive with a
// word-boundary regex, so "employees" matches "EMPLOYEES" but not
// "employee" or "employees2".
func DMLCorrectSQLTargetTableWarning(qType, correctSQL, targetTable string) string {
	if qType != "DML" {
		return ""
	}
	table := strings.TrimSpace(targetTable)
	if table == "" {
		return ""
	}

	stripped := stripSQLNoise(correctSQL)
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(table) + `\b`)
	if re.MatchString(stripped) {
		return ""
	}
	return fmt.Sprintf("Heads up: the correct SQL does not mention the target table %q. Double-check the target table is correct.", table)
}
