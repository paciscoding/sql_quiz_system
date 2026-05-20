package models

type ConsolidatedScore struct {
	StudentID     int     `json:"student_id"`
	Username      string  `json:"username"`
	StudentNumber string  `json:"student_number"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	SecLec        string  `json:"sec_lec"`
	SecLab        string  `json:"sec_lab"`
	ExamScores    map[string]float64 `json:"exam_scores"` // Map of exam title to score
	PracticeTotal float64 `json:"practice_total"`
	GrandTotal    float64 `json:"grand_total"`
}
