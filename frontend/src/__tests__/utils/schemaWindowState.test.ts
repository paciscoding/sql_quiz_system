/**
 * Tests for TODO-049..064 — schemaWindowState utility
 *
 * Covers loadState, saveState (per-key debounce), and clearAllSchemaWindowState.
 * The key regression under test: calling saveState for two DIFFERENT keys in
 * quick succession must persist BOTH keys (the prior bug dropped one due to a
 * shared timer).
 */
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { loadState, saveState, clearAllSchemaWindowState, type WindowState } from '../../utils/schemaWindowState'

const makeState = (overrides: Partial<WindowState> = {}): WindowState => ({
  mode: 'normal',
  pos: { x: 10, y: 20 },
  size: { w: 400, h: 300 },
  currentIndex: 0,
  ...overrides,
})

beforeEach(() => {
  localStorage.clear()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('loadState', () => {
  it('returns null for a key that has never been saved', () => {
    expect(loadState('category', '99')).toBeNull()
  })

  it('returns the parsed WindowState for a previously saved key', () => {
    const state = makeState({ mode: 'collapsed', currentIndex: 2 })
    localStorage.setItem('schema-window:category:5', JSON.stringify(state))

    const loaded = loadState('category', '5')
    expect(loaded).not.toBeNull()
    expect(loaded?.mode).toBe('collapsed')
    expect(loaded?.currentIndex).toBe(2)
    expect(loaded?.pos).toEqual({ x: 10, y: 20 })
  })

  it('returns null and swallows the error for a corrupt JSON value', () => {
    localStorage.setItem('schema-window:exam:3', '{{not-valid-json')
    expect(() => loadState('exam', '3')).not.toThrow()
    expect(loadState('exam', '3')).toBeNull()
  })

  it('returns null for an exam scope when only a category key is stored', () => {
    localStorage.setItem('schema-window:category:1', JSON.stringify(makeState()))
    expect(loadState('exam', '1')).toBeNull()
  })
})

describe('saveState', () => {
  it('does not write before 200ms have elapsed', () => {
    const state = makeState()
    saveState('category', '1', state)
    vi.advanceTimersByTime(199)
    expect(localStorage.getItem('schema-window:category:1')).toBeNull()
  })

  it('writes within 200ms after the call', () => {
    const state = makeState({ currentIndex: 3 })
    saveState('category', '1', state)
    vi.advanceTimersByTime(200)
    const stored = JSON.parse(localStorage.getItem('schema-window:category:1') as string)
    expect(stored.currentIndex).toBe(3)
  })

  it('round-trips all WindowState fields correctly', () => {
    const state = makeState({ mode: 'minimized', pos: { x: 55, y: 66 }, size: { w: 500, h: 200 }, currentIndex: 7 })
    saveState('exam', '42', state)
    vi.advanceTimersByTime(200)
    const loaded = loadState('exam', '42')
    expect(loaded).toEqual(state)
  })

  // ── Regression: per-key debounce ──────────────────────────────────────────
  it('REGRESSION: two different keys saved rapidly both persist (per-key debounce)', () => {
    const stateA = makeState({ currentIndex: 1 })
    const stateB = makeState({ currentIndex: 2 })

    // Call saveState for two DISTINCT keys back-to-back without any timer advance.
    saveState('category', '1', stateA)
    saveState('category', '2', stateB)

    // Neither should be written yet (within debounce window).
    expect(localStorage.getItem('schema-window:category:1')).toBeNull()
    expect(localStorage.getItem('schema-window:category:2')).toBeNull()

    // Advance past the 200ms debounce — both timers should have fired.
    vi.advanceTimersByTime(250)

    const rawA = localStorage.getItem('schema-window:category:1')
    const rawB = localStorage.getItem('schema-window:category:2')

    expect(rawA).not.toBeNull()
    expect(rawB).not.toBeNull()

    expect(JSON.parse(rawA!).currentIndex).toBe(1)
    expect(JSON.parse(rawB!).currentIndex).toBe(2)
  })

  it('two rapid calls for the SAME key coalesce — only the latest value is persisted', () => {
    const stateFirst = makeState({ currentIndex: 10 })
    const stateLast = makeState({ currentIndex: 99 })

    saveState('category', '5', stateFirst)
    vi.advanceTimersByTime(50) // still within debounce window
    saveState('category', '5', stateLast) // resets the timer with new value
    vi.advanceTimersByTime(250) // debounce from second call fires

    const stored = JSON.parse(localStorage.getItem('schema-window:category:5') as string)
    expect(stored.currentIndex).toBe(99)
  })

  it('category and exam scopes with the same numeric id are independent keys', () => {
    const catState = makeState({ currentIndex: 11 })
    const examState = makeState({ currentIndex: 22 })

    saveState('category', '7', catState)
    saveState('exam', '7', examState)
    vi.advanceTimersByTime(250)

    expect(JSON.parse(localStorage.getItem('schema-window:category:7')!).currentIndex).toBe(11)
    expect(JSON.parse(localStorage.getItem('schema-window:exam:7')!).currentIndex).toBe(22)
  })
})

describe('clearAllSchemaWindowState', () => {
  it('removes all schema-window:* keys', () => {
    localStorage.setItem('schema-window:category:1', JSON.stringify(makeState()))
    localStorage.setItem('schema-window:exam:3', JSON.stringify(makeState()))

    clearAllSchemaWindowState()

    expect(localStorage.getItem('schema-window:category:1')).toBeNull()
    expect(localStorage.getItem('schema-window:exam:3')).toBeNull()
  })

  it('leaves unrelated keys untouched', () => {
    localStorage.setItem('sql_quiz_token', 'my-token')
    localStorage.setItem('other-setting', 'value')
    localStorage.setItem('schema-window:category:2', JSON.stringify(makeState()))

    clearAllSchemaWindowState()

    expect(localStorage.getItem('sql_quiz_token')).toBe('my-token')
    expect(localStorage.getItem('other-setting')).toBe('value')
    expect(localStorage.getItem('schema-window:category:2')).toBeNull()
  })

  it('is a no-op when there are no schema-window keys', () => {
    localStorage.setItem('unrelated', 'data')
    expect(() => clearAllSchemaWindowState()).not.toThrow()
    expect(localStorage.getItem('unrelated')).toBe('data')
  })
})
