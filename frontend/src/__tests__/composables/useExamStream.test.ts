/**
 * Tests for useExamStream composable (TODO-011, TODO-013)
 *
 * We mock EventSource globally and use vi.useFakeTimers() for the 15-second
 * fallback-poll interval. fetch is also mocked to control poll responses.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ref, nextTick, defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { useExamStream } from '../../composables/useExamStream'

// ---------------------------------------------------------------------------
// EventSource mock
// ---------------------------------------------------------------------------
type ESListener = (event: MessageEvent) => void

class MockEventSource {
  static instances: MockEventSource[] = []

  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  private listeners: Record<string, ESListener[]> = {}
  closed = false

  constructor(url: string) {
    this.url = url
    MockEventSource.instances.push(this)
  }

  addEventListener(type: string, handler: ESListener) {
    if (!this.listeners[type]) this.listeners[type] = []
    this.listeners[type].push(handler)
  }

  removeEventListener(type: string, handler: ESListener) {
    if (this.listeners[type]) {
      this.listeners[type] = this.listeners[type].filter((h) => h !== handler)
    }
  }

  /** Test helper: simulate the server emitting an event */
  emit(type: string, data: unknown) {
    const event = new MessageEvent(type, { data: JSON.stringify(data) })
    ;(this.listeners[type] ?? []).forEach((h) => h(event))
  }

  close() {
    this.closed = true
  }

  static reset() {
    MockEventSource.instances = []
  }

  static last(): MockEventSource {
    return MockEventSource.instances[MockEventSource.instances.length - 1]
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function mountWithSetup<T>(fn: () => T) {
  let result!: T
  const wrapper = mount(
    defineComponent({
      setup() {
        result = fn()
        return {}
      },
      template: '<div />',
    })
  )
  return { wrapper, result }
}

// ---------------------------------------------------------------------------
// Setup / teardown
// ---------------------------------------------------------------------------
beforeEach(() => {
  MockEventSource.reset()
  vi.useFakeTimers()
  // @ts-expect-error — replacing browser global with mock
  globalThis.EventSource = MockEventSource
  // Default fetch: returns empty array (no matching exam)
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => [],
  })
  globalThis.localStorage = {
    getItem: vi.fn().mockReturnValue('test-token'),
    setItem: vi.fn(),
    removeItem: vi.fn(),
    clear: vi.fn(),
    length: 0,
    key: vi.fn(),
  } as unknown as Storage
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------
describe('useExamStream', () => {
  // -------------------------------------------------------------------------
  // (a) Parses SSE 'exam-status' event and exposes effectiveState
  // -------------------------------------------------------------------------
  it('parses exam-status SSE event and updates effectiveState to active', async () => {
    const examId = ref<number | null>(1)
    const { result } = mountWithSetup(() => useExamStream(examId))

    // Initial state
    expect(result.effectiveState.value).toBe('inactive')

    // Simulate server emitting active state via SSE
    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    es.emit('exam-status', { exam_id: 1, effective_state: 'active', server_time: new Date().toISOString() })
    await nextTick()

    expect(result.effectiveState.value).toBe('active')
  })

  it('parses exam-status SSE event with scheduled state', async () => {
    const examId = ref<number | null>(2)
    const { result } = mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    es.emit('exam-status', { exam_id: 2, effective_state: 'scheduled', server_time: new Date().toISOString() })
    await nextTick()

    expect(result.effectiveState.value).toBe('scheduled')
  })

  it('ignores malformed SSE event data (no throw)', async () => {
    const examId = ref<number | null>(3)
    const { result } = mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    // Access internal listeners to emit a raw non-JSON event
    const anyEs = es as unknown as { listeners: Record<string, ESListener[]> }
    const badEvent = new MessageEvent('exam-status', { data: 'not-json' })
    ;(anyEs.listeners['exam-status'] ?? []).forEach((h) => h(badEvent))
    await nextTick()

    expect(result.effectiveState.value).toBe('inactive') // unchanged
  })

  // -------------------------------------------------------------------------
  // (b) ~15s fallback poll is set up while waiting and cleared on active
  // -------------------------------------------------------------------------
  it('starts fallback poll after SSE opens and clears it when state becomes active via SSE', async () => {
    const examId = ref<number | null>(4)
    mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    // Poll interval should be set — advance 15 s and check fetch was called
    await vi.advanceTimersByTimeAsync(15_000)
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/v1/quiz/exams?include_inactive=true',
      expect.objectContaining({ headers: expect.objectContaining({ Authorization: 'Bearer test-token' }) })
    )

    const fetchCallsBefore = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.length

    // Now SSE fires active — poll should stop
    es.emit('exam-status', { exam_id: 4, effective_state: 'active', server_time: new Date().toISOString() })
    await nextTick()

    // Advance another 15 s — fetch should NOT be called again
    await vi.advanceTimersByTimeAsync(15_000)
    expect((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.length).toBe(fetchCallsBefore)
  })

  it('poll updates effectiveState when API returns active', async () => {
    // Arrange: fetch will return active exam on first poll
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => [{ id: 5, effective_state: 'active' }],
    })

    const examId = ref<number | null>(5)
    const { result } = mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    expect(result.effectiveState.value).toBe('inactive')

    // Trigger the 15-second poll
    await vi.advanceTimersByTimeAsync(15_000)
    // Allow microtasks (fetch promise) to resolve
    await vi.runAllTimersAsync()
    await nextTick()

    expect(result.effectiveState.value).toBe('active')
  })

  // -------------------------------------------------------------------------
  // (c) close() tears down EventSource and interval
  // -------------------------------------------------------------------------
  it('close() closes the EventSource and stops the fallback poll', async () => {
    const examId = ref<number | null>(6)
    const { result } = mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    // close composable
    result.close()
    await nextTick()

    expect(es.closed).toBe(true)
    expect(result.connected.value).toBe(false)

    const fetchCallsBefore = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.length
    // Advance timer — poll should NOT fire
    await vi.advanceTimersByTimeAsync(15_000)
    expect((globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.length).toBe(fetchCallsBefore)
  })

  it('close() on unmount — tears down EventSource', async () => {
    const examId = ref<number | null>(7)
    const { wrapper } = mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    await nextTick()

    wrapper.unmount()
    await nextTick()

    expect(es.closed).toBe(true)
  })

  // -------------------------------------------------------------------------
  // Changing examId closes old EventSource and opens new one
  // -------------------------------------------------------------------------
  it('changing examId closes old EventSource and opens new one', async () => {
    const examId = ref<number | null>(8)
    mountWithSetup(() => useExamStream(examId))

    const firstEs = MockEventSource.last()
    expect(firstEs).toBeDefined()

    examId.value = 9
    await nextTick()

    const secondEs = MockEventSource.last()
    expect(secondEs).not.toBe(firstEs)
    expect(firstEs.closed).toBe(true)
    expect(secondEs.url).toContain('/api/v1/quiz/exams/9/stream')
  })

  // -------------------------------------------------------------------------
  // Setting examId to null closes stream and resets effectiveState
  // -------------------------------------------------------------------------
  it('setting examId to null closes stream and resets state to inactive', async () => {
    const examId = ref<number | null>(10)
    const { result } = mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    es.onopen?.()
    es.emit('exam-status', { exam_id: 10, effective_state: 'scheduled' })
    await nextTick()

    expect(result.effectiveState.value).toBe('scheduled')

    examId.value = null
    await nextTick()

    expect(result.effectiveState.value).toBe('inactive')
    expect(es.closed).toBe(true)
  })

  // -------------------------------------------------------------------------
  // URL contains access_token from localStorage
  // -------------------------------------------------------------------------
  it('opens EventSource with access_token query param from localStorage', async () => {
    const examId = ref<number | null>(11)
    mountWithSetup(() => useExamStream(examId))

    const es = MockEventSource.last()
    expect(es.url).toContain('access_token=test-token')
    expect(es.url).toContain('/api/v1/quiz/exams/11/stream')
  })

  // -------------------------------------------------------------------------
  // No interval leak on repeated examId changes
  // -------------------------------------------------------------------------
  it('no interval leak on repeated examId changes — only one poll fires', async () => {
    const examId = ref<number | null>(12)
    mountWithSetup(() => useExamStream(examId))

    // Open stream and start poll
    MockEventSource.last().onopen?.()
    await nextTick()

    // Change examId several times rapidly
    for (let id = 13; id <= 16; id++) {
      examId.value = id
      await nextTick()
      MockEventSource.last().onopen?.()
      await nextTick()
    }

    const fetchBefore = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.length
    await vi.advanceTimersByTimeAsync(15_000)
    const fetchAfter = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.length

    // At most one interval should be running (for the last examId)
    // so at most 1 call should occur
    expect(fetchAfter - fetchBefore).toBeLessThanOrEqual(1)
  })
})
