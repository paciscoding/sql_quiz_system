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

  // Tracking is now cumulative (bulk reset is paginated and starts one job per
  // batch), so a second track() ADDS a job rather than replacing the first.
  // The guarantee this test still enforces is the one that mattered before:
  // exactly ONE polling chain stays alive across a re-track, so an orphaned
  // continuation can neither mutate shared state nor double the poll rate.
  it('a second track() while the first fetch is pending leaves exactly one chain alive, covering both jobs', async () => {
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
    // Both jobs are tracked; j2 has reported, j1 is still pending, so the
    // aggregate names both and carries j2's numbers so far.
    expect(job.value?.job_id).toBe('j1,j2')
    expect(job.value?.total).toBe(1)

    // j2's numbers are visible even though j1 has not answered yet: a hung
    // batch must not hold back the progress its siblings already reported.
    expect(job.value?.sent).toBe(0)

    resolveJ1!({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ job_id: 'j1', total: 1, sent: 1, failed: 0, done: false, failures: [] }),
    } as Response)
    await vi.advanceTimersByTimeAsync(0)

    // Both batches now counted.
    expect(job.value?.total).toBe(2)
    expect(job.value?.sent).toBe(1)

    // One interval later exactly ONE chain has run, polling each of the two
    // unsettled jobs once — not two chains racing at double the poll rate.
    const callsBeforeTick = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(2000)
    expect(fetchMock.mock.calls.length).toBe(callsBeforeTick + 2)
  })

  it('sums several tracked jobs into one progress figure', async () => {
    const fetchMock = vi.fn((url: string) => {
      if (String(url).includes('batch-a')) {
        return jsonResponse({ job_id: 'batch-a', total: 100, sent: 100, failed: 0, done: true, failures: [] })
      }
      return jsonResponse({ job_id: 'batch-b', total: 50, sent: 20, failed: 2, done: false, failures: ['s7', 's8'] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track } = useEmailJobStatus()
    track('batch-a')
    track('batch-b')
    await vi.advanceTimersByTimeAsync(0)

    // The admin asked for 150 students, not for whichever batch happens to be
    // newest — the bubble must report the whole run.
    expect(job.value?.total).toBe(150)
    expect(job.value?.sent).toBe(120)
    expect(job.value?.failed).toBe(2)
    expect(job.value?.failures).toEqual(['s7', 's8'])
    // One batch finished, one has not: the run as a whole is still running.
    expect(state.value).toBe('running')
  })

  it('is only done once every tracked job is done', async () => {
    let bDone = false
    const fetchMock = vi.fn((url: string) => {
      if (String(url).includes('batch-a')) {
        return jsonResponse({ job_id: 'batch-a', total: 1, sent: 1, failed: 0, done: true, failures: [] })
      }
      return jsonResponse({ job_id: 'batch-b', total: 1, sent: bDone ? 1 : 0, failed: 0, done: bDone, failures: [] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track } = useEmailJobStatus()
    track('batch-a')
    track('batch-b')
    await vi.advanceTimersByTimeAsync(0)
    expect(state.value).toBe('running')

    bDone = true
    await vi.advanceTimersByTimeAsync(2000)
    expect(state.value).toBe('done')
    expect(job.value?.sent).toBe(2)

    // A settled job is never polled again.
    const callsAtDone = fetchMock.mock.calls.length
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchMock.mock.calls.length).toBe(callsAtDone)
  })

  it('keeps an unavailable job in the total instead of shrinking the run', async () => {
    const fetchMock = vi.fn((url: string) => {
      if (String(url).includes('gone')) return jsonResponse({ error: 'job not found' }, false, 404)
      return jsonResponse({ job_id: 'live', total: 10, sent: 4, failed: 0, done: false, failures: [] })
    })
    vi.stubGlobal('fetch', fetchMock)

    const { job, state, track } = useEmailJobStatus()
    track('live')
    track('gone')
    await vi.advanceTimersByTimeAsync(0)

    // One job unpollable does not make the other one's progress disappear.
    expect(state.value).toBe('running')
    expect(job.value?.total).toBe(10)
    expect(job.value?.sent).toBe(4)
  })

  it('restores a legacy single-id string written before multi-job tracking', async () => {
    vi.stubGlobal('fetch', vi.fn(() =>
      jsonResponse({ job_id: 'old', total: 3, sent: 1, failed: 0, done: false, failures: [] })))
    // An admin whose page was open across the upgrade still has the old format
    // in localStorage; their in-flight job must not be dropped on the floor.
    localStorage.setItem(EMAIL_JOB_STORAGE_KEY, 'old')

    const { job, state, restore } = useEmailJobStatus()
    restore()
    await vi.advanceTimersByTimeAsync(0)

    expect(state.value).toBe('running')
    expect(job.value?.total).toBe(3)
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
    // Stored as a JSON array now: several batches can be in flight at once.
    expect(localStorage.getItem(EMAIL_JOB_STORAGE_KEY)).toBe('["j9"]')

    const second = useEmailJobStatus()
    second.restore()
    await vi.advanceTimersByTimeAsync(0)
    expect(second.job.value?.job_id).toBe('j9')
    expect(second.job.value?.sent).toBe(3)
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
