/**
 * Tests for the docked Exam Categories panel on ExamQuestionsPanel.vue — the
 * exam-authoring half of
 * docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md
 * (superseded by the side/bottom dock redesign — see useAuthoringLayout.ts).
 *
 * Same contract as PracticeAuthoring.layout.test.ts, with one extra assertion
 * this panel needs: its lower section used to carry an INLINE border-top /
 * padding-top divider in addition to the scoped one. Inline styles beat every
 * stylesheet, so a leftover copy would draw a stray rule across the docked
 * panel. The border must come from the global dock rules only.
 *
 * Mock conventions follow exam_categories_panel.test.ts.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  AUTHORING_LAYOUT_STORAGE_PREFIX,
  AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX,
} from '../../composables/useAuthoringLayout'
import type { ExamCategory } from '../../services/exam'

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

const KEY = `${AUTHORING_LAYOUT_STORAGE_PREFIX}exam`
const COLLAPSED_KEY = `${AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX}exam`
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

async function mountPanel() {
  const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
  await flushPromises()
  return wrapper
}

describe('ExamQuestionsPanel — docked Exam Categories panel', () => {
  beforeEach(() => {
    vi.mocked(listExamCategories).mockReset().mockResolvedValue([makeCategory()])
    mockNotify.mockClear()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => [] } as Response)))
    localStorage.clear()
    localStorage.setItem('sql_quiz_token', 'test-token')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('defaults to the side dock', async () => {
    const wrapper = await mountPanel()

    const layout = wrapper.find('.authoring-layout')
    expect(layout.exists()).toBe(true)
    expect(layout.classes()).toContain('authoring-layout--side')
  })

  it('shows the dock switch on the panel regardless of viewport (no breakpoint gate)', async () => {
    const wrapper = await mountPanel()
    expect(wrapper.find('.authoring-layout-switch').exists()).toBe(true)
    expect(wrapper.find('[data-testid="layout-mode-side"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="layout-mode-bottom"]').exists()).toBe(true)
  })

  it('keeps the panel docked (fixed overlay) in BOTH orientations', async () => {
    const wrapper = await mountPanel()
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-docked')
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('dock-side')

    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-docked')
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('dock-bottom')
  })

  it('docks the switch strip on the panel shell and flips orientation with the mode', async () => {
    const wrapper = await mountPanel()
    expect(wrapper.find('.authoring-panel-shell').classes()).toContain('authoring-panel-shell--side')

    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
    expect(wrapper.find('.authoring-panel-shell').classes()).toContain('authoring-panel-shell--bottom')
  })

  it('keeps both sections mounted, upper before lower, in BOTH modes', async () => {
    const wrapper = await mountPanel()

    for (const expected of ['authoring-layout--side', 'authoring-layout--bottom']) {
      if (expected === 'authoring-layout--bottom') {
        await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
        await flushPromises()
      }
      const layout = wrapper.find('.authoring-layout')
      expect(layout.classes(), expected).toContain(expected)
      expect(wrapper.find('.authoring-section--upper').exists(), `${expected}: upper`).toBe(true)
      expect(wrapper.find('.authoring-section--lower').exists(), `${expected}: lower`).toBe(true)

      const html = layout.html()
      expect(
        html.indexOf('authoring-section--upper'),
        `${expected}: DOM order`
      ).toBeLessThan(html.indexOf('authoring-section--lower'))
    }
  })

  it('renders the Exam Categories panel content in the docked panel', async () => {
    const wrapper = await mountPanel()

    const lower = wrapper.find('.authoring-section--lower')
    expect(lower.classes()).toContain('is-docked')
    expect(lower.find('#exam-categories-heading').text()).toBe('Exam Categories')
    expect(lower.text()).toContain('Part A')
  })

  it('has no inline divider left on the lower section — the dock rules own it', async () => {
    const wrapper = await mountPanel()

    // Inline styles beat every stylesheet, so an inline border-top would draw a
    // stray rule across the docked panel regardless of orientation.
    const lowerHtml = wrapper.find('.authoring-section--lower').html()
    const openingTagEnd = lowerHtml.indexOf('>')
    const firstChildTag = lowerHtml.slice(0, lowerHtml.indexOf('>', openingTagEnd + 1) + 1)
    expect(firstChildTag).not.toContain('border-top')
    expect(firstChildTag).not.toContain('padding-top')
  })

  it('persists the exam preference under its own key', async () => {
    const wrapper = await mountPanel()

    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
    expect(localStorage.getItem(KEY)).toBe('bottom')
    // Practice authoring keeps its own preference.
    expect(localStorage.getItem(`${AUTHORING_LAYOUT_STORAGE_PREFIX}practice`)).toBeNull()
  })

  it('offers minimize + reopen in BOTH docks', async () => {
    const wrapper = await mountPanel()
    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')

    expect(wrapper.find('[data-testid="floating-categories-minimize"]').exists()).toBe(true)
    await wrapper.find('[data-testid="floating-categories-minimize"]').trigger('click')

    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-collapsed')
    const tab = wrapper.find('[data-testid="floating-categories-reopen"]')
    expect(tab.exists()).toBe(true)
    expect(tab.classes()).toContain('authoring-floating-tab--bottom')
  })

  it('minimizing hides the panel and shows a reopen tab; persists', async () => {
    const wrapper = await mountPanel()

    expect(wrapper.find('[data-testid="floating-categories-reopen"]').exists()).toBe(false)
    await wrapper.find('[data-testid="floating-categories-minimize"]').trigger('click')

    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-collapsed')
    expect(wrapper.find('[data-testid="floating-categories-reopen"]').exists()).toBe(true)
    expect(localStorage.getItem(COLLAPSED_KEY)).toBe('1')

    await wrapper.find('[data-testid="floating-categories-reopen"]').trigger('click')
    expect(wrapper.find('.authoring-section--lower').classes()).not.toContain('is-collapsed')
    expect(localStorage.getItem(COLLAPSED_KEY)).toBe('0')
  })
})
