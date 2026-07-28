package models

import (
	"encoding/json"

	"github.com/paciscoding/sql-quiz-system/internal/publicid"
)

// This file defines the named integer id types that adopt opaque, type-
// prefixed public tokens on the JSON boundary (see publicid). Each type
// wraps a persistent PK namespace; MarshalJSON encodes and UnmarshalJSON
// decodes+validates via the publicid codec. There are exactly 8 types -
// exam-question ids (app.questions.id) intentionally stay plain int, since
// they are a student/lecturer-visible ordinal (Q1, Q2, ...), not an
// enumeration-sensitive surface (see ARCH-015). TypeQuestion is the ONLY
// publicid.Type excluded from the set below; every other defined type,
// including TypeExamCategory, is used.
//
// Student ids reuse UserID (not a separate type): a student is a row in
// app.system_users, so student id tokens must decode as TypeUser (ARCH-005).

// UserID identifies a row in app.system_users (also used for student ids).
type UserID int

func (v UserID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeUser, int(v)))
}

func (v *UserID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeUser, s)
	if err != nil {
		return err
	}
	*v = UserID(id)
	return nil
}

// ExamID identifies a row in app.examinations.
type ExamID int

func (v ExamID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeExam, int(v)))
}

func (v *ExamID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeExam, s)
	if err != nil {
		return err
	}
	*v = ExamID(id)
	return nil
}

// CategoryID identifies a row in app.practice_categories.
type CategoryID int

func (v CategoryID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeCategory, int(v)))
}

func (v *CategoryID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeCategory, s)
	if err != nil {
		return err
	}
	*v = CategoryID(id)
	return nil
}

// ExamCategoryID identifies a row in app.exam_categories.
type ExamCategoryID int

func (v ExamCategoryID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeExamCategory, int(v)))
}

func (v *ExamCategoryID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeExamCategory, s)
	if err != nil {
		return err
	}
	*v = ExamCategoryID(id)
	return nil
}

// PracticeQuestionID identifies a row in app.practice_questions.
type PracticeQuestionID int

func (v PracticeQuestionID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypePractice, int(v)))
}

func (v *PracticeQuestionID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypePractice, s)
	if err != nil {
		return err
	}
	*v = PracticeQuestionID(id)
	return nil
}

// ImageID identifies a row in app.intro_images.
type ImageID int

func (v ImageID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeImage, int(v)))
}

func (v *ImageID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeImage, s)
	if err != nil {
		return err
	}
	*v = ImageID(id)
	return nil
}

// SubmissionID identifies a row in app.submissions.
type SubmissionID int

func (v SubmissionID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeSubmission, int(v)))
}

func (v *SubmissionID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeSubmission, s)
	if err != nil {
		return err
	}
	*v = SubmissionID(id)
	return nil
}

// AttemptID identifies a row in app.practice_attempts.
type AttemptID int

func (v AttemptID) MarshalJSON() ([]byte, error) {
	return json.Marshal(publicid.Encode(publicid.TypeAttempt, int(v)))
}

func (v *AttemptID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, err := publicid.Decode(publicid.TypeAttempt, s)
	if err != nil {
		return err
	}
	*v = AttemptID(id)
	return nil
}
