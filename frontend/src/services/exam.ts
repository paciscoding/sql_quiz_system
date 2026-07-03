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
  exam_id: number
  title: string
  introduction: string
  tables: TableIntrospection[]
}

// getExamIntroduction fetches the curriculum schema + description for an exam.
// Accessible to any enrolled student regardless of whether the exam is active.
export const getExamIntroduction = (examId: number): Promise<ExamIntroduction> =>
  apiFetch(`/api/v1/quiz/exams/${examId}/introduction`)

// setExamIntroduction PUTs a lecturer-authored description to the exam.
export const setExamIntroduction = (examId: number, text: string): Promise<void> =>
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
export const importExamCurriculum = (examId: number, sql: string, confirmReplace = false): Promise<{ message: string }> =>
  apiFetch(`/api/v1/lecturer/exams/${examId}/curriculum/import${confirmReplace ? '?confirm_replace=true' : ''}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sql })
  })
