package models

// Exclusion guards (ARCH-006/ARCH-015/ARCH-013): exam-question ids
// (Question.ID, Submission.QuestionID, StudentExamResult.QuestionID,
// SubmissionRequest.QuestionNumber) intentionally stay plain, numeric int in
// JSON — they are the student/lecturer-visible ordinal (Q1, Q2, ...), not an
// enumeration-sensitive PK. Meanwhile the genuinely-encoded typed ids on the
// SAME structs (Submission.ID/ExamID/StudentID, Question.ExamID/CreatedBy)
// must be opaque strings. These tests assert both halves of that boundary so
// a future accidental encode of question_number — or a future accidental
// non-encode of a typed id — fails loudly.

import (
	"encoding/json"
	"testing"
)

// jsonNumberField unmarshals raw into a generic map and asserts field is a
// JSON number (float64 after generic unmarshal), returning it as a string
// error message on failure.
func jsonNumberField(t *testing.T, raw []byte, field string) {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := m[field]
	if !ok {
		t.Fatalf("field %q missing from JSON: %s", field, raw)
	}
	if _, isNumber := v.(float64); !isNumber {
		t.Fatalf("field %q must be a JSON number, got %T (%v) in %s", field, v, v, raw)
	}
}

func jsonStringField(t *testing.T, raw []byte, field string) {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := m[field]
	if !ok {
		t.Fatalf("field %q missing from JSON: %s", field, raw)
	}
	if _, isString := v.(string); !isString {
		t.Fatalf("field %q must be an opaque JSON string (encoded token), got %T (%v) in %s", field, v, v, raw)
	}
}

func TestQuestion_IDStaysNumeric_ButFKsAreEncoded(t *testing.T) {
	examID := ExamID(7)
	q := Question{
		ID:        123, // exam-question global PK — must stay a bare number
		ExamID:    &examID,
		CreatedBy: UserID(9),
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Exclusion: question id stays numeric.
	jsonNumberField(t, raw, "id")

	// Non-exclusion: the FKs on the same struct ARE encoded.
	jsonStringField(t, raw, "exam_id")
	jsonStringField(t, raw, "created_by")
}

func TestSubmission_QuestionIDStaysNumeric_ButOtherIDsAreEncoded(t *testing.T) {
	s := Submission{
		ID:         SubmissionID(1),
		ExamID:     ExamID(2),
		StudentID:  UserID(3),
		QuestionID: 456, // exam-question global PK — must stay a bare number
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	jsonNumberField(t, raw, "question_id")

	jsonStringField(t, raw, "id")
	jsonStringField(t, raw, "exam_id")
	jsonStringField(t, raw, "student_id")
}

func TestStudentExamResult_QuestionIDStaysNumeric(t *testing.T) {
	r := StudentExamResult{QuestionID: 789}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonNumberField(t, raw, "question_id")
}

func TestSubmissionRequest_QuestionNumberStaysNumeric(t *testing.T) {
	// Happy path: a normal, valid question number.
	req := SubmissionRequest{QuestionNumber: 3, SQLText: "SELECT 1"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonNumberField(t, raw, "question_number")

	// Edge case: zero must still round-trip as the number 0, not omitted or
	// stringified.
	zero := SubmissionRequest{QuestionNumber: 0, SQLText: "SELECT 1"}
	rawZero, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	jsonNumberField(t, rawZero, "question_number")

	// Failure case implied by done_when: unmarshaling a STRING into
	// question_number must fail (it is not a typed/opaque id, so a token
	// string is a type error, not a silently-accepted value).
	var back SubmissionRequest
	badJSON := []byte(`{"question_number":"not-a-number","sql_text":"SELECT 1"}`)
	if err := json.Unmarshal(badJSON, &back); err == nil {
		t.Fatalf("expected an error unmarshaling a string into the numeric question_number field")
	}
}

func TestIntroImage_OwnerIDStaysNumeric_ButIDIsEncoded(t *testing.T) {
	img := IntroImage{ID: ImageID(5), OwnerID: 99}
	raw, err := json.Marshal(img)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStringField(t, raw, "id")
	jsonNumberField(t, raw, "owner_id")
}
