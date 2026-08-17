import { ref, onUnmounted, getCurrentInstance, type Ref } from 'vue'

export interface EmailJobStatus {
  job_id: string
  total: number
  sent: number
  failed: number
  done: boolean
  failures: string[]
}

/** The in-flight job id survives a reload: the worker outlives the page. */
export const EMAIL_JOB_STORAGE_KEY = 'sql_quiz_email_job'

const POLL_INTERVAL_MS = 2000
/** Stop polling after this long even if the job never reports done. */
const POLL_CEILING_MS = 15 * 60 * 1000

export type EmailJobState = 'idle' | 'running' | 'done' | 'unavailable'

/**
 * Polls the credential-email job endpoint so AdminView can show real send
 * progress instead of a static "queued". A 404 is NOT an error: the registry is
 * in-memory, so a server restart or the 30-minute retention window legitimately
 * drops the job (or it belongs to a different admin, which is deliberately
 * indistinguishable from "unknown") — and every password is already in the
 * admin's results table, so this is treated as a terminal state, not a toast.
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

  function stop() {
    epoch += 1
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  async function poll(jobId: string) {
    const myEpoch = epoch
    const token = localStorage.getItem('sql_quiz_token')
    let response: Response
    try {
      response = await fetch(
        `/api/v1/students/import-credentials/${encodeURIComponent(jobId)}/status`,
        { headers: { Authorization: `Bearer ${token}` } },
      )
    } catch {
      if (myEpoch !== epoch) return
      state.value = 'unavailable'
      stop()
      return
    }

    if (myEpoch !== epoch) return

    if (!response.ok) {
      // A 404 here is an ordinary, expected outcome (job expired from the
      // in-memory registry, server restarted, or it belongs to another admin
      // — deliberately indistinguishable from "unknown"), never an error toast.
      state.value = 'unavailable'
      stop()
      return
    }

    let parsed: EmailJobStatus
    try {
      parsed = (await response.json()) as EmailJobStatus
    } catch {
      if (myEpoch !== epoch) return
      state.value = 'unavailable'
      stop()
      return
    }

    if (myEpoch !== epoch) return

    job.value = parsed
    if (job.value.done) {
      state.value = 'done'
      stop()
      return
    }
    state.value = 'running'
    if (Date.now() - startedAt > POLL_CEILING_MS) {
      state.value = 'unavailable'
      stop()
      return
    }
    timer = setTimeout(() => void poll(jobId), POLL_INTERVAL_MS)
  }

  function track(jobId: string) {
    if (!jobId) return
    stop()
    startedAt = Date.now()
    localStorage.setItem(EMAIL_JOB_STORAGE_KEY, jobId)
    state.value = 'running'
    void poll(jobId)
  }

  function restore() {
    const stored = localStorage.getItem(EMAIL_JOB_STORAGE_KEY)
    if (stored) track(stored)
  }

  function dismiss() {
    stop()
    localStorage.removeItem(EMAIL_JOB_STORAGE_KEY)
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
