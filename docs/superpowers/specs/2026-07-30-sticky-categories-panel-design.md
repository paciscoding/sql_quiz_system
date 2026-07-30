# Sticky Categories Management panel — Practice Authoring

Date: 2026-07-30
Scope: `frontend/src/components/PracticeAuthoring.vue` (+ one new composable, global CSS)
Status: approved by proxy — the user delegated every open design choice to the implementer and asked for the decisions to be reported after the fact.

## 1. Problem

`PracticeAuthoring.vue` currently renders two stacked blocks:

- `section.authoring-section--upper` — the Practice Questions table and the Create/Edit form
- `section.authoring-section--lower` — Categories Management (always rendered, one `CollapsibleCard` per category)

The upper block is tall: an 8-column table plus a two-column form plus a 200px `SqlEditor`. By the time the lecturer is editing SQL, Categories Management has scrolled entirely out of the viewport. The lecturer cannot see which categories exist, cannot check whether the selected category has a decoy schema, and cannot start an import without scrolling away from the form and losing their place.

Requirement: **Categories Management must stay visible at any scroll position.**

Secondary question raised by the user: should the split be horizontal (stacked, panel docked) or vertical (side-by-side rail), or both behind a toggle?

## 2. Answer to the horizontal-vs-vertical question

**Vertical (a sticky side rail) is the correct default. Horizontal stays available as a second mode behind a toggle.**

Three reasons, in order of weight:

**(a) `position: sticky` only works for a side rail in this shell.** The app scrolls the document — `.cms-shell` is `min-height: 100dvh` with `grid-template-rows: var(--layout-header-h) 1fr`, and nothing sets an inner `overflow`. There is no scroll container to dock a bar against. A `position: sticky; bottom: 0` element only sticks inside its own containing block, and the categories section is the last child of the page, so its containing block ends exactly where it does — sticky-bottom would be a no-op. Making the horizontal panel follow scroll would require `position: fixed`, plus `padding-bottom` on the page to stop the dock covering the Save button, plus a `z-index` negotiation with `.cms-header` (z-index 50) and with `ConfirmDialogHost`/`NotificationToast`. A sticky rail needs none of that, and `.cms-sidebar` in `style.css:603` already proves the pattern in this codebase (`align-self: start; position: sticky; top: calc(var(--layout-header-h) + var(--space-5))`).

**(b) A bottom dock has to pay viewport rent forever.** Category cards contain two `SqlEditor` instances (200px each), a 3-row textarea, and `IntroImagesEditor`. A dock that shows anything useful needs ~40% of the viewport height, permanently, on a page whose main job is a tall form plus a SQL editor. A rail spends horizontal space instead, which this page has: `.cms-body` caps at `--layout-max-w` and the form is already a two-column grid, so the wide desktop layout has slack to give.

**(c) A stacked mode has to exist anyway.** On narrower viewports a rail squeezes both the form and the categories to unusable widths (§3.3 does the arithmetic), so the stacked layout is required as the fallback. Given it must exist, exposing it as a user-selectable mode on wide screens costs one button.

## 3. Design

### 3.1 Two layout modes

| Mode | Value | Behaviour |
|---|---|---|
| Side rail | `rail` | Two-column CSS grid. Left column: questions table + create/edit form. Right column: Categories Management, `position: sticky`, its own vertical scrollbar. |
| Stacked | `stacked` | Today's behaviour, unchanged: questions block above, Categories Management below, normal document flow, no sticky. |

`rail` is the default for a lecturer who has never chosen. Categories go on the **right** — the user's original brief put the assignments on the left, and `.cms-sidebar` already occupies the page's left edge, so a second left rail would produce two adjacent rails.

Column ratio is **60/40, not 50/50**: `grid-template-columns: minmax(0, 1.5fr) minmax(320px, 1fr)`. A literal half would leave each cell of the form's inner two-column grid around 200px wide, which breaks the Requirement textarea and the Correct SQL editor. `minmax(0, …)` on the left column is required, not decorative: without it the `data-table` and `SqlEditor` force the grid track wider than the container and the page scrolls sideways.

