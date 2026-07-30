import { ref, type Ref } from 'vue'

/**
 * Dock orientation for the lecturer authoring pages' Categories Management
 * panel, which has to stay visible at any scroll position and any viewport
 * width.
 *
 * BOTH orientations are `position: fixed` overlays — this is an orientation
 * toggle, not a float-vs-in-flow toggle:
 *
 *  - 'side'   docked to the right edge, full viewport height, with the
 *             orientation switch as a vertical strip on the panel's inner edge.
 *  - 'bottom' docked to the bottom edge, full viewport width, with the
 *             orientation switch as a horizontal strip along the panel's top.
 *
 * Design: docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md
 * (superseded twice — first the 1280px-gated `position: sticky` rail, then a
 * 'floating'/'stacked' pair where only one of the two actually floated. Both
 * are gone: a gate that silently degrades and a mode that silently stops
 * floating read identically to the user as "the panel doesn't work".)
 */
export type AuthoringLayoutMode = 'side' | 'bottom'

export const AUTHORING_LAYOUT_STORAGE_PREFIX = 'authoring-layout:'
export const AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX = 'authoring-layout-collapsed:'

const DEFAULT_MODE: AuthoringLayoutMode = 'side'

/**
 * Carries prior preferences forward instead of silently resetting them:
 * 'rail'/'floating' were the right-hand-column modes this 'side' dock
 * replaced, 'stacked' was the below-the-form mode 'bottom' replaced.
 */
function normalizeStoredMode(v: unknown): AuthoringLayoutMode | null {
  if (v === 'side' || v === 'bottom') return v
  if (v === 'rail' || v === 'floating') return 'side'
  if (v === 'stacked') return 'bottom'
  return null
}

export interface UseAuthoringLayoutReturn {
  /** Which edge the panel is docked to. Persisted. */
  mode: Ref<AuthoringLayoutMode>
  /** Whether the panel is minimized to its reopen tab. Persisted. */
  collapsed: Ref<boolean>
  setMode: (m: AuthoringLayoutMode) => void
  toggleMode: () => void
  setCollapsed: (v: boolean) => void
  toggleCollapsed: () => void
}

/**
 * @param scope short key segment identifying the page ('practice', 'exam', ...)
 *              so two authoring pages keep independent preferences.
 */
export function useAuthoringLayout(scope: string): UseAuthoringLayoutReturn {
  const storageKey = `${AUTHORING_LAYOUT_STORAGE_PREFIX}${scope}`
  const collapsedKey = `${AUTHORING_LAYOUT_COLLAPSED_STORAGE_PREFIX}${scope}`

  // Storage may be unavailable (private browsing, disabled cookies) or throw on
  // read as well as write — same posture as utils/schemaWindowState.ts. Failing
  // to persist a layout preference must never break the page.
  function readStorage(key: string): string | null {
    try {
      return typeof window !== 'undefined' ? window.localStorage.getItem(key) : null
    } catch {
      return null
    }
  }
  function writeStorage(key: string, value: string) {
    try {
      window.localStorage.setItem(key, value)
    } catch {
      // Quota or disabled storage — the in-memory preference still applies for
      // this session.
    }
  }

  const mode = ref<AuthoringLayoutMode>(normalizeStoredMode(readStorage(storageKey)) ?? DEFAULT_MODE)
  const collapsed = ref<boolean>(readStorage(collapsedKey) === '1')

  function setMode(m: AuthoringLayoutMode) {
    mode.value = m
    writeStorage(storageKey, m)
  }

  function toggleMode() {
    setMode(mode.value === 'side' ? 'bottom' : 'side')
  }

  function setCollapsed(v: boolean) {
    collapsed.value = v
    writeStorage(collapsedKey, v ? '1' : '0')
  }

  function toggleCollapsed() {
    setCollapsed(!collapsed.value)
  }

  return { mode, collapsed, setMode, toggleMode, setCollapsed, toggleCollapsed }
}
