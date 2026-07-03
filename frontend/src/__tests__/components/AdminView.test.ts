/**
 * Component tests for AdminView.vue — auth/password-management frontend
 * (spec: docs/superpowers/specs/2026-07-01-auth-password-management-design.md §8).
 *
 * Covered:
 *  1. Create-staff form has NO password input and reveals generated_password on success.
 *  2. Add-student form posts the roster fields and reveals default_password.
 *  3. Per-user Reset action calls the endpoint and reveals the new generated_password.
 *  4. Password-history renders events, and shows the "key not configured" notice
 *     when key_configured=false.
 *
 * The network boundary (fetch) is mocked so no backend is required.
 */

import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The confirm service normally resolves via ConfirmDialogHost, which isn't
// mounted here. Auto-approve so the reset flow proceeds.
vi.mock('../../services/confirm_service', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(true) }),
}))

// BulkImportStudents pulls in heavier deps; stub it out.
vi.mock('../../components/BulkImportStudents.vue', () => ({
  default: { name: 'BulkImportStudents', template: '<div class="stub-bulk-import" />' },
}))

import AdminView from '../../components/AdminView.vue'

function jsonResponse(data: unknown, ok = true) {
  return Promise.resolve({
    ok,
    json: () => Promise.resolve(data),
  }) as unknown as Promise<Response>
}

const USERS_FIXTURE = [
  { id: 7, username: 'alice', role: 'LECTURER', first_name: 'Alice', last_name: 'Ng' },
]

let fetchMock: ReturnType<typeof vi.fn>

function installFetch(handler: (url: string, opts?: RequestInit) => Promise<Response>) {
  fetchMock = vi.fn((url: string, opts?: RequestInit) => handler(String(url), opts))
  vi.stubGlobal('fetch', fetchMock)
}

