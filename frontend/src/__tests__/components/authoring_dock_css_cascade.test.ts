/**
 * Cascade regression guard for the docked Categories Management panel.
 *
 * The bug this exists to prevent, in full: both authoring components carried a
 * Vue `<style scoped>` rule
 *
 *     .authoring-section { display: block; }
 *
 * which compiles to `.authoring-section[data-v-xxxxxxx]` — specificity (0,2,0),
 * the SAME as the global `.authoring-section--lower.is-docked { display: flex }`
 * and `.authoring-section--lower.is-collapsed { display: none }` in style.css.
 * Scoped component styles are injected after the imported global stylesheet, so
 * the tie broke in the scoped rule's favour and it silently won. Two
 * user-visible failures followed, neither of which any class-contract test
 * could see (the classes were all applied correctly — only the computed
 * `display` was wrong):
 *
 *   1. `display: block` instead of `flex` meant .authoring-panel-shell was
 *      never a flex item, so its `flex: 1` did nothing, the content never got a
 *      bounded height, and `overflow-y: auto` had nothing to scroll within —
 *      the panel could not be scrolled at all.
 *   2. `display: block` beat `display: none`, so Minimize toggled the state and
 *      the class but never actually hid the panel.
 *
 * happy-dom does not apply real stylesheet cascade, so this is asserted at the
 * source level instead: no scoped block in either component may style the
 * selectors the global dock rules own, and those global rules must stay
 * ancestor-qualified so an equivalent one-class scoped rule cannot tie them
 * again.
 */

/// <reference types="node" />
// The SFCs come in through Vite's `?raw`, but style.css cannot: vitest stubs
// CSS modules to an empty string by default, and that stub also swallows
// `?raw`, `?inline` and import.meta.glob — every one of them resolves to ''.
// So the stylesheet is read from disk. tsconfig.app deliberately limits
// `types` to vite/client (app code stays DOM-only); the triple-slash reference
// above re-admits the node types for this file alone rather than loosening
// that setting for the whole project.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import practiceSfc from '../../components/PracticeAuthoring.vue?raw'
import examSfc from '../../components/lecturer/ExamQuestionsPanel.vue?raw'
import { describe, expect, it } from 'vitest'

// Resolved from the Vite root (this package's directory) rather than
// import.meta.url — under the happy-dom environment import.meta.url is not a
// file: URL, so fileURLToPath on it throws.
const globalCss = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')

/** The `<style scoped>` block of a SFC, or '' when it has none. */
function scopedStyle(sfc: string): string {
  const m = sfc.match(/<style scoped>([\s\S]*?)<\/style>/)
  return m ? m[1] : ''
}

/** Strip comments so a selector named only in prose doesn't trip the check. */
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

const COMPONENTS: Array<[string, string]> = [
  ['PracticeAuthoring.vue', practiceSfc],
  ['ExamQuestionsPanel.vue', examSfc],
]

// Selectors whose geometry the global .authoring-* rules own outright. A scoped
// rule for any of these ties or beats the global one on injection order.
const GLOBAL_OWNED = [
  '.authoring-section',
  '.authoring-layout',
  '.authoring-panel-shell',
  '.authoring-panel-content',
  '.authoring-layout-switch',
  '.authoring-floating-tab',
]

describe('docked authoring panel — CSS cascade guard', () => {
  describe.each(COMPONENTS)('%s', (name, sfc) => {
    const css = stripCssComments(scopedStyle(sfc))

    it.each(GLOBAL_OWNED)('has no scoped rule for %s', (selector) => {
      // Match the selector only where it heads a declaration block, so a
      // longer unrelated class (e.g. .authoring-section-note) does not false-positive.
      const pattern = new RegExp(`\\${selector}(?![\\w-])[^{}]*\\{`)
      expect(
        pattern.test(css),
        `${name} must not style ${selector} in <style scoped> — scoped rules ` +
          `compile to [data-v] selectors that outrank the global dock rules in style.css`
      ).toBe(false)
    })
  })

  describe('style.css keeps the dock rules ancestor-qualified', () => {
    const css = globalCss

    it.each([
      ['.is-docked', 'display: flex on the docked box'],
      ['.is-collapsed', 'display: none that Minimize depends on'],
      ['.dock-side', 'the right-edge full-height dock'],
      ['.dock-bottom', 'the bottom-edge full-width dock'],
    ])('qualifies %s with the .authoring-layout ancestor', (cls, why) => {
      const qualified = new RegExp(
        `\\.authoring-layout\\s*>\\s*\\.authoring-section--lower\\${cls}\\s*\\{`
      )
      expect(
        qualified.test(css),
        `${cls} (${why}) must be written as ` +
          `\`.authoring-layout > .authoring-section--lower${cls}\` so a one-class ` +
          `scoped rule cannot tie it and win on injection order`
      ).toBe(true)
    })

    it('qualifies the scrolling content element too', () => {
      expect(/\.authoring-layout\s+\.authoring-panel-content\s*\{/.test(css)).toBe(true)
    })

    it('gives the content element a bounded, scrollable box', () => {
      // min-height:0 is what lets a flex item shrink below its content size;
      // without it overflow-y:auto never engages inside a flex column.
      const block = css.match(/\.authoring-layout\s+\.authoring-panel-content\s*\{([^}]*)\}/)
      expect(block, 'expected a .authoring-layout .authoring-panel-content rule').toBeTruthy()
      expect(block![1]).toContain('overflow-y: auto')
      expect(block![1]).toContain('min-height: 0')
    })

    it('pins the side dock top AND bottom so its height is the full viewport', () => {
      // Pinning only `bottom` plus a max-height leaves the used height as
      // shrink-to-content, which collapses the switch strip's full-height stretch.
      const block = css.match(
        /\.authoring-layout\s*>\s*\.authoring-section--lower\.dock-side\s*\{([^}]*)\}/
      )
      expect(block, 'expected a .dock-side rule').toBeTruthy()
      expect(block![1]).toMatch(/(^|\s)top:\s*0/)
      expect(block![1]).toMatch(/(^|\s)bottom:\s*0/)
    })
  })
})
