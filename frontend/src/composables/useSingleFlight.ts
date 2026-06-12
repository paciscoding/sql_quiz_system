import { ref, type Ref } from 'vue'

export interface SingleFlight<Args extends any[], R> {
  inFlight: Ref<boolean>
  run: (...args: Args) => Promise<R | undefined>
}

/**
 * Wraps an async fn so concurrent calls are dropped while one is in flight.
 * inFlight flips synchronously to true on call entry and back to false after the awaited fn settles.
 */
export function useSingleFlight<Args extends any[], R>(fn: (...args: Args) => Promise<R>): SingleFlight<Args, R> {
  const inFlight = ref(false)

  async function run(...args: Args): Promise<R | undefined> {
    if (inFlight.value) return undefined

    inFlight.value = true
    try {
      return await fn(...args)
    } finally {
      inFlight.value = false
    }
  }

  return { inFlight, run }
}
