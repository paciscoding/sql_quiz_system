package models

import "encoding/json"

// LecturerQuestionView is the ONLY serialization of a Question that carries
// auto_seed and seed_sql. ARCH-053 hides both on the model itself, so a
// response can expose them only by deliberately going through this type.
//
// The embedded Question's own AutoSeed/SeedSQL fields are tagged `json:"-"`
// and so are skipped entirely by encoding/json; the tagged fields below are
// the only ones it sees. Nothing student-facing may use this type.
type LecturerQuestionView struct {
	Question
	AutoSeed json.RawMessage `json:"auto_seed,omitempty"`
	SeedSQL  string          `json:"seed_sql,omitempty"`
}

// AsLecturerQuestionView lifts q into its lecturer-facing serialization.
func AsLecturerQuestionView(q Question) LecturerQuestionView {
	return LecturerQuestionView{Question: q, AutoSeed: q.AutoSeed, SeedSQL: q.SeedSQL}
}

// AsLecturerQuestionViews maps AsLecturerQuestionView over qs. It always
// returns a non-nil slice so a handler emits [] rather than null for an exam
// with no questions.
func AsLecturerQuestionViews(qs []Question) []LecturerQuestionView {
	out := make([]LecturerQuestionView, 0, len(qs))
	for _, q := range qs {
		out = append(out, AsLecturerQuestionView(q))
	}
	return out
}

// LecturerPracticeQuestionView is the PracticeQuestion twin of
// LecturerQuestionView. See that type for the rationale.
type LecturerPracticeQuestionView struct {
	PracticeQuestion
	AutoSeed json.RawMessage `json:"auto_seed,omitempty"`
	SeedSQL  string          `json:"seed_sql,omitempty"`
}

// AsLecturerPracticeQuestionView lifts pq into its lecturer-facing form.
func AsLecturerPracticeQuestionView(pq PracticeQuestion) LecturerPracticeQuestionView {
	return LecturerPracticeQuestionView{PracticeQuestion: pq, AutoSeed: pq.AutoSeed, SeedSQL: pq.SeedSQL}
}

// AsLecturerPracticeQuestionViews maps AsLecturerPracticeQuestionView over pqs,
// always returning a non-nil slice.
func AsLecturerPracticeQuestionViews(pqs []PracticeQuestion) []LecturerPracticeQuestionView {
	out := make([]LecturerPracticeQuestionView, 0, len(pqs))
	for _, pq := range pqs {
		out = append(out, AsLecturerPracticeQuestionView(pq))
	}
	return out
}
