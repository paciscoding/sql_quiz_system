import { describe, it, expect, vi, beforeEach } from 'vitest'

// ---------------------------------------------------------------------------
// Mock notification_service BEFORE importing error_handler
// ---------------------------------------------------------------------------
const mockNotify = vi.fn()

vi.mock('../services/notification_service', () => ({
  useNotifications: () => ({ notify: mockNotify }),
}))

// Import AFTER the mock is registered
import { handleApiError, toApiError } from '../services/error_handler'

describe('handleApiError', () => {
  beforeEach(() => {
    mockNotify.mockClear()
  })

  it('calls notify with err.message for Error instances', () => {
    handleApiError(new Error('boom'))
    expect(mockNotify).toHaveBeenCalledWith('boom', 'error', 5000)
  })

  it('calls notify with the string itself for string errors', () => {
    handleApiError('something failed')
    expect(mockNotify).toHaveBeenCalledWith('something failed', 'error', 5000)
  })

  it('calls notify with err.message for plain object with message field', () => {
    handleApiError({ message: 'object message' })
    expect(mockNotify).toHaveBeenCalledWith('object message', 'error', 5000)
  })

  it('calls notify with err.error for plain object with error field (no message)', () => {
    handleApiError({ error: 'server error' })
    expect(mockNotify).toHaveBeenCalledWith('server error', 'error', 5000)
  })

  it('prefers message over error field when both present (message branch checked first)', () => {
    // The implementation checks `message` before `error`
    handleApiError({ message: 'msg field', error: 'err field' })
    expect(mockNotify).toHaveBeenCalledWith('msg field', 'error', 5000)
  })

  it('calls notify with fallback for null', () => {
    handleApiError(null, { fallback: 'fallback message' })
    expect(mockNotify).toHaveBeenCalledWith('fallback message', 'error', 5000)
  })

  it('calls notify with fallback for undefined', () => {
    handleApiError(undefined, { fallback: 'oops' })
    expect(mockNotify).toHaveBeenCalledWith('oops', 'error', 5000)
  })

  it('uses default fallback "Something went wrong." when no fallback provided and err is unrecognized', () => {
    handleApiError(42)
    expect(mockNotify).toHaveBeenCalledWith('Something went wrong.', 'error', 5000)
  })

  it('does NOT call notify when ctx.silent=true', () => {
    handleApiError(new Error('silent error'), { silent: true })
    expect(mockNotify).not.toHaveBeenCalled()
  })

  it('uses ctx.fallback when Error has no message', () => {
    const e = new Error('')
    handleApiError(e, { fallback: 'custom fallback' })
    expect(mockNotify).toHaveBeenCalledWith('custom fallback', 'error', 5000)
  })

  it('passes ctx.toastDuration to notify', () => {
    handleApiError(new Error('timed'), { toastDuration: 8000 })
    expect(mockNotify).toHaveBeenCalledWith('timed', 'error', 8000)
  })

  it('returns the normalized message string', () => {
    const result = handleApiError(new Error('returned'))
    expect(result).toBe('returned')
  })

  it('returns fallback string when err is unrecognized', () => {
    const result = handleApiError(null, { fallback: 'the fallback', silent: true })
    expect(result).toBe('the fallback')
  })
})

describe('toApiError', () => {
  it('returns Error with body.error when JSON body has error field', async () => {
    const res = new Response(JSON.stringify({ error: 'not found' }), { status: 404 })
    const err = await toApiError(res, 'fallback')
    expect(err).toBeInstanceOf(Error)
    expect(err.message).toBe('not found')
  })

  it('returns Error with body.message when JSON body has message field', async () => {
    const res = new Response(JSON.stringify({ message: 'bad request' }), { status: 400 })
    const err = await toApiError(res, 'fallback')
    expect(err.message).toBe('bad request')
  })

  it('prefers body.error over body.message when both present', async () => {
    // Implementation checks error first
    const res = new Response(JSON.stringify({ error: 'err val', message: 'msg val' }), { status: 400 })
    const err = await toApiError(res, 'fallback')
    expect(err.message).toBe('err val')
  })

  it('uses "fallback (HTTP N)" when body is not JSON', async () => {
    const res = new Response('not-json-content', { status: 500 })
    const err = await toApiError(res, 'Server error')
    expect(err.message).toBe('Server error (HTTP 500)')
  })

  it('uses "fallback (HTTP N)" when JSON body has neither error nor message', async () => {
    const res = new Response(JSON.stringify({ code: 42 }), { status: 422 })
    const err = await toApiError(res, 'Unprocessable')
    expect(err.message).toBe('Unprocessable (HTTP 422)')
  })

  it('returns an Error instance in all cases', async () => {
    const res = new Response('x', { status: 503 })
    const err = await toApiError(res, 'f')
    expect(err).toBeInstanceOf(Error)
  })
})
