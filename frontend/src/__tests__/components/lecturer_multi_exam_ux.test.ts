/**
 * Regression tests for the "lecturer multi-exam UX" feature (TODO-002).
 *
 * Rather than mounting the 700-line LecturerView.vue (which pulls in many
 * child components and global services), we extract and test the pure
 * computed logic inline — the exact same pattern used by:
 *   - lecturer_sort_filter.test.ts (display sort/filter)
 *   - student_view_progress.test.ts (progress strip computeds)
 *
 * Behaviors covered:
 *  1. drillQuestionStats  — per-question attempts / correct-students / avgScore
 *  2. drillNotSubmitted   — roster minus any student who has a submission
 *  3. Readiness gate      — Activate is disabled when uncaptured_count > 0
 *  4. Delete guard        — delete control disabled when is_active
 *  5. Rename validation   — empty title is rejected before calling fetch
 */

import { describe, it, expect } from 'vitest'
import { ref, computed, type Ref } from 'vue'

// ─────────────────────────────────────────────────────────────────────────────
// Types mirrored from LecturerView.vue
// ─────────────────────────────────────────────────────────────────────────────

interface DrillSubmission {
  student_id: number
  question_id: number
  is_correct: boolean
  score: number
}

interface RosterStudent {
  student_id: number
  username: string
  student_number: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  assigned_at: string
}

interface Examination {
  id: number
  title: string
  is_active: boolean
  effective_state: string
  scheduled_start_at: string | null
  scheduled_end_at: string | null
  activated_at: string | null
  archived_at: string | null
  introduction: string
  question_count?: number
  total_points?: number
  uncaptured_count?: number
}

// ─────────────────────────────────────────────────────────────────────────────
// Logic mirrors — reproduce computed bodies from LecturerView.vue exactly
// ─────────────────────────────────────────────────────────────────────────────

function makeDrillQuestionStats(
  drillQuestions: Ref<any[]>,
  drillSubmissions: Ref<DrillSubmission[]>
) {
  return computed(() =>
    drillQuestions.value.map(q => {
      const subs = drillSubmissions.value.filter(s => s.question_id === q.id)
      const attempts = subs.length
      const correctStudents = new Set(subs.filter(s => s.is_correct).map(s => s.student_id)).size
      const avgScore = attempts > 0 ? subs.reduce((acc, s) => acc + (s.score || 0), 0) / attempts : 0
      return { id: q.id, requirement: q.requirement, attempts, correctStudents, avgScore }
    })
  )
}

function makeDrillNotSubmitted(
  drillSubmissions: Ref<DrillSubmission[]>,
  drillRoster: Ref<RosterStudent[]>
) {
  return computed(() => {
    const submittedIds = new Set(drillSubmissions.value.map(s => s.student_id))
    return drillRoster.value.filter(s => !submittedIds.has(s.student_id))
  })
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────────

const QUESTIONS = [
  { id: 1, requirement: 'Write a SELECT query', exam_id: 10 },
  { id: 2, requirement: 'Insert a row', exam_id: 10 },
  { id: 3, requirement: 'Join two tables', exam_id: 10 },
]

const ROSTER: RosterStudent[] = [
  { student_id: 101, username: 'alice', student_number: 'S001', first_name: 'Alice', last_name: 'A', sec_lec: 'L1', sec_lab: 'B1', assigned_at: '' },
  { student_id: 102, username: 'bob',   student_number: 'S002', first_name: 'Bob',   last_name: 'B', sec_lec: 'L1', sec_lab: 'B1', assigned_at: '' },
  { student_id: 103, username: 'carol', student_number: 'S003', first_name: 'Carol', last_name: 'C', sec_lec: 'L2', sec_lab: 'B2', assigned_at: '' },
]

// ─────────────────────────────────────────────────────────────────────────────
// 1. drillQuestionStats
// ─────────────────────────────────────────────────────────────────────────────

describe('drillQuestionStats (TODO-002)', () => {
  it('returns a stats entry for every question', () => {
    const qs = ref([...QUESTIONS])
    const subs = ref<DrillSubmission[]>([])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value).toHaveLength(3)
  })

  it('counts zero attempts when there are no submissions for a question', () => {
    const qs = ref([QUESTIONS[0]])
    const subs = ref<DrillSubmission[]>([])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].attempts).toBe(0)
    expect(stats.value[0].correctStudents).toBe(0)
    expect(stats.value[0].avgScore).toBe(0)
  })

  it('counts attempts as total submission rows for that question', () => {
    const qs = ref([QUESTIONS[0]])
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: false, score: 0 },
      { student_id: 102, question_id: 1, is_correct: true,  score: 10 },
    ])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].attempts).toBe(2)
  })

  it('counts correct students by unique student_id (not by submission count)', () => {
    // If a student submits twice and both are correct, they count as 1 correct student
    const qs = ref([QUESTIONS[0]])
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true, score: 10 },
      { student_id: 101, question_id: 1, is_correct: true, score: 10 }, // duplicate student
      { student_id: 102, question_id: 1, is_correct: true, score: 10 },
    ])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].correctStudents).toBe(2) // 101 + 102, not 3
  })

  it('does not count incorrect submissions toward correctStudents', () => {
    const qs = ref([QUESTIONS[0]])
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: false, score: 0 },
      { student_id: 102, question_id: 1, is_correct: false, score: 0 },
    ])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].correctStudents).toBe(0)
  })

  it('computes avgScore as mean of all score values for that question', () => {
    const qs = ref([QUESTIONS[0]])
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true,  score: 8 },
      { student_id: 102, question_id: 1, is_correct: false, score: 4 },
    ])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].avgScore).toBeCloseTo(6.0)
  })

  it('isolates stats per question (submissions for q2 do not bleed into q1)', () => {
    const qs = ref([QUESTIONS[0], QUESTIONS[1]])
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 2, is_correct: true, score: 5 },
    ])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].attempts).toBe(0) // q1 untouched
    expect(stats.value[1].attempts).toBe(1) // q2 has 1 attempt
  })

  it('avgScore is 0 when attempts is 0 (no division by zero)', () => {
    const qs = ref([QUESTIONS[2]])
    const subs = ref<DrillSubmission[]>([])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].avgScore).toBe(0)
  })

  it('handles score=undefined/missing gracefully (treats as 0)', () => {
    const qs = ref([QUESTIONS[0]])
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true, score: undefined as unknown as number },
    ])
    const stats = makeDrillQuestionStats(qs, subs)

    expect(stats.value[0].avgScore).toBe(0)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 2. drillNotSubmitted