beforeEach(() => {
  localStorage.setItem('sql_quiz_token', 'test-token')
  // Default handler: list endpoint returns the fixture, everything else empty.
  installFetch((url) => {
    if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
    return jsonResponse({})
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

async function mountAdmin() {
  const wrapper = mount(AdminView)
  await flushPromises()
  return wrapper
}

describe('AdminView — create staff', () => {
  it('has no password input in the create-staff form', async () => {
    const wrapper = await mountAdmin()
    const form = wrapper.find('[data-testid="create-staff-form"]')
    expect(form.exists()).toBe(true)
    expect(form.find('input[type="password"]').exists()).toBe(false)
  })

  it('posts username+role (no password) and reveals the generated password', async () => {
    let postBody: any = null
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/users') && opts?.method === 'POST') {
        postBody = JSON.parse(String(opts.body))
        return jsonResponse({ id: 5, generated_password: 'GEN-STAFF-PW' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="staff-username"]').setValue('newlecturer')
    await wrapper.find('[data-testid="staff-role"]').setValue('LECTURER')
    await wrapper.find('[data-testid="create-staff-form"]').trigger('submit')
    await flushPromises()

    expect(postBody).toEqual({ username: 'newlecturer', role: 'LECTURER' })
    expect(postBody.password).toBeUndefined()

    const reveal = wrapper.find('[data-testid="staff-password-reveal"]')
    expect(reveal.exists()).toBe(true)
    expect(reveal.text()).toContain('GEN-STAFF-PW')
    expect(wrapper.find('[data-testid="staff-copy"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="staff-email"]').exists()).toBe(true)
  })

  it('includes the optional email in the create-staff payload when filled', async () => {
    let postBody: any = null
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/users') && opts?.method === 'POST') {
        postBody = JSON.parse(String(opts.body))
        return jsonResponse({ id: 5, generated_password: 'GEN-STAFF-PW' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    const emailInput = wrapper.find('[data-testid="staff-email-input"]')
    expect(emailInput.exists()).toBe(true)
    expect(emailInput.attributes('type')).toBe('email')

    await wrapper.find('[data-testid="staff-username"]').setValue('newlecturer')
    await wrapper.find('[data-testid="staff-role"]').setValue('LECTURER')
    await emailInput.setValue('lecturer@example.com')
    await wrapper.find('[data-testid="create-staff-form"]').trigger('submit')
    await flushPromises()

    expect(postBody).toEqual({
      username: 'newlecturer',
      role: 'LECTURER',
      email: 'lecturer@example.com',
    })
  })

  it('omits email from the create-staff payload when left empty', async () => {
    let postBody: any = null
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/users') && opts?.method === 'POST') {
        postBody = JSON.parse(String(opts.body))
        return jsonResponse({ id: 5, generated_password: 'GEN-STAFF-PW' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="staff-username"]').setValue('newlecturer')
    await wrapper.find('[data-testid="create-staff-form"]').trigger('submit')
    await flushPromises()

    expect(postBody).toEqual({ username: 'newlecturer', role: 'LECTURER' })
    expect(postBody.email).toBeUndefined()
  })

  it('role dropdown offers only LECTURER and ADMIN (no STUDENT)', async () => {
    const wrapper = await mountAdmin()
    const options = wrapper
      .find('[data-testid="staff-role"]')
      .findAll('option')
      .map((o) => o.attributes('value'))
    expect(options).toContain('LECTURER')
    expect(options).toContain('ADMIN')
    expect(options).not.toContain('STUDENT')
  })

  it('email button surfaces a "delivery pending" notice', async () => {
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/users') && opts?.method === 'POST')
        return jsonResponse({ id: 5, generated_password: 'GEN-STAFF-PW' })
      return jsonResponse({})
    })
    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="staff-username"]').setValue('newlecturer')
    await wrapper.find('[data-testid="create-staff-form"]').trigger('submit')
    await flushPromises()

    await wrapper.find('[data-testid="staff-email"]').trigger('click')
    await flushPromises()
    expect(wrapper.text().toLowerCase()).toContain('email delivery pending')
  })
})

describe('AdminView — add student', () => {
  it('posts the roster fields and reveals the default password', async () => {
    let postBody: any = null
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/students') && opts?.method === 'POST') {
        postBody = JSON.parse(String(opts.body))
        return jsonResponse({ id: 9, created: true, default_password: '20231234' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="student-number"]').setValue('20231234')
    await wrapper.find('[data-testid="student-id-ext"]').setValue('EXT-1')
    await wrapper.find('[data-testid="student-sec-lec"]').setValue('L1')
    await wrapper.find('[data-testid="student-sec-lab"]').setValue('B2')
    await wrapper.find('[data-testid="student-first-name"]').setValue('Bob')
    await wrapper.find('[data-testid="student-last-name"]').setValue('Stone')
    await wrapper.find('[data-testid="add-student-form"]').trigger('submit')
    await flushPromises()

    expect(postBody).toEqual({
      student_number: '20231234',
      student_id_ext: 'EXT-1',
      sec_lec: 'L1',
      sec_lab: 'B2',
      first_name: 'Bob',
      last_name: 'Stone',
    })

    const reveal = wrapper.find('[data-testid="student-password-reveal"]')
    expect(reveal.exists()).toBe(true)
    expect(reveal.text()).toContain('20231234')
    // Reveal surfaces the login username = the Student ID (student_id_ext).
    expect(reveal.text()).toContain('EXT-1')
  })

  it('blocks the add-student submit when Student ID is missing', async () => {
    let posted = false
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/students') && opts?.method === 'POST') {
        posted = true
        return jsonResponse({ id: 9, created: true, default_password: '20231234' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="student-number"]').setValue('20231234')
    // student-id-ext left blank
    await wrapper.find('[data-testid="add-student-form"]').trigger('submit')
    await flushPromises()

    expect(posted).toBe(false)
    expect(wrapper.find('[data-testid="student-password-reveal"]').exists()).toBe(false)
  })

  it('includes the optional email in the add-student payload when filled', async () => {
    let postBody: any = null
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.endsWith('/api/v1/students') && opts?.method === 'POST') {
        postBody = JSON.parse(String(opts.body))
        return jsonResponse({ id: 9, created: true, default_password: '20231234' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    const emailInput = wrapper.find('[data-testid="student-email-input"]')
    expect(emailInput.exists()).toBe(true)
    expect(emailInput.attributes('type')).toBe('email')

    await wrapper.find('[data-testid="student-number"]').setValue('20231234')
    await wrapper.find('[data-testid="student-id-ext"]').setValue('EXT-1')
    await emailInput.setValue('student@example.com')
    await wrapper.find('[data-testid="add-student-form"]').trigger('submit')
    await flushPromises()

    expect(postBody.email).toBe('student@example.com')
    expect(postBody.student_number).toBe('20231234')
  })
})

describe('AdminView — reset password', () => {
  it('calls the reset endpoint and reveals the new password', async () => {
    let resetCalled = false
    installFetch((url, opts) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.includes('/api/v1/users/7/reset-password') && opts?.method === 'POST') {
        resetCalled = true
        return jsonResponse({ generated_password: 'RESET-PW-9' })
      }
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="reset-7"]').trigger('click')
    await flushPromises()

    expect(resetCalled).toBe(true)
    const reveal = wrapper.find('[data-testid="reset-password-reveal"]')
    expect(reveal.exists()).toBe(true)
    expect(reveal.text()).toContain('RESET-PW-9')
  })
})

describe('AdminView — password history', () => {
  it('renders the events when the key is configured', async () => {
    installFetch((url) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.includes('/api/v1/users/7/password-events'))
        return jsonResponse({
          key_configured: true,
          events: [
            {
              source: 'admin_reset',
              changed_by_username: 'root',
              created_at: '2026-07-01T10:00:00Z',
              new_password: 'HISTPW-1',
            },
            {
              source: 'create',
              changed_by_username: 'root',
              created_at: '2026-06-01T10:00:00Z',
              new_password: 'HISTPW-0',
            },
          ],
        })
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="history-7"]').trigger('click')
    await flushPromises()

    const panel = wrapper.find('[data-testid="password-history"]')
    expect(panel.exists()).toBe(true)
    expect(panel.text()).toContain('admin_reset')
    expect(panel.text()).toContain('HISTPW-1')
    expect(panel.text()).toContain('root')
    expect(wrapper.find('[data-testid="history-key-notice"]').exists()).toBe(false)
  })

  it('shows the key-not-configured notice when key_configured=false', async () => {
    installFetch((url) => {
      if (url.includes('/api/v1/users/all')) return jsonResponse(USERS_FIXTURE)
      if (url.includes('/api/v1/users/7/password-events'))
        return jsonResponse({ key_configured: false, events: [] })
      return jsonResponse({})
    })

    const wrapper = await mountAdmin()
    await wrapper.find('[data-testid="history-7"]').trigger('click')
    await flushPromises()

    const notice = wrapper.find('[data-testid="history-key-notice"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text().toLowerCase()).toContain('encryption key not configured')
  })
})