For the same reason the questions table gets wrapped in the existing global `.responsive-table-wrap` utility. It is an 8-column table whose min-content width exceeds the narrowed left column; unwrapped, it overflows its card and the page scrolls sideways in rail mode. This is the one content change the layout forces.

### 3.2 The rail must be its own scroll container

```
position: sticky;
top: calc(var(--layout-header-h) + var(--space-5));
align-self: start;
max-height: calc(100dvh - var(--layout-header-h) - var(--space-5) * 2);
overflow-y: auto;
```

`max-height` + `overflow-y` are load-bearing. A sticky element taller than the viewport scrolls with the page until its bottom edge arrives — with eight categories expanded, the rail would be several thousand pixels tall and "sticky" would be indistinguishable from static. Capping it to the viewport and giving it its own scrollbar is what makes "follows any scroll position" literally true.

`align-self: start` is required for the same reason `.cms-sidebar` has it: a stretched grid item fills the row and has no slack to stick within.

Accepted cost: `overflow-y: auto` clips anything that would visually escape the rail. `CollapsibleCard`, `SqlEditor` and `IntroImagesEditor` all render in-flow, so nothing breaks today. `SchemaIntroWindow`/`SchemaImageWindow` are separate floating windows not rendered inside these cards, so they are unaffected.

### 3.3 Mode selection is driven from JS, not from a CSS media query

A CSS-only breakpoint override would leave the toggle claiming "Side rail" while the page renders stacked. Instead one composable owns the truth:

`frontend/src/composables/useAuthoringLayout.ts`

```ts
export type AuthoringLayoutMode = 'rail' | 'stacked'

export function useAuthoringLayout(scope: string): {
  mode: Ref<AuthoringLayoutMode>          // what the user picked (persisted)
  canUseRail: Ref<boolean>                // viewport is wide enough
  effectiveMode: ComputedRef<AuthoringLayoutMode>  // what actually renders
  setMode: (m: AuthoringLayoutMode) => void
  toggleMode: () => void
}
```

- `mode` is read once on setup from `localStorage['authoring-layout:' + scope]`, defaulting to `'rail'`. Any value other than the two literals is treated as absent.
- `canUseRail` tracks `window.matchMedia('(min-width: 1280px)')`, seeded on setup and updated via a `change` listener that is removed on `onScopeDispose`.

  The threshold is derived, not picked. At viewport width `W` the authoring content receives `W - 48` (`.cms-body` padding) `- 260` (`--layout-sidebar-w`) `- 24` (shell grid gap) `- 64` (`.cms-main` padding) `= W - 396`. Take off the rail's 24px gap and its 320px floor and the question column is left with `W - 740`. At 1024px that is ~284px — the rail would win the split and leave the two-column form unusable, so the obvious "match the `.cms-sidebar` breakpoint" answer is wrong. 1280px leaves the form ~540px, which is where both halves work. One column of chrome fits long before two do.
- `effectiveMode` is `mode === 'rail' && canUseRail ? 'rail' : 'stacked'`. The persisted preference is never overwritten by a narrow viewport — resizing back up restores the rail.
- Every `localStorage` access is wrapped in `try/catch` and the composable degrades to in-memory state, matching `utils/schemaWindowState.ts`. `window`/`matchMedia` absence is handled so SSR and bare jsdom cannot throw; when `matchMedia` is missing, `canUseRail` is `true` (the rail is the design intent; a test environment should exercise it).

`scope` is a plain string (`'practice'` here) so the same composable can back exam authoring later without a second key scheme.

### 3.4 Template shape

```
.practice-authoring
  .page-header
  BackLink
  .authoring-layout-bar            ← toggle, only when canUseRail
    button[aria-pressed]  Side rail
    button[aria-pressed]  Stacked
  .authoring-layout.authoring-layout--rail|--stacked
    section.authoring-section--upper     (unchanged content)
    section.authoring-section--lower     (unchanged content)
```

