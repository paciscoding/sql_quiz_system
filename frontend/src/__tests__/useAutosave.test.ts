import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ref, nextTick } from 'vue'
import { useAutosave } from '../composables/useAutosave'

// ---------------------------------------------------------------------------
// Minimal in-memory Storage mock (injectable, no real localStorage)
// ---------------------------------------------------------------------------
function makeStorage(): Storage {
  const store: Record<string, string> = {}
  return {
    getItem: (k: string) => store[k] ?? null,
    setItem: (k: string, v: string) => { store[k] = v },
    removeItem: (k: string) => { delete store[k] },
    clear: () => { for (const k in store) delete store[k] },
    get length() { return Object.keys(store).length },
    key: (i: number) => Object.keys(store)[i] ?? null,
  }
}

describe('useAutosave composable', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('writes to storage after debounce delay', async () => {
    const storage = makeStorage()
    const val = ref('initial')
    useAutosave('draft-key', val, { debounceMs: 300, minLength: 5, storage })

    val.value = 'hello world!!'
    // Vue watch fires after nextTick; then the debounce timer must elapse
    await nextTick()
    expect(storage.getItem('draft-key')).toBeNull() // timer hasn't fired yet

    vi.advanceTimersByTime(299)
    expect(storage.getItem('draft-key')).toBeNull() // still not yet

    vi.advanceTimersByTime(1) // crosses 300ms
    expect(storage.getItem('draft-key')).toBe('hello world!!')
  })

  it('does not write before the debounce delay elapses', async () => {
    const storage = makeStorage()
    const val = ref('')
    useAutosave('k', val, { debounceMs: 500, minLength: 1, storage })

    val.value = 'abc'
    await nextTick()
    vi.advanceTimersByTime(499)
    expect(storage.getItem('k')).toBeNull()
  })

  it('multiple rapid writes coalesce into one storage write', async () => {
    const storage = makeStorage()
    const setItemSpy = vi.spyOn(storage, 'setItem')
    const val = ref('')
    useAutosave('k', val, { debounceMs: 200, minLength: 1, storage })

    val.value = 'a'
    await nextTick()
    vi.advanceTimersByTime(50) // debounce started; 50ms elapsed

    val.value = 'ab'
    await nextTick()
    vi.advanceTimersByTime(50) // 50ms into second debounce window

    val.value = 'abc'
    await nextTick()
    vi.advanceTimersByTime(50) // 50ms into third debounce window

    // 200ms debounce from last write hasn't elapsed yet
    vi.advanceTimersByTime(200) // now it elapses
    expect(setItemSpy).toHaveBeenCalledTimes(1)
    expect(storage.getItem('k')).toBe('abc')
  })

  it('respects minLength — does not write when value is below threshold', async () => {
    const storage = makeStorage()
    const val = ref('')
    useAutosave('k', val, { debounceMs: 100, minLength: 10, storage })

    val.value = 'short'  // length 5, below minLength 10
    await nextTick()
    vi.advanceTimersByTime(200)
    expect(storage.getItem('k')).toBeNull()
  })

  it('removes the key when value drops below minLength', async () => {
    const storage = makeStorage()
    storage.setItem('k', 'previously saved long text')

    const val = ref('previously saved long text')
    useAutosave('k', val, { debounceMs: 100, minLength: 10, storage })

    val.value = 'short' // below threshold → write() will removeItem
    await nextTick()
    vi.advanceTimersByTime(200)
    expect(storage.getItem('k')).toBeNull()
  })

  it('clearDraft removes the key', () => {
    const storage = makeStorage()
    storage.setItem('k', 'some draft')
    const val = ref('')
    const { clearDraft } = useAutosave('k', val, { storage })

    clearDraft()
    expect(storage.getItem('k')).toBeNull()
  })

  it('hasDraft returns true when a draft exists, false otherwise', () => {
    const storage = makeStorage()
    const val = ref('')
    const { hasDraft } = useAutosave('k', val, { storage })

    expect(hasDraft()).toBe(false)
    storage.setItem('k', 'draft content')
    expect(hasDraft()).toBe(true)
  })

  it('loadDraft returns null when no draft, returns saved string otherwise', () => {
    const storage = makeStorage()
    const val = ref('')
    const { loadDraft } = useAutosave('k', val, { storage })

    expect(loadDraft()).toBeNull()
    storage.setItem('k', 'my draft')
    expect(loadDraft()).toBe('my draft')
  })

  it('restoreDraft returns false and does not mutate ref when no draft', () => {
    const storage = makeStorage()
    const val = ref('original')
    const { restoreDraft } = useAutosave('k', val, { storage })

    const result = restoreDraft()
    expect(result).toBe(false)
    expect(val.value).toBe('original')
  })

  it('restoreDraft sets valueRef and returns true when draft exists', () => {
    const storage = makeStorage()
    storage.setItem('k', 'saved draft text')
    const val = ref('')
    const { restoreDraft } = useAutosave('k', val, { storage })

    const result = restoreDraft()
    expect(result).toBe(true)
    expect(val.value).toBe('saved draft text')
  })

  it('flush writes pending value immediately and cancels pending timer', async () => {
    const storage = makeStorage()
    const val = ref('')
    const { flush } = useAutosave('k', val, { debounceMs: 1000, minLength: 1, storage })

    val.value = 'content to flush'
    await nextTick()
    // Debounce hasn't fired yet (0ms elapsed after nextTick)
    expect(storage.getItem('k')).toBeNull()

    flush()
    // Should write synchronously
    expect(storage.getItem('k')).toBe('content to flush')

    // No additional writes when timer would have fired
    vi.advanceTimersByTime(2000)
    expect(storage.getItem('k')).toBe('content to flush')
  })
})
