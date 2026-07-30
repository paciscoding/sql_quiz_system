/**
 * Tests for the docked Categories Management panel on PracticeAuthoring.vue
 * (docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md,
 * superseded by the side/bottom dock redesign — see useAuthoringLayout.ts).
 *
 * What is asserted here is the *class contract* plus the state machine, not CSS
 * geometry: happy-dom has no layout engine, so `position: fixed` and the
 * panel's height cannot be observed from a unit test. They are reviewed
 * visually. What a unit test CAN protect is the part that silently rots:
 *   - the wrapper/dock classes the global rules in style.css hang off,
 *   - the panel being genuinely always-rendered (never behind a v-if) in BOTH
 *     modes, so a mode switch cannot unmount a half-filled import editor,
 *   - BOTH modes staying docked overlays (one used to rejoin the page flow and
 *     scroll away) and minimize working in both (it used to be gated on one),
 *   - the persisted dock + minimize preference (no viewport gate anymore —
 *     a fixed overlay works at any width).
 *
 * Mocking conventions follow exam_categories_panel.test.ts: SqlEditor is a bare
 * textarea (CodeMirror needs a real browser), notification_service is a spy,
 * IntroImagesEditor is stubbed so its fetch-on-mount never runs.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  AUTHORING_LAYOUT_STORAGE_PREFIX,
  AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX,
} from '../../composables/useAuthoringLayout'
import type { PracticeCategory, PracticeQuestion } from '../../services/practice'

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
    template: '<div class="stub-intro-images" :data-scope="scope" :data-owner-id="ownerId" />',
  },
}))

const mockNotify = vi.fn()
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: mockNotify, notifications: [], remove: vi.fn() }),
}))

vi.mock('../../services/practice', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/practice')>()
  return {
    ...actual,
    lecturerListQuestions: vi.fn(),
    listCategories: vi.fn(),
  }
})

// Imported after the mocks are hoisted.
import PracticeAuthoring from '../../components/PracticeAuthoring.vue'
import { lecturerListQuestions, listCategories } from '../../services/practice'

const KEY = `${AUTHORING_LAYOUT_STORAGE_PREFIX}practice`
const COLLAPSED_KEY = `${AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX}practice`

function makeCategory(overrides: Partial<PracticeCategory> = {}): PracticeCategory {
  return {
    id: 'cat-1',
    name: 'Joins',
    schema_name: 'practice_cat_1',
    preview_schema_name: '',
    has_preview_schema: false,
    introduction: '',
    question_count: 2,
    ...overrides,
  } as PracticeCategory
}

async function mountPage() {
  const wrapper = mount(PracticeAuthoring, {
    global: { stubs: { BackLink: true, RouterLink: true } },
  })
  await flushPromises()
  return wrapper
}

describe('PracticeAuthoring — docked Categories Management panel', () => {
  beforeEach(() => {
    vi.mocked(lecturerListQuestions).mockReset().mockResolvedValue([] as PracticeQuestion[])
    vi.mocked(listCategories).mockReset().mockResolvedValue([makeCategory()])
    mockNotify.mockClear()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => [] } as Response)))
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('defaults to the side dock', async () => {
    const wrapper = await mountPage()

    const layout = wrapper.find('.authoring-layout')
    expect(layout.exists()).toBe(true)
    expect(layout.classes()).toContain('authoring-layout--side')
    expect(layout.classes()).not.toContain('authoring-layout--bottom')
  })

  it('shows the dock switch on the panel regardless of viewport (no breakpoint gate)', async () => {
    const wrapper = await mountPage()
    expect(wrapper.find('.authoring-layout-switch').exists()).toBe(true)
    expect(wrapper.find('[data-testid="layout-mode-side"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="layout-mode-bottom"]').exists()).toBe(true)
  })

  it('keeps the panel docked (fixed overlay) in BOTH orientations', async () => {
    // The regression this pins: one of the two modes used to drop out of the
    // overlay entirely and rejoin the page flow, so it scrolled away — the
    // exact failure this whole feature exists to prevent.
    const wrapper = await mountPage()
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-docked')
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('dock-side')

    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-docked')
    expect(wrapper.find('.authoring-section--lower').classes()).toContain('dock-bottom')
  })

  it('docks the switch strip on the panel shell and flips orientation with the mode', async () => {
    // side: full-height column on the panel's inner edge. bottom: full-width
    // row along the panel's top. Both live on .authoring-panel-shell so the
    // switch is physically part of the panel, not a separate page-level bar.
    const wrapper = await mountPage()
    expect(wrapper.find('.authoring-panel-shell').classes()).toContain('authoring-panel-shell--side')

    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
    expect(wrapper.find('.authoring-panel-shell').classes()).toContain('authoring-panel-shell--bottom')
  })

  it('keeps both sections mounted, upper before lower, in BOTH modes', async () => {
    const wrapper = await mountPage()

    for (const expected of ['authoring-layout--side', 'authoring-layout--bottom']) {
      if (expected === 'authoring-layout--bottom') {
        await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
        await flushPromises()
      }
      const layout = wrapper.find('.authoring-layout')
      expect(layout.classes(), expected).toContain(expected)

      const upper = wrapper.find('.authoring-section--upper')
      const lower = wrapper.find('.authoring-section--lower')
      expect(upper.exists(), `${expected}: upper`).toBe(true)
      expect(lower.exists(), `${expected}: lower`).toBe(true)

      const html = layout.html()
      expect(
        html.indexOf('authoring-section--upper'),
        `${expected}: DOM order`
      ).toBeLessThan(html.indexOf('authoring-section--lower'))
    }
  })

  it('renders the categories panel content in the docked panel (not behind a v-if)', async () => {
    const wrapper = await mountPage()

    const lower = wrapper.find('.authoring-section--lower')
    expect(lower.classes()).toContain('is-docked')
    expect(lower.find('#practice-categories-heading').exists()).toBe(true)
    expect(lower.text()).toContain('Joins')
    expect(lower.find('[data-testid="category-cat-1"]').exists()).toBe(true)
  })

  it('renders the empty state inside the docked panel when there are no categories', async () => {
    vi.mocked(listCategories).mockResolvedValue([])
    const wrapper = await mountPage()

    expect(wrapper.find('.authoring-section--lower').text()).toContain('No categories yet')
  })

  it('switching the dock persists the preference', async () => {
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')
    expect(localStorage.getItem(KEY)).toBe('bottom')
    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--bottom')

    await wrapper.find('[data-testid="layout-mode-side"]').trigger('click')
    expect(localStorage.getItem(KEY)).toBe('side')
    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--side')
  })

  it('restores a persisted bottom-dock preference on mount', async () => {
    localStorage.setItem(KEY, 'bottom')
    const wrapper = await mountPage()

    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--bottom')
    expect(wrapper.find('[data-testid="layout-mode-bottom"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('[data-testid="layout-mode-side"]').attributes('aria-pressed')).toBe('false')
  })

  it('migrates a legacy "rail" preference to the side dock', async () => {
    localStorage.setItem(KEY, 'rail')
    const wrapper = await mountPage()

    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--side')
  })

  it('minimizing hides the panel and shows a reopen tab; persists', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('[data-testid="floating-categories-reopen"]').exists()).toBe(false)
    await wrapper.find('[data-testid="floating-categories-minimize"]').trigger('click')

    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-collapsed')
    expect(wrapper.find('[data-testid="floating-categories-reopen"]').exists()).toBe(true)
    expect(localStorage.getItem(COLLAPSED_KEY)).toBe('1')

    await wrapper.find('[data-testid="floating-categories-reopen"]').trigger('click')
    expect(wrapper.find('.authoring-section--lower').classes()).not.toContain('is-collapsed')
    expect(wrapper.find('[data-testid="floating-categories-reopen"]').exists()).toBe(false)
    expect(localStorage.getItem(COLLAPSED_KEY)).toBe('0')
  })

  it('offers minimize + reopen in BOTH docks', async () => {
    // Regression: minimize was gated on one mode, leaving the other dock as a
    // permanent overlay the lecturer could not dismiss.
    const wrapper = await mountPage()
    await wrapper.find('[data-testid="layout-mode-bottom"]').trigger('click')

    expect(wrapper.find('[data-testid="floating-categories-minimize"]').exists()).toBe(true)
    await wrapper.find('[data-testid="floating-categories-minimize"]').trigger('click')

    expect(wrapper.find('.authoring-section--lower').classes()).toContain('is-collapsed')
    const tab = wrapper.find('[data-testid="floating-categories-reopen"]')
    expect(tab.exists()).toBe(true)
    // The tab sits on the edge the panel will return to.
    expect(tab.classes()).toContain('authoring-floating-tab--bottom')
  })

  it('keeps the questions table inside a horizontal-scroll wrapper', async () => {
    vi.mocked(lecturerListQuestions).mockResolvedValue([
      {
        id: 'q-1',
        category_id: 'cat-1',
        category: 'Joins',
        category_name: 'Joins',
        title: 'Employees hired after 2020',
        requirement: 'List them',
        question_type: 'QUERY',
        points: 1,
        correct_sql: 'SELECT 1',
        expected_result_status: 'captured',
      },
    ] as unknown as PracticeQuestion[])
    const wrapper = await mountPage()

    const wrap = wrapper.find('.authoring-section--upper .responsive-table-wrap')
    expect(wrap.exists()).toBe(true)
    expect(wrap.find('table.data-table').exists()).toBe(true)
  })

  it('does not drop the old "Manage Categories" show/hide toggle back in', async () => {
    // ARCH-034: the panel is always rendered. A regression that reintroduces a
    // visibility toggle would silently undo the whole point of this layout.
    const wrapper = await mountPage()
    const labels = wrapper.findAll('button').map(b => b.text())
    expect(labels.some(t => /hide categories/i.test(t))).toBe(false)
  })
})
