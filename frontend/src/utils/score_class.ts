/**
 * Shared heatmap classifier for gradebook score cells.
 *
 * Lives in its own module so the practice matrix and the exam matrix can both
 * use it without either importing the other. `practice_matrix.ts` re-exports it
 * for the call sites (and the tests) that already import it from there.
 */

// CMS-spec mapping: 0 -> zero/red, between -> partial/amber, == max -> full/green, undefined -> pending/grey
export function scoreClass(scored: number | null | undefined, max: number | null | undefined): string {
  if (scored == null || max == null || max <= 0) {
    return 'score-pending'
  }
  if (scored <= 0) {
    return 'score-zero'
  }
  if (scored >= max) {
    return 'score-full'
  }
  return 'score-partial'
}
