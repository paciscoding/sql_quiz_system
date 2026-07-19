import { ref, watch, onUnmounted } from 'vue'
import type { Ref } from 'vue'

type EffectiveState = 'inactive' | 'scheduled' | 'active' | 'ended'

/**
 * Composable that opens an SSE connection to the exam status stream and
 * exposes the current effective_state of the exam.
 *
 * Authentication: EventSource cannot send Authorization headers, so the JWT
 * is passed as ?access_token= in the query string, and this endpoint's handler
 * reads the token from there (unlike header-only routes).
 *
 * Also runs a 15-second fallback poll while in WAITING state (inactive/scheduled).
 * The first of SSE event or fallback poll to detect 'active' wins.
 * This also acts as the lazy-activation trigger for scheduled exams (the first
 * student poll crossing scheduled_start_at fires the server-side lazy trigger
 * which broadcasts to all open SSE connections).
 */
export function useExamStream(examId: Ref<string | null>) {
  const effectiveState = ref<EffectiveState>('inactive')
  const connected = ref(false)

  let esRef: EventSource | null = null
  let intervalId: ReturnType<typeof setInterval> | null = null

  function clearPoll() {
    if (intervalId !== null) {
      clearInterval(intervalId)
      intervalId = null
    }
  }

  function closeStream() {
    if (esRef) {
      esRef.close()
      esRef = null
    }
    connected.value = false
    clearPoll()
  }

  async function pollExamStatus(id: string) {
    if (effectiveState.value === 'active' || effectiveState.value === 'ended') {
      clearPoll()
      return
    }
    try {
      const token = localStorage.getItem('sql_quiz_token') ?? ''
      const resp = await fetch('/api/v1/quiz/exams?include_inactive=true', {
        headers: { Authorization: `Bearer ${token}` }
      })
      if (!resp.ok) return
      const exams = await resp.json()
      const exam = Array.isArray(exams) ? exams.find((e: { id: string }) => e.id === id) : null
      if (exam && (exam.effective_state === 'active' || exam.effective_state === 'ended')) {
        effectiveState.value = exam.effective_state as EffectiveState
        clearPoll()
      }
    } catch {
      // Ignore network errors — just wait for next poll
    }
  }

  function startPoll(id: string) {
    clearPoll()
    // Only poll while in a waiting state
    if (effectiveState.value === 'inactive' || effectiveState.value === 'scheduled') {
      intervalId = setInterval(() => {
        pollExamStatus(id)
      }, 15000)
    }
  }

  function openStream(id: string) {
    closeStream()
    const token = localStorage.getItem('sql_quiz_token') ?? ''
    const url = `/api/v1/quiz/exams/${id}/stream?access_token=${encodeURIComponent(token)}`
    const es = new EventSource(url)
    esRef = es

    es.onopen = () => {
      connected.value = true
      // Start fallback poll once connected
      startPoll(id)
    }

    es.onerror = () => {
      connected.value = false
      // EventSource auto-reconnects after error; keep the poll running
    }

    es.addEventListener('exam-status', (event: MessageEvent) => {
      try {
        const data = JSON.parse(event.data)
        if (data.effective_state) {
          effectiveState.value = data.effective_state as EffectiveState
          // If now active or ended, stop the fallback poll
          if (effectiveState.value === 'active' || effectiveState.value === 'ended') {
            clearPoll()
          }
        }
      } catch {
        // Ignore malformed events
      }
    })
  }

  watch(
    examId,
    (newId) => {
      if (newId === null || newId === undefined) {
        closeStream()
        effectiveState.value = 'inactive'
        connected.value = false
      } else {
        effectiveState.value = 'inactive'
        openStream(newId)
      }
    },
    { immediate: true }
  )

  onUnmounted(() => {
    closeStream()
  })

  function close() {
    closeStream()
    effectiveState.value = 'inactive'
  }

  return { effectiveState, connected, close }
}
