package services

import (
	"fmt"
	"sort"
	"strings"
)

// diffRowMultiset compares two row-sets (post-image snapshots of a DML target
// table) as multisets under key-aware (strict-attribute) equality: a teacher
// row matches a student row only when both rows have the same set of keys and
// every value compares equal via valuesEqual. Order never matters — a
// genuinely correct UPDATE can reorder a bare `SELECT *` relative to the
// lecturer's capture (Postgres writes an updated tuple at the heap tail), so
// grading a full-table snapshot positionally would false-negative a correct
// student (ARCH-005). Matching is greedy: each teacher row consumes at most
// one still-unmatched student row, which is what makes duplicate rows behave
// as a multiset (2 identical expected rows require 2 identical student rows).
// Unmatched teacher rows are returned as `missing`; leftover student rows are
// returned as `extra`. The two sets are equal iff both returned slices are
// empty.
func diffRowMultiset(teacher, student []map[string]interface{}) (missing, extra []map[string]interface{}) {
	used := make([]bool, len(student))
	for _, tRow := range teacher {
		matched := false
		for i, sRow := range student {
			if used[i] {
				continue
			}
			if rowsEqualStrict(tRow, sRow) {
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			missing = append(missing, tRow)
		}
	}
	for i, sRow := range student {
		if !used[i] {
			extra = append(extra, sRow)
		}
	}
	return missing, extra
}

// rowsEqualStrict is key-aware row equality: both rows must carry the same
// number of keys and every value must compare equal via valuesEqual. Unlike
// ValidationService.compareRows, this is not gated by StrictMode.Attributes —
// DML forces key-aware comparison unconditionally, because both snapshots come
// from `SELECT *` on the same table, so column names always line up and
// ignoring them (the non-strict path) only manufactures false positives.
// Column names are matched case-insensitively (ARCH-043) for the same reason as
// on the QUERY path: an identifier's capture casing is an artefact of whether it
// was quoted, not of whether the answer is right. When EITHER row's own column
// names collide under folding, folding has no unambiguous answer for that pair
// and we fall back to the pre-ARCH-043 exact-key comparison (ARCH-044). The
// original code failed closed there, which zeroed every student whose target
// table was created with case-distinct quoted columns ("Total" and "total"):
// the TEACHER's captured post-image collided, so diffRowMultiset reported every
// teacher row as missing no matter what the student wrote. The fallback is
// still deterministic — no dependence on Go's randomised map order.
// Values still compare case-sensitively through valuesEqual.
func rowsEqualStrict(teacher, student map[string]interface{}) bool {
	// Folding never changes a row's key count unless it collides, so this arity
	// check remains a valid pre-filter over the folded maps below — and it is
	// also the arity precondition rowsEqualExactKeys relies on.
	if len(teacher) != len(student) {
		return false
	}
	tFolded, tOK := foldRowKeys(teacher)
	sFolded, sOK := foldRowKeys(student)
	if !tOK || !sOK {
		return rowsEqualExactKeys(teacher, student)
	}
	for k, v := range tFolded {
		sv, ok := sFolded[k]
		if !ok || !valuesEqual(v, sv) {
			return false
		}
	}
	return true
}

// dmlDiffMaxSampleRows caps how many sample rows formatDMLDiff renders per
// bucket (Missing / Unexpected), and dmlDiffMaxCellRunes caps each rendered
// cell's width, so a diff against a 10,000-row table stays a small, readable
// string instead of a multi-megabyte response.
const (
	dmlDiffMaxSampleRows = 3
	dmlDiffMaxCellRunes  = 40
)

// formatDMLDiff renders a compact, human-readable multi-line summary of a
// failed DML comparison: overall row counts, missing/unexpected counts, and
// up to dmlDiffMaxSampleRows sample rows per bucket. columns (when non-empty)
// fixes the key order each row is rendered in; otherwise keys are sorted.
func formatDMLDiff(targetTable string, teacherCount, studentCount int, missing, extra []map[string]interface{}, columns []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The final state of %s does not match the expected result.\n", targetTable)
	fmt.Fprintf(&b, "Expected %d row(s), your answer produced %d.\n", teacherCount, studentCount)
	fmt.Fprintf(&b, "%d expected row(s) are missing, %d unexpected row(s) are present.", len(missing), len(extra))

	if len(missing) > 0 {
		b.WriteString("\nMissing:")
		appendSampleRows(&b, missing, columns)
	}
	if len(extra) > 0 {
		b.WriteString("\nUnexpected:")
		appendSampleRows(&b, extra, columns)
	}
	return b.String()
}

func appendSampleRows(b *strings.Builder, rows []map[string]interface{}, columns []string) {
	shown := len(rows)
	if shown > dmlDiffMaxSampleRows {
		shown = dmlDiffMaxSampleRows
	}
	for i := 0; i < shown; i++ {
		b.WriteString("\n  ")
		b.WriteString(formatDMLRow(rows[i], columns))
	}
	if len(rows) > shown {
		fmt.Fprintf(b, "\n  ... and %d more", len(rows)-shown)
	}
}

func formatDMLRow(row map[string]interface{}, columns []string) string {
	// ARCH-043: matching folds column names, but the DISPLAY must not. When the
	// caller supplies the teacher's captured column list we look each cell up by
	// folded name (so a student row keyed "Fname" still renders under the
	// teacher's "fname" slot) yet print the teacher's original spelling, which
	// is the one the question was authored with and the one the student should
	// see in the feedback. A row whose own names collide under folding gets no
	// usable folded index, so it falls back to rendering its own keys verbatim
	// rather than dropping cells.
	folded, foldOK := foldRowKeys(row)
	if len(columns) > 0 && foldOK {
		parts := make([]string, 0, len(columns))
		for _, k := range columns {
			v, ok := folded[foldColumnName(k)]
			if !ok {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%s", k, truncateCell(fmt.Sprintf("%v", v))))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}

	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncateCell(fmt.Sprintf("%v", row[k]))))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func truncateCell(s string) string {
	r := []rune(s)
	if len(r) <= dmlDiffMaxCellRunes {
		return s
	}
	return string(r[:dmlDiffMaxCellRunes]) + "..."
}
