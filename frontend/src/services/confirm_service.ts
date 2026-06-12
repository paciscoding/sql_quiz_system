import { reactive } from 'vue'

export interface ConfirmOptions {
  title: string
  body?: string
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
}

interface PendingConfirm extends ConfirmOptions {
  id: number
  resolve: (value: boolean) => void
}

const state = reactive<{ current: PendingConfirm | null }>({ current: null })
let nextId = 1

export function useConfirm() {
  function confirm(opts: ConfirmOptions): Promise<boolean> {
    return new Promise<boolean>((resolve) => {
      // If a confirm is already open, reject the previous one (resolve false) to avoid stacking.
      if (state.current) state.current.resolve(false)
      state.current = { ...opts, id: nextId++, resolve }
    })
  }

  function _resolveCurrent(value: boolean) {
    if (!state.current) return
    const c = state.current
    state.current = null
    c.resolve(value)
  }

  return { state, confirm, _resolveCurrent }
}
