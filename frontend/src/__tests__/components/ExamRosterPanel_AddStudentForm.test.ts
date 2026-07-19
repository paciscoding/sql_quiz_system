/**
 * Regression test for ARCH-003 (AddStudentForm reused by the lecturer roster
 * surface): AddStudentForm.vue must be reachable from BOTH of its mount
 * sites — AdminView.vue (already covered by AdminView.test.ts) and
 * ExamRosterPanel.vue (previously untested; lecturer_decomposed.test.ts only
 * ever stubbed ExamRosterPanel out). This file covers the second site.
 *
 * Network boundary (fetch) is mocked so no backend is required.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// BulkImportStudents pulls in heavier deps unrelated to this test; stub it,
// same as AdminView.test.ts does.
vi.mock('../../components/BulkImportStudents.vue', () => ({
  default: { name: 'BulkImportStudents', template: '<div class="stub-bulk-import" />' },
}))

import ExamRosterPanel from '../../components/lecturer/ExamRosterPanel.vue'
import type { Examination } from '../../components/lecturer/types'

function jsonResponse(data: unknown, ok = true) {
  return Promise.resolve({
    ok,
    json: () => Promise.resolve(data),
  }) as unknown as Promise<Response>
}

const EXAM: Examination = {
  id: 'exam-1',
  title: 'Test Exam',
  is_active: false,
  effective_state: 'inactive',
  scheduled_start_at: null,
  scheduled_end_at: null,
  activated_at: null,
  archived_at: null,
  introduction: '',
  block_practice_during_exam: false,
}

let fetchMock: ReturnType<typeof vi.fn>

function installFetch(handler: (url: string, opts?: RequestInit) => Promise<Response>) {
  fetchMock = vi.fn((url: string, opts?: RequestInit) => handler(String(url), opts))
  vi.stubGlobal('fetch', fetchMock)
}

beforeEach(() => {
  localStorage.setItem('sql_quiz_token', 'test-token')
  installFetch((url) => {
    if (url.includes('/users/students')) return jsonResponse([])
    if (url.includes('/students')) return jsonResponse([])
    return jsonResponse({ students: [] })
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

const STUDENT_TESTIDS = [
  'add-student-form',
  'student-number',
  'student-id-ext',
  'student-sec-lec',
  'student-sec-lab',
  'student-first-name',
  'student-last-name',
  'student-email-input',
]

describe('ExamRosterPanel — AddStudentForm mount site (ARCH-003)', () => {
  it('renders all AddStudentForm data-testids inside the Add Student details panel', async () => {
    const wrapper = mount(ExamRosterPanel, { props: { exam: EXAM } })
    await flushPromises()

    for (const testid of STUDENT_TESTIDS) {
      expect(wrapper.find(`[data-testid="${testid}"]`).exists()).toBe(true)
    }
  })

  it('placed inside a <details> alongside BulkImportStudents (not replacing it)', async () => {
    const wrapper = mount(ExamRosterPanel, { props: { exam: EXAM } })
    await flushPromises()

    expect(wrapper.findComponent({ name: 'BulkImportStudents' }).exists()).toBe(true)
    const addStudentDetails = wrapper.find('.add-student-panel')
    expect(addStudentDetails.exists()).toBe(true)
    expect(addStudentDetails.find('[data-testid="add-student-form"]').exists()).toBe(true)
  })

  it('submitting the form posts roster fields and refreshes the student list (fetchStudents re-fires)', async () => {
    installFetch((url, opts) => {
      if (url.includes('/users/students')) return jsonResponse([])
      if (url.endsWith('/api/v1/students') && opts?.method === 'POST') {
        return jsonResponse({ created: true, generated_password: 'genpass123' })
      }
      return jsonResponse({ students: [] })
    })

    const wrapper = mount(ExamRosterPanel, { props: { exam: EXAM } })
    await flushPromises()

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit | undefined]>
    const callsBeforeSubmit = calls.filter((c) => c[0].includes('/users/students')).length

    await wrapper.find('[data-testid="student-number"]').setValue('20231234')
    await wrapper.find('[data-testid="student-id-ext"]').setValue('EXT-1')
    await wrapper.find('[data-testid="add-student-form"]').trigger('submit')
    await flushPromises()

    const callsAfterSubmit = calls.filter((c) => c[0].includes('/users/students')).length
    // @created="fetchStudents" must re-fire the students list fetch.
    expect(callsAfterSubmit).toBeGreaterThan(callsBeforeSubmit)

    const postCall = calls.find((c) => c[0].endsWith('/api/v1/students') && c[1]?.method === 'POST')
    expect(postCall).toBeDefined()
    const body = JSON.parse((postCall as [string, RequestInit])[1].body as string)
    expect(body.student_number).toBe('20231234')
    expect(body.student_id_ext).toBe('EXT-1')
  })
})
