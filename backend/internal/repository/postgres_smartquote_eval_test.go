package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

// TestNormalizeSmartQuotes_ReportedInsert pins the exact lecturer-reported case:
// INSERT ... VALUES (‘111111100’, 62, 527) with curly quotes must normalise to
// straight quotes. No database needed.
func TestNormalizeSmartQuotes_ReportedInsert(t *testing.T) {
	in := "INSERT INTO WORKS_ON VALUES (‘111111100’, 62, 527)"
	want := "INSERT INTO WORKS_ON VALUES ('111111100', 62, 527)"
	if got := normalizeSmartQuotes(in); got != want {
		t.Fatalf("normalizeSmartQuotes:\n got=%q\nwant=%q", got, want)
	}
}

// TestCreateQuestion_SmartQuoteDMLCaptures is the regression test for the
// reported lecturer bug: creating a DML question whose correct SQL uses curly
// quotes previously failed Phase-0 capture with `column "‘…’" does not exist`.
// After the fix the quotes are normalised before capture AND before the row is
// persisted, so capture succeeds and the stored SQL is clean.
func TestCreateQuestion_SmartQuoteDMLCaptures(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	examID, tbl, uid := mkExamAndTable(t, repo, ctx) // tbl is public.<tbl>(id int)

	q := models.Question{
		ExamID:      &examID,
		TargetTable: tbl,
		// Curly quotes around the value — exactly what a paste from a doc produces.
		CorrectSQL:  "INSERT INTO " + tbl + " VALUES (‘42’)",
		Type:        "DML",
		Points:      5,
		Requirement: "insert 42",
		CreatedBy:   uid,
	}
	id, err := repo.CreateQuestion(ctx, q)
	if err != nil {
		t.Fatalf("CreateQuestion: %v", err)
	}

	saved, err := repo.GetQuestion(ctx, id)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if saved.ExpectedResultStatus != "captured" {
		t.Fatalf("capture failed: status=%q err=%q (smart quotes not repaired)",
			saved.ExpectedResultStatus, saved.CaptureError)
	}
	if strings.ContainsAny(saved.CorrectSQL, "‘’") {
		t.Fatalf("stored correct SQL still has curly quotes: %q", saved.CorrectSQL)
	}
}

// TestEvaluate_SmartQuoteSelectSucceeds covers the graded student path: a SELECT
// with curly quotes must be normalised and run, not fail as an unknown column.
func TestEvaluate_SmartQuoteSelectSucceeds(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	// ExamID nil => practice-style QUERY; querySchemaFor returns the passed schema.
	q := models.Question{Type: "QUERY"}
	res, err := repo.Evaluate(ctx, 1, q, "SELECT ‘hello’ AS greeting", "public")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("expected success, got status=%q feedback=%q (smart quotes not repaired)",
			res.Status, res.Feedback)
	}
}
