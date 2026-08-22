import { toCsv } from './csv'
import { scoreClass } from './score_class'

/**
 * MatrixQuestion isolates a narrow projection of PracticeQuestion (id/title/requirement/points/categoryName)
 * so lecturer-authored answer-key fields (correct_sql, seed_sql, expected_result) can never reach
 * a rendered cell, tooltip, or CSV export.
 */
export interface MatrixQuestion {
  id: string
  title: string
  requirement: string
  points: number
  categoryName: string
}

export interface MatrixStudent {
  student_id: string
  student_number: string
  username: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  practice_total: number
}

export interface MatrixCell {
  earned: number
  solved: boolean
  attempts: number
}

export interface PracticeResultRow {
  student_id: string
  student_number: string
  username: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  question_id: string
  category_id: string
  category_name: string
  question_title: string
  requirement: string
  points: number
  is_solved: boolean
  attempt_count: number
  earned_points: number
}

/** Structural minimum toMatrixQuestions needs. The real PracticeQuestion from
 *  services/practice.ts satisfies this, so no import (and no drift) is required. */
export interface PracticeQuestionInput {
  id: string
  title?: string
  requirement: string
  points: number
  category_name?: string
  category?: string
}

/**
 * Projects full PracticeQuestion array into MatrixQuestion array.
 * Preserves the server's ascending-id order (never calls .sort()).
 */
export function toMatrixQuestions(qs: PracticeQuestionInput[]): MatrixQuestion[] {
  return qs.map(q => ({
    id: q.id,
    title: q.title ?? '',
    requirement: q.requirement ?? '',
    points: q.points ?? 0,
    categoryName: q.category_name || q.category || 'Uncategorized'
  }))
}

/**
 * Degraded fallback when question list fetch fails.
 * Derives MatrixQuestion columns from PracticeResultRow array in first-seen order without sorting.
 */
export function columnsFromResults(rows: PracticeResultRow[]): MatrixQuestion[] {
  const map = new Map<string, MatrixQuestion>()
  for (const row of rows) {
    if (!map.has(row.question_id)) {
      map.set(row.question_id, {
        id: row.question_id,
        title: row.question_title ?? '',
        requirement: row.requirement ?? '',
        points: row.points ?? 0,
        categoryName: row.category_name || 'Uncategorized'
      })
    }
  }
  return Array.from(map.values())
}

/**
 * Builds a two-level Map (studentId -> questionId -> MatrixCell).
 * Uses JavaScript Map objects at both levels to avoid prototype collision hazards and cleanly distinguish
 * absent cells from present zero-score cells.
 */
export function buildMatrix(rows: PracticeResultRow[]): Map<string, Map<string, MatrixCell>> {
  const matrix = new Map<string, Map<string, MatrixCell>>()
  for (const row of rows) {
    let studentMap = matrix.get(row.student_id)
    if (!studentMap) {
      studentMap = new Map<string, MatrixCell>()
      matrix.set(row.student_id, studentMap)
    }
    // Direct set: backend contract guarantees at most one result row per student-question pair.
    // A duplicate row appearing would be a backend contract change.
    studentMap.set(row.question_id, {
      earned: row.earned_points,
      solved: row.is_solved,
      attempts: row.attempt_count
    })
  }
  return matrix
}

/**
 * Returns students array in existing order, plus any missing students from result rows appended defensively.
 * Appended students have practice_total set to NaN (row total is always computed fresh from cells).
 */
export function buildRowAxis(students: MatrixStudent[], rows: PracticeResultRow[]): MatrixStudent[] {
  const seenStudentIds = new Set<string>()
  const result: MatrixStudent[] = []

  for (const s of students) {
    seenStudentIds.add(s.student_id)
    result.push(s)
  }

  for (const r of rows) {
    if (!seenStudentIds.has(r.student_id)) {
      seenStudentIds.add(r.student_id)
      result.push({
        student_id: r.student_id,
        student_number: r.student_number ?? '',
        username: r.username ?? '',
        first_name: r.first_name ?? '',
        last_name: r.last_name ?? '',
        sec_lec: r.sec_lec ?? '',
        sec_lab: r.sec_lab ?? '',
        practice_total: NaN
      })
    }
  }

  return result
}

