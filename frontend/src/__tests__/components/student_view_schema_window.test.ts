/**
 * The curriculum schema must stay reachable while the student answers, not only
 * on the intro sub-view. The questions sub-view mounts the same floating
 * SchemaIntroWindow that PracticeView.vue uses for category schemas; closing it
 * reveals a "Show schema panel" control that mounts it again.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
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
  getExamIntroduction: vi.fn(() =>
    Promise.resolve({ introduction: 'Read the schema.', tables: [{ name: 'employee', columns: [] }] })
  ),
}))
vi.mock('../../services/schemaImages', () => ({
  listExamIntroImages: vi.fn(() => Promise.resolve([])),
}))

import StudentView from '../../components/StudentView.vue'

const QUESTIONS = [{ id: 1, type: 'QUERY', points: 10, requirement: 'a', target_table: 't' }]

function makeFetchMock() {
  return vi.fn().mockImplementation((url: string) => {
    if (typeof url === 'string' && url.includes('/exams?include_inactive')) {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve([
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
    if (typeof url === 'string' && /\/exams\/\d+\/questions$/.test(url)) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(QUESTIONS) })
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
        SchemaIntroWindow: true,
      },
    },
  })
  await flushPromises()

  await wrapper.findAll('button').find(b => b.text() === 'Select')!.trigger('click')
  await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === 'Questions')!.trigger('click')
  await flushPromises()

  return wrapper
}

describe('StudentView — schema panel stays available in the questions sub-view', () => {
  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 'test-token')
    vi.stubGlobal('fetch', makeFetchMock())
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('mounts the floating schema window while answering questions', async () => {
    const wrapper = await mountInQuestionsView()

    const win = wrapper.findComponent({ name: 'SchemaIntroWindow' })
    expect(win.exists()).toBe(true)
    expect(win.props('scope')).toBe('exam')
    expect(win.props('mode')).toBe('student')
    expect(win.props('introduction')).toBe('Read the schema.')
    expect(win.props('imported')).toBe(true)

    wrapper.unmount()
  })

  it('closing the window offers a control that brings it back', async () => {
    const wrapper = await mountInQuestionsView()

    expect(wrapper.find('[data-testid="student-schema-window-reopen"]').exists()).toBe(false)

    await wrapper.findComponent({ name: 'SchemaIntroWindow' }).vm.$emit('close')
    await flushPromises()
    expect(wrapper.findComponent({ name: 'SchemaIntroWindow' }).exists()).toBe(false)

    const reopen = wrapper.find('[data-testid="student-schema-window-reopen"]')
    expect(reopen.exists()).toBe(true)
    await reopen.trigger('click')
    expect(wrapper.findComponent({ name: 'SchemaIntroWindow' }).exists()).toBe(true)

    wrapper.unmount()
  })
})
