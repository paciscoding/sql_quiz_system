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
	Columns           []string        `json:"-"`
}

type StrictMode struct {
	Ordering    bool `json:"ordering"`
	Attributes  bool `json:"attributes"`
	ColumnOrder bool `json:"column_order"`
}

type Submission struct {
	ID                SubmissionID    `json:"id"`
	ExamID            ExamID          `json:"exam_id"`
	StudentID         UserID          `json:"student_id"`
	QuestionID        int             `json:"question_id"`
	SQLText           string          `json:"sql_text"`
	IsCorrect         bool            `json:"is_correct"`
	Score             float64         `json:"score"`
	ValidationDetails json.RawMessage `json:"validation_details"`
	SubmittedAt       time.Time       `json:"submitted_at"`
}

type Question struct {
	ID          int        `json:"id"`
	ExamID      *ExamID    `json:"exam_id,omitempty"`
	TargetTable string     `json:"target_table"`
	CorrectSQL  string     `json:"correct_sql"`
	Type        string     `json:"type"` // "QUERY" or "DML"
	Points      float64    `json:"points"`
	Requirement string     `json:"requirement"`
	StrictMode  StrictMode `json:"strict_mode"`
	// DualSchema, when true, routes the student PREVIEW to a decoy schema A
	// while GRADING stays on the real schema B (dual-schema grading).
	// Defaults to false — unset/omitted means single-schema (today's) behaviour.
	DualSchema               bool            `json:"dual_schema"`
	ExpectedResult           json.RawMessage `json:"expected_result,omitempty"`
	ExpectedColumns          json.RawMessage `json:"expected_columns,omitempty"`
	ExpectedResultStatus     string          `json:"expected_result_status"`
	ExpectedResultCapturedAt *time.Time      `json:"expected_result_captured_at,omitempty"`
	// ExpectedRowsAffected is the rows-affected count of the lecturer's DML
	// correct_sql at capture time, for diagnostics only. NULL for QUERY
	// questions and for failed captures. Never read during grading (ARCH-009).
	ExpectedRowsAffected *int `json:"expected_rows_affected,omitempty"`
	// AutoSeed is the ARCH-040 AutoSeedPlan JSON
	// (`{"table": "...", "columns": [{"name": "...", "value": "..."}]}`) used
	// to seed a missing row inside the rolled-back grading savepoint before
	// evaluating a DML answer. NULL for QUERY questions and for DML questions
	// whose model answer already affected rows on first run. Derived entirely
	// by the backend at capture time; it must never be accepted from a client
	// request body.
	AutoSeed     json.RawMessage `json:"auto_seed,omitempty"`
	CaptureError string          `json:"capture_error,omitempty"`
	CreatedBy    UserID          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	// CurriculumSchema is the schema that holds the exam's reference database for
	// this question (populated by GetQuestion only, from the exam's curriculum_schema
	// column). Defaults to 'public' when not set — the pre-curriculum-import
	// behaviour is unchanged. Omitted from JSON (used internally by the evaluator).
	CurriculumSchema string `json:"curriculum_schema,omitempty"`
	// CategoryID is the FK to app.exam_categories, populated by read joins.
	// Nil means the question has no category and resolves against
	// CurriculumSchema (the exam-level fallback) exactly as before categories
	// existed.
	CategoryID *ExamCategoryID `json:"category_id,omitempty"`
	// CategoryName is populated by read joins for response convenience; it is
	// not stored on app.questions.
	CategoryName string `json:"category_name,omitempty"`
	// CategorySchema / CategoryPreviewSchema are read-join-only and never
	// serialised (json:"-"), exactly like PracticeQuestion.CategorySchema /
	// CategoryPreviewSchema (models/practice.go:41-46). They are consulted by
	// the evaluator to resolve the grading/preview schema per category.
	CategorySchema        string `json:"-"`
	CategoryPreviewSchema string `json:"-"`
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
	ExamID        ExamID  `json:"exam_id"`
	ExamTitle     string  `json:"exam_title"`
	IsActive      bool    `json:"is_active"`
	QuestionCount int     `json:"question_count"`
	AnsweredCount int     `json:"answered_count"`
	CorrectCount  int     `json:"correct_count"`
	TotalScore    float64 `json:"total_score"`
	MaxScore      float64 `json:"max_score"`
}
