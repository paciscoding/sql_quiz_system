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
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse({ error: 'job not found' }, false, 404)))

    const { state, track } = useEmailJobStatus()
    track('gone')
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('unavailable')
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
