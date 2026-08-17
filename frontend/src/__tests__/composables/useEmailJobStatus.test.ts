import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEmailJobStatus, EMAIL_JOB_STORAGE_KEY } from '../../composables/useEmailJobStatus'

function jsonResponse(data: unknown, ok = true, status = 200) {
  return Promise.resolve({ ok, status, json: () => Promise.resolve(data) } as Response)
}

describe('useEmailJobStatus', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('polls until the job is done, then stops', async () => {
    const fetchMock = vi.fn()
      .mockReturnValueOnce(jsonResponse({ job_id: 'j1', total: 2, sent: 1, failed: 0, done: false, failures: [] }))
      .mockReturnValueOnce(jsonResponse({ job_id: 'j1', total: 2, sent: 2, failed: 0, done: true, failures: [] }))
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track } = useEmailJobStatus()
    track('j1')
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('running')
    expect(job.value?.sent).toBe(1)

    await vi.advanceTimersByTimeAsync(2000)
    expect(state.value).toBe('done')
    expect(job.value?.sent).toBe(2)

    const callsAtDone = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchMock.mock.calls.length).toBe(callsAtDone)
  })

  it('enters "unavailable" on 404 instead of erroring', async () => {
    const fetchMock = vi.fn(() => jsonResponse({ error: 'job not found' }, false, 404))
    vi.stubGlobal('fetch', fetchMock)

    const { state, track } = useEmailJobStatus()
    track('gone')
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('unavailable')

    const callsAtUnavailable = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchMock.mock.calls.length).toBe(callsAtUnavailable)
  })

  it('a second track() while the first fetch is pending leaves only the second chain alive', async () => {
    let resolveJ1: (r: Response) => void
    const j1Pending = new Promise<Response>((resolve) => {
      resolveJ1 = resolve
    })
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('j1')) return j1Pending
      return jsonResponse({ job_id: 'j2', total: 1, sent: 0, failed: 0, done: false, failures: [] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track } = useEmailJobStatus()
    track('j1')
    // j1's poll() is now suspended awaiting fetch — no timer scheduled yet.
    track('j2')
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('running')
    expect(job.value?.job_id).toBe('j2')

    const j1CallsBeforeResolve = fetchMock.mock.calls.filter((c) => String(c[0]).includes('j1')).length
    const totalCallsBeforeResolve = fetchMock.mock.calls.length
    // Now let the orphaned j1 fetch resolve.
    resolveJ1!({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ job_id: 'j1', total: 1, sent: 1, failed: 0, done: false, failures: [] }),
    } as Response)
    await vi.advanceTimersByTimeAsync(0)

    // The j1 continuation must not have mutated shared state.
    expect(job.value?.job_id).toBe('j2')

    // Advance well past a poll interval: only j2's chain should still be
    // fetching; j1's orphaned continuation must never fetch again (no new
    // timer for j1 was scheduled by the resolved continuation above).
    await vi.advanceTimersByTimeAsync(2000)
    const j1CallsAfter = fetchMock.mock.calls.filter((c) => String(c[0]).includes('j1')).length
    expect(j1CallsAfter).toBe(j1CallsBeforeResolve)
    expect(job.value?.job_id).toBe('j2')
    expect(fetchMock.mock.calls.length).toBeGreaterThan(totalCallsBeforeResolve)
  })

  it('dismiss() while a fetch is in flight does not let the response resurrect state', async () => {
    let resolveFetch: (r: Response) => void
    const pending = new Promise<Response>((resolve) => {
      resolveFetch = resolve
    })
    const fetchMock = vi.fn(() => pending)
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track, dismiss } = useEmailJobStatus()
    track('j3')
    // poll() is suspended awaiting fetch here.
    dismiss()
    expect(state.value).toBe('idle')
    expect(job.value).toBeNull()

    resolveFetch!({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ job_id: 'j3', total: 1, sent: 0, failed: 0, done: false, failures: [] }),
    } as Response)
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('idle')
    expect(job.value).toBeNull()

    const callsAfterResolve = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchMock.mock.calls.length).toBe(callsAfterResolve)
  })

  it('persists the tracked job and restores it on a fresh mount', async () => {
    vi.stubGlobal('fetch', vi.fn(() =>
      jsonResponse({ job_id: 'j9', total: 5, sent: 3, failed: 1, done: false, failures: ['s1'] })))

    const first = useEmailJobStatus()
    first.track('j9')
    await vi.advanceTimersByTimeAsync(0)
    expect(localStorage.getItem(EMAIL_JOB_STORAGE_KEY)).toBe('j9')

    const second = useEmailJobStatus()
    second.restore()
    await vi.advanceTimersByTimeAsync(0)
    expect(second.job.value?.job_id).toBe('j9')
    expect(second.state.value).toBe('running')
  })

  it('dismiss clears the stored job and returns to idle', async () => {
    vi.stubGlobal('fetch', vi.fn(() =>
      jsonResponse({ job_id: 'j2', total: 1, sent: 1, failed: 0, done: true, failures: [] })))

    const { state, track, dismiss } = useEmailJobStatus()
    track('j2')
    await vi.advanceTimersByTimeAsync(0)
    expect(state.value).toBe('done')

    dismiss()
    expect(state.value).toBe('idle')
    expect(localStorage.getItem(EMAIL_JOB_STORAGE_KEY)).toBeNull()
  })
})
