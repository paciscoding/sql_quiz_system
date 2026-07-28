/**
 * Tests for TODO-081/082 (ARCH-034 (3) always-rendered Exam Categories lower
 * panel on ExamQuestionsPanel.vue, driven by the ARCH-029 service wrappers in
 * services/exam.ts, with ARCH-036's exam-category delete semantics).
 *
 * Conventions borrowed from lecturer_decomposed.test.ts:
 *  - SqlEditor.vue is mocked as a plain textarea (CodeMirror needs a real
 *    browser, not happy-dom).
 *  - notification_service is mocked with a bare notify spy so 409 messages
 *    can be asserted without depending on toast auto-dismiss timers.
 *  - confirm_service is the REAL singleton; dialogs are resolved with
 *    useConfirm()._resolveCurrent(...).
 *  - services/exam is partially mocked (importOriginal + override) so the
 *    category wrappers are spies while unrelated exports (setExamIntroduction,
 *    importExamCurriculumPreview) stay real.
 *
 * Scenarios covered (done_when for TODO-081/082):
 *  1. Lower "Exam Categories" section renders on first paint, no interaction.
 *  2. Question-authoring (upper) section precedes the categories (lower) section.
 *  3. Categories list from listExamCategories, one CollapsibleCard each.
 *  4. Create / rename / delete (empty category) via the ARCH-029 wrappers.
 *  5. ARCH-036: delete 409 surfaces the attached-question count, no force/detach retry.
 *  6. Import schema + import decoy schema, both with the 409 confirm-replace retry.
 *  7. Save a category description via setExamCategoryIntroduction.
 *  8. "exam-level schema" badge shown only when owns_schema is false.
 *  9. No intro-images editor anywhere in the categories panel.
 *  10. Question form Category <select>: options, category_id in the save body,
 *      "None" posts null, and loading an existing question preselects it.
 *  11. FIX-A (validator gap): "Preview correct SQL" and "Preview as Student"
 *      send the selected question's category_id to /api/v1/lecturer/preview
 *      so the preview runs against the category's own schema, and omit the
 *      field entirely (not null) for "None (exam-level schema)".
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../services/practice'
import { useConfirm } from '../../services/confirm_service'
import type { ExamCategory } from '../../services/exam'

// ---------------------------------------------------------------------------
// Module-level mocks — hoisted before imports by Vitest
// ---------------------------------------------------------------------------

vi.mock('../../components/SqlEditor.vue', () => ({
  default: {
    name: 'SqlEditor',
    props: ['modelValue', 'placeholder', 'height'],
    emits: ['update:modelValue'],
    template:
      '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
}))

// Exam categories have NO intro-images sub-panel (ARCH-026 (6) / ARCH-030).
// Stubbing IntroImagesEditor lets us assert exactly how many instances exist
// (and with what scope) without running its own fetch-on-mount logic.
vi.mock('../../components/IntroImagesEditor.vue', () => ({
  default: {
    name: 'IntroImagesEditor',
    props: ['scope', 'ownerId'],
    template: '<div class="stub-intro-images" :data-scope="scope" :data-owner-id="ownerId" />',
  },
}))

const mockNotify = vi.fn()
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: mockNotify, notifications: [], remove: vi.fn() }),
}))

// Keep the rest of services/exam real (setExamIntroduction, importExamCurriculumPreview,
// etc.) so the mock doesn't accidentally hide type errors — only the category
// wrappers are replaced with spies.
vi.mock('../../services/exam', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/exam')>()
  return {
    ...actual,
    listExamCategories: vi.fn(),
    createExamCategory: vi.fn(),
    renameExamCategory: vi.fn(),
    deleteExamCategory: vi.fn(),
    importExamCategorySchema: vi.fn(),
    importExamCategoryPreviewSchema: vi.fn(),
    setExamCategoryIntroduction: vi.fn(),
  }
})

// ---------------------------------------------------------------------------
// Import components AFTER mocks are hoisted
// ---------------------------------------------------------------------------
import ExamQuestionsPanel from '../../components/lecturer/ExamQuestionsPanel.vue'
import {
  listExamCategories,
  createExamCategory,
  renameExamCategory,
  deleteExamCategory,
  importExamCategorySchema,
  importExamCategoryPreviewSchema,
  setExamCategoryIntroduction,
} from '../../services/exam'

const EXAM_ID = 'exam-1'

function makeExam(overrides: Partial<{
  id: string
  has_preview_schema: boolean
  introduction: string
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
    question_count: 0,
    total_points: 0,
    uncaptured_count: 0,
    block_practice_during_exam: false,
    has_preview_schema: false,
    ...overrides,
  }
}

function makeCategory(overrides: Partial<ExamCategory> = {}): ExamCategory {
  return {
    id: 'cat-1',
    exam_id: EXAM_ID,
    name: 'Part A',
    schema_name: 'exam_cat_1',
    preview_schema_name: '',
    has_preview_schema: false,
    owns_schema: true,
    introduction: '',
    question_count: 0,
    ...overrides,
  }
}

/** Build a vi.fn() fetch that routes GET-ish reads by URL substring. */
function buildFetchMock(routes: Array<[string, unknown]>): ReturnType<typeof vi.fn> {
  return vi.fn().mockImplementation(async (url: string) => {
    for (const [pattern, data] of routes) {
      if (url.includes(pattern)) {
        return { ok: true, json: async () => data } as Response
      }
    }
    return { ok: true, json: async () => [] } as Response
  })
}

