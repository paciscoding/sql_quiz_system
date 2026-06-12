export type WindowMode = 'normal' | 'collapsed' | 'minimized'

export interface WindowState {
  mode: WindowMode
  pos: { x: number; y: number }
  size: { w: number; h: number }
  currentIndex: number
}

function storageKey(scope: 'category' | 'exam', id: number): string {
  return `schema-window:${scope}:${id}`
}

export function loadState(scope: 'category' | 'exam', id: number): WindowState | null {
  try {
    const raw = localStorage.getItem(storageKey(scope, id))
    if (!raw) return null
    return JSON.parse(raw) as WindowState
  } catch {
    // Corrupt or un-parseable — treat as no saved state
    return null
  }
}

// Per-key debounce timers so back-to-back writes for the SAME key coalesce,
// but writes for distinct keys (e.g. two windows mounted at once) never drop each other.
const _saveTimers = new Map<string, ReturnType<typeof setTimeout>>()

export function saveState(scope: 'category' | 'exam', id: number, state: WindowState): void {
  const key = storageKey(scope, id)
  const existing = _saveTimers.get(key)
  if (existing !== undefined) clearTimeout(existing)
  const t = setTimeout(() => {
    try {
      localStorage.setItem(key, JSON.stringify(state))
    } catch {
      // Storage quota or private-browsing — silently ignore
    }
    _saveTimers.delete(key)
  }, 200)
  _saveTimers.set(key, t)
}

export function clearAllSchemaWindowState(): void {
  const keysToRemove: string[] = []
  for (let i = 0; i < localStorage.length; i++) {
    const k = localStorage.key(i)
    if (k && k.startsWith('schema-window:')) {
      keysToRemove.push(k)
    }
  }
  keysToRemove.forEach((k) => localStorage.removeItem(k))
}