The two `section`s keep their existing class names and their existing DOM order — `--upper` before `--lower` in both modes. Only the wrapper class changes; the grid does the visual placement. This means:

- no content is duplicated, no `v-if` on either section, so no component inside them is ever unmounted and remounted by a mode switch (an expanded card stays expanded, a half-typed import SQL survives);
- `exam_categories_panel.test.ts` (which asserts `--upper` precedes `--lower` in the HTML for the exam panel) documents a contract this change keeps rather than breaks.

In `--stacked` the wrapper is `display: block` and `--lower` keeps its existing top border and spacing, i.e. byte-for-byte the current look.

### 3.5 Where the CSS lives

The grid, rail and stacked rules go into `frontend/src/style.css` as global `.authoring-layout*` classes, next to the existing shell rules. `PracticeAuthoring.vue`'s `<style scoped>` cannot express them anyway once exam authoring wants the same treatment, and the existing `.authoring-section` rules are already duplicated verbatim between `PracticeAuthoring.vue` and `ExamQuestionsPanel.vue`. Putting the new rules in one global place stops a third copy from being born.

`ExamQuestionsPanel.vue` was initially out of scope — the user named Practice Authoring — and was then adopted in a follow-up on the user's go-ahead, under the `'exam'` scope key so the two pages keep independent preferences. It needed one extra fix the practice page did not: its lower section carried an **inline** `border-top`/`padding-top` divider on top of the scoped one. Inline styles beat every stylesheet, so left in place it would have drawn a horizontal rule down the side of the rail. Removing it also fixes the double divider that inline rule had been rendering in the stacked layout all along.

## 4. Testing

New: `frontend/src/__tests__/composables/useAuthoringLayout.test.ts`

- defaults to `rail` with empty storage
- restores a persisted `stacked`
- ignores a garbage persisted value (falls back to `rail`)
- `setMode`/`toggleMode` write through to storage
- `effectiveMode` is `stacked` when `matchMedia` reports a narrow viewport even though `mode` stays `rail`
- a narrow→wide `matchMedia` `change` event flips `effectiveMode` back to `rail`
- a throwing `localStorage` does not propagate

New: `frontend/src/__tests__/components/PracticeAuthoring.layout.test.ts`

- mounts with default state → wrapper carries `authoring-layout--rail`, and the categories `section` is inside the wrapper
- both `--upper` and `--lower` are present and in that order in **both** modes
- clicking the Stacked toggle switches the wrapper class and persists `stacked`
- narrow viewport → wrapper is `--stacked` and the toggle bar is absent
- the Categories heading and the "No categories yet" empty state still render in rail mode (i.e. the panel is genuinely always rendered, not `v-if`-ed)

New: `frontend/src/__tests__/components/ExamQuestionsPanel.layout.test.ts` — the same contract for exam authoring, plus one assertion specific to it: the lower section's first child must carry no inline `border-top`/`padding-top`.

Mocking: the component's `services/practice` module is mocked the way `exam_categories_panel.test.ts` mocks `services/exam`; `matchMedia` is installed on `window` per-test with a drivable fake, so a narrow→wide transition can be exercised rather than just one sampled width.

CSS geometry (sticky offsets, max-height) is not unit-testable in jsdom and is not asserted — the tests cover the class contract and the composable's state machine, and the geometry is reviewed visually.

## 5. Non-goals

- No numbering, pagination, jump-to-N, reordering or `sort_order` — explicitly withdrawn by the user earlier.
- No drag-to-resize splitter. Two fixed ratios only; a resizer adds a persisted pixel value, pointer capture and a keyboard-accessible fallback for a problem the toggle already solves.
- No change to any category or question API call, payload, or backend behaviour. This is layout only.
- No `position: fixed` bottom dock (see §2a).
