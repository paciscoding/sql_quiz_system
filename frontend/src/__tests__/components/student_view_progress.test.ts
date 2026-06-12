/**
 * Tests for TODO-076 and TODO-087
 * Tests the progressMap seeding logic and answeredCount/correctCount/progressPct computeds.
 * We mirror the exact logic from StudentView.vue rather than mounting the full component.
 */
import { describe, it, expect } from 'vitest'
import { ref, computed, type Ref } from 'vue'

// Mirror the exact progressMap + answerStatus merge logic from StudentView.vue
function makeProgressComputeds(
  questions: Ref<any[]>,
  progressMap: Ref<Record<number, { answered: boolean; correct: boolean }>>,
  answerStatus: Ref<Record<number, boolean>>
) {
  const answeredCount = computed(() => {
    const merged = { ...progressMap.value }
    for (const [k, v] of Object.entries(answerStatus.value)) {
      merged[Number(k)] = { answered: true, correct: v }
    }
    return Object.values(merged).filter(v => v.answered).length
  })

  const correctCount = computed(() => {
    const merged = { ...progressMap.value }
    for (const [k, v] of Object.entries(answerStatus.value)) {
      merged[Number(k)] = { answered: true, correct: v }
    }
    return Object.values(merged).filter(v => v.correct).length
  })

  const progressPct = computed(() =>
    questions.value.length === 0
      ? 0
      : Math.round((answeredCount.value / questions.value.length) * 100)
  )

  return { answeredCount, correctCount, progressPct }
}

const QUESTIONS = [
  { id: 1, type: 'QUERY', points: 10, requirement: 'Q1' },
  { id: 2, type: 'QUERY', points: 10, requirement: 'Q2' },
  { id: 3, type: 'QUERY', points: 10, requirement: 'Q3' },
  { id: 4, type: 'QUERY', points: 10, requirement: 'Q4' },
]

describe('StudentView progress strip logic (TODO-076, TODO-087)', () => {
  it('returns 0% when no questions', () => {
    const questions = ref<any[]>([])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({})
    const answerStatus = ref<Record<number, boolean>>({})
    const { progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(progressPct.value).toBe(0)
  })

  it('returns 0 answered/correct with empty progressMap and no answerStatus', () => {
    const questions = ref([...QUESTIONS])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({})
    const answerStatus = ref<Record<number, boolean>>({})
    const { answeredCount, correctCount } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(0)
    expect(correctCount.value).toBe(0)
  })

  it('seeds progressMap from server data (answered=true, correct=true)', () => {
    const questions = ref([...QUESTIONS])
    // Simulate server seeding 2 answered questions (both correct)
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
      2: { answered: true, correct: false },
    })
    const answerStatus = ref<Record<number, boolean>>({})
    const { answeredCount, correctCount, progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(2)
    expect(correctCount.value).toBe(1)
    expect(progressPct.value).toBe(50) // 2/4 = 50%
  })

  it('answerStatus overrides progressMap for the same question', () => {
    const questions = ref([...QUESTIONS])
    // Server says question 1 was incorrect
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: false },
    })
    // Local answer status says question 1 is now correct (just submitted)
    const answerStatus = ref<Record<number, boolean>>({ 1: true })
    const { correctCount } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(correctCount.value).toBe(1)
  })

  it('calculates 100% when all questions answered', () => {
    const questions = ref([...QUESTIONS])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
      2: { answered: true, correct: true },
      3: { answered: true, correct: false },
      4: { answered: true, correct: false },
    })
    const answerStatus = ref<Record<number, boolean>>({})
    const { answeredCount, progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(4)
    expect(progressPct.value).toBe(100)
  })

  it('progressPct rounds to nearest integer', () => {
    // 1 out of 3 questions answered = 33.33% -> rounds to 33
    const questions = ref([
      { id: 1, type: 'QUERY', points: 10, requirement: 'Q1' },
      { id: 2, type: 'QUERY', points: 10, requirement: 'Q2' },
      { id: 3, type: 'QUERY', points: 10, requirement: 'Q3' },
    ])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
    })
    const answerStatus = ref<Record<number, boolean>>({})
    const { progressPct } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(progressPct.value).toBe(33)
  })

  it('merges new answerStatus entries with existing progressMap', () => {
    const questions = ref([...QUESTIONS])
    const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({
      1: { answered: true, correct: true },
    })
    // Add a new answer for question 3
    const answerStatus = ref<Record<number, boolean>>({ 3: true })
    const { answeredCount, correctCount } = makeProgressComputeds(questions, progressMap, answerStatus)

    expect(answeredCount.value).toBe(2)
    expect(correctCount.value).toBe(2)
  })
})
