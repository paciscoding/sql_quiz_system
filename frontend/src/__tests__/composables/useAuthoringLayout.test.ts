/**
 * Tests for useAuthoringLayout — the persisted rail/stacked layout preference
 * behind the sticky Categories Management panel
 * (docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md §3.3).
 *
 * The composable is deliberately the single source of truth for which layout
 * renders: a CSS-only breakpoint override would leave the toggle claiming
 * "Side rail" while the page rendered stacked. So the media query is read
 * through window.matchMedia and everything derived from it is asserted here.
 *
 * window.matchMedia is replaced per-test with a controllable fake rather than
 * leaning on happy-dom's own implementation — the tests need to drive a
 * narrow -> wide transition, not just sample one viewport width.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { useAuthoringLayout, AUTHORING_LAYOUT_STORAGE_PREFIX } from '../../composables/useAuthoringLayout'

const KEY = `${AUTHORING_LAYOUT_STORAGE_PREFIX}practice`

interface FakeMediaQueryList {
  matches: boolean
  media: string
  addEventListener: (type: string, cb: () => void) => void
  removeEventListener: (type: string, cb: () => void) => void
  /** Test helper: flip `matches` and notify every registered listener. */
  _set: (v: boolean) => void
  _listenerCount: () => number
}

function installMatchMedia(initialMatches: boolean): FakeMediaQueryList {
  const listeners = new Set<() => void>()
  const mql: FakeMediaQueryList = {
    matches: initialMatches,
    media: '',
    addEventListener: (_type, cb) => { listeners.add(cb) },
    removeEventListener: (_type, cb) => { listeners.delete(cb) },
    _set: (v: boolean) => {
      mql.matches = v
      listeners.forEach(cb => cb())
    },
    _listenerCount: () => listeners.size,
  }
  const fn = vi.fn((query: string) => {
    mql.media = query
    return mql as unknown as MediaQueryList
  })
  vi.stubGlobal('matchMedia', fn)
  // vi.stubGlobal patches globalThis; happy-dom's `window` is the same object,
  // but assign explicitly so a `window.matchMedia` read cannot miss it.
  ;(window as unknown as { matchMedia: unknown }).matchMedia = fn
  return mql
}

/**
 * Runs the composable inside a real effect scope so onScopeDispose is legal,
 * and hands back a stop() so listener cleanup can be asserted.
 */
function withScope<T>(fn: () => T): { result: T; stop: () => void } {
  const scope = effectScope()
  const result = scope.run(fn) as T
  return { result, stop: () => scope.stop() }
}

describe('useAuthoringLayout', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('defaults to rail with empty storage', () => {
    installMatchMedia(true)
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))
    expect(result.mode.value).toBe('rail')
    expect(result.effectiveMode.value).toBe('rail')
    // Reading a default must NOT write — an untouched preference stays absent
    // so a future default change still reaches existing users.
    expect(localStorage.getItem(KEY)).toBeNull()
    stop()
  })

  it('restores a persisted stacked preference', () => {
    installMatchMedia(true)
    localStorage.setItem(KEY, 'stacked')
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))
    expect(result.mode.value).toBe('stacked')
    expect(result.effectiveMode.value).toBe('stacked')
    stop()
  })

  it('ignores a garbage persisted value and falls back to rail', () => {
    installMatchMedia(true)
    localStorage.setItem(KEY, 'sideways')
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))
    expect(result.mode.value).toBe('rail')
    stop()
  })

  it('setMode and toggleMode write through to storage', () => {
    installMatchMedia(true)
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))

    result.setMode('stacked')
    expect(result.mode.value).toBe('stacked')
    expect(localStorage.getItem(KEY)).toBe('stacked')

    result.toggleMode()
    expect(result.mode.value).toBe('rail')
    expect(localStorage.getItem(KEY)).toBe('rail')
    stop()
  })

  it('scopes the storage key so exam authoring cannot clobber practice', () => {
    installMatchMedia(true)
    const practice = withScope(() => useAuthoringLayout('practice'))
    const exam = withScope(() => useAuthoringLayout('exam'))

    practice.result.setMode('stacked')
    expect(localStorage.getItem(KEY)).toBe('stacked')
    expect(localStorage.getItem(`${AUTHORING_LAYOUT_STORAGE_PREFIX}exam`)).toBeNull()
    expect(exam.result.mode.value).toBe('rail')

    practice.stop()
    exam.stop()
  })

  it('forces stacked on a narrow viewport without discarding the rail preference', () => {
    const mql = installMatchMedia(false)
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))

    expect(result.canUseRail.value).toBe(false)
    expect(result.mode.value).toBe('rail')
    expect(result.effectiveMode.value).toBe('stacked')
    expect(mql.media).toContain('1280px')
    stop()
  })

  it('flips effectiveMode back to rail when the viewport widens', () => {
    const mql = installMatchMedia(false)
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))
    expect(result.effectiveMode.value).toBe('stacked')

    mql._set(true)

    expect(result.canUseRail.value).toBe(true)
    expect(result.effectiveMode.value).toBe('rail')
    stop()
  })

  it('keeps stacked when the viewport widens if stacked is what the user picked', () => {
    const mql = installMatchMedia(false)
    localStorage.setItem(KEY, 'stacked')
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))

    mql._set(true)

    expect(result.canUseRail.value).toBe(true)
    expect(result.effectiveMode.value).toBe('stacked')
    stop()
  })

  it('removes its media-query listener when the scope is disposed', () => {
    const mql = installMatchMedia(true)
    const { stop } = withScope(() => useAuthoringLayout('practice'))
    expect(mql._listenerCount()).toBe(1)
    stop()
    expect(mql._listenerCount()).toBe(0)
  })

  it('survives a throwing localStorage and still tracks the mode in memory', () => {
    installMatchMedia(true)
    const getSpy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError: storage disabled')
    })
    const setSpy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    try {
      const { result, stop } = withScope(() => useAuthoringLayout('practice'))
      expect(result.mode.value).toBe('rail')
      expect(() => result.setMode('stacked')).not.toThrow()
      expect(result.mode.value).toBe('stacked')
      expect(result.effectiveMode.value).toBe('stacked')
      stop()
    } finally {
      getSpy.mockRestore()
      setSpy.mockRestore()
    }
  })

  it('assumes the rail is available when matchMedia is missing entirely', () => {
    // Older/headless environments without matchMedia must not crash, and must
    // not silently downgrade every lecturer to the stacked layout.
    vi.stubGlobal('matchMedia', undefined)
    ;(window as unknown as { matchMedia: unknown }).matchMedia = undefined
    const { result, stop } = withScope(() => useAuthoringLayout('practice'))
    expect(result.canUseRail.value).toBe(true)
    expect(result.effectiveMode.value).toBe('rail')
    stop()
  })
})
