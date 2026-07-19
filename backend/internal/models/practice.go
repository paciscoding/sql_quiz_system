package models

import (
	"encoding/json"
	"time"
)

// PracticeCategory is a first-class organizational unit for practice questions.
// Each category owns its own SQL schema (named "practice_cat_<id>", see
// PracticeCategorySchemaName) into which a lecturer imports a database; every
// practice question in the category runs against that schema. This is what keeps
// practice databases separate from the examination curriculum (public).
type PracticeCategory struct {
	ID            CategoryID `json:"id"`
	Name          string     `json:"name"`
	SchemaName    string     `json:"schema_name"`
	CreatedBy     UserID     `json:"created_by"`
	QuestionCount int        `json:"question_count"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Introduction  string     `json:"introduction"`
}

// PracticeQuestion mirrors models.Question (submission.go) minus ExamID.
// JSON tags match the DDL column names in app.practice_questions exactly.
type PracticeQuestion struct {
	ID        PracticeQuestionID `json:"id"`
	CreatedBy UserID             `json:"created_by"`
	// CategoryID is the FK to app.practice_categories. It is the authoritative
	// category reference; the legacy free-text Category field below is retained
	// for one release for rollback and is write-dead (new writes ignore it).
	CategoryID CategoryID `json:"category_id"`
	// CategoryName / CategorySchema are populated by read joins for response
	// convenience; they are not stored on app.practice_questions.
	CategoryName   string `json:"category_name,omitempty"`
	CategorySchema string `json:"-"`
	Category       string `json:"category,omitempty"`
	// Title is the practice-question display name. Empty string means "no
	// title set"; the UI falls back to an ordinal in that case (ARCH-009).
	Title                    string          `json:"title"`
	TargetTable              string          `json:"target_table"`
	CorrectSQL               string          `json:"correct_sql"`
	Type                     string          `json:"question_type"`
	Requirement              string          `json:"requirement"`
	Points                   float64         `json:"points"`
	StrictOrdering           bool            `json:"strict_ordering"`
	StrictAttrs              bool            `json:"strict_attrs"`
	StrictColumnOrder        bool            `json:"strict_column_order"`
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
		// Bridge: Question.ID stays plain int (ARCH-015, exam-question ids are
		// not encoded) while PracticeQuestion.ID is the typed PracticeQuestionID
		// (still encoded) - this is the one place the two PK namespaces
		// intentionally meet internally. The resulting Question is only used by
		// the validator, never re-encoded/serialized on this path.
		ID:                       int(pq.ID),
		ExamID:                   nil,
		Type:                     pq.Type,
		TargetTable:              pq.TargetTable,
		CorrectSQL:               pq.CorrectSQL,
		Points:                   pq.Points,
		Requirement:              pq.Requirement,
		StrictMode:               StrictMode{Ordering: pq.StrictOrdering, Attributes: pq.StrictAttrs, ColumnOrder: pq.StrictColumnOrder},
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
	ID                 AttemptID          `json:"id"`
	StudentID          UserID             `json:"student_id"`
	PracticeQuestionID PracticeQuestionID `json:"practice_question_id"`
	SQLText            string             `json:"sql_text"`
	IsCorrect          bool               `json:"is_correct"`
	ValidationDetails  json.RawMessage    `json:"validation_details,omitempty"`
	SubmittedAt        time.Time          `json:"submitted_at"`
}

type PracticeSummary struct {
	QuestionID   PracticeQuestionID `json:"question_id"`
	CategoryID   CategoryID         `json:"category_id"`
	CategoryName string             `json:"category_name"`
	Category     string             `json:"category"`
	Requirement  string             `json:"requirement"`
	Points       float64            `json:"points"`
	IsSolved     bool               `json:"is_solved"`
	AttemptCount int                `json:"attempt_count"`
}
