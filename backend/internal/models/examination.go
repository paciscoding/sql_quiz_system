package models

import "time"

type Examination struct {
	ID               ExamID     `json:"id"`
	Title            string     `json:"title"`
	IsActive         bool       `json:"is_active"`
	CreatedBy        UserID     `json:"created_by"`
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
	// CurriculumSchema is the PostgreSQL schema that holds this exam's curriculum
	// tables. Defaults to 'public'; updated to curriculum_exam_{id} when a
	// lecturer imports a per-exam curriculum via ImportSchemaIntoExamCurriculum.
	CurriculumSchema string `json:"curriculum_schema"`
	// PreviewCurriculumSchema is the decoy curriculum schema (dual-schema
	// grading) imported for this exam's student previews; empty when no decoy
	// has been imported. Mirrors CurriculumSchema.
	PreviewCurriculumSchema string `json:"preview_curriculum_schema"`
	// HasPreviewSchema is a derived convenience flag: PreviewCurriculumSchema
	// != "". Populated by the lecturer list read path; false when no decoy
	// curriculum has been imported.
	HasPreviewSchema bool `json:"has_preview_schema"`
	// Readiness counts — populated only by the lecturer list path.
	QuestionCount   int     `json:"question_count"`
	TotalPoints     float64 `json:"total_points"`
	UncapturedCount int     `json:"uncaptured_count"`
}

type ExamStudentRequest struct {
	StudentIDs []UserID `json:"student_ids"`
}

// RosterStudent represents a student enrolled in an exam, as returned by GetExamRoster.
type RosterStudent struct {
	StudentID     UserID    `json:"student_id"`
	Username      string    `json:"username"`
	StudentNumber string    `json:"student_number"`
	FirstName     string    `json:"first_name"`
	LastName      string    `json:"last_name"`
	SecLec        string    `json:"sec_lec"`
	SecLab        string    `json:"sec_lab"`
	AssignedAt    time.Time `json:"assigned_at"`
}
