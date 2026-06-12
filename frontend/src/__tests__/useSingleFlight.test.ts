import { describe, it, expect, vi } from 'vitest'
import { useSingleFlight } from '../composables/useSingleFlight'

describe('useSingleFlight', () => {
  it('first call invokes the inner fn and returns its result; inFlight flips true then false', async () => {
    const inner = vi.fn(async () => {
      return 42
    })

    const { inFlight, run } = useSingleFlight(inner)

    expect(inFlight.value).toBe(false)

    const resultPromise = run()

    // inFlight should be true synchronously before the await resolves
    expect(inFlight.value).toBe(true)

    const result = await resultPromise

    expect(result).toBe(42)
    expect(inFlight.value).toBe(false)
    expect(inner).toHaveBeenCalledTimes(1)
  })

  it('re-entrant call while inFlight=true returns undefined immediately without invoking inner fn', async () => {
    let resolveInner!: (v: number) => void
    const inner = vi.fn(
      () =>
        new Promise<number>((resolve) => {
          resolveInner = resolve
        })
    )

    const { inFlight, run } = useSingleFlight(inner)

    const first = run()
    expect(inFlight.value).toBe(true)

    // Second call while first is still pending
    const second = await run()
    expect(second).toBeUndefined()
    expect(inner).toHaveBeenCalledTimes(1)

    // Settle the first
    resolveInner(99)
    const firstResult = await first
    expect(firstResult).toBe(99)
    expect(inFlight.value).toBe(false)
  })

  it('after completion, the wrapper accepts a new call normally', async () => {
    const inner = vi.fn(async (x: number) => x * 2)
    const { inFlight, run } = useSingleFlight(inner)

    const r1 = await run(3)
    expect(r1).toBe(6)
    expect(inFlight.value).toBe(false)

    // Second independent call is accepted
    const r2 = await run(5)
    expect(r2).toBe(10)
    expect(inner).toHaveBeenCalledTimes(2)
  })

  it('inner fn throwing — wrapper re-throws — inFlight returns to false', async () => {
    const err = new Error('boom')
    const inner = vi.fn(async () => {
      throw err
    })

    const { inFlight, run } = useSingleFlight(inner)

    await expect(run()).rejects.toThrow('boom')
    expect(inFlight.value).toBe(false)
  })

  it('inner fn rejecting (async) — wrapper rejects — inFlight returns to false', async () => {
    const inner = vi.fn(() => Promise.reject(new Error('async-reject')))
    const { inFlight, run } = useSingleFlight(inner)

    const promise = run()
    expect(inFlight.value).toBe(true)
    await expect(promise).rejects.toThrow('async-reject')
    expect(inFlight.value).toBe(false)
  })

  it('inFlight flips synchronously before the wrapped fn awaits anything', async () => {
    const snapshots: boolean[] = []
    const inner = vi.fn(async () => {
      // By the time this runs inFlight should already be true
      snapshots.push(true) // placeholder — we test via the outer check
      return 1
    })

    const { inFlight, run } = useSingleFlight(inner)
    const p = run()
    // Synchronously after the call, before any microtask:
    expect(inFlight.value).toBe(true)
    await p
    expect(inFlight.value).toBe(false)
  })
})
