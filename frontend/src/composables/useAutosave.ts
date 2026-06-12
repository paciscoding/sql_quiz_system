import { onBeforeUnmount, watch, type Ref } from 'vue'

export interface UseAutosaveOptions {
  debounceMs?: number
  minLength?: number
  storage?: Storage
}

export interface UseAutosaveReturn {
  hasDraft: () => boolean
  loadDraft: () => string | null
  restoreDraft: () => boolean
  clearDraft: () => void
  flush: () => void
}

export function useAutosave(
  key: string,
  valueRef: Ref<string>,
  options: UseAutosaveOptions = {}
): UseAutosaveReturn {
  const storage = options.storage ?? (typeof window !== 'undefined' ? window.localStorage : null)
  const debounceMs = options.debounceMs ?? 600
  const minLength = options.minLength ?? 10

  let timer: number | null = null

  function write() {
    if (!storage) return
    const v = valueRef.value ?? ''
    if (v.length < minLength) {
      storage.removeItem(key)
    } else {
      try {
        storage.setItem(key, v)
      } catch {
        // quota — silently ignore
      }
    }
  }

  function schedule() {
    if (timer != null) window.clearTimeout(timer)
    timer = window.setTimeout(() => {
      write()
      timer = null
    }, debounceMs)
  }

  watch(valueRef, schedule)

  onBeforeUnmount(() => {
    if (timer != null) {
      window.clearTimeout(timer)
      write()
    }
  })

  return {
    hasDraft: () => !!(storage && storage.getItem(key)),
    loadDraft: () => (storage ? storage.getItem(key) : null),
    restoreDraft: () => {
      if (!storage) return false
      const d = storage.getItem(key)
      if (d == null) return false
      valueRef.value = d
      return true
    },
    clearDraft: () => {
      if (storage) storage.removeItem(key)
    },
    flush: () => {
      if (timer != null) {
        window.clearTimeout(timer)
        timer = null
      }
      write()
    },
  }
}
