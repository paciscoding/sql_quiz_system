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
