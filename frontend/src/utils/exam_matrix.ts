import { toCsv } from './csv'
import { scoreClass } from './score_class'

/**
 * ExamMatrixQuestion isolates a narrow projection of the lecturer exam-question payload
 * (id/displayNo/requirement/points) so lecturer-authored answer-key fields
 * (correct_sql, target_table, expected_result, expected_columns, auto_seed) can never reach
 * a rendered cell, tooltip, or CSV export.
 */
export interface ExamMatrixQuestion {
  id: number
  displayNo: number
  requirement: string
  points: number
}

export interface ExamMatrixStudent {
  student_id: string
  student_number: string
  username: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
}

export interface ExamMatrixCell {
  score: number
  correct: boolean
  attempts: number
}

/**
 * Structural minimum this module needs from GET /lecturer/exams/:id/submissions.
 * student_id is an opaque publicid token (never parsed as a number); question_id is a
 * readable int, as exam question ids deliberately stayed numeric.
 */
export interface ExamSubmissionRow {
  student_id: string
  question_id: number
  is_correct: boolean
  score: number
}

/** Structural minimum toExamMatrixQuestions needs from the exam-questions payload. */
export interface ExamQuestionInput {
  id: number
  display_no?: number
  requirement?: string
  points?: number
}

/**
 * Projects the full exam-question payload into ExamMatrixQuestion.
 *
 * This is the security boundary: GET /lecturer/exams/:id/questions returns the whole
 * models.Question, answer key included. Narrowing here is what keeps correct_sql,
 * target_table, expected_result, expected_columns and auto_seed out of every cell,
 * tooltip and CSV.
 *
 * Preserves the server's order (category grouping, then id — the same order display_no
 * is computed in), and never calls .sort().
 */
export function toExamMatrixQuestions(qs: ExamQuestionInput[]): ExamMatrixQuestion[] {
  return qs.map(q => ({
    id: q.id,
    displayNo: q.display_no ?? 0,
    requirement: q.requirement ?? '',
    points: q.points ?? 0
  }))
}

/**
 * Builds a two-level Map (studentId -> questionId -> ExamMatrixCell) holding each student's
 * BEST submission per question.
 *
 * This mirrors GetConsolidatedScores' BestSubmissions CTE
 * (DISTINCT ON (student_id, question_id) ORDER BY score DESC, submitted_at DESC), so a row
 * total here equals that student's cell for the same exam in the consolidated view. The
 * endpoint returns rows ORDER BY submitted_at DESC, so keeping the FIRST row seen at a given
 * score reproduces the SQL's submitted_at DESC tiebreak.
 *
 * Unlike the practice endpoint, this one returns every attempt, not one pre-aggregated row
 * per pair — attempts counts them all.
 */
export function buildExamMatrix(subs: ExamSubmissionRow[]): Map<string, Map<number, ExamMatrixCell>> {
  const matrix = new Map<string, Map<number, ExamMatrixCell>>()
  for (const sub of subs) {
    let studentMap = matrix.get(sub.student_id)
    if (!studentMap) {
      studentMap = new Map<number, ExamMatrixCell>()
      matrix.set(sub.student_id, studentMap)
    }
    const existing = studentMap.get(sub.question_id)
    const score = sub.score ?? 0
    if (existing === undefined) {
      studentMap.set(sub.question_id, { score, correct: sub.is_correct, attempts: 1 })
      continue
    }
    existing.attempts += 1
    // Strictly greater: a tie keeps the first-seen (latest) submission.
    if (score > existing.score) {
      existing.score = score
      existing.correct = sub.is_correct
    }
  }
  return matrix
}

/**
 * Returns the exam roster in existing order, plus any student appearing in submissions but
 * not on the roster appended defensively (e.g. unenrolled after submitting) — so no graded
 * work is silently dropped from the grid.
 */
export function buildExamRowAxis(
  roster: ExamMatrixStudent[],
  subs: ExamSubmissionRow[]
): ExamMatrixStudent[] {
  const seenStudentIds = new Set<string>()
  const result: ExamMatrixStudent[] = []

  for (const s of roster) {
    seenStudentIds.add(s.student_id)
    result.push(s)
  }

  // The submissions payload carries no identity columns, so an off-roster student can only
  // be labelled by their opaque id. Rare by construction, and better than an omitted row.
  for (const sub of subs) {
    if (!seenStudentIds.has(sub.student_id)) {
      seenStudentIds.add(sub.student_id)
      result.push({
        student_id: sub.student_id,
        student_number: '',
        username: '(not on roster)',
        first_name: '',
        last_name: '',
        sec_lec: '',
        sec_lab: ''
      })
    }
  }

  return result
}

/**
 * Exam grading carries partial credit, so the cell class comes straight from the score —
 * unlike the practice matrix, which must branch on `solved`.
 *
 * The one guard that carries over: a question worth 0 points makes score === max === 0,
 * which falls into scoreClass's max <= 0 -> score-pending branch and would paint a
 * correctly-answered zero-point question grey instead of green.
 */
export function examCellClass(cell: ExamMatrixCell | undefined, max: number): string {
  if (cell === undefined) {
    return 'score-pending'
  }
  const cls = scoreClass(cell.score, max)
  if (cls === 'score-pending' && cell.correct) {
    return 'score-full'
  }
  if (cls === 'score-pending') {
    return 'score-zero'
  }
  return cls
}

/**
 * Returns human-readable cell tooltip.
 * Uses middle dot separators and correct singular/plural for attempts.
 */
export function examCellTooltip(cell: ExamMatrixCell | undefined, max: number): string {
  if (cell === undefined) {
    return 'Not attempted'
  }
  const attemptsPhrase = cell.attempts === 1 ? '1 attempt' : `${cell.attempts} attempts`
  const correctPhrase = cell.correct ? 'correct' : 'incorrect'
  return `${attemptsPhrase} · best ${cell.score.toFixed(2)} / ${max.toFixed(2)} · ${correctPhrase}`
}

/**
 * Formats displayed column label from the backend's per-exam display_no (gap-free, restarts
 * at 1 per exam). Falls back to the 1-based array position when display_no is absent.
 */
export function examColumnLabel(q: ExamMatrixQuestion, index: number): string {
  return `Q${q.displayNo || index + 1}`
}

/** Formats multi-line column header tooltip with label, requirement, points. */
export function examColumnTooltip(q: ExamMatrixQuestion, index: number): string {
  const parts: string[] = [examColumnLabel(q, index)]
  if (q.requirement && q.requirement.trim() !== '') {
    parts.push(q.requirement)
  }
  parts.push(`${q.points} pts`)
  return parts.join('\n')
}

/** Formats column header for CSV export. */
export function examCsvColumnHeader(q: ExamMatrixQuestion, index: number): string {
  return `${examColumnLabel(q, index)} [max ${q.points.toFixed(2)}]`
}

/**
 * Builds RFC-4180 compliant CSV string for the exam score matrix.
 * Unattempted cells emit empty string '', numeric cells use toFixed(2) — so a question
 * never attempted stays distinguishable from one attempted and scored 0.
 */
export function buildExamMatrixCsv(
  roster: ExamMatrixStudent[],
  questions: ExamMatrixQuestion[],
  subs: ExamSubmissionRow[]
): string {
  const matrix = buildExamMatrix(subs)
  const rowAxis = buildExamRowAxis(roster, subs)

  const header = [
    'StudentNumber',
    'Username',
    'FirstName',
    'LastName',
    'SecLec',
    'SecLab',
    ...questions.map((q, i) => examCsvColumnHeader(q, i)),
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
      rowTotal += cell.score
      return cell.score.toFixed(2)
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
