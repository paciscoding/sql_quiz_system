import { computed, onScopeDispose, ref, type ComputedRef, type Ref } from 'vue'

/**
 * Layout preference for the lecturer authoring pages, where the Categories
 * Management panel has to stay visible at any scroll position.
 *
 *  - 'rail'    two columns; the categories panel is a `position: sticky` right
 *              rail with its own scrollbar, so it follows the page scroll.
 *  - 'stacked' the original single-column layout, questions above categories.
 *
 * Design: docs/superpowers/specs/2026-07-30-sticky-categories-panel-design.md
 */
export type AuthoringLayoutMode = 'rail' | 'stacked'

export const AUTHORING_LAYOUT_STORAGE_PREFIX = 'authoring-layout:'

/**
 * The rail is only offered at or above this width, which is derived from the
 * shell's own chrome rather than picked as a round number. At viewport width W
 * the authoring content gets:
 *
 *   W - 48 (.cms-body padding) - 260 (--layout-sidebar-w) - 24 (grid gap)
 *     - 64 (.cms-main padding) = W - 396
 *
 * Subtract the rail's 24px gap and its 320px minimum and the question column is
 * left with W - 740. At 1024px that is ~284px — narrower than the two-column
 * question form can use, so the rail would win the split and make the form
 * unusable. 1280px leaves the form ~540px, which is the point where both halves
 * are workable. (Note this is deliberately *not* the 1023.98px breakpoint that
 * collapses .cms-sidebar: one column of chrome fits long before two do.)
 */
export const AUTHORING_RAIL_MEDIA_QUERY = '(min-width: 1280px)'

const DEFAULT_MODE: AuthoringLayoutMode = 'rail'

function isMode(v: unknown): v is AuthoringLayoutMode {
  return v === 'rail' || v === 'stacked'
}

export interface UseAuthoringLayoutReturn {
  /** What the user picked. Persisted; never overwritten by a narrow viewport. */
  mode: Ref<AuthoringLayoutMode>
  /** Whether the viewport is wide enough for the rail to make sense. */
  canUseRail: Ref<boolean>
  /** What actually renders — 'stacked' whenever the rail is unavailable. */
  effectiveMode: ComputedRef<AuthoringLayoutMode>
  setMode: (m: AuthoringLayoutMode) => void
  toggleMode: () => void
}

/**
 * @param scope short key segment identifying the page ('practice', 'exam', ...)
 *              so two authoring pages keep independent preferences.
 */
export function useAuthoringLayout(scope: string): UseAuthoringLayoutReturn {
  const storageKey = `${AUTHORING_LAYOUT_STORAGE_PREFIX}${scope}`

  // Storage may be unavailable (private browsing, disabled cookies) or throw on
  // read as well as write — same posture as utils/schemaWindowState.ts. Failing
  // to persist a layout preference must never break the page.
  let stored: string | null = null
  try {
    stored = typeof window !== 'undefined' ? window.localStorage.getItem(storageKey) : null
  } catch {
    stored = null
  }

  const mode = ref<AuthoringLayoutMode>(isMode(stored) ? stored : DEFAULT_MODE)

  // Absent matchMedia (non-browser or very old environment) is treated as
  // "wide enough": the rail is the design intent, and silently downgrading
  // everyone to stacked would hide the feature rather than degrade it.
  const mql: MediaQueryList | null =
    typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      ? window.matchMedia(AUTHORING_RAIL_MEDIA_QUERY)
      : null

  const canUseRail = ref(mql ? mql.matches : true)

  if (mql) {
    const onChange = () => { canUseRail.value = mql.matches }
    mql.addEventListener('change', onChange)
    onScopeDispose(() => mql.removeEventListener('change', onChange))
  }

  const effectiveMode = computed<AuthoringLayoutMode>(() =>
    mode.value === 'rail' && canUseRail.value ? 'rail' : 'stacked'
  )

  function setMode(m: AuthoringLayoutMode) {
    mode.value = m
    try {
      window.localStorage.setItem(storageKey, m)
    } catch {
      // Quota or disabled storage — the in-memory preference still applies for
      // this session.
    }
  }

  function toggleMode() {
    setMode(mode.value === 'rail' ? 'stacked' : 'rail')
  }

  return { mode, canUseRail, effectiveMode, setMode, toggleMode }
}
