package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPracticeQuestion_AsQuestion(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	captured := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)

	rawResult, _ := json.Marshal([]map[string]interface{}{{"id": 1}})
	rawCols, _ := json.Marshal([]string{"id"})

	cases := []struct {
		name string
		pq   PracticeQuestion
	}{
		{
			name: "QUERY question all fields",
			pq: PracticeQuestion{
				ID:                       42,
				CreatedBy:                7,
				TargetTable:              "employees",
				CorrectSQL:               "SELECT * FROM employees",
				Type:                     "QUERY",
				Requirement:              "List all employees",
				Points:                   5.5,
				StrictOrdering:           true,
				StrictAttrs:              false,
				ExpectedResult:           rawResult,
				ExpectedColumns:          rawCols,
				ExpectedResultStatus:     "captured",
				CaptureError:             "",
				ExpectedResultCapturedAt: &captured,
				CreatedAt:                now,
				UpdatedAt:                now,
			},
		},
		{
			name: "DML question strict attrs",
			pq: PracticeQuestion{
				ID:                   99,
				CreatedBy:            3,
				TargetTable:          "orders",
				CorrectSQL:           "INSERT INTO orders VALUES (1,'test')",
				Type:                 "DML",
				Requirement:          "Insert a row",
				Points:               2.0,
				StrictOrdering:       false,
				StrictAttrs:          true,
				ExpectedResultStatus: "pending",
				CreatedAt:            now,
				UpdatedAt:            now,
			},
		},
		{
			name: "zero value question",
			pq:   PracticeQuestion{},
		},
		{
			name: "points zero",
			pq: PracticeQuestion{
				ID:                   1,
				Type:                 "QUERY",
				Points:               0,
				ExpectedResultStatus: "failed",
				CaptureError:         "timeout",
				CreatedAt:            now,
				UpdatedAt:            now,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.pq.AsQuestion()

			// ExamID must always be nil — core invariant
			if got.ExamID != nil {
				t.Errorf("ExamID: want nil, got %v", got.ExamID)
			}

			// Every other relevant field must round-trip exactly
			if got.ID != int(tc.pq.ID) {
				t.Errorf("ID: want %d, got %d", tc.pq.ID, got.ID)
			}
			if got.Type != tc.pq.Type {
				t.Errorf("Type: want %q, got %q", tc.pq.Type, got.Type)
			}
			if got.TargetTable != tc.pq.TargetTable {
				t.Errorf("TargetTable: want %q, got %q", tc.pq.TargetTable, got.TargetTable)
			}
			if got.CorrectSQL != tc.pq.CorrectSQL {
				t.Errorf("CorrectSQL: want %q, got %q", tc.pq.CorrectSQL, got.CorrectSQL)
			}
			if got.Points != tc.pq.Points {
				t.Errorf("Points: want %v, got %v", tc.pq.Points, got.Points)
			}
			if got.Requirement != tc.pq.Requirement {
				t.Errorf("Requirement: want %q, got %q", tc.pq.Requirement, got.Requirement)
			}
			// StrictMode struct reconstruction
			if got.StrictMode.Ordering != tc.pq.StrictOrdering {
				t.Errorf("StrictMode.Ordering: want %v, got %v", tc.pq.StrictOrdering, got.StrictMode.Ordering)
			}
			if got.StrictMode.Attributes != tc.pq.StrictAttrs {
				t.Errorf("StrictMode.Attributes: want %v, got %v", tc.pq.StrictAttrs, got.StrictMode.Attributes)
			}
			if string(got.ExpectedResult) != string(tc.pq.ExpectedResult) {
				t.Errorf("ExpectedResult: want %s, got %s", tc.pq.ExpectedResult, got.ExpectedResult)
			}
			if string(got.ExpectedColumns) != string(tc.pq.ExpectedColumns) {
				t.Errorf("ExpectedColumns: want %s, got %s", tc.pq.ExpectedColumns, got.ExpectedColumns)
			}
			if got.ExpectedResultStatus != tc.pq.ExpectedResultStatus {
				t.Errorf("ExpectedResultStatus: want %q, got %q", tc.pq.ExpectedResultStatus, got.ExpectedResultStatus)
			}
			if got.CaptureError != tc.pq.CaptureError {
				t.Errorf("CaptureError: want %q, got %q", tc.pq.CaptureError, got.CaptureError)
			}
			if got.ExpectedResultCapturedAt != tc.pq.ExpectedResultCapturedAt {
				t.Errorf("ExpectedResultCapturedAt: want %v, got %v", tc.pq.ExpectedResultCapturedAt, got.ExpectedResultCapturedAt)
			}
			if got.CreatedBy != tc.pq.CreatedBy {
				t.Errorf("CreatedBy: want %d, got %d", tc.pq.CreatedBy, got.CreatedBy)
			}
			if !got.CreatedAt.Equal(tc.pq.CreatedAt) {
				t.Errorf("CreatedAt: want %v, got %v", tc.pq.CreatedAt, got.CreatedAt)
			}
			if !got.UpdatedAt.Equal(tc.pq.UpdatedAt) {
				t.Errorf("UpdatedAt: want %v, got %v", tc.pq.UpdatedAt, got.UpdatedAt)
			}
		})
	}
}

func TestAsQuestionCarriesSeedFields(t *testing.T) {
	pq := PracticeQuestion{
		Type:        "DML",
		TargetTable: "employee",
		DMLKind:     "DELETE",
		SeedSQL:     "INSERT INTO employee (ssn) VALUES ('1')",
	}
	got := pq.AsQuestion()
	if got.DMLKind != "DELETE" {
		t.Errorf("DMLKind: got %q, want %q", got.DMLKind, "DELETE")
	}
	if got.SeedSQL != pq.SeedSQL {
		t.Errorf("SeedSQL: got %q, want %q", got.SeedSQL, pq.SeedSQL)
	}
}
