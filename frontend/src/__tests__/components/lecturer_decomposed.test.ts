/**
 * Regression tests for the decomposed lecturer UI (TODO-LEX-014).
 *
 * Five scenarios:
 *  1. LecturerView master/detail toggle (show list → open workspace → back to list)
 *  2. ExamCard Activate button disabled when uncaptured_count > 0
 *  3. ExamSchemaPanel 409 confirm-replace retry (non-active 409 → confirm → retry(true))
 *     Also: 409 with "active" in message → no retry
 *  4. LecturerResults drill-down fires /questions + /submissions + /students on exam select
 *  5. Router guard: STUDENT blocked from /lecturer/results; LECTURER and ADMIN allowed
 */

import { mount, shallowMount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../services/practice'
import { useAuth } from '../../services/auth'
import { useConfirm } from '../../services/confirm_service'

// ---------------------------------------------------------------------------
// Module-level mocks — hoisted before imports by Vitest
// ---------------------------------------------------------------------------

// SqlEditor wraps CodeMirror (browser-only DOM APIs).  Replace with a plain
// textarea so tests run in happy-dom without needing a full browser environment.
vi.mock('../../components/SqlEditor.vue', () => ({
  default: {
    name: 'SqlEditor',
    props: ['modelValue', 'placeholder', 'height'],
    emits: ['update:modelValue'],
    template:
      '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
}))

// importExamCurriculum is the network boundary for test 3.  Keep the rest of
// the exam service real so the mock doesn't accidentally hide type errors.
vi.mock('../../services/exam', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/exam')>()
  return { ...actual, importExamCurriculum: vi.fn() }
})

// ExamQuestionsPanel (~600 lines) and ExamRosterPanel both pull in heavy child
// components.  Stub them at module level so shallowMount + full-mount of
// LecturerView / ExamWorkspace remain fast and dependency-free.
vi.mock('../../components/lecturer/ExamQuestionsPanel.vue', () => ({
  default: {
    name: 'ExamQuestionsPanel',
    props: ['exam'],
    emits: ['changed'],
    template: '<div class="stub-questions-panel" />',
  },
}))
vi.mock('../../components/lecturer/ExamRosterPanel.vue', () => ({
  default: {
    name: 'ExamRosterPanel',
    props: ['exam'],
    emits: ['changed'],
    template: '<div class="stub-roster-panel" />',
  },
}))

// ---------------------------------------------------------------------------
// Import components AFTER mocks are hoisted
// ---------------------------------------------------------------------------
import LecturerView from '../../components/lecturer/LecturerView.vue'
import ExamCard from '../../components/lecturer/ExamCard.vue'
import ExamSchemaPanel from '../../components/lecturer/ExamSchemaPanel.vue'
import LecturerResults from '../../components/lecturer/LecturerResults.vue'
import router from '../../router'
import { importExamCurriculum } from '../../services/exam'

// ---------------------------------------------------------------------------
// Shared fixture helpers
// ---------------------------------------------------------------------------

const EXAM_ID = 'exam-42'

function makeExam(overrides: Partial<{
  id: string
  title: string
  is_active: boolean
  effective_state: string
  uncaptured_count: number
  question_count: number
  total_points: number
  introduction: string
  block_practice_during_exam: boolean
}> = {}) {
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
    question_count: 3,
    total_points: 30,
    uncaptured_count: 0,
    block_practice_during_exam: false,
    ...overrides,
  }
}

/** Build a vi.fn() fetch that routes by URL substring. */
function buildFetchMock(routes: Array<[string, unknown]>): ReturnType<typeof vi.fn> {
  return vi.fn().mockImplementation(async (url: string) => {
    for (const [pattern, data] of routes) {
      if (url.includes(pattern)) {
        return { ok: true, json: async () => data } as Response
      }
    }
    // Default: return empty array (safe for any list endpoint)
    return { ok: true, json: async () => [] } as Response
  })
}

// =============================================================================
// 1. LecturerView — master/detail toggle
// =============================================================================

