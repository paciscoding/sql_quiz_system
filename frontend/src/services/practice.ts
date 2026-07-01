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
  id: number
  name: string
  schema_name: string
  created_by: number
  question_count: number
  created_at: string
  updated_at: string
  introduction: string
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
  category_id: number
  category_name: string
  introduction: string
  imported: boolean
  tables: TableIntrospection[]
}

export interface PracticeQuestion {
  id: number
  created_by: number
  category_id: number
  category_name?: string
  category?: string
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
}

export interface PracticeAttempt {
  id: number
  student_id: number
  practice_question_id: number
  sql_text: string
  is_correct: boolean
  validation_details?: any
  submitted_at: string
}

export interface PracticeSummary {
  question_id: number
  category_id: number
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
    ...(options.headers as Record<string, string> || {}),
    'Authorization': `Bearer ${token}`
  }
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

export const getQuestion = (id: number): Promise<PracticeQuestion> =>
  apiFetch(`/api/v1/practice/questions/${id}`)

export const submit = (practiceQuestionId: number, sqlText: string): Promise<ValidationResult> =>
  apiFetch('/api/v1/practice/submit', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ practice_question_id: practiceQuestionId, sql_text: sqlText })
  })

export const preview = (practiceQuestionId: number, sqlText: string): Promise<PreviewResult> =>
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

export const upsertQuestion = (payload: Partial<PracticeQuestion>): Promise<any> =>
  apiFetch('/api/v1/lecturer/practice/question', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  })

export const deleteQuestion = (id: number): Promise<void> =>
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

export const renameCategory = (id: number, name: string): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })

export const deleteCategory = (id: number, force = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}${force ? '?force=true' : ''}`, { method: 'DELETE' })

export const importCategorySchema = (id: number, sql: string, confirmReplace = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}/import${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

export const lecturerPreview = (payload: { type: string; target_table: string; correct_sql: string }): Promise<any> =>
  apiFetch('/api/v1/lecturer/practice/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  })

// --- Practice category introduction ---

export const getCategoryIntroduction = (id: number): Promise<CategoryIntroduction> =>
  apiFetch(`/api/v1/practice/categories/${id}/introduction`)

export const setCategoryIntroduction = (id: number, text: string): Promise<void> =>
  apiFetch(`/api/v1/lecturer/practice/categories/${id}/introduction`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ introduction: text })
  })
