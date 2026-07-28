/**
 * Component + route-guard tests for BulkPasswordReset.vue (ARCH-031/032/033,
 * TODO-075/076/077).
 *
 * Covered:
 *  1. Route guard: /admin/bulk-reset is ADMIN-only, LECTURER/STUDENT -> /403.
 *  2. Each of the three selection modes posts the right outgoing request body.
 *  3. The confirm control stays disabled for anything but the exact literal RESET.
 *  4. The cursor loop across two mocked pages: exactly two POSTs, the second
 *     carrying the first response's next_after_user_id, no duplicates in the
 *     final table.
 *  5. Partial-failure UI: a rejecting second batch keeps the first batch's rows
 *     rendered, shows an error banner, and renders the failures table.
 *  6. CSV: filename + header + row count.
 *  7. Print: window.print() called once per action, one .slip per result row.
 *  8. No password ever lands in localStorage/sessionStorage.
 *  9. beforeunload listener registered while results are held, removed once cleared.
 *
 * The network boundary (fetch) is mocked so no backend is required.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Keep toCsv real (so the header/content assertions are genuine) but stub the
// DOM-touching download trigger.
vi.mock('../../utils/csv', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../utils/csv')>()
  return { ...actual, downloadCsv: vi.fn() }
})

import BulkPasswordReset from '../../components/BulkPasswordReset.vue'
import StudentMultiSelect from '../../components/StudentMultiSelect.vue'
import { downloadCsv } from '../../utils/csv'
import router from '../../router'
import { useAuth, type AuthUser } from '../../services/auth'

function jsonResponse(data: unknown, ok = true) {
  return Promise.resolve({
    ok,
    json: () => Promise.resolve(data),
  }) as unknown as Promise<Response>
}

const STUDENTS_FIXTURE = [
  { id: 'stu-1', username: 'alice' },
  { id: 'stu-2', username: 'bob' },
]
const EXAMS_FIXTURE = [{ id: 'exam-1', title: 'Midterm' }]

function resultRow(id: string, username: string, password = `pw-${id}`) {
  return {
    user_id: id,
    username,
    student_number: `SN-${id}`,
    first_name: 'First',
    last_name: 'Last',
    sec_lec: 'L1',
    sec_lab: 'B1',
    password,
  }
}

let fetchMock: ReturnType<typeof vi.fn>

function installFetch(handler: (url: string, opts?: RequestInit) => Promise<Response>) {
  fetchMock = vi.fn((url: string, opts?: RequestInit) => handler(String(url), opts))
  vi.stubGlobal('fetch', fetchMock)
}

function defaultRoutes(url: string): Promise<Response> | null {
  if (url.includes('/api/v1/lecturer/exams/') && url.endsWith('/students')) {
    return jsonResponse({ count: 2, students: [] })
  }
  if (url.endsWith('/api/v1/lecturer/exams')) {
    return jsonResponse(EXAMS_FIXTURE)
  }
  if (url.endsWith('/api/v1/users/students')) {
    return jsonResponse(STUDENTS_FIXTURE)
  }
  return null
}

beforeEach(() => {
  localStorage.setItem('sql_quiz_token', 'test-token')
  window.print = vi.fn()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

async function mountBulkReset() {
  const wrapper = mount(BulkPasswordReset)
  await flushPromises()
  return wrapper
}

function bulkPostCalls() {
  const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit | undefined]>
  return calls.filter((c) => c[0].endsWith('/api/v1/users/bulk-reset-password'))
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Route guard
// ─────────────────────────────────────────────────────────────────────────────

describe('route guard (ARCH-033 H1)', () => {
  const START_PATH = '/__bulk-reset-route-test__'

  function loginAs(role: AuthUser['role']) {
    useAuth().login(`${role.toLowerCase()}-token`, {
      id: '99',
      username: role.toLowerCase(),
      role,
    })
  }

  beforeEach(async () => {
    useAuth().logout()
    localStorage.clear()
    await router.push('/')
    await router.push(START_PATH)
  })

  it('ADMIN can navigate to /admin/bulk-reset', async () => {
    loginAs('ADMIN')
    await router.push('/admin/bulk-reset')
    expect(router.currentRoute.value.name).toBe('admin-bulk-reset')
  })

  it('LECTURER is redirected to /403', async () => {
    loginAs('LECTURER')
    await router.push('/admin/bulk-reset')
    expect(router.currentRoute.value.path).toBe('/403')
  })

  it('STUDENT is redirected to /403', async () => {
    loginAs('STUDENT')
    await router.push('/admin/bulk-reset')
    expect(router.currentRoute.value.path).toBe('/403')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 2. Three selection modes -> exact outgoing request body
// ─────────────────────────────────────────────────────────────────────────────

describe('selection modes (ARCH-033 H2)', () => {
  it('mode=ids posts exactly the selected user_ids + confirm:RESET', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice'), resultRow('stu-2', 'bob')])))

    const wrapper = await mountBulkReset()
    await wrapper.findComponent(StudentMultiSelect).vm.$emit('update:modelValue', ['stu-1', 'stu-2'])
    await flushPromises()

    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const calls = bulkPostCalls()
    expect(calls.length).toBe(1)
    const body = JSON.parse(calls[0][1]!.body as string)
    expect(body.mode).toBe('ids')
    expect(body.user_ids).toEqual(['stu-1', 'stu-2'])
    expect(body.confirm).toBe('RESET')
    expect(body.exam_id).toBeUndefined()
  })

  it('mode=all_students posts confirm:RESET with no user_ids/exam_id', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice'), resultRow('stu-2', 'bob')])))

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()

    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const calls = bulkPostCalls()
    expect(calls.length).toBe(1)
    const body = JSON.parse(calls[0][1]!.body as string)
    expect(body.mode).toBe('all_students')
    expect(body.confirm).toBe('RESET')
    expect(body.user_ids).toBeUndefined()
    expect(body.exam_id).toBeUndefined()
  })

  it('mode=exam_roster posts the selected exam_id + confirm:RESET', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice'), resultRow('stu-2', 'bob')])))

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="exam_roster"]').setValue()
    await flushPromises()
    await wrapper.find('#bulk-reset-exam').setValue('exam-1')
    await flushPromises()

    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const calls = bulkPostCalls()
    expect(calls.length).toBe(1)
    const body = JSON.parse(calls[0][1]!.body as string)
    expect(body.mode).toBe('exam_roster')
    expect(body.exam_id).toBe('exam-1')
    expect(body.confirm).toBe('RESET')
    expect(body.user_ids).toBeUndefined()
  })
})

function bulkResp(resultsRows: ReturnType<typeof resultRow>[], overrides: Record<string, unknown> = {}) {
  return {
    mode: 'ids',
    processed: resultsRows.length,
    succeeded: resultsRows.length,
    failed: 0,
    remaining: 0,
    next_after_user_id: null,
    key_configured: true,
    results: resultsRows,
    failures: [],
    ...overrides,
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Confirm-literal gate
// ─────────────────────────────────────────────────────────────────────────────

describe('confirm gate (ARCH-033 H3)', () => {
  it('stays disabled for anything but the exact literal RESET', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([])))
    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')

    const submit = () => wrapper.find('[data-testid="bulk-reset-submit"]')
    expect(submit().attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('reset')
    expect(submit().attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESE')
    expect(submit().attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    expect(submit().attributes('disabled')).toBeUndefined()
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 4. Cursor loop across two mocked pages
// ─────────────────────────────────────────────────────────────────────────────

describe('cursor loop (ARCH-033 H4)', () => {
  it('two mocked pages -> exactly two POSTs, second carries the cursor, union table has no duplicates', async () => {
    let call = 0
    installFetch((url) => {
      const known = defaultRoutes(url)
      if (known) return known
      if (url.endsWith('/api/v1/users/bulk-reset-password')) {
        call += 1
        if (call === 1) {
          return jsonResponse(bulkResp([resultRow('stu-1', 'alice')], { remaining: 1, next_after_user_id: 'cursor-1' }))
        }
        return jsonResponse(bulkResp([resultRow('stu-2', 'bob')], { remaining: 0, next_after_user_id: null }))
      }
      return jsonResponse({})
    })

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const calls = bulkPostCalls()
    expect(calls.length).toBe(2)
    const secondBody = JSON.parse(calls[1][1]!.body as string)
    expect(secondBody.after_user_id).toBe('cursor-1')

    const rows = wrapper.findAll('[data-testid="bulk-reset-results-table"] tbody tr')
    expect(rows.length).toBe(2)
    const userIds = rows.map((r) => r.text())
    expect(new Set(userIds).size).toBe(2) // no duplicates
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 5. Partial-failure UI
// ─────────────────────────────────────────────────────────────────────────────

describe('partial failure UI (ARCH-033 H5)', () => {
  it('a rejecting second batch keeps the first batch rendered, shows an error banner and a failures table', async () => {
    let call = 0
    installFetch((url) => {
      const known = defaultRoutes(url)
      if (known) return known
      if (url.endsWith('/api/v1/users/bulk-reset-password')) {
        call += 1
        if (call === 1) {
          return jsonResponse(
            bulkResp([resultRow('stu-1', 'alice')], {
              remaining: 1,
              next_after_user_id: 'cursor-1',
              failed: 1,
              failures: [{ user_id: 'stu-9', username: 'flaky', error: 'db timeout' }],
            }),
          )
        }
        return jsonResponse({ error: 'server exploded' }, false)
      }
      return jsonResponse({})
    })

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    expect(bulkPostCalls().length).toBe(2)

    const errorBanner = wrapper.find('[data-testid="bulk-reset-error"]')
    expect(errorBanner.exists()).toBe(true)

    const resultsRows = wrapper.findAll('[data-testid="bulk-reset-results-table"] tbody tr')
    expect(resultsRows.length).toBe(1)
    expect(resultsRows[0].text()).toContain('alice')

    const failuresTable = wrapper.find('[data-testid="bulk-reset-failures-table"]')
    expect(failuresTable.exists()).toBe(true)
    expect(failuresTable.text()).toContain('flaky')
    expect(failuresTable.text()).toContain('db timeout')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 6. CSV
// ─────────────────────────────────────────────────────────────────────────────

describe('CSV export (ARCH-033 H6)', () => {
  it('downloadCsv is called once with the right filename/header/row-count', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice'), resultRow('stu-2', 'bob')])))

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const buttons = wrapper.findAll('.results-actions button')
    const csvButton = buttons.find((b) => b.text().includes('Download CSV'))!
    await csvButton.trigger('click')

    expect(downloadCsv).toHaveBeenCalledTimes(1)
    const [filename, content] = (downloadCsv as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [string, string]
    expect(filename).toMatch(/^password-reset-(ids|all_students|exam_roster)-\d{8}-\d{4}\.csv$/)
    const lines = content.split('\r\n')
    expect(lines[0]).toBe('username,student_number,first_name,last_name,sec_lec,sec_lab,password')
    expect(lines.length - 1).toBe(2) // header + 2 rows
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 7. Print
// ─────────────────────────────────────────────────────────────────────────────

describe('print artifacts (ARCH-033 H7)', () => {
  it('Print table calls window.print() exactly once', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice')])))
    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const buttons = wrapper.findAll('.results-actions button')
    const printTableBtn = buttons.find((b) => b.text().includes('Print table'))!
    await printTableBtn.trigger('click')
    await flushPromises()

    expect(window.print).toHaveBeenCalledTimes(1)
  })

  it('Print slips calls window.print() exactly once and renders one .slip per result row', async () => {
    installFetch((url) =>
      defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice'), resultRow('stu-2', 'bob')])),
    )
    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    const buttons = wrapper.findAll('.results-actions button')
    const printSlipsBtn = buttons.find((b) => b.text().includes('Print slips'))!
    await printSlipsBtn.trigger('click')
    await flushPromises()

    expect(window.print).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('.slip').length).toBe(2)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 8. No persistence
// ─────────────────────────────────────────────────────────────────────────────

describe('no persistence (ARCH-033 H8 / ARCH-032 G3)', () => {
  it('after a successful run, neither localStorage nor sessionStorage holds a returned password', async () => {
    const pw1 = 'never-persist-me-1'
    const pw2 = 'never-persist-me-2'
    installFetch((url) =>
      defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice', pw1), resultRow('stu-2', 'bob', pw2)])),
    )

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain(pw1) // sanity: the run really did produce these passwords

    for (const store of [localStorage, sessionStorage]) {
      for (let i = 0; i < store.length; i++) {
        const key = store.key(i)!
        const value = store.getItem(key) ?? ''
        expect(value).not.toContain(pw1)
        expect(value).not.toContain(pw2)
      }
    }
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 9. beforeunload guard
// ─────────────────────────────────────────────────────────────────────────────

describe('beforeunload guard (ARCH-033 G4)', () => {
  it('registered once results are held, removed once cleared', async () => {
    installFetch((url) => defaultRoutes(url) ?? jsonResponse(bulkResp([resultRow('stu-1', 'alice')])))
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')

    const wrapper = await mountBulkReset()
    await wrapper.find('input[type="radio"][value="all_students"]').setValue()
    await flushPromises()
    await wrapper.find('.btn-primary').trigger('click')
    await wrapper.find('[data-testid="bulk-reset-confirm-input"]').setValue('RESET')
    await wrapper.find('[data-testid="bulk-reset-submit"]').trigger('click')
    await flushPromises()

    expect(addSpy.mock.calls.some((c) => c[0] === 'beforeunload')).toBe(true)

    await wrapper.find('[data-testid="bulk-reset-clear"]').trigger('click')
    await flushPromises()

    expect(removeSpy.mock.calls.some((c) => c[0] === 'beforeunload')).toBe(true)
  })
})