// ─────────────────────────────────────────────────────────────────────────────

describe('drillNotSubmitted (TODO-002)', () => {
  it('returns all roster students when there are no submissions', () => {
    const subs = ref<DrillSubmission[]>([])
    const roster = ref([...ROSTER])
    const notSubmitted = makeDrillNotSubmitted(subs, roster)

    expect(notSubmitted.value).toHaveLength(3)
  })

  it('excludes students who have at least one submission', () => {
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true, score: 10 },
    ])
    const roster = ref([...ROSTER])
    const notSubmitted = makeDrillNotSubmitted(subs, roster)

    const ids = notSubmitted.value.map(s => s.student_id)
    expect(ids).not.toContain(101)
    expect(ids).toContain(102)
    expect(ids).toContain(103)
  })

  it('returns empty list when all roster students have submitted', () => {
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true,  score: 10 },
      { student_id: 102, question_id: 2, is_correct: false, score: 0  },
      { student_id: 103, question_id: 1, is_correct: true,  score: 8  },
    ])
    const roster = ref([...ROSTER])
    const notSubmitted = makeDrillNotSubmitted(subs, roster)

    expect(notSubmitted.value).toHaveLength(0)
  })

  it('counts a student as submitted even if they submitted only one of many questions', () => {
    // A student who answered q1 but not q2/q3 still has a submission → excluded
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true, score: 10 },
    ])
    const roster = ref([ROSTER[0]]) // only alice
    const notSubmitted = makeDrillNotSubmitted(subs, roster)

    expect(notSubmitted.value).toHaveLength(0)
  })

  it('works correctly with an empty roster', () => {
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true, score: 10 },
    ])
    const roster = ref<RosterStudent[]>([])
    const notSubmitted = makeDrillNotSubmitted(subs, roster)

    expect(notSubmitted.value).toHaveLength(0)
  })

  it('does not exclude students whose student_id appears in submissions for another exam segment (uniqueness is by student_id only)', () => {
    // roster has student 201 who has NOT submitted; student 101 has
    const extraStudent: RosterStudent = {
      student_id: 201, username: 'dave', student_number: 'S004',
      first_name: 'Dave', last_name: 'D', sec_lec: 'L1', sec_lab: 'B1', assigned_at: ''
    }
    const subs = ref<DrillSubmission[]>([
      { student_id: 101, question_id: 1, is_correct: true, score: 10 },
    ])
    const roster = ref([ROSTER[0], extraStudent])
    const notSubmitted = makeDrillNotSubmitted(subs, roster)

    const ids = notSubmitted.value.map(s => s.student_id)
    expect(ids).toContain(201)
    expect(ids).not.toContain(101)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 3. Readiness gate: Activate disabled when uncaptured_count > 0
// ─────────────────────────────────────────────────────────────────────────────

describe('Readiness gate — Activate button disabled logic (TODO-002)', () => {
  // In LecturerView.vue the Activate button is disabled when:
  //   exam.uncaptured_count != null && exam.uncaptured_count > 0
  //
  // We test the predicate directly.

  function activateIsDisabled(exam: Examination): boolean {
    return exam.uncaptured_count != null && exam.uncaptured_count > 0
  }

  it('disabled when uncaptured_count > 0', () => {
    const exam: Examination = {
      id: 1, title: 'Mid', is_active: false, effective_state: 'inactive',
      scheduled_start_at: null, scheduled_end_at: null, activated_at: null,
      archived_at: null, introduction: '', question_count: 3, total_points: 30,
      uncaptured_count: 2
    }
    expect(activateIsDisabled(exam)).toBe(true)
  })

  it('enabled when uncaptured_count is 0', () => {
    const exam: Examination = {
      id: 1, title: 'Mid', is_active: false, effective_state: 'inactive',
      scheduled_start_at: null, scheduled_end_at: null, activated_at: null,
      archived_at: null, introduction: '', question_count: 3, total_points: 30,
      uncaptured_count: 0
    }
    expect(activateIsDisabled(exam)).toBe(false)
  })

  it('enabled when uncaptured_count is undefined (legacy exam without enrichment)', () => {
    const exam: Examination = {
      id: 1, title: 'Mid', is_active: false, effective_state: 'inactive',
      scheduled_start_at: null, scheduled_end_at: null, activated_at: null,
      archived_at: null, introduction: '',
      // no uncaptured_count field
    }
    expect(activateIsDisabled(exam)).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 4. Delete guard: delete control disabled when is_active
// ─────────────────────────────────────────────────────────────────────────────

describe('Delete guard — delete button disabled logic (TODO-002)', () => {
  // In LecturerView.vue the delete button is disabled (or hidden) when
  // exam.is_active is true.

  function deleteIsDisabled(exam: Examination): boolean {
    return exam.is_active
  }

  it('disabled when exam is active', () => {
    const exam: Examination = {
      id: 1, title: 'Mid', is_active: true, effective_state: 'active',
      scheduled_start_at: null, scheduled_end_at: null, activated_at: null,
      archived_at: null, introduction: ''
    }
    expect(deleteIsDisabled(exam)).toBe(true)
  })

  it('enabled when exam is inactive', () => {
    const exam: Examination = {
      id: 1, title: 'Mid', is_active: false, effective_state: 'inactive',
      scheduled_start_at: null, scheduled_end_at: null, activated_at: null,
      archived_at: null, introduction: ''
    }
    expect(deleteIsDisabled(exam)).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 5. Rename validation: empty title is rejected before calling fetch
// ─────────────────────────────────────────────────────────────────────────────

describe('Rename validation — saveRename empty-title guard (TODO-002)', () => {
  // saveRename in LecturerView.vue does:
  //   const title = renameDraft.value.trim()
  //   if (!title) { notify(...); return }
  //   // ... fetch(PUT)
  //
  // We test the guard predicate and verify fetch is not called for empty/ws titles.

  function makeSaveRenameMirror(notify: (msg: string, type: string) => void, fetchSpy: () => void) {
    return async function saveRename(renameDraft: string) {
      const title = renameDraft.trim()
      if (!title) {
        notify('Exam title cannot be empty', 'error')
        return
      }
      // Would call fetch here in the real component
      fetchSpy()
    }
  }

  it('calls notify with error and does NOT call fetch when title is empty string', async () => {
    const notifyCalls: Array<[string, string]> = []
    let fetchCalled = false

    const saveRename = makeSaveRenameMirror(
      (msg, type) => notifyCalls.push([msg, type]),
      () => { fetchCalled = true }
    )

    await saveRename('')

    expect(notifyCalls).toHaveLength(1)
    expect(notifyCalls[0][1]).toBe('error')
    expect(fetchCalled).toBe(false)
  })

  it('calls notify with error and does NOT call fetch when title is whitespace-only', async () => {
    const notifyCalls: Array<[string, string]> = []
    let fetchCalled = false

    const saveRename = makeSaveRenameMirror(
      (msg, type) => notifyCalls.push([msg, type]),
      () => { fetchCalled = true }
    )

    await saveRename('   \t  ')

    expect(notifyCalls).toHaveLength(1)
    expect(fetchCalled).toBe(false)
  })

  it('does NOT call notify and DOES call fetch when title is valid', async () => {
    const notifyCalls: Array<[string, string]> = []
    let fetchCalled = false

    const saveRename = makeSaveRenameMirror(
      (msg, type) => notifyCalls.push([msg, type]),
      () => { fetchCalled = true }
    )

    await saveRename('Midterm Exam')

    expect(notifyCalls.filter(([,t]) => t === 'error')).toHaveLength(0)
    expect(fetchCalled).toBe(true)
  })

  it('trims leading/trailing spaces and still allows a non-empty title through', async () => {
    let fetchCalled = false
    const saveRename = makeSaveRenameMirror(() => {}, () => { fetchCalled = true })

    await saveRename('  Final  ')

    expect(fetchCalled).toBe(true)
  })
})
