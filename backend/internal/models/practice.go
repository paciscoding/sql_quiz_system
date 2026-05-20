package models

import (
	"encoding/json"
	"time"
)

// PracticeQuestion mirrors models.Question (submission.go) minus ExamID.
// JSON tags match the DDL column names in app.practice_questions exactly.
type PracticeQuestion struct {
	ID                       int             `json:"id"`
	CreatedBy                int             `json:"created_by"`
	Category                 string          `json:"category"`
	TargetTable              string          `json:"target_table"`
	CorrectSQL               string          `json:"correct_sql"`
	Type                     string          `json:"question_type"`
	Requirement              string          `json:"requirement"`
	Points                   float64         `json:"points"`
	StrictOrdering           bool            `json:"strict_ordering"`
	StrictAttrs              bool            `json:"strict_attrs"`
	ExpectedResult           json.RawMessage `json:"expected_result,omitempty"`
	ExpectedColumns          json.RawMessage `json:"expected_columns,omitempty"`
	ExpectedResultStatus     string          `json:"expected_result_status"`
	CaptureError             string          `json:"capture_error,omitempty"`
	ExpectedResultCapturedAt *time.Time      `json:"expected_result_captured_at,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
}

// AsQuestion builds a *Question suitable for passing to ValidationService.
// ExamID is always nil because practice questions are not exam-scoped.
func (pq *PracticeQuestion) AsQuestion() *Question {
	return &Question{
		ID:                       pq.ID,
		ExamID:                   nil,
		Type:                     pq.Type,
		TargetTable:              pq.TargetTable,
		CorrectSQL:               pq.CorrectSQL,
		Points:                   pq.Points,
		Requirement:              pq.Requirement,
		StrictMode:               StrictMode{Ordering: pq.StrictOrdering, Attributes: pq.StrictAttrs},
		ExpectedResult:           pq.ExpectedResult,
		ExpectedColumns:          pq.ExpectedColumns,
		ExpectedResultStatus:     pq.ExpectedResultStatus,
		ExpectedResultCapturedAt: pq.ExpectedResultCapturedAt,
		CaptureError:             pq.CaptureError,
		CreatedBy:                pq.CreatedBy,
		CreatedAt:                pq.CreatedAt,
		UpdatedAt:                pq.UpdatedAt,
	}
}

// PracticeAttempt mirrors models.Submission (submission.go) minus ExamID,
// with QuestionID renamed to PracticeQuestionID.
// JSON tags match the DDL column names in app.practice_attempts exactly.
type PracticeAttempt struct {
	ID                 int             `json:"id"`
	StudentID          int             `json:"student_id"`
	PracticeQuestionID int             `json:"practice_question_id"`
	SQLText            string          `json:"sql_text"`
	IsCorrect          bool            `json:"is_correct"`
	ValidationDetails  json.RawMessage `json:"validation_details,omitempty"`
	SubmittedAt        time.Time       `json:"submitted_at"`
}

type PracticeSummary struct {
	QuestionID  int     `json:"question_id"`
	Category    string  `json:"category"`
	Requirement string  `json:"requirement"`
	Points      float64 `json:"points"`
	IsSolved    bool    `json:"is_solved"`
	AttemptCount int    `json:"attempt_count"`
}
