/**
 * The examination view must show the student the result table of their own
 * query when they submit — the same behaviour PracticeView already has
 * (doSubmit awaits doPreview before submitting). Before this, the exam view
 * only ran a preview when the student explicitly pressed "Test Run", so a
 * submit produced a verdict with no visible query output.
 *
 * The preview runs in the read-only sandbox (ValidatePreview -> EvaluatePreview)
 * and persists nothing, so it cannot influence the grade. DML previews are
 * redacted server-side; only row_count comes back, which the template already
 * handles.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

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

// SqlEditor is imported directly by StudentView.vue, so a global stub cannot
// replace it — mock the module with a plain textarea the test can type into.
vi.mock('../../components/SqlEditor.vue', () => ({
  default: defineComponent({
    props: { modelValue: { type: String, default: '' } },
    emits: ['update:modelValue'],
    setup(props, { emit }) {
      return () =>
        h('textarea', {
          class: 'sql-input',
          value: props.modelValue,
          onInput: (e: Event) => emit('update:modelValue', (e.target as HTMLTextAreaElement).value),
        })
    },
  }),
}))

import StudentView from '../../components/StudentView.vue'

const QUESTIONS = [
  { id: 7, type: 'QUERY', points: 10, requirement: 'first', target_table: 't' },
]

const PREVIEW_PAYLOAD = {
  status: 'preview',
  feedback: '',
  rows: [{ id: 1, name: 'ada' }],
  columns: ['id', 'name'],
  row_count: 1,
  truncated: false,
}

function makeFetchMock(calls: string[]) {
  return vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    if (typeof url === 'string') calls.push(`${init?.method ?? 'GET'} ${url}`)

    if (typeof url === 'string' && url.includes('/exams?include_inactive')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve([
          { id: 1, title: 'Exam One', is_active: true, effective_state: 'active', scheduled_start_at: null, scheduled_end_at: null },
        ]),
      })
    }
    if (typeof url === 'string' && /\/exams\/\d+\/questions$/.test(url)) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(QUESTIONS) })
    }
    if (typeof url === 'string' && url.endsWith('/quiz/preview')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(PREVIEW_PAYLOAD) })
    }
    if (typeof url === 'string' && url.endsWith('/quiz/submit')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ status: 'correct', score: 10, feedback: 'ok', is_correct: true }),
      })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
  })
}

async function mountAndAnswer() {
  const wrapper = mount(StudentView, {
    attachTo: document.body,
    global: {
      stubs: {
        EmptyState: true,
        SchemaIntroduction: true,
        Spinner: true,
        SchemaImageWindow: true,
      },
    },
  })
  await flushPromises()

  await wrapper.findAll('button').find(b => b.text() === 'Select')!.trigger('click')
  await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === 'Questions')!.trigger('click')
  await flushPromises()

  // Pick the only question, then type some SQL.
  const select = wrapper.find('select')
  await select.setValue(String(QUESTIONS[0].id))
  await wrapper.find('textarea.sql-input').setValue('SELECT * FROM t')
  await flushPromises()

  return wrapper
}

describe('StudentView submit shows the query result preview (like PracticeView)', () => {
  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 'test-token')
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('previews before submitting so the student sees their own result rows', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', makeFetchMock(calls))

    const wrapper = await mountAndAnswer()

    const submitBtn = wrapper.findAll('button').find(b => b.text().includes('Submit'))
    expect(submitBtn, 'submit button should render').toBeTruthy()
    await submitBtn!.trigger('click')
    await flushPromises()

    const previewCall = calls.findIndex(c => c.includes('/quiz/preview'))
    const submitCall = calls.findIndex(c => c.includes('/quiz/submit'))
    expect(previewCall, 'submitting must trigger a preview').toBeGreaterThanOrEqual(0)
    expect(submitCall, 'submitting must still submit').toBeGreaterThanOrEqual(0)
    expect(previewCall, 'preview must run before the submission is graded').toBeLessThan(submitCall)

    // The preview panel — and the student's own returned rows — must be on screen.
    const text = wrapper.text()
    expect(text).toContain('Test Run')
    expect(text).toContain('ada')

    wrapper.unmount()
  })
})
