import { describe, it, expect, beforeEach } from 'vitest'
import { useConfirm } from '../services/confirm_service'

// Reset the module-level singleton state between tests by resolving/clearing any open confirm
function resetConfirmState() {
  const { state, _resolveCurrent } = useConfirm()
  if (state.current) {
    _resolveCurrent(false)
  }
}

describe('useConfirm', () => {
  beforeEach(() => {
    resetConfirmState()
  })

  it('confirm(opts) returns a Promise', () => {
    const { confirm, _resolveCurrent } = useConfirm()
    const p = confirm({ title: 'Are you sure?' })
    expect(p).toBeInstanceOf(Promise)
    // Clean up
    _resolveCurrent(false)
  })

  it('resolving the dialog with true resolves the Promise with true', async () => {
    const { confirm, _resolveCurrent } = useConfirm()
    const p = confirm({ title: 'Test' })
    _resolveCurrent(true)
    await expect(p).resolves.toBe(true)
  })

  it('resolving the dialog with false resolves the Promise with false', async () => {
    const { confirm, _resolveCurrent } = useConfirm()
    const p = confirm({ title: 'Test' })
    _resolveCurrent(false)
    await expect(p).resolves.toBe(false)
  })

  it('stacking: opening a second confirm resolves the first with false', async () => {
    const { confirm, _resolveCurrent } = useConfirm()

    const first = confirm({ title: 'First dialog' })
    // Open second — should auto-resolve first with false
    const second = confirm({ title: 'Second dialog' })

    // First is already resolved
    await expect(first).resolves.toBe(false)

    // Second is still pending; resolve it
    _resolveCurrent(true)
    await expect(second).resolves.toBe(true)
  })

  it('module-level singleton: two useConfirm() calls share state', async () => {
    const c1 = useConfirm()
    const c2 = useConfirm()

    // c1 opens a dialog
    const p = c1.confirm({ title: 'Singleton test' })
    // c2 should see the same state.current
    expect(c2.state.current).not.toBeNull()
    expect(c2.state.current?.title).toBe('Singleton test')

    // c2 resolves it
    c2._resolveCurrent(true)
    await expect(p).resolves.toBe(true)
    expect(c1.state.current).toBeNull()
  })

  it('state.current is null before any confirm is opened', () => {
    const { state } = useConfirm()
    expect(state.current).toBeNull()
  })

  it('state.current is null after resolving', async () => {
    const { confirm, _resolveCurrent, state } = useConfirm()
    const p = confirm({ title: 'x' })
    _resolveCurrent(true)
    await p
    expect(state.current).toBeNull()
  })

  it('state.current carries the options and an incrementing id', async () => {
    const { confirm, _resolveCurrent, state } = useConfirm()

    const p1 = confirm({ title: 'First', body: 'body1', danger: true })
    const id1 = state.current?.id
    expect(state.current?.title).toBe('First')
    expect(state.current?.body).toBe('body1')
    expect(state.current?.danger).toBe(true)
    _resolveCurrent(false)
    await p1

    const p2 = confirm({ title: 'Second' })
    const id2 = state.current?.id
    expect(typeof id2).toBe('number')
    if (id1 !== undefined && id2 !== undefined) {
      expect(id2).toBeGreaterThan(id1)
    }
    _resolveCurrent(false)
    await p2
  })
})
