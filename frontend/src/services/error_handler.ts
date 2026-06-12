import { useNotifications } from './notification_service'

export interface HandleApiErrorContext {
  fallback?: string
  silent?: boolean
  toastDuration?: number
}

/**
 * Normalize an arbitrary thrown value into a user-facing string and surface it as an error toast.
 * Returns the normalized string so callers may use it for additional logging.
 */
export function handleApiError(err: unknown, ctx: HandleApiErrorContext = {}): string {
  const fallback = ctx.fallback || 'Something went wrong.'
  let message = fallback

  if (err instanceof Error) {
    message = err.message || fallback
  } else if (typeof err === 'string') {
    message = err || fallback
  } else if (err && typeof err === 'object' && 'message' in err && typeof err.message === 'string') {
    message = err.message || fallback
  } else if (err && typeof err === 'object' && 'error' in err && typeof err.error === 'string') {
    message = err.error || fallback
  }

  if (!ctx.silent) {
    const { notify } = useNotifications()
    notify(message, 'error', ctx.toastDuration ?? 5000)
  }

  return message
}

/**
 * Helper to convert a non-OK Response into a thrown Error with the best available message.
 * Use as: if (!res.ok) throw await toApiError(res, 'Failed to save question')
 */
export async function toApiError(res: Response, fallback: string): Promise<Error> {
  let body: unknown = null

  try {
    body = await res.json()
  } catch {
    // Not JSON.
  }

  const message =
    body && typeof body === 'object' && 'error' in body && typeof body.error === 'string'
      ? body.error
      : body && typeof body === 'object' && 'message' in body && typeof body.message === 'string'
        ? body.message
        : fallback + ' (HTTP ' + res.status + ')'

  return new Error(message)
}
