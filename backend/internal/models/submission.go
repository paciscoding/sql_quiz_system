package models

import (
	"encoding/json"
	"time"
)

type SubmissionRequest struct {
	QuestionNumber int    `json:"question_number"`
	SQLText        string `json:"sql_text"`
}

type ValidationResult struct {
	Status            string          `json:"status"`
	Score             float64         `json:"score"`
	Feedback          string          `json:"feedback"`
	IsCorrect         bool            `json:"is_correct"`
	ValidationDetails json.RawMessage `json:"validation_details,omitempty"`
}

type StrictMode struct {
	Ordering   bool `json:"ordering"`
	Attributes bool `json:"attributes"`
}

type Submission struct {
	ID                int             `json:"id"`
	ExamID            int             `json:"exam_id"`
	StudentID         int             `json:"student_id"`
	QuestionID        int             `json:"question_id"`
	SQLText           string          `json:"sql_text"`
	IsCorrect         bool            `json:"is_correct"`
	Score             float64         `json:"score"`
	ValidationDetails json.RawMessage `json:"validation_details"`
	SubmittedAt       time.Time       `json:"submitted_at"`
}

type Question struct {
	ID                        int             `json:"id"`
	ExamID                    *int            `json:"exam_id,omitempty"`
	TargetTable               string          `json:"target_table"`
	CorrectSQL                string          `json:"correct_sql"`
	Type                      string          `json:"type"` // "QUERY" or "DML"
	Points                    float64         `json:"points"`
	Requirement               string          `json:"requirement"`
	StrictMode                StrictMode      `json:"strict_mode"`
	ExpectedResult            json.RawMessage `json:"expected_result,omitempty"`
	ExpectedColumns           json.RawMessage `json:"expected_columns,omitempty"`
	ExpectedResultStatus      string          `json:"expected_result_status"`
	ExpectedResultCapturedAt  *time.Time      `json:"expected_result_captured_at,omitempty"`
	CaptureError              string          `json:"capture_error,omitempty"`
	CreatedBy                 int             `json:"created_by"`
	CreatedAt                 time.Time       `json:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at"`
}

type StudentExamResult struct {
	QuestionID        int             `json:"question_id"`
	Requirement       string          `json:"requirement"`
	Type              string          `json:"type"`
	Points            float64         `json:"points"`
	SQLText           string          `json:"sql_text"`
	IsCorrect         bool            `json:"is_correct"`
	Score             float64         `json:"score"`
	SubmittedAt       time.Time       `json:"submitted_at"`
	ValidationDetails json.RawMessage `json:"validation_details"`
}

type ExamResultSummary struct {
	ExamID        int     `json:"exam_id"`
	ExamTitle     string  `json:"exam_title"`
	IsActive      bool    `json:"is_active"`
	QuestionCount int     `json:"question_count"`
	AnsweredCount int     `json:"answered_count"`
	CorrectCount  int     `json:"correct_count"`
	TotalScore    float64 `json:"total_score"`
	MaxScore      float64 `json:"max_score"`
}
