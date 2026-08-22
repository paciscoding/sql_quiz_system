/**
 * Regression tests for the lecturer "Student x Question Scores" exam matrix.
 *
 * Covers:
 *  1. Column axis: one column per question in props order, labelled from the
 *     backend's per-exam display_no; a question nobody attempted still renders
 *     a full pending column.
 *  2. toExamMatrixQuestions is the security boundary — correct_sql,
 *     target_table and expected_result must not reach the DOM or the CSV.
 *  3. A rostered student with zero submissions still gets a row of dashes.
 *  4. Cell class x text, including the 0-point correct guard.
 *  5. Best-of-attempts reduction, attempt counting, and the score tie rule.
 *  6. Row-total invariant: the row total is the sum of best-per-question, i.e.
 *     what GetConsolidatedScores puts in that exam's consolidated column.
 *  7. buildExamMatrixCsv: exact quoted header line + empty-vs-0.00.
 *  8. LecturerResults integration: the matrix and its export button appear only
 *     when a specific exam is selected.
 *
 * Only this file is touched — ExamScoreMatrix.vue, LecturerResults.vue and
 * exam_matrix.ts are read-only inputs.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import ExamScoreMatrix from '../../components/lecturer/ExamScoreMatrix.vue'
import LecturerResults from '../../components/lecturer/LecturerResults.vue'
import { buildExamMatrixCsv, buildExamMatrix, toExamMatrixQuestions } from '../../utils/exam_matrix'
import type { ExamMatrixQuestion, ExamMatrixStudent, ExamSubmissionRow } from '../../utils/exam_matrix'

// ---------------------------------------------------------------------------
// Shared fixtures
//
// 3 questions: 101 (5 pts), 102 (10 pts), 103 (0 pts — attempted by nobody in
// the base fixture). 3 rostered students: U1, U2, U3 — U3 has no submissions.
// Submissions are listed newest-first, as GET /lecturer/exams/:id/submissions
// returns them (ORDER BY submitted_at DESC).
// ---------------------------------------------------------------------------

function makeQuestions(): ExamMatrixQuestion[] {
  return [
    { id: 101, displayNo: 1, requirement: 'Select every student in section L1', points: 5 },
    { id: 102, displayNo: 2, requirement: 'Update the salary of one employee', points: 10 },
    { id: 103, displayNo: 3, requirement: 'Zero-point placeholder question', points: 0 },
  ]
}

function makeRoster(): ExamMatrixStudent[] {
  return [
    { student_id: 'U1', student_number: 'S1', username: 'u1', first_name: 'Alice', last_name: 'Anders', sec_lec: 'L1', sec_lab: 'B1' },
    { student_id: 'U2', student_number: 'S2', username: 'u2', first_name: 'Bob', last_name: 'Brown', sec_lec: 'L1', sec_lab: 'B1' },
    { student_id: 'U3', student_number: 'S3', username: 'u3', first_name: 'Cara', last_name: 'Chan', sec_lec: 'L1', sec_lab: 'B1' },
  ]
}

// U1/101: 2 attempts, both scoring 5 — the newest is correct, so the tie must
//         keep it. U1/102: 3 attempts, best 9 of 10 (partial credit).
// U2/101: one wrong attempt. U2/103: a correct answer to a 0-point question.
function makeSubmissions(): ExamSubmissionRow[] {
  return [
    { student_id: 'U1', question_id: 101, is_correct: true, score: 5 },
    { student_id: 'U1', question_id: 101, is_correct: false, score: 5 },
    { student_id: 'U1', question_id: 102, is_correct: false, score: 9 },
    { student_id: 'U1', question_id: 102, is_correct: false, score: 5 },
    { student_id: 'U1', question_id: 102, is_correct: false, score: 3 },
    { student_id: 'U2', question_id: 101, is_correct: false, score: 0 },
    { student_id: 'U2', question_id: 103, is_correct: true, score: 0 },
  ]
}

function mountMatrix(overrides: Record<string, unknown> = {}) {
  return mount(ExamScoreMatrix, {
    props: {
      students: makeRoster(),
      questions: makeQuestions(),
      submissions: makeSubmissions(),
      ...overrides,
    },
  })
}

// =============================================================================
// 1. Column axis
// =============================================================================

describe('ExamScoreMatrix — column axis (group 1)', () => {
  it('renders one column per question in props order labelled by display_no, and Q3 is a full pending column despite zero attempts', () => {
    const wrapper = mountMatrix()

    const headers = wrapper.findAll('th.esm-q')
    expect(headers).toHaveLength(3)
    expect(headers.map(h => h.find('.esm-q-label').text())).toEqual(['Q1', 'Q2', 'Q3'])

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    // Q3 (column index 2) is pending for everyone except U2, who answered it.
    expect(rows[0].findAll('td.score-cell')[2].text()).toBe('—')
    expect(rows[2].findAll('td.score-cell')[2].text()).toBe('—')
  })

  it('falls back to the 1-based position when display_no is absent', () => {
    const questions = makeQuestions().map(q => ({ ...q, displayNo: 0 }))
    const wrapper = mountMatrix({ questions })
    expect(wrapper.findAll('th.esm-q').map(h => h.find('.esm-q-label').text())).toEqual(['Q1', 'Q2', 'Q3'])
  })
})

// =============================================================================
// 2. Answer-key narrowing at the fetch boundary
// =============================================================================

describe('toExamMatrixQuestions — answer-key narrowing (group 2)', () => {
  const rawPayload = [
    {
      id: 101, display_no: 1, requirement: 'Select every student in section L1', points: 5,
      correct_sql: 'SELECT * FROM app.secret_answer_key',
      target_table: 'secret_target_table',
      expected_result: [{ leak: 'expected_result_blob' }],
      expected_columns: ['leaked_column'],
      auto_seed: { plan: 'leaked_seed_plan' },
      type: 'QUERY',
    },
  ]

  it('keeps only id/displayNo/requirement/points', () => {
    expect(toExamMatrixQuestions(rawPayload as never)).toEqual([
      { id: 101, displayNo: 1, requirement: 'Select every student in section L1', points: 5 },
    ])
  })

  it('keeps the answer key out of the rendered DOM and the CSV', () => {
    const questions = toExamMatrixQuestions(rawPayload as never)
    const wrapper = mount(ExamScoreMatrix, {
      props: { students: makeRoster(), questions, submissions: makeSubmissions() },
    })
    const csv = buildExamMatrixCsv(makeRoster(), questions, makeSubmissions())

    for (const secret of ['secret_answer_key', 'secret_target_table', 'expected_result_blob', 'leaked_column', 'leaked_seed_plan']) {
      expect(wrapper.html()).not.toContain(secret)
      expect(csv).not.toContain(secret)
    }
  })
})

// =============================================================================
// 3. Rostered student with no submissions
// =============================================================================

describe('ExamScoreMatrix — never-submitted student row (group 3)', () => {
  it('still renders U3, with every question cell pending and a zero total', () => {
    const wrapper = mountMatrix()
    const cells = wrapper.findAll('tbody tr')[2].findAll('td.score-cell')

    // 3 question cells + 1 total cell
    expect(cells).toHaveLength(4)
    for (let i = 0; i < 3; i++) {
      expect(cells[i].text()).toBe('—')
      expect(cells[i].classes()).toContain('score-pending')
    }
    // 5 + 10 + 0 = 15 max
    expect(cells[3].text()).toBe('0.0 / 15.0')
  })

  it('appends a student who submitted but is no longer on the roster', () => {
    const submissions = [
      ...makeSubmissions(),
      { student_id: 'U9', question_id: 101, is_correct: true, score: 5 },
    ]
    const wrapper = mountMatrix({ submissions })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(4)
    expect(rows[3].find('.esm-student-num').text()).toBe('(not on roster)')
  })
})

// =============================================================================
// 4. Cell class x text, including the 0-point correct guard
// =============================================================================

describe('ExamScoreMatrix — cell class/text matrix (group 4)', () => {
  it('classifies full/partial/zero/pending, and a CORRECT 0-point question as score-full', () => {
    const wrapper = mountMatrix()
    const u1 = wrapper.findAll('tbody tr')[0].findAll('td.score-cell')
    const u2 = wrapper.findAll('tbody tr')[1].findAll('td.score-cell')

    // U1/101: best 5 of 5.
    expect(u1[0].classes()).toContain('score-full')
    expect(u1[0].text()).toBe('5.0 / 5.0')

    // U1/102: best 9 of 10 — partial credit is real on exams.
    expect(u1[1].classes()).toContain('score-partial')
    expect(u1[1].text()).toBe('9.0 / 10.0')

    // U2/101: attempted and scored nothing.
    expect(u2[0].classes()).toContain('score-zero')
    expect(u2[0].text()).toBe('0.0 / 5.0')

    // U2/102: never attempted.
    expect(u2[1].classes()).toContain('score-pending')
    expect(u2[1].text()).toBe('—')

    // U2/103: correct on a 0-point question. score === max === 0 must NOT fall
    // into scoreClass's max<=0 pending branch.
    expect(u2[2].classes()).toContain('score-full')
    expect(u2[2].classes()).not.toContain('score-pending')
    expect(u2[2].text()).toBe('0.0 / 0.0')
  })

  it('reports attempts, best score and correctness in the cell tooltip', () => {
    const wrapper = mountMatrix()
    const u1 = wrapper.findAll('tbody tr')[0].findAll('td.score-cell')
    expect(u1[1].attributes('title')).toBe('3 attempts · best 9.00 / 10.00 · incorrect')
    expect(u1[2].attributes('title')).toBe('Not attempted')
  })
})

// =============================================================================
// 5. Best-of-attempts reduction
// =============================================================================

describe('buildExamMatrix — best of attempts (group 5)', () => {
  it('keeps the maximum score, counts every attempt, and breaks a tie on the newest (first-listed) row', () => {
    const matrix = buildExamMatrix(makeSubmissions())

    // 3 attempts scoring 9, 5, 3 -> best 9.
    expect(matrix.get('U1')!.get(102)).toEqual({ score: 9, correct: false, attempts: 3 })

    // Two attempts both scoring 5; the first listed (newest) is the correct one,
    // matching the SQL's `ORDER BY score DESC, submitted_at DESC` tiebreak.
    expect(matrix.get('U1')!.get(101)).toEqual({ score: 5, correct: true, attempts: 2 })

    expect(matrix.get('U1')!.get(103)).toBeUndefined()
  })
})

// =============================================================================
// 6. Row-total invariant
// =============================================================================

describe('ExamScoreMatrix — row-total invariant (group 6)', () => {
  it("U1's rendered total equals the sum of best-per-question, i.e. the consolidated exam column", () => {
    const matrix = buildExamMatrix(makeSubmissions())
    const bestSum = Array.from(matrix.get('U1')!.values()).reduce((acc, c) => acc + c.score, 0)
    expect(bestSum).toBe(14)

    const wrapper = mountMatrix()
    expect(wrapper.findAll('tbody tr')[0].findAll('td.score-cell')[3].text()).toBe('14.0 / 15.0')
  })
})

// =============================================================================
// 7. CSV export
// =============================================================================

describe('buildExamMatrixCsv (group 7)', () => {
  it('emits the exact header line and distinguishes never-attempted (empty) from attempted-and-zero (0.00)', () => {
    const csv = buildExamMatrixCsv(makeRoster(), makeQuestions(), makeSubmissions())
    const lines = csv.split('\r\n')

    expect(lines[0]).toBe(
      'StudentNumber,Username,FirstName,LastName,SecLec,SecLab,' +
      'Q1 [max 5.00],Q2 [max 10.00],Q3 [max 0.00],Total,MaxTotal'
    )

    const u1 = lines[1].split(',')
    expect(u1[6]).toBe('5.00')  // Q1: best of two attempts
    expect(u1[7]).toBe('9.00')  // Q2: best of three attempts
    expect(u1[8]).toBe('')      // Q3: never attempted -> empty, not '0.00'
    expect(u1[9]).toBe('14.00') // row total
    expect(u1[10]).toBe('15.00')

    const u2 = lines[2].split(',')
    expect(u2[6]).toBe('0.00')  // attempted and wrong -> 0.00, not blank
    expect(u2[7]).toBe('')      // never attempted
    expect(u2[8]).toBe('0.00')  // correct on a 0-point question
  })
})

// =============================================================================
// 8. LecturerResults integration
// =============================================================================

function buildRoutedFetch(routes: Array<[string, unknown]>) {
  return vi.fn(async (url: string) => {
    for (const [pattern, data] of routes) {
      if (url.includes(pattern)) {
        return { ok: true, json: async () => data } as Response
      }
    }
    return { ok: true, json: async () => [] } as Response
  })
}

describe('LecturerResults — exam matrix integration (group 8)', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  const examQuestionsPayload = [
    {
      id: 101, display_no: 1, requirement: 'Select every student in section L1', points: 5,
      correct_sql: 'SELECT * FROM app.secret_answer_key', target_table: 'secret_target_table',
    },
    { id: 102, display_no: 2, requirement: 'Update the salary of one employee', points: 10 },
  ]

  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 't')
    // Specific drill-down routes come first so the looser '/lecturer/exams'
    // list route cannot shadow them.
    fetchMock = buildRoutedFetch([
      ['/lecturer/exams/EX1/questions', examQuestionsPayload],
      ['/lecturer/exams/EX1/submissions', makeSubmissions()],
      ['/lecturer/exams/EX1/students', { students: makeRoster() }],
      ['/lecturer/practice/results', []],
      ['/exams/max-scores', { max_scores: {} }],
      ['/lecturer/exams', [{ id: 'EX1', title: 'Midterm 1' }]],
      ['/lecturer/scores', []],
    ])
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('shows the matrix and its export button only once a specific exam is selected, and never leaks the answer key', async () => {
    const wrapper = mount(LecturerResults, {
      global: { stubs: { Skeleton: true, EmptyState: true } },
    })
    await flushPromises()

    const exportLabels = () => wrapper.findAll('button').map(b => b.text())
    expect(wrapper.find('.esm-table').exists()).toBe(false)
    expect(exportLabels()).not.toContain('Export Exam Matrix (CSV)')

    const select = wrapper.find('select')
    await select.setValue('EX1')
    await flushPromises()

    expect(wrapper.find('.esm-table').exists()).toBe(true)
    expect(exportLabels()).toContain('Export Exam Matrix (CSV)')
    expect(wrapper.findAll('h3').some(h => h.text() === 'Student × Question Scores')).toBe(true)
    expect(wrapper.html()).not.toContain('secret_answer_key')
    expect(wrapper.html()).not.toContain('secret_target_table')

    // The matrix is scoped to the drill-down: switching to the practice summary
    // takes it away again.
    await select.setValue('__practice_matrix__')
    await flushPromises()
    expect(wrapper.find('.esm-table').exists()).toBe(false)
    expect(exportLabels()).not.toContain('Export Exam Matrix (CSV)')
  })
})
