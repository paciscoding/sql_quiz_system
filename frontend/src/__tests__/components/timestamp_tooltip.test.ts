/**
 * Tests for TODO-073 and TODO-086
 * Mounts StudentResults in details view to assert:
 *   1. The title attribute on the timestamp span is a valid ISO string.
 *   2. The inline relative-time span renders inside the same cell.
 */
import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import StudentResults from '../../components/StudentResults.vue'

// Stub child components that have complex dependencies
vi.mock('../../components/ResultTable.vue', () => ({
  default: { template: '<div class="result-table-stub" />' },
}))
vi.mock('../../components/EmptyState.vue', () => ({
  default: { template: '<div class="empty-state-stub" />' },
}))
vi.mock('../../components/Skeleton.vue', () => ({
  default: { template: '<div class="skeleton-stub" />' },
}))
vi.mock('../../services/error_handler', () => ({
  handleApiError: vi.fn(),
}))

const ISO_TIMESTAMP = '2024-06-01T14:30:00.000Z'
// Exam ids are opaque publicid tokens on the wire, never bare integers.
const EXAM_ID = 'exm_9Kq2Xb'

function makeFetchMock() {
  return vi.fn().mockImplementation((url: string) => {
    // Per-exam details: GET /api/v1/quiz/exams/:id/results. Returns a BARE ARRAY
    // of flat models.StudentExamResult rows (backend/internal/models/submission.go).
    // Must be matched BEFORE the looser '/results' branch below, which would
    // otherwise shadow it and return the exam-LIST summary shape instead.
    if (typeof url === 'string' && /\/api\/v1\/quiz\/exams\/[^/]+\/results$/.test(url)) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve([
          {
            question_id: 1,
            requirement: 'Write a SELECT query',
            type: 'QUERY',
            points: 10,
            sql_text: 'SELECT * FROM users',
            is_correct: true,
            score: 10,
            submitted_at: ISO_TIMESTAMP,
            validation_details: null,
          },
        ]),
      })
    }
    if (typeof url === 'string' && url.includes('/practice/summary')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    }
    // Exam list: GET /api/v1/quiz/results -> models.ExamResultSummary[].
    if (typeof url === 'string' && url.includes('/results')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve([
          {
            exam_id: EXAM_ID,
            exam_title: 'Test Exam',
            is_active: true,
            question_count: 1,
            answered_count: 1,
            correct_count: 1,
            total_score: 10,
            max_score: 10,
          },
        ]),
      })
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
  })
}

beforeEach(() => {
  vi.stubGlobal('fetch', makeFetchMock())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('StudentResults timestamp tooltip (TODO-073, TODO-086)', () => {
  it('renders timestamp span with ISO string in title attribute on drill-down view', async () => {
    const wrapper = mount(StudentResults, { attachTo: document.body })
    await flushPromises()

    // Click an exam card to drill down
    const examCard = wrapper.find('.exam-card.clickable')
    expect(examCard.exists()).toBe(true)
    await examCard.trigger('click')
    await flushPromises()

    // Should now be in details view - find the timestamp span with a title
    const timestampSpan = wrapper.find('span[title]')
    expect(timestampSpan.exists()).toBe(true)

    // The title attribute should be a valid ISO string
    const titleAttr = timestampSpan.attributes('title')
    expect(titleAttr).toBeTruthy()
    const parsed = new Date(titleAttr!)
    expect(isNaN(parsed.getTime())).toBe(false)
    expect(titleAttr).toContain('T') // ISO format contains 'T'

    wrapper.unmount()
  })

  it('renders the relative-time inline span inside the timestamp cell', async () => {
    const wrapper = mount(StudentResults, { attachTo: document.body })
    await flushPromises()

    const examCard = wrapper.find('.exam-card.clickable')
    await examCard.trigger('click')
    await flushPromises()

    // The relative time span is a .muted.small inside the timestamp cell
    const relativeSpan = wrapper.find('span[title] span.muted.small')
    expect(relativeSpan.exists()).toBe(true)
    // It should have non-empty text (relative time like "2 years ago")
    expect(relativeSpan.text().trim().length).toBeGreaterThan(0)

    wrapper.unmount()
  })

  it('component renders without crashing when exam list is empty', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([]),
    }))

    const wrapper = mount(StudentResults, { attachTo: document.body })
    await flushPromises()
    expect(wrapper.exists()).toBe(true)
    wrapper.unmount()
  })
})
