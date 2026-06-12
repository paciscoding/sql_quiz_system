import type { TableIntrospection } from './practice'

// ExamIntroduction is the shape returned by GET /api/v1/quiz/exams/:id/introduction
export interface ExamIntroduction {
  exam_id: number
  title: string
  introduction: string
  tables: TableIntrospection[]
}

// getExamIntroduction fetches the curriculum schema + description for an exam.
// Accessible to any enrolled student regardless of whether the exam is active.
export async function getExamIntroduction(examId: number): Promise<ExamIntroduction> {
  const token = localStorage.getItem('sql_quiz_token')
  const res = await fetch(`/api/v1/quiz/exams/${examId}/introduction`, {
    headers: { 'Authorization': `Bearer ${token}` }
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `HTTP ${res.status}`)
  }
  return res.json()
}

// setExamIntroduction PUTs a lecturer-authored description to the exam.
export async function setExamIntroduction(examId: number, text: string): Promise<void> {
  const token = localStorage.getItem('sql_quiz_token')
  const res = await fetch(`/api/v1/lecturer/exams/${examId}/introduction`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    },
    body: JSON.stringify({ introduction: text })
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `HTTP ${res.status}`)
  }
}
