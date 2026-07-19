package models

type ConsolidatedScore struct {
	StudentID     UserID             `json:"student_id"`
	Username      string             `json:"username"`
	StudentNumber string             `json:"student_number"`
	FirstName     string             `json:"first_name"`
	LastName      string             `json:"last_name"`
	SecLec        string             `json:"sec_lec"`
	SecLab        string             `json:"sec_lab"`
	ExamScores    map[string]float64 `json:"exam_scores"` // Map of exam title to score
	PracticeTotal float64            `json:"practice_total"`
	GrandTotal    float64            `json:"grand_total"`
}

// LecturerPracticeResultRow is a single per-student per-question practice
// result row for the lecturer practice-results breakdown (GET
// /api/v1/lecturer/practice/results). Only student/question pairs with at
// least one attempt are represented; this shape is frozen per ARCH-006.
type LecturerPracticeResultRow struct {
	StudentID     UserID             `json:"student_id"`
	StudentNumber string             `json:"student_number"`
	Username      string             `json:"username"`
	FirstName     string             `json:"first_name"`
	LastName      string             `json:"last_name"`
	SecLec        string             `json:"sec_lec"`
	SecLab        string             `json:"sec_lab"`
	QuestionID    PracticeQuestionID `json:"question_id"`
	CategoryID    CategoryID         `json:"category_id"`
	CategoryName  string             `json:"category_name"`
	QuestionTitle string             `json:"question_title"`
	Requirement   string             `json:"requirement"`
	Points        float64            `json:"points"`
	IsSolved      bool               `json:"is_solved"`
	AttemptCount  int                `json:"attempt_count"`
	EarnedPoints  float64            `json:"earned_points"`
}
