export interface Examination {
  id: string
  title: string
  is_active: boolean
  effective_state: string
  scheduled_start_at: string | null
  scheduled_end_at: string | null
  activated_at: string | null
  archived_at: string | null
  introduction: string
  question_count?: number
  total_points?: number
  uncaptured_count?: number
  block_practice_during_exam: boolean
  has_preview_schema?: boolean
}

export interface Student {
  id: string
  username: string
}

export interface RosterStudent {
  student_id: string
  username: string
  student_number: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  assigned_at: string
}

export interface CaptureStatus {
  status: 'captured' | 'failed' | 'pending' | ''
  error: string
  capturedAt: string | null
}

export interface PreviewResult {
  status: 'ok' | 'error' | ''
  rows: Record<string, any>[]
  columns: string[]
  error: string
}

// AutoSeedColumn is one column/value pair the backend derived to satisfy a
// DML question's WHERE clause when the row does not exist in the exam
// curriculum database. Rendered read-only in ExamQuestionsPanel.vue's
// "Auto-seeded row" panel; never sent back to the backend.
export interface AutoSeedColumn {
  name: string
  value: string
}

// AutoSeedPlan describes a row the backend invents (inside a rolled-back
// transaction) so a DML question's answer has something to act on. Display
// only -- see ExamQuestionsPanel.vue's "Auto-seeded row" panel.
export interface AutoSeedPlan {
  table: string
  columns: AutoSeedColumn[]
}

// ExamQuestion mirrors the backend's models.Question JSON shape (see
// backend/internal/models/submission.go), i.e. the objects returned by
// GET /api/v1/lecturer/exams/:id/questions and consumed by
// ExamQuestionsPanel.vue.
export interface ExamQuestion {
  id: number
  exam_id?: string
  category_id?: string | null
  category_name?: string
  target_table: string
  correct_sql?: string
  type: string
  points: number
  requirement: string
  strict_mode?: { ordering: boolean; attributes: boolean; column_order: boolean }
  dual_schema?: boolean
  expected_result?: any
  expected_columns?: any
  expected_result_status?: string
  expected_result_captured_at?: string | null
  capture_error?: string
  created_by?: string
  created_at?: string
  updated_at?: string
  auto_seed?: AutoSeedPlan | null
  // DML statement kind the lecturer selected: 'INSERT' | 'UPDATE' | 'DELETE'.
  // Absent for QUERY questions and for DML questions authored before the
  // column existed. Presentation and validation only.
  dml_kind?: string
  // Lecturer-authored INSERT-only seed block, run inside a rolled-back
  // transaction so an UPDATE/DELETE question has a row to act on. Never
  // present in a student-facing response.
  seed_sql?: string
}
