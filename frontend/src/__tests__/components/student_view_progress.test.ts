/**
 * Tests for TODO-076 and TODO-087
 * Tests the progressMap seeding logic and answeredCount/correctCount/progressPct computeds.
 * We mirror the exact logic from StudentView.vue rather than mounting the full component.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ref, computed, type Ref } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

// ─── Mocks for mounting the real StudentView.vue ─────────────────────────────
// (only needed by the "active-phase question picker labels" describe block below)
vi.mock('../../composables/useExamStream', () => ({
  useExamStream: () => ({
    effectiveState: { value: 'active' },
    connected: { value: true },
    close: vi.fn(),
  }),
}))
vi.mock('../../composables/useKeyboardShortcut', () => ({
  useKeyboardShortcut: vi.fn(),
}))
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: vi.fn() }),
}))
vi.mock('../../services/error_handler', () => ({
  handleApiError: vi.fn(),
}))
vi.mock('../../services/exam', () => ({
  getExamIntroduction: vi.fn(() => Promise.resolve({ introduction: '', tables: [] })),
}))
vi.mock('../../services/schemaImages', () => ({
  listExamIntroImages: vi.fn(() => Promise.resolve([])),
}))

// Mirror the exact progressMap + answerStatus merge logic from StudentView.vue
function makeProgressComputeds(
  questions: Ref<any[]>,
  progressMap: Ref<Record<number, { answered: boolean; correct: boolean }>>,
  answerStatus: Ref<Record<number, boolean>>
) {
  const answeredCount = computed(() => {
    const merged = { ...progressMap.value }
    for (const [k, v] of Object.entries(answerStatus.value)) {
      merged[Number(k)] = { answered: true, correct: v }
    }
    return Object.values(merged).filter(v => v.answered).length
  })

  const correctCount = computed(() => {
    const merged = { ...progressMap.value }
    for (const [k, v] of Object.entries(answerStatus.value)) {
      merged[Number(k)] = { answered: true, correct: v }
    }
    return Object.values(merged).filter(v => v.correct).length
  })

  const progressPct = computed(() =>
    questions.value.length === 0
      ? 0
      : Math.round((answeredCount.value / questions.value.length) * 100)
  )

  return { answeredCount, correctCount, progressPct }
}

const QUESTIONS = [
  { id: 1, type: 'QUERY', points: 10, requirement: 'Q1' },
  { id: 2, type: 'QUERY', points: 10, requirement: 'Q2' },
  { id: 3, type: 'QUERY', points: 10, requirement: 'Q3' },
  { id: 4, type: 'QUERY', points: 10, requirement: 'Q4' },
]

describe('StudentView progress strip logic (TODO-076, TODO-087)', () => {
  it('returns 0% when no questions', () => {
    const questions = ref<any[]>([])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({})
    const answerStatus = ref<Record<number, boolean>>({})
    const { progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(progressPct.value).toBe(0)
  })

  it('returns 0 answered/correct with empty progressMap and no answerStatus', () => {
    const questions = ref([...QUESTIONS])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({})
    const answerStatus = ref<Record<number, boolean>>({})
    const { answeredCount, correctCount } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(0)
    expect(correctCount.value).toBe(0)
  })

  it('seeds progressMap from server data (answered=true, correct=true)', () => {
    const questions = ref([...QUESTIONS])
    // Simulate server seeding 2 answered questions (both correct)
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
      2: { answered: true, correct: false },
    })
    const answerStatus = ref<Record<number, boolean>>({})
    const { answeredCount, correctCount, progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(2)
    expect(correctCount.value).toBe(1)
    expect(progressPct.value).toBe(50) // 2/4 = 50%
  })

  it('answerStatus overrides progressMap for the same question', () => {
    const questions = ref([...QUESTIONS])
    // Server says question 1 was incorrect
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: false },
    })
    // Local answer status says question 1 is now correct (just submitted)
    const answerStatus = ref<Record<number, boolean>>({ 1: true })
    const { correctCount } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(correctCount.value).toBe(1)
  })

  it('calculates 100% when all questions answered', () => {
    const questions = ref([...QUESTIONS])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
      2: { answered: true, correct: true },
      3: { answered: true, correct: false },
      4: { answered: true, correct: false },
    })
    const answerStatus = ref<Record<number, boolean>>({})
    const { answeredCount, progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(4)
    expect(progressPct.value).toBe(100)
  })

  it('progressPct rounds to nearest integer', () => {
    // 1 out of 3 questions answered = 33.33% -> rounds to 33
    const questions = ref([
      { id: 1, type: 'QUERY', points: 10, requirement: 'Q1' },
      { id: 2, type: 'QUERY', points: 10, requirement: 'Q2' },
      { id: 3, type: 'QUERY', points: 10, requirement: 'Q3' },
    ])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
    })
    const answerStatus = ref<Record<number, boolean>>({})
    const { progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(progressPct.value).toBe(33)
  })

  it('merges new answerStatus entries with existing progressMap', () => {
    const questions = ref([...QUESTIONS])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
    })
    // Add a new answer for question 3
    const answerStatus = ref<Record<number, boolean>>({ 3: true })
    const { answeredCount, correctCount } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(2)
    expect(correctCount.value).toBe(2)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// UX-consistency bug: the active-phase question picker <option> labels must use
// the sequential 1-based position (matching the "Question X of N" progress line),
// NOT the raw non-contiguous database id. The <option> VALUE must stay the q.id.
// ─────────────────────────────────────────────────────────────────────────────

import StudentView from '../../components/StudentView.vue'

// Non-sequential DB ids to expose the bug (position != id).
const NONSEQ_QUESTIONS = [
  { id: 7, type: 'QUERY', points: 10, requirement: 'first', target_table: 't' },
  { id: 2, type: 'DDL', points: 5, requirement: 'second', target_table: 't' },
  { id: 15, type: 'QUERY', points: 20, requirement: 'third', target_table: 't' },
]

function makeStudentFetchMock() {
  return vi.fn().mockImplementation((url: string) => {
    // onMounted: enrolled exams list — one active exam
    if (typeof url === 'string' && url.includes('/exams?include_inactive')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve([
          {
            id: 1,
            title: 'Exam One',
            is_active: true,
            effective_state: 'active',
            scheduled_start_at: null,
            scheduled_end_at: null,
          },
        ]),
      })
    }
    // fetchQuestions
    if (typeof url === 'string' && /\/exams\/\d+\/questions$/.test(url)) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(NONSEQ_QUESTIONS) })
    }
    // watch(selectedExamId): prior results seeding
    if (typeof url === 'string' && /\/results\/\d+$/.test(url)) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
  })
}

async function mountInQuestionsView() {
  const wrapper = mount(StudentView, {
    attachTo: document.body,
    global: {
      stubs: {
        SqlEditor: true,
        EmptyState: true,
        ResultTable: true,
        SchemaIntroduction: true,
        Spinner: true,
        SchemaImageWindow: true,
      },
    },
  })
  await flushPromises()

  // SELECT phase → click the exam's "Select" button (effective_state 'active' → active phase)
  const selectBtn = wrapper.findAll('button').find(b => b.text() === 'Select')
  expect(selectBtn, 'Select button should render in select phase').toBeTruthy()
  await selectBtn!.trigger('click')
  await flushPromises()

  // ACTIVE phase defaults to the 'intro' sub-view → switch to the 'questions' sub-view
  const questionsBtn = wrapper.findAll('button').find(b => b.text() === 'Questions')
  expect(questionsBtn, 'Questions toggle should render in active phase').toBeTruthy()
  await questionsBtn!.trigger('click')
  await flushPromises()

  return wrapper
}

describe('StudentView active-phase question picker labels', () => {
  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 'test-token')
    vi.stubGlobal('fetch', makeStudentFetchMock())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('numbers <option> labels by sequential 1-based position, not the raw DB id', async () => {
    const wrapper = await mountInQuestionsView()

    // The picker <select> should now be rendered with a placeholder + 3 questions
    const options = wrapper.findAll('select option')
    // First option is the "-- Select question --" placeholder; the rest are questions
    const questionOptions = options.slice(1)
    expect(questionOptions).toHaveLength(NONSEQ_QUESTIONS.length)

    const labels = questionOptions.map(o => o.text().trim())

    // Labels must use sequential positions Q1/Q2/Q3 (consistent with "Question X of N")…
    expect(labels[0]).toContain('Q1')
    expect(labels[1]).toContain('Q2')
    expect(labels[2]).toContain('Q3')

    // …and must NOT surface the raw non-contiguous ids Q7 / Q15.
    const joined = labels.join(' | ')
    expect(joined).not.toContain('Q7')
    expect(joined).not.toContain('Q15')

    // Sanity: type/points metadata still present.
    expect(labels[0]).toContain('QUERY')
    expect(labels[0]).toContain('10 pts')

    wrapper.unmount()
  })

  it('keeps each <option> value bound to the question id (selection logic unchanged)', async () => {
    const wrapper = await mountInQuestionsView()

    const questionOptions = wrapper.findAll('select option').slice(1)
    const values = questionOptions.map(o => (o.element as HTMLOptionElement).value)

    // Values remain the raw ids so v-model / submit / preview keep working.
    expect(values).toEqual(NONSEQ_QUESTIONS.map(q => String(q.id)))

    wrapper.unmount()
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Performance / reduced-motion: the progress-bar fill must be driven by a
// GPU-friendly transform (scaleX) rather than transitioning `width`, which
// triggers layout every frame. The inline style on `.qp-fill` must therefore
// use `transform: scaleX(...)` and must NOT use a percentage `width`.
// ─────────────────────────────────────────────────────────────────────────────
describe('StudentView progress-bar fill uses transform, not width', () => {
  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 'test-token')
    vi.stubGlobal('fetch', makeStudentFetchMock())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('drives the .qp-fill via a scaleX transform rather than a percentage width', async () => {
    const wrapper = await mountInQuestionsView()

    const fill = wrapper.find('.qp-fill')
    expect(fill.exists(), '.qp-fill should render in the questions sub-view').toBe(true)

    const style = fill.attributes('style') ?? ''

    // GPU-friendly transform binding…
    expect(style).toContain('transform')
    expect(style).toContain('scaleX(')

    // …and NOT a layout-triggering percentage width binding.
    expect(style).not.toMatch(/width\s*:\s*\d/)

    wrapper.unmount()
  })
})
