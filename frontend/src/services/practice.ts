import { useNotifications } from './notification_service'

export class PracticeUnavailableError extends Error {
  constructor() {
    super('Practice unavailable while an exam is active.')
    this.name = 'PracticeUnavailableError'
  }
}

// ApiError carries the HTTP status so callers can branch on it (e.g. 409 to
// surface a "confirm replace / force delete" dialog).
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export interface PracticeCategory {
  id: string
  name: string
  schema_name: string
  created_by: string
  question_count: number
  created_at: string
  updated_at: string
  introduction: string
  preview_schema_name?: string
  has_preview_schema?: boolean
}

// Schema introspection types (shared between practice intro and exam intro)
export interface ColumnIntrospection {
  name: string
  data_type: string
  nullable: boolean
  is_primary_key: boolean
  is_foreign_key: boolean
  references_table?: string
  references_column?: string
}

export interface TableIntrospection {
  name: string
  columns: ColumnIntrospection[]
}

export interface CategoryIntroduction {
  category_id: string
  category_name: string
  introduction: string
  imported: boolean
  tables: TableIntrospection[]
}

// AutoSeedColumn is one column/value pair the backend derived to satisfy a
// DML question's WHERE clause when the row does not exist in the category
// database. Rendered read-only in the authoring UI; never sent back to the
// backend.
export interface AutoSeedColumn {
  name: string
  value: string
}

// AutoSeedPlan describes a row the backend invents (inside a rolled-back
// transaction) so a DML question's answer has something to act on. Display
// only -- see PracticeAuthoring.vue's "Auto-seeded row" panel.
export interface AutoSeedPlan {
  table: string
  columns: AutoSeedColumn[]
}

export interface PracticeQuestion {
  id: string
  created_by: string
  category_id: string
  category_name?: string
  category?: string
  title?: string
  target_table: string
  correct_sql?: string
  question_type: string
  requirement: string
  points: number
  strict_ordering: boolean
  strict_attrs: boolean
  strict_column_order: boolean
  expected_result?: any
  expected_columns?: any
  expected_result_status: string
  capture_error?: string
  expected_result_captured_at?: string
  created_at: string
  updated_at: string
  dual_schema?: boolean
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

export interface PracticeAttempt {
  id: string
  student_id: string
  practice_question_id: string
  sql_text: string
  is_correct: boolean
  validation_details?: any
  submitted_at: string
}

export interface PracticeSummary {
  question_id: string
  category_id: string
  category_name: string
  category: string
  requirement: string
  points: number
  is_solved: boolean
  attempt_count: number
}

export interface ValidationResult {
  status: string
  score: number
  feedback: string
  is_correct: boolean
  validation_details?: any
}

export interface PreviewResult {
  status: string
  feedback: string
  rows: Record<string, any>[]
  columns: string[]
  row_count: number
  truncated: boolean
}

async function apiFetch(path: string, options: RequestInit = {}): Promise<any> {
  const token = localStorage.getItem('sql_quiz_token')
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string> || {})
  }
  if (token) headers['Authorization'] = `Bearer ${token}`
  const response = await fetch(path, { ...options, headers })
  if (response.status === 423) {
    const { notify } = useNotifications()
    notify('Practice is paused while an exam with focus mode is active.', 'error')
    throw new PracticeUnavailableError()
  }
  if (!response.ok) {
    let body: any = {}
    try { body = await response.json() } catch { /* ignore */ }
    throw new ApiError(response.status, body.error || 'Request failed')
  }
  return response.json()
}

export const listQuestions = (): Promise<PracticeQuestion[]> =>
  apiFetch('/api/v1/practice/questions')

export const getQuestion = (id: string): Promise<PracticeQuestion> =>
  apiFetch(`/api/v1/practice/questions/${id}`)

export const submit = (practiceQuestionId: string, sqlText: string): Promise<ValidationResult> =>
  apiFetch('/api/v1/practice/submit', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ practice_question_id: practiceQuestionId, sql_text: sqlText })
  })

export const preview = (practiceQuestionId: string, sqlText: string): Promise<PreviewResult> =>
  apiFetch('/api/v1/practice/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ practice_question_id: practiceQuestionId, sql_text: sqlText })
  })

export const listMyAttempts = (): Promise<PracticeAttempt[]> =>
  apiFetch('/api/v1/practice/attempts')

export const getSummary = (): Promise<PracticeSummary[]> =>
  apiFetch('/api/v1/practice/summary')

export const lecturerListQuestions = (): Promise<PracticeQuestion[]> =>
  apiFetch('/api/v1/lecturer/practice/questions')

export const upsertQuestion = (payload: Partial<PracticeQuestion>): Promise<{
  message: string
  id?: string
  expected_result_status?: string
  expected_result_captured_at?: string
  expected_rows_affected?: number
  capture_error?: string
  table_warning?: string
  dual_schema_warning?: string
  mixed_dual_warning?: string
}> =>
  apiFetch('/api/v1/lecturer/practice/question', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  })

export const deleteQuestion = (id: string): Promise<void> =>
  apiFetch(`/api/v1/lecturer/practice/question/${id}`, { method: 'DELETE' })

// --- Practice categories ---

export const listCategories = (): Promise<PracticeCategory[]> =>
  apiFetch('/api/v1/lecturer/practice/categories')

export const createCategory = (name: string): Promise<PracticeCategory> =>
  apiFetch('/api/v1/lecturer/practice/categories', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })

export const renameCategory = (id: string, name: string): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })

export const deleteCategory = (id: string, force = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}${force ? '?force=true' : ''}`, { method: 'DELETE' })

export const importCategorySchema = (id: string, sql: string, confirmReplace = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}/import${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

export const importCategoryPreviewSchema = (id: string, sql: string, confirmReplace = false): Promise<{ message: string; parity_warning?: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}/import-preview${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

export const lecturerPreview = (payload: { type: string; target_table: string; correct_sql: string; category_id?: string; schema?: 'grading' | 'preview' }): Promise<any> =>
  apiFetch('/api/v1/lecturer/practice/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  })

// --- Practice category introduction ---

export const getCategoryIntroduction = (id: string): Promise<CategoryIntroduction> =>
  apiFetch(`/api/v1/practice/categories/${id}/introduction`)

export const setCategoryIntroduction = (id: string, text: string): Promise<void> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}/introduction`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ introduction: text })
  })
