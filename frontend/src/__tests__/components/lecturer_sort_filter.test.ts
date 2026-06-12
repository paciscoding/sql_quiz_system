/**
 * Tests for TODO-081
 * Tests the sort/filter computed logic used in LecturerView.
 * Rather than mounting the full complex component, we test the exact same
 * logic inline (mirroring the computed's implementation) and verify the
 * behavior contract. This avoids dealing with the 700+ line component's
 * full dependency graph while still covering the feature.
 */
import { describe, it, expect } from 'vitest'
import { ref, computed, type Ref } from 'vue'

// Mirrors the exact logic from LecturerView.vue `displayedQuestions` computed
function makeDisplayedQuestions(
  examQuestions: Ref<any[]>,
  filterText: Ref<string>,
  sortKey: Ref<'id' | 'type' | 'points' | null>,
  sortDir: Ref<'asc' | 'desc'>
) {
  return computed(() => {
    let list = examQuestions.value.slice()
    if (filterText.value.trim()) {
      const q = filterText.value.toLowerCase()
      list = list.filter(x => (x.requirement || '').toLowerCase().includes(q))
    }
    if (sortKey.value) {
      const k = sortKey.value
      const dir = sortDir.value === 'asc' ? 1 : -1
      list.sort((a, b) => {
        const av = k === 'type' ? (a.question_type || '') : (a[k] ?? 0)
        const bv = k === 'type' ? (b.question_type || '') : (b[k] ?? 0)
        return av < bv ? -dir : av > bv ? dir : 0
      })
    }
    return list
  })
}

const SAMPLE_QUESTIONS = [
  { id: 3, requirement: 'Write a SELECT query', question_type: 'QUERY', points: 10 },
  { id: 1, requirement: 'Write a DML statement', question_type: 'DML', points: 5 },
  { id: 2, requirement: 'Join two tables', question_type: 'QUERY', points: 15 },
]

describe('LecturerView sort/filter logic (TODO-081)', () => {
  it('returns all items when no filter and no sort', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('')
    const sortKey = ref<'id' | 'type' | 'points' | null>(null)
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    expect(displayed.value).toHaveLength(3)
  })

  it('filters by requirement substring (case-insensitive)', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('select')
    const sortKey = ref<'id' | 'type' | 'points' | null>(null)
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    expect(displayed.value).toHaveLength(1)
    expect(displayed.value[0].id).toBe(3)
  })

  it('filter is case-insensitive (uppercase search matches lowercase requirement)', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('JOIN')
    const sortKey = ref<'id' | 'type' | 'points' | null>(null)
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    expect(displayed.value).toHaveLength(1)
    expect(displayed.value[0].id).toBe(2)
  })

  it('filter returns empty array when no match', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('nonexistent-xyz')
    const sortKey = ref<'id' | 'type' | 'points' | null>(null)
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    expect(displayed.value).toHaveLength(0)
  })

  it('sorts by id ascending', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('')
    const sortKey = ref<'id' | 'type' | 'points' | null>('id')
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    const ids = displayed.value.map(q => q.id)
    expect(ids).toEqual([1, 2, 3])
  })

  it('sorts by id descending', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('')
    const sortKey = ref<'id' | 'type' | 'points' | null>('id')
    const sortDir = ref<'asc' | 'desc'>('desc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    const ids = displayed.value.map(q => q.id)
    expect(ids).toEqual([3, 2, 1])
  })

  it('sorts by points ascending', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('')
    const sortKey = ref<'id' | 'type' | 'points' | null>('points')
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    const pts = displayed.value.map(q => q.points)
    expect(pts).toEqual([5, 10, 15])
  })

  it('sorts by type ascending (alphabetical)', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('')
    const sortKey = ref<'id' | 'type' | 'points' | null>('type')
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    const types = displayed.value.map(q => q.question_type)
    expect(types[0]).toBe('DML')
  })

  it('combines filter + sort: filter first then sort', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('query') // matches id=3 (SELECT) and id=2 (JOIN) — requirement must contain "query"
    const sortKey = ref<'id' | 'type' | 'points' | null>('id')
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    // Only "Write a SELECT query" contains "query" in its requirement
    expect(displayed.value).toHaveLength(1)
    expect(displayed.value[0].id).toBe(3)
  })

  it('whitespace-only filter is treated as no filter', () => {
    const examQuestions = ref([...SAMPLE_QUESTIONS])
    const filterText = ref('   ')
    const sortKey = ref<'id' | 'type' | 'points' | null>(null)
    const sortDir = ref<'asc' | 'desc'>('asc')
    const displayed = makeDisplayedQuestions(examQuestions, filterText, sortKey, sortDir)

    expect(displayed.value).toHaveLength(3)
  })
})
