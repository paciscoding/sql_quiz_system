package models

import "time"

// ExamAnnouncement is a lecturer-authored message broadcast to every student
// enrolled in an examination. Announcements are never edited or deleted on
// exam re-activation — instead, the student-facing read path filters rows to
// sent_at >= examinations.activated_at, so re-activating an exam effectively
// clears the announcement feed for students without touching any row here.
type ExamAnnouncement struct {
	ID           int       `json:"id"`
	ExamID       ExamID    `json:"exam_id"`
	LecturerName string    `json:"lecturer_name"`
	Message      string    `json:"message"`
	SentAt       time.Time `json:"sent_at"`
}

// StudentExamAnnouncement is the student-facing projection of ExamAnnouncement:
// identical fields minus exam_id, which is redundant on a per-exam list endpoint.
type StudentExamAnnouncement struct {
	ID           int       `json:"id"`
	LecturerName string    `json:"lecturer_name"`
	Message      string    `json:"message"`
	SentAt       time.Time `json:"sent_at"`
}
