/**
 * Regression test for ARCH-009 (title-or-ordinal question label,
 * TODO-021): the question-list header must never render the raw opaque
 * q.id token (#{{ q.id }}); it must show q.title when set, else a
 * category-relative '#N' ordinal.
 *
 * Network boundary (fetch) is mocked so no backend is required.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PracticeView from '../../components/PracticeView.vue'

function jsonResponse(data: unknown, ok = true) {
  return Promise.resolve({
    ok,
    json: () => Promise.resolve(data),
  }) as unknown as Promise<Response>
}

function mkQuestion(overrides: Record<string, unknown> = {}) {
  return {
    id: 'PQ_opaque_token_should_never_render',
    created_by: 'U1',
    category_id: 'C1',
    category_name: 'Joins',
    target_table: '',
    question_type: 'QUERY',
    requirement: 'do the thing',
    points: 5,
    strict_ordering: false,
    strict_attrs: false,
    strict_column_order: false,
    expected_result_status: 'captured',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

let fetchMock: ReturnType<typeof vi.fn>

function installFetch(routes: Array<[string, unknown]>) {
  fetchMock = vi.fn(async (url: string) => {
    for (const [pattern, data] of routes) {
      if (url.includes(pattern)) return jsonResponse(data)
    }
    return jsonResponse([])
  })
  vi.stubGlobal('fetch', fetchMock)
}

beforeEach(() => {
  localStorage.setItem('sql_quiz_token', 'test-token')
})

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

describe('PracticeView — question label (title || #ordinal), ARCH-009', () => {
  it('renders the title when a question has one', async () => {
    installFetch([
      ['/practice/questions', [mkQuestion({ id: 'PQ1', title: 'Inner Joins 101' })]],
      ['/practice/attempts', []],
    ])
    const wrapper = mount(PracticeView)
    await flushPromises()

    const label = wrapper.find('.q-id')
    expect(label.exists()).toBe(true)
    expect(label.text()).toBe('Inner Joins 101')
  })

  it('falls back to a category-relative #N ordinal when title is empty, and never renders the raw id', async () => {
    installFetch([
      [
        '/practice/questions',
        [
          mkQuestion({ id: 'PQ_first', title: '', category_name: 'Joins' }),
          mkQuestion({ id: 'PQ_second', title: '', category_name: 'Joins' }),
        ],
      ],
      ['/practice/attempts', []],
    ])
    const wrapper = mount(PracticeView)
    await flushPromises()

    const labels = wrapper.findAll('.q-id').map((n) => n.text())
    expect(labels).toEqual(['#1', '#2'])
    // The opaque token must never leak into the rendered markup.
    expect(wrapper.text()).not.toContain('PQ_first')
    expect(wrapper.text()).not.toContain('PQ_second')
  })

  it('gives each attempt a question-identity label via the client-side id->label map', async () => {
    installFetch([
      ['/practice/questions', [mkQuestion({ id: 'PQ1', title: 'Named Question' })]],
      [
        '/practice/attempts',
        [
          {
            id: 'A1',
            student_id: 'U1',
            practice_question_id: 'PQ1',
            sql_text: 'SELECT 1',
            is_correct: true,
            submitted_at: '2026-01-01T00:00:00Z',
          },
        ],
      ],
    ])
    const wrapper = mount(PracticeView)
    await flushPromises()

    const attemptLabel = wrapper.find('.attempt-question')
    expect(attemptLabel.exists()).toBe(true)
    expect(attemptLabel.text()).toBe('Named Question')
  })
})
