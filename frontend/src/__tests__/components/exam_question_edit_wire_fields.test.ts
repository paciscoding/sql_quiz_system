/**
 * Regression tests for two exam-authoring defects:
 *
 *  1. The exam-question wire format serializes the kind as `type`
 *     (models.Question, backend/internal/models/submission.go) — NOT
 *     `question_type`, which is the practice-question field name. The panel
 *     read only `question_type`, so loading a stored DML question into the form
 *     silently showed (and would re-save) it as a QUERY.
 *  2. Dual schema is now always on and hidden from the lecturer: no checkbox,
 *     and every save carries dual_schema: true — including an edit of a
 *     question stored with dual_schema: false.
 *
 * Mount/stub setup copied from exam_questions_seed.test.ts.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../components/SqlEditor.vue', () => ({
  default: {
    name: 'SqlEditor',
    props: ['modelValue', 'placeholder', 'height'],
    emits: ['update:modelValue'],
    template:
      '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
}))

vi.mock('../../components/IntroImagesEditor.vue', () => ({
  default: {
    name: 'IntroImagesEditor',
    props: ['scope', 'ownerId'],
    template: '<div class="stub-intro-images" />',
  },
}))

const mockNotify = vi.fn()
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: mockNotify, notifications: [], remove: vi.fn() }),
}))

vi.mock('../../services/exam', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/exam')>()
  return { ...actual, listExamCategories: vi.fn() }
})

import ExamQuestionsPanel from '../../components/lecturer/ExamQuestionsPanel.vue'
import { listExamCategories } from '../../services/exam'

const EXAM_ID = 'exam-1'

function makeExam() {
  return {
    id: EXAM_ID,
    title: 'Test Exam',
    is_active: false,
    effective_state: 'inactive',
    scheduled_start_at: null as string | null,
    scheduled_end_at: null as string | null,
    activated_at: null as string | null,
    archived_at: null as string | null,
    introduction: '',
    question_count: 0,
    total_points: 0,
    uncaptured_count: 0,
    block_practice_during_exam: false,
    has_preview_schema: false,
  }
}

// Shaped like the real GET /lecturer/exams/:id/questions payload: `type`, not
// `question_type`.
function makeWireQuestion(overrides: Record<string, any> = {}) {
  return {
    id: 42,
    exam_id: EXAM_ID,
    category_id: null,
    target_table: 'employee',
    correct_sql: "DELETE FROM employee WHERE ssn = '1'",
    type: 'DML',
    dml_kind: 'DELETE',
    requirement: 'Delete the employee',
    points: 10,
    dual_schema: false,
    expected_result_status: '',
    ...overrides,
  }
}

async function mountPanel(examQuestionsFixture: any[] = []) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: RequestInfo | URL) => {
      if (typeof url === 'string' && url.includes('/questions')) {
        return { ok: true, json: async () => examQuestionsFixture } as Response
      }
      return { ok: true, json: async () => [] } as Response
    })
  )
  const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
  await flushPromises()
  return wrapper
}

describe('ExamQuestionsPanel — loading a stored question uses the `type` wire field', () => {
  beforeEach(() => {
    vi.mocked(listExamCategories).mockReset().mockResolvedValue([])
    mockNotify.mockClear()
    localStorage.clear()
    localStorage.setItem('sql_quiz_token', 'test-token')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('loads a DML question as DML (not QUERY) from a payload that only has `type`', async () => {
    const wrapper = await mountPanel([makeWireQuestion()])

    await wrapper.find('#exam-question-select').setValue('42')
    await flushPromises()

    expect((wrapper.vm as any).question.type).toBe('DML')
    expect((wrapper.vm as any).question.dml_kind).toBe('DELETE')
    expect((wrapper.find('#exam-question-type').element as HTMLSelectElement).value).toBe('DELETE')
    // DML-only field must appear, and the strict-mode block must not.
    expect(wrapper.find('#exam-question-target-table').exists()).toBe(true)
  })

  it('renders the stored kind in the questions list', async () => {
    const wrapper = await mountPanel([makeWireQuestion()])
    expect(wrapper.find('.questions-list-section').text()).toContain('DELETE')
  })

  it('still honours the practice-style `question_type` key if present', async () => {
    const wrapper = await mountPanel([
      makeWireQuestion({ type: undefined, question_type: 'DML', dml_kind: 'UPDATE' }),
    ])

    await wrapper.find('#exam-question-select').setValue('42')
    await flushPromises()

    expect((wrapper.vm as any).question.type).toBe('DML')
    expect((wrapper.vm as any).question.dml_kind).toBe('UPDATE')
  })
})

describe('ExamQuestionsPanel — dual schema is always on and hidden', () => {
  beforeEach(() => {
    vi.mocked(listExamCategories).mockReset().mockResolvedValue([])
    mockNotify.mockClear()
    localStorage.clear()
    localStorage.setItem('sql_quiz_token', 'test-token')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('renders no dual-schema checkbox', async () => {
    const wrapper = await mountPanel()
    expect(wrapper.text()).not.toContain('Dual schema (hidden grading)')
  })

  it('sends dual_schema: true when saving an edit of a dual_schema:false question', async () => {
    const wrapper = await mountPanel([makeWireQuestion({ dual_schema: false })])

    await wrapper.find('#exam-question-select').setValue('42')
    await flushPromises()
    expect((wrapper.vm as any).question.dual_schema).toBe(true)

    const fetchMock = vi.mocked(fetch)
    fetchMock.mockClear()
    fetchMock.mockImplementation(async (url: RequestInfo | URL) => {
      if (typeof url === 'string' && url.includes('/lecturer/question')) {
        return { ok: true, json: async () => ({ id: 42 }) } as Response
      }
      return { ok: true, json: async () => [] } as Response
    })

    await wrapper.vm.$nextTick()
    await (wrapper.vm as any).saveQuestion()
    await flushPromises()

    const saveCall = fetchMock.mock.calls.find(
      (c) => typeof c[0] === 'string' && (c[0] as string).includes('/lecturer/question')
    )
    const body = JSON.parse((saveCall![1] as RequestInit).body as string)
    expect(body.dual_schema).toBe(true)
    expect(body.type).toBe('DML')
    expect(body.dml_kind).toBe('DELETE')
  })
})
