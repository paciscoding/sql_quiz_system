import { ApiError } from './practice'
import type { TableIntrospection } from './practice'

async function apiFetch(path: string, options: RequestInit = {}): Promise<any> {
  const token = localStorage.getItem('sql_quiz_token')
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string> || {})
  }
  if (token) headers['Authorization'] = `Bearer ${token}`
  const response = await fetch(path, { ...options, headers })
  if (!response.ok) {
    let body: any = {}
    try { body = await response.json() } catch { /* ignore */ }
    throw new ApiError(response.status, body.error || `HTTP ${response.status}`)
  }
  return response.json()
}

// ExamIntroduction is the shape returned by GET /api/v1/quiz/exams/:id/introduction
export interface ExamIntroduction {
  exam_id: string
  title: string
  introduction: string
  tables: TableIntrospection[]
}

// getExamIntroduction fetches the curriculum schema + description for an exam.
// Accessible to any enrolled student regardless of whether the exam is active.
export const getExamIntroduction = (examId: string): Promise<ExamIntroduction> =>
  apiFetch(`/api/v1/quiz/exams/${examId}/introduction`)

// setExamIntroduction PUTs a lecturer-authored description to the exam.
export const setExamIntroduction = (examId: string, text: string): Promise<void> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/introduction`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ introduction: text })
  })

// importExamCurriculum POSTs a SQL script into the per-exam curriculum schema
// (curriculum_exam_{examId}). Pass confirmReplace=true to overwrite a non-empty schema.
// Auth: ADMIN | LECTURER.
// 409 if the exam is currently active, or if the schema already has tables and
// confirmReplace is false. 404 if the exam does not exist.
export const importExamCurriculum = (examId: string, sql: string, confirmReplace = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/curriculum/import${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

// importExamCurriculumPreview POSTs a SQL script into the per-exam decoy curriculum
// schema (curriculum_exam_{examId}_preview), used for the dual-schema student preview.
// Pass confirmReplace=true to overwrite a non-empty schema.
// Auth: ADMIN | LECTURER.
// 409 if the exam is currently active, or if the schema already has tables and
// confirmReplace is false. 404 if the exam does not exist.
export const importExamCurriculumPreview = (examId: string, sql: string, confirmReplace = false): Promise<{ message: string; parity_warning?: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/curriculum/import-preview${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

// --- Exam categories (ARCH-029) ---

// ExamCategory mirrors the backend's models.ExamCategory. schema_name /
// preview_schema_name are exposed here (lecturer view) but are NEVER present
// in the student-facing GET /quiz/exams/:id/categories endpoint's response.
export interface ExamCategory {
  id: string
  exam_id: string
  name: string
  schema_name: string
  preview_schema_name: string
  has_preview_schema: boolean
  owns_schema: boolean
  introduction: string
  question_count: number
}

// listExamCategories (GET /lecturer/exams/:id/categories). Auth: ADMIN | LECTURER.
export const listExamCategories = (examId: string): Promise<ExamCategory[]> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories`)

// createExamCategory (POST /lecturer/exams/:id/categories). Auth: ADMIN | LECTURER.
export const createExamCategory = (examId: string, name: string): Promise<ExamCategory> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })

// renameExamCategory (PUT /lecturer/exams/:id/categories/:categoryId). Auth: ADMIN | LECTURER.
export const renameExamCategory = (examId: string, categoryId: string, name: string): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories/${categoryId}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name })
  })

// deleteExamCategory (DELETE /lecturer/exams/:id/categories/:categoryId). Auth: ADMIN | LECTURER.
// Per ARCH-036, this route accepts NO force/detach parameter: it 409s with the
// attached-question count whenever any question still references the category.
export const deleteExamCategory = (examId: string, categoryId: string): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories/${categoryId}`, { method: 'DELETE' })

// importExamCategorySchema (POST /lecturer/exams/:id/categories/:categoryId/import). Auth: ADMIN | LECTURER.
// Pass confirmReplace=true to overwrite a non-empty category schema.
export const importExamCategorySchema = (examId: string, categoryId: string, sql: string, confirmReplace = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories/${categoryId}/import${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

// importExamCategoryPreviewSchema (POST /lecturer/exams/:id/categories/:categoryId/import-preview). Auth: ADMIN | LECTURER.
// Pass confirmReplace=true to overwrite a non-empty decoy schema.
export const importExamCategoryPreviewSchema = (examId: string, categoryId: string, sql: string, confirmReplace = false): Promise<{ message: string; parity_warning?: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories/${categoryId}/import-preview${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })

// setExamCategoryIntroduction (PUT /lecturer/exams/:id/categories/:categoryId/introduction). Auth: ADMIN | LECTURER.
export const setExamCategoryIntroduction = (examId: string, categoryId: string, text: string): Promise<void> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/categories/${categoryId}/introduction`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ introduction: text })
  })
