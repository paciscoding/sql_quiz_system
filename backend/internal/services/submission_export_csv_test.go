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
