// Typed client for the admin/lecturer Submission Log (read-only browser over
// app.submissions / app.practice_attempts). Same auth-header/base-url/error
// convention as services/exam.ts: bearer token from localStorage, JSON body,
// ApiError on non-2xx.
import { ApiError } from './practice'

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

export type SubmissionLogSource = 'exam' | 'practice'
export type SubmissionLogOutcome = 'all' | 'correct' | 'incorrect'

export interface SubmissionLogFilters {
  source: SubmissionLogSource
  examId?: string
  studentId?: string
  questionId?: number
  outcome?: SubmissionLogOutcome
  countedOnly?: boolean
  from?: string
  to?: string
  limit?: number
  offset?: number
}

// Exam-mode list/detail row. attempt_no/is_counted are computed window
// functions, never persisted — see SubmissionLog.vue for the ordering
// contract they must mirror (postgres_results.go).
export interface ExamSubmissionLogRow {
  id: string
  exam_id: string
  exam_title: string
  student_id: string
  username: string
  student_number: string
  question_id: number
  question_display_no: number
  requirement: string
  question_type: string
  points: number
  attempt_no: number
  is_counted: boolean
  is_correct: boolean
  score: number
  submitted_at: string
  sql_preview: string
}

// Practice-mode list/detail row. Practice has no score column, so
// is_counted there is derived from is_correct DESC, submitted_at DESC.
export interface PracticeSubmissionLogRow {
  id: string
  student_id: string
  username: string
  student_number: string
  practice_question_id: string
  category_name: string
  requirement: string
  attempt_no: number
  is_counted: boolean
  is_correct: boolean
  submitted_at: string
  sql_preview: string
}

export type SubmissionLogRow = ExamSubmissionLogRow | PracticeSubmissionLogRow

export interface SubmissionLogDetail extends Record<string, any> {
  sql_text: string
  validation_details: any
}

export interface SubmissionLogListResponse {
  rows: SubmissionLogRow[]
  total: number
  limit: number
  offset: number
}

function buildQuery(filters: SubmissionLogFilters): string {
  const params = new URLSearchParams()
  params.set('source', filters.source)
  if (filters.examId) params.set('exam_id', filters.examId)
  if (filters.studentId) params.set('student_id', filters.studentId)
  if (filters.questionId !== undefined && filters.questionId !== null) params.set('question_id', String(filters.questionId))
  if (filters.outcome && filters.outcome !== 'all') params.set('outcome', filters.outcome)
  if (filters.countedOnly) params.set('counted_only', 'true')
  if (filters.from) params.set('from', filters.from)
  if (filters.to) params.set('to', filters.to)
  return params.toString()
}

// listSubmissionLog (GET /lecturer/submission-log). Auth: ADMIN | LECTURER.
export const listSubmissionLog = (filters: SubmissionLogFilters): Promise<SubmissionLogListResponse> => {
  const params = buildQuery(filters)
  const extra = new URLSearchParams()
  extra.set('limit', String(filters.limit ?? 50))
  extra.set('offset', String(filters.offset ?? 0))
  return apiFetch(`/api/v1/lecturer/submission-log?${params}&${extra.toString()}`)
}

// getSubmissionLogDetail (GET /lecturer/submission-log/:submissionId). Auth: ADMIN | LECTURER.
export const getSubmissionLogDetail = (submissionId: string, source: SubmissionLogSource): Promise<SubmissionLogDetail> =>
  apiFetch(`/api/v1/lecturer/submission-log/${submissionId}?source=${source}`)

// downloadSubmissionLogCsv streams GET /lecturer/submission-log.csv (same
// filters as the list, minus limit/offset) and triggers a real browser file
// download. Uses fetch -> blob -> object URL -> revoke (not window.open,
// which cannot send the Authorization header). Filename is taken from the
// response's Content-Disposition header when present, else falls back to a
// source-qualified default name.
export async function downloadSubmissionLogCsv(filters: Omit<SubmissionLogFilters, 'limit' | 'offset'>): Promise<void> {
  const params = buildQuery(filters)
  const token = localStorage.getItem('sql_quiz_token')
  const headers: Record<string, string> = {}
  if (token) headers['Authorization'] = `Bearer ${token}`

  const response = await fetch(`/api/v1/lecturer/submission-log.csv?${params}`, { headers })
  if (!response.ok) {
    let body: any = {}
    try { body = await response.json() } catch { /* ignore */ }
    throw new ApiError(response.status, body.error || `HTTP ${response.status}`)
  }

  let filename = `submission-log-${filters.source}.csv`
  const disposition = response.headers.get('Content-Disposition')
  if (disposition) {
    const match = /filename="?([^";]+)"?/.exec(disposition)
    if (match && match[1]) filename = match[1]
  }

  const blob = await response.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
