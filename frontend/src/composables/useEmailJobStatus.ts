import { ref, onUnmounted, getCurrentInstance, type Ref } from 'vue'

export interface EmailJobStatus {
  job_id: string
  total: number
  sent: number
  failed: number
  done: boolean
  failures: string[]
}

/** The in-flight job ids survive a reload: the worker outlives the page. */
export const EMAIL_JOB_STORAGE_KEY = 'sql_quiz_email_job'

const POLL_INTERVAL_MS = 2000
/** Stop polling after this long even if a job never reports done. */
const POLL_CEILING_MS = 15 * 60 * 1000

export type EmailJobState = 'idle' | 'running' | 'done' | 'unavailable'

/**
 * Polls the credential-email job endpoint so AdminView can show real send
 * progress instead of a static "queued". A 404 is NOT an error: the registry is
 * in-memory, so a server restart or the 30-minute retention window legitimately
 * drops the job (or it belongs to a different admin, which is deliberately
 * indistinguishable from "unknown") — and every password is already in the
 * admin's results table, so this is treated as a terminal state, not a toast.
 *
 * SEVERAL jobs are tracked at once and reported as ONE aggregate. Bulk password
 * reset is paginated: a 250-student reset is three requests and therefore three
 * independent email jobs. Tracking only the newest would show "40/100" while
 * 150 further emails were also in flight — a number that answers a question the
 * admin never asked. Summing them makes the bubble report the whole run.
 */
export function useEmailJobStatus(): {
  job: Ref<EmailJobStatus | null>
  state: Ref<EmailJobState>
  track: (jobId: string) => void
  restore: () => void
  dismiss: () => void
} {
  const job = ref<EmailJobStatus | null>(null)
  const state = ref<EmailJobState>('idle')

  let timer: ReturnType<typeof setTimeout> | null = null
  let startedAt = 0
  // Monotonic request identity. Every track()/dismiss() bumps this via stop(),
  // which invalidates any poll() continuation still suspended on a stale
  // epoch — it must not mutate job/state or reschedule itself once it wakes.
  let epoch = 0

  /** Tracked ids, in the order they were started. */
  let tracked: string[] = []
  /** Last known status per id; absent until that id's first successful poll. */
  const statuses = new Map<string, EmailJobStatus>()
  /** Ids whose status can no longer be fetched (404, network, bad body). */
  const unavailable = new Set<string>()

  function stop() {
    epoch += 1
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  function reset() {
    tracked = []
    statuses.clear()
    unavailable.clear()
  }

  /** True once an id needs no further polling: it finished, or it is gone. */
  function isSettled(id: string): boolean {
    return unavailable.has(id) || statuses.get(id)?.done === true
  }

  // Sums every tracked job into the single status the bubble renders. Ids that
  // went unavailable contribute whatever was last known about them (nothing, if
  // they were never successfully polled) rather than being dropped — dropping
  // them would make `total` shrink mid-run and the progress bar jump backwards.
  function recompute() {
    if (tracked.length === 0) {
      job.value = null
      state.value = 'idle'
      return
    }

    const known = tracked.map(id => statuses.get(id)).filter((s): s is EmailJobStatus => !!s)
    job.value = {
      job_id: tracked.join(','),
      total: known.reduce((n, s) => n + s.total, 0),
      sent: known.reduce((n, s) => n + s.sent, 0),
      failed: known.reduce((n, s) => n + s.failed, 0),
      done: tracked.every(isSettled),
      failures: known.flatMap(s => s.failures ?? []),
    }

    if (known.length === 0) {
      // Every tracked id is unpollable and none ever reported: there is no
      // progress to show at all, which is what 'unavailable' means.
      state.value = 'unavailable'
      return
    }
    state.value = tracked.every(isSettled) ? 'done' : 'running'
  }

  async function pollOnce(id: string, myEpoch: number): Promise<void> {
    const token = localStorage.getItem('sql_quiz_token')
    let response: Response
    try {
      response = await fetch(
        `/api/v1/students/import-credentials/${encodeURIComponent(id)}/status`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
    } catch {
      if (myEpoch !== epoch) return
      unavailable.add(id)
      return
    }

    if (myEpoch !== epoch) return

    if (!response.ok) {
      // A 404 here is an ordinary, expected outcome (job expired from the
      // in-memory registry, server restarted, or it belongs to another admin
      // — deliberately indistinguishable from "unknown"), never an error toast.
      unavailable.add(id)
      return
    }

    let parsed: EmailJobStatus
    try {
      parsed = (await response.json()) as EmailJobStatus
    } catch {
      if (myEpoch !== epoch) return
      unavailable.add(id)
      return
    }

    if (myEpoch !== epoch) return
    statuses.set(id, parsed)
    // Recompute per response rather than only once the whole cycle settles, so
    // a slow or hung job cannot hold back the numbers its siblings already
    // reported.
    recompute()
  }

  async function pollCycle() {
    const myEpoch = epoch
    // Snapshot: track() during this cycle must not extend the loop being run
    // under an older epoch — that id is picked up by the cycle track() starts.
    const pending = tracked.filter(id => !isSettled(id))

    // Concurrently, NOT in sequence: these are independent batches of one run,
    // and awaiting them one after another would let a single slow job stall
    // every other batch's polling behind it.
    await Promise.all(pending.map(id => pollOnce(id, myEpoch)))
    if (myEpoch !== epoch) return

    recompute()

    if (tracked.every(isSettled)) {
      stop()
      return
    }
    if (Date.now() - startedAt > POLL_CEILING_MS) {
      state.value = 'unavailable'
      stop()
      return
    }
    timer = setTimeout(() => void pollCycle(), POLL_INTERVAL_MS)
  }

  function persist() {
    localStorage.setItem(EMAIL_JOB_STORAGE_KEY, JSON.stringify(tracked))
  }

  function track(jobId: string) {
    if (!jobId) return
    if (tracked.includes(jobId)) return
    // stop() before mutating: it bumps the epoch, so any cycle already in
    // flight retires instead of racing the one started below.
    stop()
    if (tracked.length === 0) startedAt = Date.now()
    tracked.push(jobId)
    persist()
    state.value = 'running'
    void pollCycle()
  }

  function restore() {
    const stored = localStorage.getItem(EMAIL_JOB_STORAGE_KEY)
    if (!stored) return
    // Accepts the legacy single-id string this key used to hold, so an admin
    // with a page open across the upgrade keeps their in-flight job.
    let ids: string[]
    try {
      const parsed = JSON.parse(stored)
      ids = Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : [stored]
    } catch {
      ids = [stored]
    }
    ids.forEach(track)
  }

  function dismiss() {
    stop()
    localStorage.removeItem(EMAIL_JOB_STORAGE_KEY)
    reset()
    job.value = null
    state.value = 'idle'
  }

  // Guard: onUnmounted throws/warns when called outside an active component
  // instance (e.g. in composable unit tests that call useEmailJobStatus()
  // directly rather than mounting a host component).
  if (getCurrentInstance()) {
    onUnmounted(stop)
  }

  return { job, state, track, restore, dismiss }
}