function inputGroupByLabel(wrapper: ReturnType<typeof mount>, label: string) {
  const group = wrapper.findAll('.input-group').find(g => g.find('label').text() === label)
  if (!group) throw new Error(`No .input-group found with label "${label}"`)
  return group
}

describe('ExamQuestionsPanel — Exam Categories lower panel (ARCH-034 (3) / ARCH-029 / ARCH-036)', () => {
  let listMock: ReturnType<typeof vi.mocked<typeof listExamCategories>>
  let createMock: ReturnType<typeof vi.mocked<typeof createExamCategory>>
  let renameMock: ReturnType<typeof vi.mocked<typeof renameExamCategory>>
  let deleteMock: ReturnType<typeof vi.mocked<typeof deleteExamCategory>>
  let importMock: ReturnType<typeof vi.mocked<typeof importExamCategorySchema>>
  let importPreviewMock: ReturnType<typeof vi.mocked<typeof importExamCategoryPreviewSchema>>
  let introMock: ReturnType<typeof vi.mocked<typeof setExamCategoryIntroduction>>
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    listMock = vi.mocked(listExamCategories)
    createMock = vi.mocked(createExamCategory)
    renameMock = vi.mocked(renameExamCategory)
    deleteMock = vi.mocked(deleteExamCategory)
    importMock = vi.mocked(importExamCategorySchema)
    importPreviewMock = vi.mocked(importExamCategoryPreviewSchema)
    introMock = vi.mocked(setExamCategoryIntroduction)

    listMock.mockReset().mockResolvedValue([])
    createMock.mockReset()
    renameMock.mockReset()
    deleteMock.mockReset()
    importMock.mockReset()
    importPreviewMock.mockReset()
    introMock.mockReset()
    mockNotify.mockClear()

    fetchMock = buildFetchMock([])
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem('sql_quiz_token', 'test-token')
  })

  afterEach(() => {
    // Discard any pending confirm dialog so subsequent tests start clean.
    useConfirm()._resolveCurrent(false)
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  // ---------------------------------------------------------------------------
  // 1 & 2. Always rendered, on first paint, upper before lower
  // ---------------------------------------------------------------------------

  it('renders the Exam Categories section on first paint with no interaction, after the question-authoring section', async () => {
    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const upper = wrapper.find('.authoring-section--upper')
    const lower = wrapper.find('.authoring-section--lower')
    expect(upper.exists()).toBe(true)
    expect(lower.exists()).toBe(true)
    expect(wrapper.find('#exam-categories-heading').text()).toBe('Exam Categories')

    const html = wrapper.html()
    expect(html.indexOf('authoring-section--upper')).toBeLessThan(html.indexOf('authoring-section--lower'))

    expect(listMock).toHaveBeenCalledWith(EXAM_ID)
  })

  it('renders "No categories yet" when the exam has no categories', async () => {
    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    expect(wrapper.find('.authoring-section--lower').text()).toContain('No categories yet')
  })

  // ---------------------------------------------------------------------------
  // 3. Lists categories as CollapsibleCards
  // ---------------------------------------------------------------------------

  it('lists categories from listExamCategories, one CollapsibleCard per category', async () => {
    listMock.mockResolvedValue([
      makeCategory({ id: 'cat-1', name: 'Part A' }),
      makeCategory({ id: 'cat-2', name: 'Part B' }),
    ])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const cards = wrapper.findAll('.collapsible-card')
    expect(cards).toHaveLength(2)
    expect(wrapper.find('[data-testid="exam-category-cat-1"]').text()).toContain('Part A')
    expect(wrapper.find('[data-testid="exam-category-cat-2"]').text()).toContain('Part B')
  })

  // Regression test for TODO-078, through a REAL consumer's v-for (not
  // CollapsibleCard mounted in isolation, which is all CollapsibleCard.test.ts
  // covers). uidCounter previously lived inside CollapsibleCard's
  // <script setup>, so it was reset to 0 on every instance and every card in
  // this v-for rendered the identical body id — every button's aria-controls
  // then resolved to the FIRST card's body regardless of which button was
  // clicked. Asserting id uniqueness alone would not catch a component that
  // hard-wired aria-controls to a constant string, so this asserts that EACH
  // card's button aria-controls equals THAT card's own body id specifically,
  // across N=3 real categories rendered by ExamQuestionsPanel's v-for.
  it('gives each of N real category CollapsibleCards a distinct body id, with aria-controls wired to its own body (TODO-078 regression through a real v-for consumer)', async () => {
    listMock.mockResolvedValue([
      makeCategory({ id: 'cat-1', name: 'Part A' }),
      makeCategory({ id: 'cat-2', name: 'Part B' }),
      makeCategory({ id: 'cat-3', name: 'Part C' }),
    ])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() }, attachTo: document.body })
    await flushPromises()

    const cardTestIds = ['exam-category-cat-1', 'exam-category-cat-2', 'exam-category-cat-3']
    const bodyIds = new Set<string>()

    for (const testId of cardTestIds) {
      const card = wrapper.find(`[data-testid="${testId}"]`)
      expect(card.exists()).toBe(true)

      const button = card.find('.collapsible-card__toggle')
      const body = card.find('.collapsible-card__body')
      expect(button.exists()).toBe(true)
      expect(body.exists()).toBe(true)

      const bodyId = body.attributes('id')
      expect(bodyId).toBeTruthy()
      // The decisive assertion: THIS card's button points at THIS card's own
      // body, not merely "some" body id shared across the v-for.
      expect(button.attributes('aria-controls')).toBe(bodyId)

      bodyIds.add(bodyId as string)
    }

    expect(bodyIds.size).toBe(cardTestIds.length)

    wrapper.unmount()
  })

  // ---------------------------------------------------------------------------
  // 4. Create / rename / delete (empty category)
  // ---------------------------------------------------------------------------

  it('creates a category via createExamCategory and refreshes the list', async () => {
    listMock
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([makeCategory({ id: 'cat-9', name: 'New Cat' })])
    createMock.mockResolvedValue(makeCategory({ id: 'cat-9', name: 'New Cat' }))

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    await wrapper.find('input[placeholder="New category name"]').setValue('New Cat')
    const createBtn = wrapper.findAll('button').find(b => b.text() === 'New Category')!
    await createBtn.trigger('click')
    await flushPromises()

    expect(createMock).toHaveBeenCalledWith(EXAM_ID, 'New Cat')
    expect(listMock).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="exam-category-cat-9"]').exists()).toBe(true)
  })

  it('renames a category via renameExamCategory using window.prompt', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])
    renameMock.mockResolvedValue({ message: 'Category renamed' })
    vi.stubGlobal('prompt', vi.fn().mockReturnValue('Part A2'))

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    const renameBtn = card.findAll('button').find(b => b.text() === 'Rename')!
    await renameBtn.trigger('click')
    await flushPromises()

    expect(renameMock).toHaveBeenCalledWith(EXAM_ID, 'cat-1', 'Part A2')
  })

  it('deletes an empty category via deleteExamCategory after the confirm dialog is accepted', async () => {
    listMock
      .mockResolvedValueOnce([makeCategory({ id: 'cat-1', name: 'Part A' })])
      .mockResolvedValueOnce([])
    deleteMock.mockResolvedValue({ message: 'Category deleted' })

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    const deleteBtn = card.findAll('button').find(b => b.text() === 'Delete')!
    await deleteBtn.trigger('click')
    await flushPromises()

    useConfirm()._resolveCurrent(true)
    await flushPromises()

    expect(deleteMock).toHaveBeenCalledWith(EXAM_ID, 'cat-1')
    expect(listMock).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="exam-category-cat-1"]').exists()).toBe(false)
  })

  it('does NOT call deleteExamCategory when the confirm dialog is cancelled', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    const deleteBtn = card.findAll('button').find(b => b.text() === 'Delete')!
    await deleteBtn.trigger('click')
    await flushPromises()

    useConfirm()._resolveCurrent(false)
    await flushPromises()

    expect(deleteMock).not.toHaveBeenCalled()
  })

  // ---------------------------------------------------------------------------
  // 5. ARCH-036 — 409 has-questions delete semantics, NO force/detach retry
  // ---------------------------------------------------------------------------

  it('ARCH-036: a 409 on delete surfaces the attached-question count and offers NO force/detach retry', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A', question_count: 3 })])
    deleteMock.mockRejectedValue(
      new ApiError(
        409,
        'exam category still has attached questions: 3 question(s) still attached to this category'
      )
    )

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    const deleteBtn = card.findAll('button').find(b => b.text() === 'Delete')!
    await deleteBtn.trigger('click')
    await flushPromises()

    // Confirm the initial "Delete this category?" dialog.
    useConfirm()._resolveCurrent(true)
    await flushPromises()

    expect(deleteMock).toHaveBeenCalledWith(EXAM_ID, 'cat-1')

    // The category still exists — DELETE was refused, not retried.
    expect(wrapper.find('[data-testid="exam-category-cat-1"]').exists()).toBe(true)

    // No second confirm dialog was raised (no "delete anyway" / force flow).
    expect(useConfirm().state.current).toBeNull()

    // No button anywhere mentions forcing / detaching.
    const anyForceButton = wrapper.findAll('button').some(b => /force|detach|anyway/i.test(b.text()))
    expect(anyForceButton).toBe(false)

    // The count from the 409 body is surfaced to the lecturer.
    expect(mockNotify).toHaveBeenCalledWith(
      expect.stringContaining('3 question(s)'),
      'error'
    )
  })

  // ---------------------------------------------------------------------------
  // 6. Import schema / import decoy schema, both with 409 confirm-replace retry
  // ---------------------------------------------------------------------------

  it('imports a category database schema, retrying with confirmReplace=true after a 409', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])
    importMock
      .mockRejectedValueOnce(new ApiError(409, 'category schema already has tables'))
      .mockResolvedValueOnce({ message: 'Database replaced' })

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    const openBtn = card.findAll('button').find(b => b.text() === 'Import Database')!
    await openBtn.trigger('click')
    await flushPromises()

    const sqlEditor = card.findComponent({ name: 'SqlEditor' })
    await sqlEditor.find('textarea').setValue('CREATE TABLE foo (id INT);')

    const submitBtn = card.findAll('button').find(b => b.text().includes('Import Database'))!
    await submitBtn.trigger('click')
    await flushPromises()

    useConfirm()._resolveCurrent(true)
    await flushPromises()

    expect(importMock).toHaveBeenCalledTimes(2)
    expect(importMock).toHaveBeenNthCalledWith(2, EXAM_ID, 'cat-1', 'CREATE TABLE foo (id INT);', true)
  })

  it('does NOT retry the schema import when the confirm-replace dialog is cancelled', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])
    importMock.mockRejectedValueOnce(new ApiError(409, 'category schema already has tables'))

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    await card.findAll('button').find(b => b.text() === 'Import Database')!.trigger('click')
    await flushPromises()

    const sqlEditor = card.findComponent({ name: 'SqlEditor' })
    await sqlEditor.find('textarea').setValue('CREATE TABLE foo (id INT);')
    await card.findAll('button').find(b => b.text().includes('Import Database'))!.trigger('click')
    await flushPromises()

    useConfirm()._resolveCurrent(false)
    await flushPromises()

    expect(importMock).toHaveBeenCalledTimes(1)
  })

  it('imports a category decoy schema, retrying with confirmReplace=true after a 409, and surfaces parity_warning', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])
    importPreviewMock
      .mockRejectedValueOnce(new ApiError(409, 'category decoy schema already has tables'))
      .mockResolvedValueOnce({ message: 'Decoy schema replaced', parity_warning: 'table "foo" column mismatch' })

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    const openBtn = card.findAll('button').find(b => b.text() === 'Import Decoy Schema')!
    await openBtn.trigger('click')
    await flushPromises()

    const sqlEditor = card.findComponent({ name: 'SqlEditor' })
    await sqlEditor.find('textarea').setValue('CREATE TABLE foo (id INT);')

    const submitBtn = card.findAll('button').find(b => b.text().includes('Import Decoy Schema'))!
    await submitBtn.trigger('click')
    await flushPromises()

    useConfirm()._resolveCurrent(true)
    await flushPromises()

    expect(importPreviewMock).toHaveBeenCalledTimes(2)
    expect(importPreviewMock).toHaveBeenNthCalledWith(2, EXAM_ID, 'cat-1', 'CREATE TABLE foo (id INT);', true)
    expect(mockNotify).toHaveBeenCalledWith('table "foo" column mismatch', 'error')
  })

  // ---------------------------------------------------------------------------
  // 7. Category description
  // ---------------------------------------------------------------------------

  it('saves a category description via setExamCategoryIntroduction', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A', introduction: '' })])
    introMock.mockResolvedValue(undefined)

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    // No import mode active for this category, so the Description textarea is the only one in the card.
    await card.find('textarea').setValue('New category description')

    const saveBtn = card.findAll('button').find(b => b.text().includes('Save Description'))!
    await saveBtn.trigger('click')
    await flushPromises()

    expect(introMock).toHaveBeenCalledWith(EXAM_ID, 'cat-1', 'New category description')
  })

  // ---------------------------------------------------------------------------
  // 8. "exam-level schema" badge for owns_schema === false
  // ---------------------------------------------------------------------------

  it('shows the "exam-level schema" badge only for a category whose owns_schema is false', async () => {
    listMock.mockResolvedValue([
      makeCategory({ id: 'cat-1', name: 'General', owns_schema: false, schema_name: 'public' }),
      makeCategory({ id: 'cat-2', name: 'Part B', owns_schema: true, schema_name: 'exam_cat_2' }),
    ])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    expect(wrapper.find('[data-testid="exam-category-cat-1"]').text()).toContain('exam-level schema')
    expect(wrapper.find('[data-testid="exam-category-cat-2"]').text()).not.toContain('exam-level schema')
  })

  it('shows a "decoy" badge only for a category with has_preview_schema true', async () => {
    listMock.mockResolvedValue([
      makeCategory({ id: 'cat-1', name: 'Part A', has_preview_schema: true }),
      makeCategory({ id: 'cat-2', name: 'Part B', has_preview_schema: false }),
    ])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    // Scope to the .badge elements (not the whole card text) — the
    // "Decoy database schema" sub-panel label and its always-visible
    // helper copy also contain the word "decoy" regardless of this badge.
    const cat1Badges = wrapper.find('[data-testid="exam-category-cat-1"]').findAll('.badge').map(b => b.text())
    const cat2Badges = wrapper.find('[data-testid="exam-category-cat-2"]').findAll('.badge').map(b => b.text())
    expect(cat1Badges).toContain('decoy')
    expect(cat2Badges).not.toContain('decoy')
  })

  // ---------------------------------------------------------------------------
  // 9. No intro-images editor for exam categories
  // ---------------------------------------------------------------------------

  it('renders exactly one IntroImagesEditor (exam-level) and none for any category', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const introImages = wrapper.findAllComponents({ name: 'IntroImagesEditor' })
    expect(introImages).toHaveLength(1)
    expect(introImages[0].props('scope')).toBe('exam')

    const card = wrapper.find('[data-testid="exam-category-cat-1"]')
    expect(card.findComponent({ name: 'IntroImagesEditor' }).exists()).toBe(false)
  })

  // ---------------------------------------------------------------------------
  // 10. Question form Category <select>
  // ---------------------------------------------------------------------------

  it('question form Category select offers "None (exam-level schema)" plus each loaded category', async () => {
    listMock.mockResolvedValue([
      makeCategory({ id: 'cat-1', name: 'Part A' }),
      makeCategory({ id: 'cat-2', name: 'Part B' }),
    ])

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    const categoryGroup = inputGroupByLabel(wrapper, 'Category:')
    const optionLabels = categoryGroup.findAll('option').map(o => o.text())
    expect(optionLabels).toEqual(['None (exam-level schema)', 'Part A', 'Part B'])
  })

  it('posts category_id in the save body, "None" posts null, and loading an existing question preselects its category', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])

    const existingQuestion = {
      id: 5,
      exam_id: EXAM_ID,
      category_id: 'cat-1',
      requirement: 'Existing requirement',
      question_type: 'QUERY',
      points: 10,
      correct_sql: 'SELECT 1;',
      dual_schema: false,
      strict_mode: { ordering: false, attributes: false, column_order: false },
    }

    let savedBody: any = null
    fetchMock = vi.fn().mockImplementation(async (url: string, options?: RequestInit) => {
      if (typeof url === 'string' && url.includes('/lecturer/question') && options?.method === 'POST') {
        savedBody = JSON.parse(options.body as string)
        return { ok: true, json: async () => ({ id: 5, message: 'Question saved' }) } as Response
      }
      if (typeof url === 'string' && url.includes('/questions')) {
        return { ok: true, json: async () => [existingQuestion] } as Response
      }
      return { ok: true, json: async () => [] } as Response
    })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    // Load the existing question via the "Question ID" dropdown -> editQuestion()
    const questionIdSelect = inputGroupByLabel(wrapper, 'Question ID:').find('select')
    await questionIdSelect.setValue('5')
    await flushPromises()

    const categorySelect = inputGroupByLabel(wrapper, 'Category:').find('select')
    // Preselected to the loaded question's category.
    expect((categorySelect.element as HTMLSelectElement).value).toBe('cat-1')

    // Switch to "None (exam-level schema)" — its option has no `value` DOM
    // attribute (Vue omits it for a null-bound :value), so the browser falls
    // back to the option's text content as its native .value; matching on
    // THAT (rather than getAttribute('value'), which is null) is what
    // actually reselects it and makes Vue's v-model read back `_value: null`.
    const noneOption = categorySelect
      .findAll('option')
      .find(o => o.text() === 'None (exam-level schema)')!
    await categorySelect.setValue(noneOption.element.value)
    await flushPromises()

    const saveBtn = wrapper.findAll('button').find(b => b.text().includes('Save Question'))!
    await saveBtn.trigger('click')
    await flushPromises()

    expect(savedBody).toBeTruthy()
    expect(savedBody.category_id).toBeNull()
  })

  // ---------------------------------------------------------------------------
  // 11. FIX-A — Preview requests carry category_id
  // ---------------------------------------------------------------------------

  it('previewCorrectSQL sends category_id when a category is selected, and omits it (not null) for "None"', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])

    const existingQuestion = {
      id: 5,
      exam_id: EXAM_ID,
      category_id: 'cat-1',
      requirement: 'Existing requirement',
      question_type: 'QUERY',
      points: 10,
      correct_sql: 'SELECT 1;',
      dual_schema: false,
      strict_mode: { ordering: false, attributes: false, column_order: false },
    }

    let previewBody: any = null
    fetchMock = vi.fn().mockImplementation(async (url: string, options?: RequestInit) => {
      if (typeof url === 'string' && url.includes('/lecturer/preview')) {
        previewBody = JSON.parse(options!.body as string)
        return { ok: true, json: async () => ({ status: 'ok', rows: [], columns: [] }) } as Response
      }
      if (typeof url === 'string' && url.includes('/questions')) {
        return { ok: true, json: async () => [existingQuestion] } as Response
      }
      return { ok: true, json: async () => [] } as Response
    })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
    await flushPromises()

    // Load the existing (categorised) question.
    const questionIdSelect = inputGroupByLabel(wrapper, 'Question ID:').find('select')
    await questionIdSelect.setValue('5')
    await flushPromises()

    const categorySelect = inputGroupByLabel(wrapper, 'Category:').find('select')
    expect((categorySelect.element as HTMLSelectElement).value).toBe('cat-1')

    const previewBtn = wrapper.findAll('button').find(b => b.text().includes('Preview Correct SQL Result'))!
    await previewBtn.trigger('click')
    await flushPromises()

    expect(previewBody).toEqual({
      exam_id: EXAM_ID,
      type: 'QUERY',
      target_table: '',
      correct_sql: 'SELECT 1;',
      category_id: 'cat-1',
    })

    // Switch to "None (exam-level schema)" and preview again — category_id
    // must be OMITTED entirely, not sent as null: the backend's
    // ExamCategoryID.UnmarshalJSON only accepts an opaque id string, and an
    // explicit null would fail to decode instead of falling back cleanly.
    const noneOption = categorySelect
      .findAll('option')
      .find(o => o.text() === 'None (exam-level schema)')!
    await categorySelect.setValue(noneOption.element.value)
    await flushPromises()

    await previewBtn.trigger('click')
    await flushPromises()

    expect(previewBody).toEqual({
      exam_id: EXAM_ID,
      type: 'QUERY',
      target_table: '',
      correct_sql: 'SELECT 1;',
    })
    expect('category_id' in previewBody).toBe(false)
  })

  it('previewAsStudent sends category_id alongside schema: "preview" for a categorised, dual-schema question', async () => {
    listMock.mockResolvedValue([makeCategory({ id: 'cat-1', name: 'Part A' })])

    const existingQuestion = {
      id: 5,
      exam_id: EXAM_ID,
      category_id: 'cat-1',
      requirement: 'Existing requirement',
      question_type: 'QUERY',
      points: 10,
      correct_sql: 'SELECT 1;',
      dual_schema: true,
      strict_mode: { ordering: false, attributes: false, column_order: false },
    }

    let previewBody: any = null
    fetchMock = vi.fn().mockImplementation(async (url: string, options?: RequestInit) => {
      if (typeof url === 'string' && url.includes('/lecturer/preview')) {
        previewBody = JSON.parse(options!.body as string)
        return { ok: true, json: async () => ({ status: 'ok', rows: [], columns: [] }) } as Response
      }
      if (typeof url === 'string' && url.includes('/questions')) {
        return { ok: true, json: async () => [existingQuestion] } as Response
      }
      return { ok: true, json: async () => [] } as Response
    })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam({ has_preview_schema: true }) } })
    await flushPromises()

    const questionIdSelect = inputGroupByLabel(wrapper, 'Question ID:').find('select')
    await questionIdSelect.setValue('5')
    await flushPromises()

    const previewAsStudentBtn = wrapper.findAll('button').find(b => b.text().includes('Preview as Student'))!
    await previewAsStudentBtn.trigger('click')
    await flushPromises()

    expect(previewBody).toEqual({
      exam_id: EXAM_ID,
      type: 'QUERY',
      target_table: '',
      correct_sql: 'SELECT 1;',
      schema: 'preview',
      category_id: 'cat-1',
    })
  })
})
