package models

import "time"

type Examination struct {
	ID          int        `json:"id"`
	Title       string     `json:"title"`
	IsActive    bool       `json:"is_active"`
	CreatedBy   int        `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

type ExamStudentRequest struct {
	StudentIDs []int `json:"student_ids"`
}
