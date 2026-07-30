/**
 * Tests for useAuthoringLayout — the persisted dock orientation + minimize
 * state behind the Categories Management panel
 * (docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md §3.3,
 * superseded twice; see the composable's own doc comment).
 *
 * Both modes are `position: fixed` overlays, so unlike the original sticky
 * rail there is no matchMedia breakpoint to test here. What matters is that
 * the preference round-trips, that legacy stored values migrate rather than
 * resetting, and that a hostile localStorage cannot break the page.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  useAuthoringLayout,
  AUTHORING_LAYOUT_STORAGE_PREFIX,
  AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX,
} from '../../composables/useAuthoringLayout'

const KEY = `${AUTHORING_LAYOUT_STORAGE_PREFIX}practice`
const COLLAPSED_KEY = `${AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX}practice`

describe('useAuthoringLayout', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('defaults to the side dock, uncollapsed, with empty storage', () => {
    const result = useAuthoringLayout('practice')
    expect(result.mode.value).toBe('side')
    expect(result.collapsed.value).toBe(false)
    // Reading a default must NOT write — an untouched preference stays absent
    // so a future default change still reaches existing users.
    expect(localStorage.getItem(KEY)).toBeNull()
    expect(localStorage.getItem(COLLAPSED_KEY)).toBeNull()
  })

  it('restores a persisted bottom-dock preference', () => {
    localStorage.setItem(KEY, 'bottom')
    const result = useAuthoringLayout('practice')
    expect(result.mode.value).toBe('bottom')
  })

  it.each([
    ['rail', 'side'],
    ['floating', 'side'],
    ['stacked', 'bottom'],
  ])('migrates the legacy "%s" stored value to "%s"', (stored, expected) => {
    localStorage.setItem(KEY, stored)
    const result = useAuthoringLayout('practice')
    expect(result.mode.value).toBe(expected)
  })

  it('ignores a garbage persisted value and falls back to the side dock', () => {
    localStorage.setItem(KEY, 'sideways')
    const result = useAuthoringLayout('practice')
    expect(result.mode.value).toBe('side')
  })

  it('setMode and toggleMode write through to storage', () => {
    const result = useAuthoringLayout('practice')

    result.setMode('bottom')
    expect(result.mode.value).toBe('bottom')
    expect(localStorage.getItem(KEY)).toBe('bottom')

    result.toggleMode()
    expect(result.mode.value).toBe('side')
    expect(localStorage.getItem(KEY)).toBe('side')
  })

  it('setCollapsed and toggleCollapsed write through to storage', () => {
    const result = useAuthoringLayout('practice')

    result.setCollapsed(true)
    expect(result.collapsed.value).toBe(true)
    expect(localStorage.getItem(COLLAPSED_KEY)).toBe('1')

    result.toggleCollapsed()
    expect(result.collapsed.value).toBe(false)
    expect(localStorage.getItem(COLLAPSED_KEY)).toBe('0')
  })

  it('restores a persisted collapsed preference', () => {
    localStorage.setItem(COLLAPSED_KEY, '1')
    const result = useAuthoringLayout('practice')
    expect(result.collapsed.value).toBe(true)
  })

  it('keeps the collapsed state independent of the dock orientation', () => {
    // Regression: minimize used to be wired to one mode only, so switching
    // docks silently stranded the panel with no way to get it out of the way.
    const result = useAuthoringLayout('practice')
    result.setCollapsed(true)
    result.setMode('bottom')
    expect(result.collapsed.value).toBe(true)
  })

  it('scopes the storage key so exam authoring cannot clobber practice', () => {
    const practice = useAuthoringLayout('practice')
    const exam = useAuthoringLayout('exam')

    practice.setMode('bottom')
    expect(localStorage.getItem(KEY)).toBe('bottom')
    expect(localStorage.getItem(`${AUTHORING_LAYOUT_STORAGE_PREFIX}exam`)).toBeNull()
    expect(exam.mode.value).toBe('side')
  })

  it('survives a throwing localStorage and still tracks the mode in memory', () => {
    const getSpy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError: storage disabled')
    })
    const setSpy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    try {
      const result = useAuthoringLayout('practice')
      expect(result.mode.value).toBe('side')
      expect(() => result.setMode('bottom')).not.toThrow()
      expect(result.mode.value).toBe('bottom')
    } finally {
      getSpy.mockRestore()
      setSpy.mockRestore()
    }
  })
})