describe('LecturerView — master/detail toggle (TODO-LEX-011)', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = buildFetchMock([['/lecturer/exams', [makeExam()]]])
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders ExamListPanel initially (selectedExamId === null)', async () => {
    const wrapper = shallowMount(LecturerView)
    await flushPromises()

    expect(wrapper.findComponent({ name: 'ExamListPanel' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'ExamWorkspace' }).exists()).toBe(false)
  })

  it('hides ExamListPanel and shows ExamWorkspace when list panel emits open', async () => {
    const wrapper = shallowMount(LecturerView)
    await flushPromises()

    // Emit 'open' from the auto-generated ExamListPanel stub
    await wrapper.findComponent({ name: 'ExamListPanel' }).vm.$emit('open', EXAM_ID)
    await flushPromises()

    expect(wrapper.findComponent({ name: 'ExamListPanel' }).exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'ExamWorkspace' }).exists()).toBe(true)
  })

  it('returns to ExamListPanel when ExamWorkspace emits back', async () => {
    const wrapper = shallowMount(LecturerView)
    await flushPromises()

    // Navigate into workspace
    await wrapper.findComponent({ name: 'ExamListPanel' }).vm.$emit('open', EXAM_ID)
    await flushPromises()
    expect(wrapper.findComponent({ name: 'ExamWorkspace' }).exists()).toBe(true)

    // Emit 'back' from the workspace stub
    await wrapper.findComponent({ name: 'ExamWorkspace' }).vm.$emit('back')
    await flushPromises()

    expect(wrapper.findComponent({ name: 'ExamListPanel' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'ExamWorkspace' }).exists()).toBe(false)
  })
})

// =============================================================================
// 2. ExamCard — Activate button disabled when uncaptured_count > 0
// =============================================================================

