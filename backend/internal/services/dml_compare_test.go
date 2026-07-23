package services

import (
	"strings"
	"testing"
)

func row(kv ...interface{}) map[string]interface{} {
	m := make(map[string]interface{}, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestDiffRowMultiset_PermutationIsEqual(t *testing.T) {
	teacher := []map[string]interface{}{
		row("pno", 1, "hours", 40),
		row("pno", 2, "hours", 20),
	}
	// Same rows, reversed order — a genuinely correct UPDATE can reorder the
	// heap relative to the capture; ordering must never matter for DML.
	student := []map[string]interface{}{
		row("pno", 2, "hours", 20),
		row("pno", 1, "hours", 40),
	}

	missing, extra := diffRowMultiset(teacher, student)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("expected a permutation to be equal, got missing=%v extra=%v", missing, extra)
	}
}

func TestDiffRowMultiset_DuplicateRowsRequireDuplicateMatches(t *testing.T) {
	teacher := []map[string]interface{}{
		row("pno", 1, "hours", 40),
		row("pno", 1, "hours", 40),
	}

	// Only one matching student row -> one teacher row must be reported missing.
	student := []map[string]interface{}{
		row("pno", 1, "hours", 40),
	}
	missing, extra := diffRowMultiset(teacher, student)
	if len(missing) != 1 || len(extra) != 0 {
		t.Fatalf("expected 1 missing row with a single duplicate short, got missing=%v extra=%v", missing, extra)
	}

	// Exactly two matching student rows -> fully equal.
	student2 := []map[string]interface{}{
		row("pno", 1, "hours", 40),
		row("pno", 1, "hours", 40),
	}
	missing2, extra2 := diffRowMultiset(teacher, student2)
	if len(missing2) != 0 || len(extra2) != 0 {
		t.Fatalf("expected 2 identical expected rows to be satisfied by 2 identical student rows, got missing=%v extra=%v", missing2, extra2)
	}
}

func TestDiffRowMultiset_MissingAndExtraBuckets(t *testing.T) {
	teacher := []map[string]interface{}{
		row("pno", 1, "hours", 40),
		row("pno", 2, "hours", 20),
	}
	student := []map[string]interface{}{
		row("pno", 1, "hours", 40),
		row("pno", 3, "hours", 99),
	}

	missing, extra := diffRowMultiset(teacher, student)
	if len(missing) != 1 || missing[0]["pno"] != 2 {
		t.Fatalf("expected pno=2 missing, got %v", missing)
	}
	if len(extra) != 1 || extra[0]["pno"] != 3 {
		t.Fatalf("expected pno=3 extra, got %v", extra)
	}
}

func TestDiffRowMultiset_ForcedStrictAttributesRejectsValueOnlyPermutation(t *testing.T) {
	// Same values, different column names/positions — under the OLD non-strict
	// mode this would incorrectly match on bare value sets. DML forces
	// key-aware comparison unconditionally, so it must NOT match here.
	teacher := []map[string]interface{}{
		row("pno", 1, "hours", 40),
	}
	student := []map[string]interface{}{
		row("pno", 40, "hours", 1),
	}

	missing, extra := diffRowMultiset(teacher, student)
	if len(missing) != 1 || len(extra) != 1 {
		t.Fatalf("expected value-permuted row to be rejected under strict-attribute matching, got missing=%v extra=%v", missing, extra)
	}
}

func TestDiffRowMultiset_RowCountMismatch(t *testing.T) {
	teacher := []map[string]interface{}{
		row("pno", 1, "hours", 40),
	}
	student := []map[string]interface{}{
		row("pno", 1, "hours", 40),
		row("pno", 2, "hours", 20),
	}
	missing, extra := diffRowMultiset(teacher, student)
	if len(missing) != 0 || len(extra) != 1 {
		t.Fatalf("expected 0 missing, 1 extra for a longer student set, got missing=%v extra=%v", missing, extra)
	}
}

func TestDiffRowMultiset_BothEmptyIsEqual(t *testing.T) {
	missing, extra := diffRowMultiset(nil, nil)
	if len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("expected two empty sets to be equal, got missing=%v extra=%v", missing, extra)
	}
}

func TestFormatDMLDiff_CapsSampleRowsAndAddsMoreSuffix(t *testing.T) {
	var missing []map[string]interface{}
	for i := 0; i < 5; i++ {
		missing = append(missing, row("pno", i, "hours", i*10))
	}
	var extra []map[string]interface{}
	for i := 0; i < 2; i++ {
		extra = append(extra, row("pno", 100+i, "hours", i))
	}

	out := formatDMLDiff("works_on", 47, 48, missing, extra, []string{"pno", "hours"})

	if !strings.Contains(out, "Expected 47 row(s), your answer produced 48.") {
		t.Fatalf("expected row-count line, got:\n%s", out)
	}
	if !strings.Contains(out, "5 expected row(s) are missing, 2 unexpected row(s) are present.") {
		t.Fatalf("expected missing/extra count line, got:\n%s", out)
	}
	if !strings.Contains(out, "... and 2 more") {
		t.Fatalf("expected a '... and N more' suffix for the 5-row Missing bucket capped at 3, got:\n%s", out)
	}
	// Unexpected bucket has only 2 rows, under the cap — no "more" suffix for it.
	missingSection := out[strings.Index(out, "Missing:"):strings.Index(out, "Unexpected:")]
	if strings.Count(missingSection, "pno=") != dmlDiffMaxSampleRows {
		t.Fatalf("expected exactly %d sample rows under Missing, got section:\n%s", dmlDiffMaxSampleRows, missingSection)
	}
}

func TestFormatDMLDiff_CellTruncation(t *testing.T) {
	longVal := strings.Repeat("x", 100)
	missing := []map[string]interface{}{row("note", longVal)}
	out := formatDMLDiff("t", 1, 0, missing, nil, []string{"note"})

	if strings.Contains(out, longVal) {
		t.Fatalf("expected the 100-rune cell to be truncated, got:\n%s", out)
	}
	if !strings.Contains(out, strings.Repeat("x", dmlDiffMaxCellRunes)+"...") {
		t.Fatalf("expected a %d-rune-truncated cell with an ellipsis, got:\n%s", dmlDiffMaxCellRunes, out)
	}
}

func TestFormatDMLDiff_OverallSizeBound(t *testing.T) {
	var missing, extra []map[string]interface{}
	longVal := strings.Repeat("y", 1000)
	for i := 0; i < 10000; i++ {
		missing = append(missing, row("a", longVal, "b", longVal, "c", longVal))
		extra = append(extra, row("a", longVal, "b", longVal, "c", longVal))
	}

	out := formatDMLDiff("huge_table", 10000, 10000, missing, extra, []string{"a", "b", "c"})
	if len(out) > 2048 {
		t.Fatalf("expected Feedback to stay under ~2KB regardless of result-set size, got %d bytes", len(out))
	}
}

func TestFormatDMLDiff_FallsBackToSortedKeysWhenColumnsEmpty(t *testing.T) {
	missing := []map[string]interface{}{row("zeta", 1, "alpha", 2)}
	out := formatDMLDiff("t", 1, 0, missing, nil, nil)
	// alpha must render before zeta when columns is empty (sorted-key fallback).
	if strings.Index(out, "alpha=") > strings.Index(out, "zeta=") {
		t.Fatalf("expected sorted-key fallback (alpha before zeta), got:\n%s", out)
	}
}