// scoreClass lives in ./score_class so the exam matrix can share it without
// importing this module. Re-exported here so existing call sites keep working.
export { scoreClass } from './score_class'

// Branch on cell.solved, NEVER on earned >= max directly.
// A question worth 0 points makes earned === max === 0, which is indistinguishable
// by value from a genuinely-unattempted pending state and would fall into scoreClass's
// max <= 0 -> score-pending branch, incorrectly painting a correctly-solved zero-point question grey instead of green.
export function cellClass(cell: MatrixCell | undefined, max: number): string {
  if (cell === undefined) {
    return 'score-pending'
  }
  if (!cell.solved) {
    return 'score-zero'
  }
  const cls = scoreClass(cell.earned, max)
  if (cls === 'score-pending') {
    return 'score-full'
  }
  return cls
}

/**
 * Returns human-readable cell tooltip.
 * Uses middle dot separator and correct singular/plural for attempts.
 */
export function cellTooltip(cell: MatrixCell | undefined): string {
  if (cell === undefined) {
    return 'Not attempted'
  }
  const attemptsPhrase = cell.attempts === 1 ? '1 attempt' : `${cell.attempts} attempts`
  const solvedPhrase = cell.solved ? 'solved' : 'not solved'
  return `${attemptsPhrase} · ${solvedPhrase}`
}

/**
 * Formats displayed column label as Q1, Q2, etc. (1-indexed).
 */
export function columnLabel(_q: MatrixQuestion, index: number): string {
  return `Q${index + 1}`
}

/**
 * Formats multi-line column header tooltip with title, category, requirement, points.
 */
export function columnTooltip(q: MatrixQuestion, index: number): string {
  const parts: string[] = []
  const label = columnLabel(q, index)
  if (q.title && q.title.trim() !== '') {
    parts.push(`${label} — ${q.title}`)
  } else {
    parts.push(label)
  }
  if (q.categoryName) {
    parts.push(q.categoryName)
  }
  if (q.requirement && q.requirement.trim() !== '') {
    parts.push(q.requirement)
  }
  parts.push(`${q.points} pts`)
  return parts.join('\n')
}

/**
 * Formats column header for CSV export.
 */
export function csvColumnHeader(q: MatrixQuestion, index: number): string {
  const label = columnLabel(q, index)
  const maxPts = q.points.toFixed(2)
  if (q.title && q.title.trim() !== '') {
    return `${label} - ${q.title} [max ${maxPts}]`
  }
  return `${label} [max ${maxPts}]`
}

/**
 * Builds RFC-4180 compliant CSV string for practice quiz matrix.
 * Unattempted cells emit empty string '', numeric cells use toFixed(2).
 */
export function buildMatrixCsv(
  students: MatrixStudent[],
  questions: MatrixQuestion[],
  results: PracticeResultRow[]
): string {
  const matrix = buildMatrix(results)
  const rowAxis = buildRowAxis(students, results)

  const header = [
    'StudentNumber',
    'Username',
    'FirstName',
    'LastName',
    'SecLec',
    'SecLab',
    ...questions.map((q, i) => csvColumnHeader(q, i)),
    'Total',
    'MaxTotal'
  ]

  const maxTotal = questions.reduce((sum, q) => sum + (q.points || 0), 0)

  const dataRows: Array<Array<string | number>> = rowAxis.map(student => {
    const studentMatrix = matrix.get(student.student_id)
    let rowTotal = 0

    const questionCells = questions.map(q => {
      const cell = studentMatrix?.get(q.id)
      if (cell === undefined) {
        return ''
      }
      rowTotal += cell.earned
      return cell.earned.toFixed(2)
    })

    return [
      student.student_number ?? '',
      student.username ?? '',
      student.first_name ?? '',
      student.last_name ?? '',
      student.sec_lec ?? '',
      student.sec_lab ?? '',
      ...questionCells,
      rowTotal.toFixed(2),
      maxTotal.toFixed(2)
    ]
  })

  return toCsv(header, dataRows)
}
