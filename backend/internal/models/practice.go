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
	// PreviewSchemaName is the decoy schema (dual-schema grading) imported for
	// this category's student previews; empty when no decoy has been imported.
	PreviewSchemaName string `json:"preview_schema_name"`
	// HasPreviewSchema is a derived convenience flag: PreviewSchemaName != "".
	// Populated by the category read paths; false when no decoy exists.
	HasPreviewSchema bool `json:"has_preview_schema"`
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
	// CategoryPreviewSchema is the category's decoy schema (dual-schema
	// grading), populated by read joins for response convenience; not stored
	// on app.practice_questions. Empty when the category has no decoy.
	CategoryPreviewSchema string `json:"-"`
	Category              string `json:"category,omitempty"`
	// Title is the practice-question display name. Empty string means "no
	// title set"; the UI falls back to an ordinal in that case (ARCH-009).
	Title       string `json:"title"`
	TargetTable string `json:"target_table"`
	CorrectSQL  string `json:"correct_sql"`
	Type        string `json:"question_type"`
	// DualSchema, when true, routes the student PREVIEW to the category's
	// decoy schema (CategoryPreviewSchema) while GRADING stays on the real
	// category schema. Defaults to false — unset/omitted means single-schema
	// (today's) behaviour.
	DualSchema               bool            `json:"dual_schema"`
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
	AutoSeed  json.RawMessage `json:"auto_seed,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
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
		DualSchema:               pq.DualSchema,
		TargetTable:              pq.TargetTable,
		CorrectSQL:               pq.CorrectSQL,
		Points:                   pq.Points,
		Requirement:              pq.Requirement,
		StrictMode:               StrictMode{Ordering: pq.StrictOrdering, Attributes: pq.StrictAttrs, ColumnOrder: pq.StrictColumnOrder},
		ExpectedResult:           pq.ExpectedResult,
		ExpectedColumns:          pq.ExpectedColumns,
		ExpectedResultStatus:     pq.ExpectedResultStatus,
		ExpectedResultCapturedAt: pq.ExpectedResultCapturedAt,
		ExpectedRowsAffected:     pq.ExpectedRowsAffected,
		AutoSeed:                 pq.AutoSeed,
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
