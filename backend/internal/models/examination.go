package models

import "time"

type Examination struct {
	ID               int        `json:"id"`
	Title            string     `json:"title"`
	IsActive         bool       `json:"is_active"`
	CreatedBy        int        `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
	ScheduledStartAt *time.Time `json:"scheduled_start_at,omitempty"`
	ScheduledEndAt   *time.Time `json:"scheduled_end_at,omitempty"`
	// BlockPracticeDuringExam, when true, freezes student practice while this
	// exam is active (the optional "focus" rule). Default false = practice stays
	// live during exams, since practice categories own schemas separate from the
	// exam curriculum.
	BlockPracticeDuringExam bool `json:"block_practice_during_exam"`
	// Introduction is a lecturer-authored free-text description of the exam
	// curriculum, shown to enrolled students on the mandatory intro page.
	Introduction string `json:"introduction"`
}

type ExamStudentRequest struct {
	StudentIDs []int `json:"student_ids"`
}

// RosterStudent represents a student enrolled in an exam, as returned by GetExamRoster.
type RosterStudent struct {
	StudentID     int       `json:"student_id"`
	Username      string    `json:"username"`
	StudentNumber string    `json:"student_number"`
	FirstName     string    `json:"first_name"`
	LastName      string    `json:"last_name"`
	SecLec        string    `json:"sec_lec"`
	SecLab        string    `json:"sec_lab"`
	AssignedAt    time.Time `json:"assigned_at"`
}
