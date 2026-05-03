package models

type SubmissionRequest struct {
	StudentID      string `json:"student_id"`
	QuestionNumber int    `json:"question_number"`
	SQLText        string `json:"sql_text"`
}

type ValidationResult struct {
	Status    string  `json:"status"`
	Score     float64 `json:"score"`
	Feedback  string  `json:"feedback"`
	IsCorrect bool    `json:"is_correct"`
}

type StrictMode struct {
	Ordering    bool `json:"ordering"`
	Attributes  bool `json:"attributes"`
	RecordOrder bool `json:"record_order"`
}

type Question struct {
	ID             int        `json:"id"`
	ExamID         *int       `json:"exam_id,omitempty"`
	TargetTable    string     `json:"target_table"`
	CorrectSQL     string     `json:"correct_sql"`
	Type           string     `json:"type"` // "QUERY" or "DML"
	Points         float64    `json:"points"`
	StrictMode     StrictMode `json:"strict_mode"`
}