describe('ExamCard — Activate button disabled when uncaptured_count > 0 (TODO-LEX-004)', () => {
  it('Activate button has disabled attribute when uncaptured_count = 2', () => {
    const exam = makeExam({ uncaptured_count: 2, effective_state: 'inactive' })
    const wrapper = mount(ExamCard, { props: { exam } })

    // The Activate button is inside the activation-controls block (shown for
    // inactive exams).  It carries :disabled="actionLoading || (exam.uncaptured_count ?? 0) > 0".
    const activateBtn = wrapper.findAll('button').find(b => b.text().trim() === 'Activate')
    expect(activateBtn).toBeDefined()
    expect((activateBtn!.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('Activate button is enabled when uncaptured_count = 0', () => {
    const exam = makeExam({ uncaptured_count: 0, effective_state: 'inactive' })
    const wrapper = mount(ExamCard, { props: { exam } })

    const activateBtn = wrapper.findAll('button').find(b => b.text().trim() === 'Activate')
    expect(activateBtn).toBeDefined()
    expect((activateBtn!.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('Activate controls are hidden entirely when exam.effective_state === active', () => {
    // Active exam shows Deactivate controls, not Activate
    const exam = makeExam({ is_active: true, effective_state: 'active' })
    const wrapper = mount(ExamCard, { props: { exam } })

    const activateBtn = wrapper.findAll('button').find(b => b.text().trim() === 'Activate')
    expect(activateBtn).toBeUndefined()
  })
})

// =============================================================================
// 3. ExamSchemaPanel — 409 confirm-replace retry
// =============================================================================

describe('ExamSchemaPanel — 409 confirm-replace retry (TODO-LEX-005)', () => {
  const importMock = vi.mocked(importExamCurriculum)

  beforeEach(() => {
    importMock.mockReset()
  })

  afterEach(() => {
    // Discard any pending confirm dialog so subsequent tests start clean
    useConfirm()._resolveCurrent(false)
  })

  async function mountSchema(exam = makeExam()) {
    const wrapper = mount(ExamSchemaPanel, { props: { exam } })
    // Set SQL content via the mocked textarea (replaces CodeMirror SqlEditor)
    await wrapper.find('textarea').setValue('CREATE TABLE test_tbl (id INT);')
    return wrapper
  }

  it('calls importExamCurriculum twice and the second call has confirmReplace=true when user confirms a non-active 409', async () => {
    importMock
      .mockRejectedValueOnce(new ApiError(409, 'schema already has tables'))
      .mockResolvedValueOnce({ message: 'Curriculum replaced' })

    const wrapper = await mountSchema()

    // Click Import — triggers doImport()
    await wrapper.find('button').trigger('click')
    await flushPromises()

    // At this point the catch block has created a pending confirm dialog.
    // Resolve it as "yes" (user confirmed the replace)
    useConfirm()._resolveCurrent(true)
    await flushPromises()

    expect(importMock).toHaveBeenCalledTimes(2)
    // The second call must pass confirmReplace = true as the third argument
    expect(importMock).toHaveBeenNthCalledWith(
      2,
      EXAM_ID,
      'CREATE TABLE test_tbl (id INT);',
      true,
    )
  })

  it('does NOT retry when user cancels the confirm dialog after a non-active 409', async () => {
    importMock.mockRejectedValueOnce(new ApiError(409, 'schema already has tables'))

    const wrapper = await mountSchema()

    await wrapper.find('button').trigger('click')
    await flushPromises()

    // User cancels
    useConfirm()._resolveCurrent(false)
    await flushPromises()

    // Only the first call happened
    expect(importMock).toHaveBeenCalledTimes(1)
  })

  it('does NOT show a confirm and does NOT retry when 409 message indicates the exam is active', async () => {
    // When the server says "active", retrying with confirmReplace=true would still 409.
    // The component should surface the server error directly, no confirm dialog.
    importMock.mockRejectedValueOnce(
      new ApiError(409, 'cannot import curriculum for an active exam'),
    )

    const wrapper = await mountSchema()

    await wrapper.find('button').trigger('click')
    await flushPromises()

    // No confirm was registered — state.current is null
    expect(useConfirm().state.current).toBeNull()
    // Only one call was made
    expect(importMock).toHaveBeenCalledTimes(1)
  })
})

// =============================================================================
// 4. LecturerResults — drill-down fires three fetches when exam is selected
// =============================================================================

describe('LecturerResults — drill-down fetches on exam select (TODO-LEX-010)', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = buildFetchMock([
      // Exam list for the drill-down <select>
      ['/lecturer/exams', [makeExam()]],
      // Consolidated scores endpoint
      ['/lecturer/scores', []],
      // Per-exam max scores (called inside fetchScores)
      ['/exams/max-scores', { max_scores: {} }],
      // Drill-down responses (matched after exam select)
      ['/questions', []],
      ['/submissions', []],
      ['/students', { students: [] }],
    ])
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('fires /questions, /submissions and /students for the selected exam id', async () => {
    const wrapper = mount(LecturerResults)
    // Wait for onMounted to complete (fetchExams + fetchScores)
    await flushPromises()

    // The <select> should now have an option for EXAM_ID (rendered by v-for over exams)
    const select = wrapper.find('select')
    expect(select.exists()).toBe(true)

    // Simulate the lecturer choosing an exam from the drill-down select
    await select.setValue(String(EXAM_ID))
    await flushPromises()

    const calledUrls = fetchMock.mock.calls.map((call) => call[0] as string)

    expect(calledUrls.some((u) => u.includes(`/exams/${EXAM_ID}/questions`))).toBe(true)
    expect(calledUrls.some((u) => u.includes(`/exams/${EXAM_ID}/submissions`))).toBe(true)
    expect(calledUrls.some((u) => u.includes(`/exams/${EXAM_ID}/students`))).toBe(true)
  })

  it('does NOT fire drill fetches before an exam is selected', async () => {
    mount(LecturerResults)
    await flushPromises()

    const calledUrls = fetchMock.mock.calls.map((call) => call[0] as string)

    // Initial load fires /lecturer/exams, /lecturer/scores, and /exams/max-scores
    // but none of the per-exam drill URLs
    expect(calledUrls.some((u) => /\/exams\/\d+\/questions/.test(u))).toBe(false)
    expect(calledUrls.some((u) => /\/exams\/\d+\/submissions/.test(u))).toBe(false)
    expect(calledUrls.some((u) => /\/exams\/\d+\/students/.test(u))).toBe(false)
  })
})

// =============================================================================
// 5. Router guard — role-based access to /lecturer/results
// =============================================================================

describe('Router guard — /lecturer/results access control (TODO-LEX-012)', () => {
  beforeEach(async () => {
    useAuth().logout()
    localStorage.clear()
    // Reset router to a neutral position (the catch-all not-found route)
    await router.push('/__reset__')
  })

  it('redirects STUDENT to /403 when navigating to /lecturer/results', async () => {
    useAuth().login('student-token', { id: '2', username: 'student1', role: 'STUDENT' })

    await router.push('/lecturer/results')

    expect(router.currentRoute.value.path).toBe('/403')
    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('allows LECTURER to reach /lecturer/results (route meta allowedRoles includes LECTURER)', async () => {
    useAuth().login('lecturer-token', { id: '3', username: 'lecturer1', role: 'LECTURER' })

    await router.push('/lecturer/results')

    expect(router.currentRoute.value.name).toBe('lecturer-results')
    expect(router.currentRoute.value.path).toBe('/lecturer/results')
  })

  it('allows ADMIN to reach /lecturer/results (ADMIN wildcard, adminBlocked not set)', async () => {
    useAuth().login('admin-token', { id: '1', username: 'admin1', role: 'ADMIN' })

    await router.push('/lecturer/results')

    expect(router.currentRoute.value.name).toBe('lecturer-results')
    expect(router.currentRoute.value.path).toBe('/lecturer/results')
  })
})
