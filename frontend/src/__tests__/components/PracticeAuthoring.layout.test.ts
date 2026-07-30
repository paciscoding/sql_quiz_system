/**
 * Tests for the sticky Categories Management panel on PracticeAuthoring.vue
 * (docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md).
 *
 * What is asserted here is the *class contract* plus the state machine, not CSS
 * geometry: happy-dom has no layout engine, so `position: sticky`, the rail's
 * max-height and the grid track sizes cannot be observed from a unit test.
 * They are reviewed visually. What a unit test CAN protect is the part that
 * silently rots:
 *   - the wrapper class the global rules in style.css hang off,
 *   - the panel being genuinely always-rendered (never behind a v-if) in BOTH
 *     modes, so a mode switch cannot unmount a half-filled import editor,
 *   - the persisted preference and the narrow-viewport fallback.
 *
 * Mocking conventions follow exam_categories_panel.test.ts: SqlEditor is a bare
 * textarea (CodeMirror needs a real browser), notification_service is a spy,
 * IntroImagesEditor is stubbed so its fetch-on-mount never runs.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AUTHORING_LAYOUT_STORAGE_PREFIX } from '../../composables/useAuthoringLayout'
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

/** Controllable window.matchMedia; happy-dom's own is not drivable per-test. */
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

async function mountPage() {
  const wrapper = mount(PracticeAuthoring, {
    global: { stubs: { BackLink: true, RouterLink: true } },
  })
  await flushPromises()
  return wrapper
}

describe('PracticeAuthoring — sticky Categories Management layout', () => {
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

  it('defaults to the rail layout on a wide viewport', async () => {
    installMatchMedia(true)
    const wrapper = await mountPage()

    const layout = wrapper.find('.authoring-layout')
    expect(layout.exists()).toBe(true)
    expect(layout.classes()).toContain('authoring-layout--rail')
    expect(layout.classes()).not.toContain('authoring-layout--stacked')
  })

  it('keeps both sections mounted, upper before lower, in BOTH modes', async () => {
    installMatchMedia(true)
    const wrapper = await mountPage()

    for (const expected of ['authoring-layout--rail', 'authoring-layout--stacked']) {
      if (expected === 'authoring-layout--stacked') {
        await wrapper.find('[data-testid="layout-mode-stacked"]').trigger('click')
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

  it('renders the categories panel content in rail mode (not behind a v-if)', async () => {
    installMatchMedia(true)
    const wrapper = await mountPage()

    const lower = wrapper.find('.authoring-section--lower')
    expect(lower.find('#practice-categories-heading').exists()).toBe(true)
    expect(lower.text()).toContain('Joins')
    expect(lower.find('[data-testid="category-cat-1"]').exists()).toBe(true)
  })

  it('renders the empty state inside the rail when there are no categories', async () => {
    installMatchMedia(true)
    vi.mocked(listCategories).mockResolvedValue([])
    const wrapper = await mountPage()

    expect(wrapper.find('.authoring-section--lower').text()).toContain('No categories yet')
  })

  it('switching to stacked persists the preference', async () => {
    installMatchMedia(true)
    const wrapper = await mountPage()

    await wrapper.find('[data-testid="layout-mode-stacked"]').trigger('click')
    expect(localStorage.getItem(KEY)).toBe('stacked')
    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--stacked')

    await wrapper.find('[data-testid="layout-mode-rail"]').trigger('click')
    expect(localStorage.getItem(KEY)).toBe('rail')
    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--rail')
  })

  it('restores a persisted stacked preference on mount', async () => {
    installMatchMedia(true)
    localStorage.setItem(KEY, 'stacked')
    const wrapper = await mountPage()

    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--stacked')
    expect(wrapper.find('[data-testid="layout-mode-stacked"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('[data-testid="layout-mode-rail"]').attributes('aria-pressed')).toBe('false')
  })

  it('falls back to stacked and hides the switch on a narrow viewport', async () => {
    installMatchMedia(false)
    const wrapper = await mountPage()

    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--stacked')
    expect(wrapper.find('.authoring-layout-bar').exists()).toBe(false)
    // The categories panel is still there — narrow just means stacked, not gone.
    expect(wrapper.find('#practice-categories-heading').exists()).toBe(true)
  })

  it('reveals the switch and the rail when the viewport widens', async () => {
    const mm = installMatchMedia(false)
    const wrapper = await mountPage()
    expect(wrapper.find('.authoring-layout-bar').exists()).toBe(false)

    mm.set(true)
    await flushPromises()

    expect(wrapper.find('.authoring-layout-bar').exists()).toBe(true)
    expect(wrapper.find('.authoring-layout').classes()).toContain('authoring-layout--rail')
  })

  it('keeps the questions table inside a horizontal-scroll wrapper', async () => {
    // The rail narrows the left column below the 8-column table's min-content
    // width. Unwrapped, the table overflows its card and the whole page gains a
    // horizontal scrollbar — the regression this wrapper exists to prevent.
    installMatchMedia(true)
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
    installMatchMedia(true)
    const wrapper = await mountPage()
    const labels = wrapper.findAll('button').map(b => b.text())
    expect(labels.some(t => /hide categories/i.test(t))).toBe(false)
  })
})
