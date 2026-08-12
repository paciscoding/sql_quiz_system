import { ApiError } from './practice'

// Same auth-header/fetch/base-url convention as services/exam.ts.
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

// ExamAnnouncement is the shape returned by GET /api/v1/quiz/exams/:id/announcements.
// Only rows sent since the exam's current activation are returned by the
// backend — announcements are effectively cleared whenever the exam is
// re-activated (no deletion happens server-side).
export interface ExamAnnouncement {
  id: number
  lecturer_name: string
  message: string
  sent_at: string
}

// listExamAnnouncements fetches the current student-visible announcements for
// an exam, newest first. The caller must be enrolled in the exam and the exam
// must be effectively active; the backend 403/404s otherwise.
export const listExamAnnouncements = (examId: string): Promise<ExamAnnouncement[]> =>
  apiFetch(`/api/v1/quiz/exams/${examId}/announcements`)
