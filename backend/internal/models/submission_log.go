package models

import (
	"encoding/json"
	"time"
)

// SubmissionLogFilter is the shared query filter for both the exam and
// practice submission-log listings (list, detail-by-id lookups are keyed
// separately, and the CSV stream). Source selects which table is queried;
// the other fields are optional narrowing filters.
//
// ExamID / StudentID / QuestionID are already-decoded plain ints: the
// controller is responsible for decoding the opaque public ids (exam id,
// student/user id) off the wire before building this filter — the
// repository layer never sees or validates the opaque token form.
type SubmissionLogFilter struct {
	// Source is "exam" or "practice".
	Source string

	ExamID     *int
	StudentID  *int
	QuestionID *int

	// Outcome is "all" (default), "correct", or "incorrect".
	Outcome string

	// CountedOnly, when true, restricts results to the attempt that is the
	// one actually counted for the student's score (see is_counted below).
	CountedOnly bool

	From *time.Time
	To   *time.Time

	Limit  int
	Offset int
}

// ExamSubmissionLogRow is one row of the exam submission log listing.
//
// attempt_no / is_counted are computed per request with window functions,
// never persisted. is_counted's ordering
// (PARTITION BY student_id, question_id ORDER BY score DESC, submitted_at DESC)
// MUST stay in exact sync with the "best score wins, ties go to the most
// recent" ordering in backend/internal/repository/postgres_results.go's
// GetStudentExamResults / GetStudentResultsSummary (the DISTINCT ON queries
// that decide which submission actually counts for a student's grade). If
// the two orderings ever diverge, this log will mark the wrong attempt as
// counted.
type ExamSubmissionLogRow struct {
	ID                SubmissionID `json:"id"`
	ExamID            ExamID       `json:"exam_id"`
	ExamTitle         string       `json:"exam_title"`
	StudentID         UserID       `json:"student_id"`
	Username          string       `json:"username"`
	StudentNumber     string       `json:"student_number"`
	QuestionID        int          `json:"question_id"`
	QuestionDisplayNo int          `json:"question_display_no"`
	Requirement       string       `json:"requirement"`
	QuestionType      string       `json:"question_type"`
	Points            float64      `json:"points"`
	AttemptNo         int          `json:"attempt_no"`
	IsCounted         bool         `json:"is_counted"`
	IsCorrect         bool         `json:"is_correct"`
	Score             float64      `json:"score"`
	SubmittedAt       time.Time    `json:"submitted_at"`
	SQLPreview        string       `json:"sql_preview"`
}

// ExamSubmissionLogDetail is ExamSubmissionLogRow plus the full sql_text and
// parsed validation_details, returned by the single-submission detail
// endpoint only (never the list, which caps sql_text to SQLPreview).
type ExamSubmissionLogDetail struct {
	ExamSubmissionLogRow
	SQLText           string          `json:"sql_text"`
	ValidationDetails json.RawMessage `json:"validation_details"`
}

// PracticeSubmissionLogRow is one row of the practice submission log listing.
//
// app.practice_attempts carries no score column (practice is pass/fail), so
// is_counted here is derived from
// PARTITION BY student_id, practice_question_id ORDER BY is_correct DESC, submitted_at DESC
// i.e. the first correct attempt wins; if the question was never solved,
// the most recent attempt is the one that "counts" (mirrors the exam
// tie-break rule, substituting is_correct for score since practice has no
// score column). This is this feature's own derivation — there is no
// existing practice equivalent of postgres_results.go to stay in sync with.
type PracticeSubmissionLogRow struct {
	ID                 AttemptID          `json:"id"`
	StudentID          UserID             `json:"student_id"`
	Username           string             `json:"username"`
	StudentNumber      string             `json:"student_number"`
	PracticeQuestionID PracticeQuestionID `json:"practice_question_id"`
	CategoryName       string             `json:"category_name"`
	Requirement        string             `json:"requirement"`
	AttemptNo          int                `json:"attempt_no"`
	IsCounted          bool               `json:"is_counted"`
	IsCorrect          bool               `json:"is_correct"`
	SubmittedAt        time.Time          `json:"submitted_at"`
	SQLPreview         string             `json:"sql_preview"`
}

// PracticeAttemptLogDetail is PracticeSubmissionLogRow plus the full sql_text
// and parsed validation_details, returned by the single-attempt detail
// endpoint only.
type PracticeAttemptLogDetail struct {
	PracticeSubmissionLogRow
	SQLText           string          `json:"sql_text"`
	ValidationDetails json.RawMessage `json:"validation_details"`
}
