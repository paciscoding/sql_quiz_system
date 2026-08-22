/**
 * Regression tests for the lecturer "Practice Quiz Summary" gradebook matrix
 * (ARCH-055 / TODO-055-5).
 *
 * Covers:
 *  1. Column axis: exactly 3 Q-columns in props order; a zero-attempt
 *     question still renders a full pending column; no opaque id leaks.
 *  2. Untitled-question header fallback + requirement in the title attr.
 *  3. Never-attempted student still gets a row, entirely pending, correct
 *     zero total.
 *  4. Cell class × text matrix, including the 0-point "solved" guard
 *     (cellClass must branch on `solved`, never on `earned >= max`).
 *  5. Row-total invariant against the student fixture's practice_total.
 *  6. buildMatrixCsv: exact quoted header line + empty-vs-0.00 distinction
 *     between never-attempted and attempted-but-wrong cells.
 *  7. LecturerResults integration: practice questions are fetched lazily
 *     and only once (one-shot guard), and the view swaps out the
 *     "Practice Breakdown" section for the matrix.
 *
 * Only this file is touched — PracticeQuizMatrix.vue, LecturerResults.vue
 * and practice_matrix.ts are read-only inputs.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import PracticeQuizMatrix from '../../components/lecturer/PracticeQuizMatrix.vue'
import LecturerResults from '../../components/lecturer/LecturerResults.vue'
import { buildMatrixCsv } from '../../utils/practice_matrix'
import type { MatrixQuestion, MatrixStudent, PracticeResultRow } from '../../utils/practice_matrix'

// ---------------------------------------------------------------------------
// Shared fixtures
//
// 3 questions: PQ_a (titled, 5pts), PQ_b (untitled, 3pts),
// PQ_c (titled "Zero", 0pts, attempted by nobody in the base fixture).
// 3 students: U1, U2, U3 — U3 has NO practice result rows at all (mirrors
// the backend's INNER JOIN: no row for an unattempted (student,question)
// pair, and no row at all for a zero-attempt student).
// ---------------------------------------------------------------------------

function makeQuestions(): MatrixQuestion[] {
  return [
    { id: 'PQ_a', title: 'Joins, basic', requirement: 'Write an inner join over students and enrollments', points: 5, categoryName: 'Cat1' },
    { id: 'PQ_b', title: '', requirement: 'Update a single row by primary key', points: 3, categoryName: 'Cat1' },
    { id: 'PQ_c', title: 'Zero', requirement: 'Zero-point placeholder question', points: 0, categoryName: 'Cat1' },
  ]
}

function makeStudents(): MatrixStudent[] {
  return [
    { student_id: 'U1', student_number: 'S1', username: 'u1', first_name: 'Alice', last_name: 'Anders', sec_lec: 'L1', sec_lab: 'B1', practice_total: 5 },
    { student_id: 'U2', student_number: 'S2', username: 'u2', first_name: 'Bob', last_name: 'Brown', sec_lec: 'L1', sec_lab: 'B1', practice_total: 0 },
    { student_id: 'U3', student_number: 'S3', username: 'u3', first_name: 'Cara', last_name: 'Chan', sec_lec: 'L1', sec_lab: 'B1', practice_total: 0 },
  ]
}

function resultRow(overrides: Partial<PracticeResultRow>): PracticeResultRow {
  return {
    student_id: 'U1',
    student_number: 'S1',
    username: 'u1',
    first_name: 'Alice',
    last_name: 'Anders',
    sec_lec: 'L1',
    sec_lab: 'B1',
    question_id: 'PQ_a',
    category_id: 'C1',
    category_name: 'Cat1',
    question_title: 'Joins, basic',
    requirement: 'Write an inner join over students and enrollments',
    points: 5,
    is_solved: true,
    attempt_count: 1,
    earned_points: 0,
    ...overrides,
  }
}

// U1/PQ_a solved (5/5, 2 attempts); U1/PQ_b unsolved (0/3, 4 attempts);
// U2/PQ_a unsolved (0/5, 1 attempt). U3 and PQ_c appear nowhere here.
function makeResults(): PracticeResultRow[] {
  return [
    resultRow({
      student_id: 'U1', question_id: 'PQ_a', question_title: 'Joins, basic',
      requirement: 'Write an inner join over students and enrollments',
      points: 5, is_solved: true, attempt_count: 2, earned_points: 5,
    }),
    resultRow({
      student_id: 'U1', question_id: 'PQ_b', question_title: '',
      requirement: 'Update a single row by primary key',
      points: 3, is_solved: false, attempt_count: 4, earned_points: 0,
    }),
    resultRow({
      student_id: 'U2', student_number: 'S2', username: 'u2', first_name: 'Bob', last_name: 'Brown',
      question_id: 'PQ_a', question_title: 'Joins, basic',
      requirement: 'Write an inner join over students and enrollments',
      points: 5, is_solved: false, attempt_count: 1, earned_points: 0,
    }),
  ]
}

// =============================================================================
// 1. Column axis
// =============================================================================

describe('PracticeQuizMatrix — column axis (group 1)', () => {
  it('renders exactly 3 Q-columns in props order, PQ_c is a full pending column despite zero attempts, and no opaque id leaks into text', () => {
    const wrapper = mount(PracticeQuizMatrix, {
      props: { students: makeStudents(), questions: makeQuestions(), results: makeResults() },
    })

    const headers = wrapper.findAll('th.pqm-q')
    expect(headers).toHaveLength(3)
    expect(headers.map(h => h.find('.pqm-q-label').text())).toEqual(['Q1', 'Q2', 'Q3'])

    // PQ_c is column index 2 (Q1=PQ_a, Q2=PQ_b, Q3=PQ_c) — pending for every row.
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(3)
    for (const row of rows) {
      const cells = row.findAll('td.score-cell')
      const pqcCell = cells[2]
      expect(pqcCell.text()).toBe('—')
      expect(pqcCell.classes()).toContain('score-pending')
    }

    expect(wrapper.text()).not.toContain('PQ_a')
    expect(wrapper.text()).not.toContain('PQ_b')
    expect(wrapper.text()).not.toContain('PQ_c')
  })
})

// =============================================================================
// 2. Untitled-question header fallback
// =============================================================================

describe('PracticeQuizMatrix — column header title fallback (group 2)', () => {
  it('shows a title span for titled questions, omits it for untitled ones, and always sets a tooltip title attr containing the requirement', () => {
    const questions = makeQuestions()
    const wrapper = mount(PracticeQuizMatrix, {
      props: { students: makeStudents(), questions, results: makeResults() },
    })

    const headers = wrapper.findAll('th.pqm-q')

    expect(headers[0].find('.pqm-q-title').exists()).toBe(true)
    expect(headers[0].find('.pqm-q-title').text()).toBe('Joins, basic')

    expect(headers[1].find('.pqm-q-title').exists()).toBe(false)

    headers.forEach((h, i) => {
      const title = h.attributes('title')
      expect(title).toBeTruthy()
      expect(title as string).toContain(questions[i].requirement)
    })
  })
})

// =============================================================================
// 3. Never-attempted student row
// =============================================================================

describe('PracticeQuizMatrix — never-attempted student row (group 3)', () => {
  it('still renders U3, with every question cell pending and a correct zero total', () => {
    const wrapper = mount(PracticeQuizMatrix, {
      props: { students: makeStudents(), questions: makeQuestions(), results: makeResults() },
    })

    const rows = wrapper.findAll('tbody tr')
    const u3Row = rows[2]
    const cells = u3Row.findAll('td.score-cell')
    // 3 question cells + 1 total cell
    expect(cells).toHaveLength(4)

    for (let i = 0; i < 3; i++) {
      expect(cells[i].text()).toBe('—')
      expect(cells[i].classes()).toContain('score-pending')
    }

    // 5 + 3 + 0 = 8 max; U3 earned nothing.
    expect(cells[3].text()).toBe('0.0 / 8.0')
  })
})

// =============================================================================
// 4. Cell class × text matrix, including the 0-point solved guard
// =============================================================================

describe('PracticeQuizMatrix — cell class/text matrix incl. 0-point solved guard (group 4)', () => {
  it('classifies full/zero/pending correctly, and a SOLVED 0-point question as score-full (never score-pending)', () => {
    const resultsWithZeroSolve: PracticeResultRow[] = [
      ...makeResults(),
      resultRow({
        student_id: 'U2', student_number: 'S2', username: 'u2', first_name: 'Bob', last_name: 'Brown',
        question_id: 'PQ_c', question_title: 'Zero', requirement: 'Zero-point placeholder question',
        points: 0, is_solved: true, attempt_count: 1, earned_points: 0,
      }),
    ]

    const wrapper = mount(PracticeQuizMatrix, {
      props: { students: makeStudents(), questions: makeQuestions(), results: resultsWithZeroSolve },
    })

    const rows = wrapper.findAll('tbody tr')
    const u1Cells = rows[0].findAll('td.score-cell')
    const u2Cells = rows[1].findAll('td.score-cell')

    // U1/PQ_a: solved, full points.
    expect(u1Cells[0].classes()).toContain('score-full')
    expect(u1Cells[0].text()).toBe('5.0 / 5.0')

    // U1/PQ_b: attempted, unsolved.
    expect(u1Cells[1].classes()).toContain('score-zero')
    expect(u1Cells[1].text()).toBe('0.0 / 3.0')

    // U2/PQ_b: U2 never attempted PQ_b.
    expect(u2Cells[1].classes()).toContain('score-pending')
    expect(u2Cells[1].text()).toBe('—')

    // U2/PQ_c: solved a 0-point question. earned === max === 0 must NOT
    // fall into scoreClass's max<=0 pending branch — cellClass must branch
    // on cell.solved instead.
    expect(u2Cells[2].classes()).toContain('score-full')
    expect(u2Cells[2].classes()).not.toContain('score-pending')
    expect(u2Cells[2].text()).toBe('0.0 / 0.0')
  })
})

// =============================================================================
// 5. Row-total invariant
// =============================================================================

describe('PracticeQuizMatrix — row-total invariant (group 5)', () => {
  it("U1's rendered total matches the practice_total on the U1 student fixture", () => {
    const students = makeStudents()
    const u1 = students.find(s => s.student_id === 'U1')!
    expect(u1.practice_total).toBe(5)

    const wrapper = mount(PracticeQuizMatrix, {
      props: { students, questions: makeQuestions(), results: makeResults() },
    })

    const rows = wrapper.findAll('tbody tr')
    const totalCell = rows[0].findAll('td.score-cell')[3]
    expect(totalCell.text()).toBe('5.0 / 8.0')
  })
})

// =============================================================================
// 6. CSV export
// =============================================================================

describe('buildMatrixCsv (group 6)', () => {
  it('emits the exact quoted header line and distinguishes never-attempted (empty) from attempted-wrong (0.00)', () => {
    const csv = buildMatrixCsv(makeStudents(), makeQuestions(), makeResults())
    const lines = csv.split('\r\n')

    expect(lines[0]).toBe(
      'StudentNumber,Username,FirstName,LastName,SecLec,SecLab,' +
      '"Q1 - Joins, basic [max 5.00]",Q2 [max 3.00],Q3 - Zero [max 0.00],Total,MaxTotal'
    )

    // U1's row: StudentNumber,Username,FirstName,LastName,SecLec,SecLab,
    //           PQ_a,PQ_b,PQ_c,Total,MaxTotal
    const u1Fields = lines[1].split(',')
    expect(u1Fields[6]).toBe('5.00') // PQ_a: attempted & solved
    expect(u1Fields[7]).toBe('0.00') // PQ_b: attempted, wrong -> 0.00, not blank
    expect(u1Fields[8]).toBe('')     // PQ_c: never attempted -> empty, not '0.00'
    expect(u1Fields[9]).toBe('5.00') // row total
    expect(u1Fields[10]).toBe('8.00') // max total
  })
})

// =============================================================================
// 7. LecturerResults integration
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

describe('LecturerResults — Practice Quiz Summary integration (group 7)', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  const questionsPayload = makeQuestions().map(q => ({
    id: q.id,
    title: q.title,
    requirement: q.requirement,
    points: q.points,
    category_name: q.categoryName,
  }))
  const resultsPayload = makeResults()
  const scoresPayload = makeStudents().map(s => ({
    student_id: s.student_id,
    username: s.username,
    student_number: s.student_number,
    first_name: s.first_name,
    last_name: s.last_name,
    sec_lec: s.sec_lec,
    sec_lab: s.sec_lab,
    exam_scores: {},
    practice_total: s.practice_total,
    grand_total: s.practice_total,
  }))

  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 't')
    // '/lecturer/practice/questions' is matched BEFORE any looser '/questions'
    // pattern so it can never be shadowed by a drill-down exam-questions route.
    fetchMock = buildRoutedFetch([
      ['/lecturer/practice/questions', questionsPayload],
      ['/lecturer/practice/results', resultsPayload],
      ['/lecturer/exams', []],
      ['/lecturer/scores', scoresPayload],
      ['/exams/max-scores', { max_scores: {} }],
    ])
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('does not fetch practice questions on mount, fetches exactly once when the matrix view is selected, and never refetches on toggle (one-shot guard)', async () => {
    const wrapper = mount(LecturerResults, {
      global: { stubs: { Skeleton: true, EmptyState: true } },
    })
    await flushPromises()

    let calledUrls = fetchMock.mock.calls.map(c => c[0] as string)
    expect(calledUrls.some(u => u.includes('/lecturer/practice/questions'))).toBe(false)
    expect(wrapper.find('.pqm-table').exists()).toBe(false)
    // The "Practice Breakdown" *heading* (not the always-present export
    // button of the same name) is shown outside matrix mode.
    expect(wrapper.findAll('h3').some(h => h.text() === 'Practice Breakdown')).toBe(true)

    const select = wrapper.find('select')
    await select.setValue('__practice_matrix__')
    await flushPromises()

    calledUrls = fetchMock.mock.calls.map(c => c[0] as string)
    expect(calledUrls.filter(u => u.includes('/lecturer/practice/questions'))).toHaveLength(1)
    expect(wrapper.find('.pqm-table').exists()).toBe(true)
    expect(wrapper.findAll('h3').some(h => h.text() === 'Practice Breakdown')).toBe(false)

    // Switch away (back to "All exams") and back to the matrix view.
    await select.setValue('')
    await flushPromises()
    await select.setValue('__practice_matrix__')
    await flushPromises()

    calledUrls = fetchMock.mock.calls.map(c => c[0] as string)
    expect(calledUrls.filter(u => u.includes('/lecturer/practice/questions'))).toHaveLength(1)
  })
})
