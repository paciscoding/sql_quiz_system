package models

import "time"

// ExportState is the singleton row driving the submission-log exporter.
type ExportState struct {
	Enabled      bool      `json:"enabled"`
	LastExportAt time.Time `json:"last_export_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SubmissionExportRow is one exam submission row rendered into the export CSV.
// The FK columns are nullable in the schema, so they are pointers.
type SubmissionExportRow struct {
	ID                int
	ExamID            *int
	StudentID         *int
	Username          string
	QuestionID        *int
	IsCorrect         bool
	Score             float64
	SubmittedAt       time.Time
	SQLText           string
	ValidationDetails string
}

// PracticeAttemptExportRow is one practice attempt rendered into the export CSV.
type PracticeAttemptExportRow struct {
	ID                  int
	StudentID           int
	Username            string
	PracticeQuestionID  int
	CategoryName        string
	QuestionRequirement string
	IsCorrect           bool
	SubmittedAt         time.Time
	SQLText             string
	ValidationDetails   string
}
