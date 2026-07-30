/**
 * Tests for the sticky Exam Categories panel on ExamQuestionsPanel.vue — the
 * exam-authoring half of
 * docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md.
 *
 * Same contract as PracticeAuthoring.layout.test.ts, with one extra assertion
 * this panel needs: its lower section used to carry an INLINE border-top /
 * padding-top divider in addition to the scoped one. Inline styles beat every
 * stylesheet, so a leftover copy would draw a horizontal rule down the side of
 * the rail. The divider must come from the mode-aware global rule only.
 *
 * Mock conventions follow exam_categories_panel.test.ts.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AUTHORING_LAYOUT_STORAGE_PREFIX } from '../../composables/useAuthoringLayout'
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

function installMatchMedia(matches: boolean) {
  const listeners = new Set<() => void>()
  const mql = {
    matches,
    media: '',
    addEventListener: (_t: string, cb: () => void) => { listeners.add(cb) },
    removeEventListener: (_t: string, cb: () => void) => { listeners.delete(cb) },
  }
  const fn = vi.fn(() => mql as unknown as MediaQueryList)
  vi.stubGlobal('matchMedia', fn)
  ;(window as unknown as { matchMedia: unknown }).matchMedia = fn
  return {
    set(v: boolean) {
      mql.matches = v
      listeners.forEach(cb => cb())
    },
  }
}

async function mountPanel() {
  const wrapper = mount(ExamQuestionsPanel, { props: { exam: makeExam() } })
  await flushPromises()
  return wrapper
}

describe('ExamQuestionsPanel — sticky Exam Categories layout', () => {
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

  it('defaults to the rail layout on a wide viewport', async () => {
    installMatchMedia(true)
    const wrapper = await mountPanel()

    const layout = wrapper.find('.authoring-layout')
    expect(layout.exists()).toBe(true)
    expect(layout.classes()).toContain('authoring-layout--rail')
  })

  it('keeps both sections mounted, upper before lower, in BOTH modes', async () => {
    installMatchMedia(true)
    const wrapper = await mountPanel()

    for (const expected of ['authoring-layout--rail', 'authoring-layout--stacked']) {
      if (expected === 'authoring-layout--stacked') {
        await wrapper.find('[data-testid="layout-mode-stacked"]').trigger('click')
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

  it('renders the Exam Categories panel content in rail mode', async () => {
    installMatchMedia(true)
    const wrapper = await mountPanel()

    const lower = wrapper.find('.authoring-section--lower')
    expect(lower.find('#exam-categories-heading').text()).toBe('Exam Categories')
    expect(lower.text()).toContain('Part A')
  })

  it('has no inline divider left on the lower section — the mode-aware rule owns it', async () => {
    installMatchMedia(true)
    const wrapper = await mountPanel()

    // Inline styles beat every stylesheet, so an inline border-top would draw a
    // rule down the side of the rail regardless of the layout mode.
    const lowerHtml = wrapper.find('.authoring-section--lower').html()
    const openingTagEnd = lowerHtml.indexOf('>')
    const firstChildTag = lowerHtml.slice(0, lowerHtml.indexOf('>', openingTagEnd + 1) + 1)
    expect(firstChildTag).not.toContain('border-top')
    expect(firstChildTag).not.toContain('padding-top')
  })

  it('persists the exam preference under its own key', async () => {
    installMatchMedia(true)
    const wrapper = await mountPanel()

    await wrapper.find('[data-testid="layout-mode-stacked"]').trigger('click')
    expect(localStorage.getItem(KEY)).toBe('stacked')
    // Practice authoring keeps its own preference.
    expect(localStorage.getItem(`${AUTHORING_LAYOUT_STORAGE_PREFIX}practice`)).toBeNull()
  })

  it('falls back to stacked and hides the switch on a narrow viewport', async () => {
    installMatchMedia(false)
    const wrapper = await mountPanel()

    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--stacked')
    expect(wrapper.find('.authoring-layout-bar').exists()).toBe(false)
    expect(wrapper.find('#exam-categories-heading').exists()).toBe(true)
  })

  it('reveals the switch and the rail when the viewport widens', async () => {
    const mm = installMatchMedia(false)
    const wrapper = await mountPanel()
    expect(wrapper.find('.authoring-layout-bar').exists()).toBe(false)

    mm.set(true)
    await flushPromises()

    expect(wrapper.find('.authoring-layout-bar').exists()).toBe(true)
    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--rail')
  })
})
