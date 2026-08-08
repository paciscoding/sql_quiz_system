/**
 * Tests for Task 10 of docs/superpowers/plans/2026-08-08-lecturer-seed-rows.md
 * — the exam-authoring half of the lecturer seed-rows feature.
 *
 * Mount/stub setup copied from ExamQuestionsPanel.layout.test.ts.
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

function makeExamQuestion(overrides: Record<string, any> = {}) {
  return {
    id: 42,
    exam_id: EXAM_ID,
    category_id: null,
    target_table: 'employee',
    correct_sql: "DELETE FROM employee WHERE ssn = '1'",
    question_type: 'DML',
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

describe('ExamQuestionsPanel — four-way type select and seed field (Task 10)', () => {
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

  it('offers exactly QUERY, INSERT, UPDATE, DELETE as selectable options', async () => {
    const wrapper = await mountPanel()

    const options = wrapper.find('#exam-question-type').findAll('option')
    const selectable = options
      .filter((o) => o.attributes('disabled') === undefined)
      .map((o) => o.attributes('value'))
    expect(selectable).toEqual(['QUERY', 'INSERT', 'UPDATE', 'DELETE'])

    // The disabled DML placeholder still exists, for the "loaded question with
    // no dml_kind" case (assertion 6 below).
    const dmlOption = options.find((o) => o.attributes('value') === 'DML')
    expect(dmlOption?.exists()).toBe(true)
    expect(dmlOption?.attributes('disabled')).toBeDefined()
  })

  it('choosing DELETE sets question.type to DML and question.dml_kind to DELETE', async () => {
    const wrapper = await mountPanel()

    await wrapper.find('#exam-question-type').setValue('DELETE')

    expect((wrapper.vm as any).question.type).toBe('DML')
    expect((wrapper.vm as any).question.dml_kind).toBe('DELETE')
  })

  it('choosing QUERY sets question.type to QUERY and clears dml_kind', async () => {
    const wrapper = await mountPanel()

    await wrapper.find('#exam-question-type').setValue('DELETE')
    await wrapper.find('#exam-question-type').setValue('QUERY')

    expect((wrapper.vm as any).question.type).toBe('QUERY')
    expect((wrapper.vm as any).question.dml_kind).toBe('')
  })

  it('shows the seed textarea only for UPDATE and DELETE, not QUERY or INSERT', async () => {
    const wrapper = await mountPanel()
    const select = wrapper.find('#exam-question-type')

    await select.setValue('QUERY')
    expect(wrapper.find('#exam-question-seed-sql').exists()).toBe(false)

    await select.setValue('INSERT')
    expect(wrapper.find('#exam-question-seed-sql').exists()).toBe(false)

    await select.setValue('UPDATE')
    expect(wrapper.find('#exam-question-seed-sql').exists()).toBe(true)

    await select.setValue('DELETE')
    expect(wrapper.find('#exam-question-seed-sql').exists()).toBe(true)
  })

  it('typing a seed then switching to INSERT clears question.seed_sql', async () => {
    const wrapper = await mountPanel()
    const select = wrapper.find('#exam-question-type')

    await select.setValue('DELETE')
    await wrapper.find('#exam-question-seed-sql').setValue("INSERT INTO employee (ssn) VALUES ('1')")
    expect((wrapper.vm as any).question.seed_sql).toBe("INSERT INTO employee (ssn) VALUES ('1')")

    await select.setValue('INSERT')
    expect((wrapper.vm as any).question.seed_sql).toBe('')
  })

  it('loading a DML question with no dml_kind falls back to the disabled DML placeholder', async () => {
    const wrapper = await mountPanel([makeExamQuestion({ dml_kind: undefined })])

    await wrapper.find('#exam-question-select').setValue('42')
    await flushPromises()

    const select = wrapper.find('#exam-question-type').element as HTMLSelectElement
    expect(select.value).toBe('DML')
    expect((wrapper.vm as any).question.type).toBe('DML')
    expect((wrapper.vm as any).question.dml_kind).toBe('')
  })

  it('includes dml_kind and seed_sql in the save payload', async () => {
    const wrapper = await mountPanel()

    await wrapper.find('#exam-question-type').setValue('DELETE')
    await wrapper.find('#exam-question-seed-sql').setValue("INSERT INTO employee (ssn) VALUES ('1')")

    const fetchMock = vi.mocked(fetch)
    fetchMock.mockClear()
    fetchMock.mockImplementation(async (url: RequestInfo | URL) => {
      if (typeof url === 'string' && url.includes('/lecturer/question')) {
        return { ok: true, json: async () => ({ id: 99 }) } as Response
      }
      return { ok: true, json: async () => [] } as Response
    })

    ;(wrapper.vm as any).question.requirement = 'Delete a row'
    ;(wrapper.vm as any).question.correct_sql = "DELETE FROM employee WHERE ssn = '1'"
    // Flush so the form-validation watchers (which mirror question.value.*
    // into questionValidation.values.*) run before saveQuestion validates.
    await wrapper.vm.$nextTick()
    await (wrapper.vm as any).saveQuestion()
    await flushPromises()

    const saveCall = fetchMock.mock.calls.find(
      (c) => typeof c[0] === 'string' && (c[0] as string).includes('/lecturer/question')
    )
    expect(saveCall).toBeTruthy()
    const body = JSON.parse((saveCall![1] as RequestInit).body as string)
    expect(body.dml_kind).toBe('DELETE')
    expect(body.seed_sql).toBe("INSERT INTO employee (ssn) VALUES ('1')")
  })

  it('gates the read-only auto-seed panel on the absence of a lecturer seed', async () => {
    const wrapper = await mountPanel([
      makeExamQuestion({
        dml_kind: 'DELETE',
        auto_seed: { table: 'employee', columns: [{ name: 'ssn', value: '1' }] },
      }),
    ])

    await wrapper.find('#exam-question-select').setValue('42')
    await flushPromises()

    // No lecturer seed_sql on this fixture -> the derived panel shows.
    expect(wrapper.find('.auto-seed-panel').exists()).toBe(true)

    // Once the lecturer types a seed, the derived panel must disappear
    // (ARCH-045: the two are mutually exclusive).
    await wrapper.find('#exam-question-seed-sql').setValue("INSERT INTO employee (ssn) VALUES ('2')")
    expect(wrapper.find('.auto-seed-panel').exists()).toBe(false)
  })
})
