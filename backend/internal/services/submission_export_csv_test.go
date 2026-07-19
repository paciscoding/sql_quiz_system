package services

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"io"
	"testing"
	"time"

	"github.com/paciscoding/sql-quiz-system/internal/models"
)

func gunzipCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gz: %v", err)
	}
	recs, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	return recs
}

func TestBuildSubmissionsCSVGz_HeaderAndNullFK(t *testing.T) {
	eid, sid, qid := 3, 7, 9
	rows := []models.SubmissionExportRow{
		{ID: 1, ExamID: &eid, StudentID: &sid, Username: "alice", QuestionID: &qid,
			IsCorrect: true, Score: 2.5, SubmittedAt: time.Unix(0, 0).UTC(),
			SQLText: "SELECT 1, 2", ValidationDetails: `{"ok":true}`},
		{ID: 2, ExamID: nil, StudentID: nil, Username: "", QuestionID: nil,
			IsCorrect: false, Score: 0, SubmittedAt: time.Unix(0, 0).UTC(),
			SQLText: "line1\nline2", ValidationDetails: ""},
	}
	gz, err := buildSubmissionsCSVGz(rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	recs := gunzipCSV(t, gz)
	if recs[0][0] != "id" || recs[0][3] != "username" {
		t.Fatalf("header wrong: %v", recs[0])
	}
	if len(recs) != 3 { // header + 2 rows
		t.Fatalf("row count = %d, want 3", len(recs))
	}
	// Null FK row renders empty strings, not "0".
	if recs[2][1] != "" || recs[2][2] != "" {
		t.Fatalf("null FK not empty: %v", recs[2])
	}
	// Embedded comma + newline survive CSV quoting.
	if recs[1][8] != "SELECT 1, 2" {
		t.Fatalf("sql_text mangled: %q", recs[1][8])
	}
	if recs[2][8] != "line1\nline2" {
		t.Fatalf("multiline sql_text mangled: %q", recs[2][8])
	}
}

// TestBuildPracticeAttemptsCSVGz_HeaderAndColumns is the regression test for
// ARCH-005: the additive category_name/question_requirement columns must land
// right after practice_question_id (both header and row), and
// practice_question_id itself must stay the raw int (never replaced by a name).
func TestBuildPracticeAttemptsCSVGz_HeaderAndColumns(t *testing.T) {
	rows := []models.PracticeAttemptExportRow{
		{
			ID: 1, StudentID: 5, Username: "bob", PracticeQuestionID: 42,
			CategoryName: "Joins", QuestionRequirement: "write an inner join",
			IsCorrect: true, SubmittedAt: time.Unix(0, 0).UTC(),
			SQLText: "SELECT 1, 2", ValidationDetails: `{"ok":true}`,
		},
		{
			// Orphaned/edge case: names empty (e.g. COALESCE fallback), still
			// must render as empty CSV cells, not panic or shift columns.
			ID: 2, StudentID: 6, Username: "carol", PracticeQuestionID: 43,
			CategoryName: "", QuestionRequirement: "",
			IsCorrect: false, SubmittedAt: time.Unix(0, 0).UTC(),
			SQLText: "SELECT 3", ValidationDetails: "",
		},
	}
	gz, err := buildPracticeAttemptsCSVGz(rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	recs := gunzipCSV(t, gz)

	wantHeader := []string{"id", "student_id", "username", "practice_question_id",
		"category_name", "question_requirement",
		"is_correct", "submitted_at", "sql_text", "validation_details"}
	if len(recs[0]) != len(wantHeader) {
		t.Fatalf("header column count = %d, want %d: %v", len(recs[0]), len(wantHeader), recs[0])
	}
	for i, col := range wantHeader {
		if recs[0][i] != col {
			t.Fatalf("header[%d] = %q, want %q (full header: %v)", i, recs[0][i], col, recs[0])
		}
	}
	if len(recs) != 3 { // header + 2 rows
		t.Fatalf("row count = %d, want 3", len(recs))
	}

	// Row 1: practice_question_id stays the raw int "42" (col 3), names populated at cols 4/5.
	if recs[1][3] != "42" {
		t.Fatalf("practice_question_id = %q, want raw int %q (ids stay raw; names are additive)", recs[1][3], "42")
	}
	if recs[1][4] != "Joins" || recs[1][5] != "write an inner join" {
		t.Fatalf("category_name/question_requirement = %q/%q, want %q/%q", recs[1][4], recs[1][5], "Joins", "write an inner join")
	}

	// Row 2: empty names render as empty cells at the same positions, id stays raw.
	if recs[2][3] != "43" {
		t.Fatalf("row2 practice_question_id = %q, want %q", recs[2][3], "43")
	}
	if recs[2][4] != "" || recs[2][5] != "" {
		t.Fatalf("row2 category_name/question_requirement should be empty, got %q/%q", recs[2][4], recs[2][5])
	}
}

func TestParseExportInterval(t *testing.T) {
	if ParseExportInterval("") != 24*time.Hour {
		t.Fatal("empty should default 24h")
	}
	if ParseExportInterval("garbage") != 24*time.Hour {
		t.Fatal("invalid should default 24h")
	}
	if ParseExportInterval("15m") != 15*time.Minute {
		t.Fatal("valid duration not parsed")
	}
}
