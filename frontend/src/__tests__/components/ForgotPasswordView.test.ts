/**
 * Component tests for ForgotPasswordView.vue — forgot-password OTP wizard
 * (spec: docs/superpowers/specs/2026-07-01-auth-password-management-design.md).
 *
 * The forgot-password flow must NEVER reveal whether an account exists:
 * - Step 1 (request) always advances to the code-entry step with the same
 *   generic message, whether the request resolved or rejected.
 * - Step 2 (verify) shows one generic error for every failure cause.
 *
 * The network boundary (fetch) is mocked so no backend is required.
 */
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, describe, it, expect, vi } from 'vitest'
import { createRouter, createMemoryHistory } from 'vue-router'

vi.mock('../../components/Spinner.vue', () => ({
  default: { template: '<span />' },
}))

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/login', component: { template: '<div />' } },
      { path: '/forgot-password', component: { template: '<div />' } },
    ],
  })
}

function jsonResponse(data: unknown, ok = true) {
  return Promise.resolve({
    ok,
    json: () => Promise.resolve(data),
  }) as unknown as Promise<Response>
}

async function mountView() {
  const router = makeRouter()
  await router.push('/forgot-password')
  const ForgotPasswordView = (await import('../../components/ForgotPasswordView.vue')).default
  const wrapper = mount(ForgotPasswordView, { global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

/** Fill the username and submit step 1. */
async function submitUsername(wrapper: Awaited<ReturnType<typeof mountView>>, username: string) {
  await wrapper.find('[data-testid="forgot-username"]').setValue(username)
  await wrapper.find('[data-testid="forgot-password-form"]').trigger('submit')
  await flushPromises()
}

/** Fill the OTP and submit step 2. */
async function submitOtp(wrapper: Awaited<ReturnType<typeof mountView>>, otp: string) {
  await wrapper.find('[data-testid="forgot-otp"]').setValue(otp)
  await wrapper.find('[data-testid="forgot-otp-form"]').trigger('submit')
  await flushPromises()
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ForgotPasswordView', () => {
  describe('step 1 — request a verification code', () => {
    it('submits the username to POST /api/v1/forgot-password, advances to the code step, and renders the returned generic message', async () => {
      let postUrl = ''
      let postBody: any = null
      const fetchMock = vi.fn((url: string, opts?: RequestInit) => {
        postUrl = String(url)
        postBody = opts?.body ? JSON.parse(String(opts.body)) : null
        return jsonResponse({
          message: 'If an account with that username exists and has an email on file, a verification code has been sent.',
        })
      })
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, 'alice')

      expect(postUrl).toContain('/api/v1/forgot-password')
      expect(postBody).toEqual({ username: 'alice' })

      // Advanced to the code-entry step with the generic message.
      const msg = wrapper.find('[data-testid="forgot-message"]')
      expect(msg.exists()).toBe(true)
      expect(msg.text().toLowerCase()).toContain('verification code has been sent')
      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="forgot-password-form"]').exists()).toBe(false)
    })

    it('advances to the code step with the generic message even when the request fails (never reveals existence)', async () => {
      // Any failure — error response or network rejection — must look identical
      // to success from the UI.
      const fetchMock = vi.fn(() => Promise.reject(new Error('network down')))
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, 'does-not-exist')

      const msg = wrapper.find('[data-testid="forgot-message"]')
      expect(msg.exists()).toBe(true)
      expect(msg.text().toLowerCase()).toContain('if an account')
      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(true)
      // No branch that reveals success/failure — no error alert is shown.
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    })

    it('advances to the code step on a non-OK backend response too', async () => {
      const fetchMock = vi.fn(() => jsonResponse({ error: 'boom' }, false))
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, 'whoever')

      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(true)
      const msg = wrapper.find('[data-testid="forgot-message"]')
      expect(msg.text().toLowerCase()).toContain('if an account')
      expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    })
  })

  describe('step 2 — verify the code', () => {
    it('posts trimmed username + entered code to /api/v1/forgot-password/verify and shows the done state on success', async () => {
      const calls: Array<{ url: string; body: any }> = []
      const fetchMock = vi.fn((url: string, opts?: RequestInit) => {
        calls.push({ url: String(url), body: opts?.body ? JSON.parse(String(opts.body)) : null })
        if (String(url).includes('/verify')) {
          return jsonResponse({ message: 'Verification successful. A new password has been sent to your email.' })
        }
        return jsonResponse({ message: 'If an account with that username exists and has an email on file, a verification code has been sent.' })
      })
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, '  alice  ')
      await submitOtp(wrapper, '123456')

      const verifyCall = calls.find((c) => c.url.includes('/api/v1/forgot-password/verify'))
      expect(verifyCall).toBeTruthy()
      expect(verifyCall!.body).toEqual({ username: 'alice', otp: '123456' })

      // Done state: success message + back-to-login.
      const success = wrapper.find('[data-testid="forgot-success"]')
      expect(success.exists()).toBe(true)
      expect(success.text().toLowerCase()).toContain('new password has been sent')
      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="back-to-login"]').exists()).toBe(true)
    })

    it('shows the generic OTP error and stays on step 2 when verification fails', async () => {
      const fetchMock = vi.fn((url: string) => {
        if (String(url).includes('/verify')) {
          // Backend collapses every cause into one generic 400.
          return jsonResponse({ error: 'invalid or expired code' }, false)
        }
        return jsonResponse({ message: 'code sent' })
      })
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, 'alice')
      await submitOtp(wrapper, '000000')

      const err = wrapper.find('[data-testid="forgot-otp-error"]')
      expect(err.exists()).toBe(true)
      expect(err.attributes('role')).toBe('alert')
      // Generic text only — never anything cause-specific.
      expect(err.text()).toContain('Invalid or expired code')
      expect(err.text().toLowerCase()).not.toContain('attempts')
      expect(err.text().toLowerCase()).not.toContain('username')
      // Still on step 2 — the code input remains available.
      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="forgot-success"]').exists()).toBe(false)
    })

    it('"Request a new code" returns to step 1 with the username retained', async () => {
      const fetchMock = vi.fn(() => jsonResponse({ message: 'code sent' }))
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, 'alice')
      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(true)

      await wrapper.find('[data-testid="forgot-request-new-code"]').trigger('click')
      await flushPromises()

      const usernameInput = wrapper.find('[data-testid="forgot-username"]')
      expect(usernameInput.exists()).toBe(true)
      expect((usernameInput.element as HTMLInputElement).value).toBe('alice')
      expect(wrapper.find('[data-testid="forgot-otp"]').exists()).toBe(false)
    })

    it('renders an accessible 6-digit code input', async () => {
      const fetchMock = vi.fn(() => jsonResponse({ message: 'code sent' }))
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = await mountView()
      await submitUsername(wrapper, 'alice')

      const otpInput = wrapper.find('[data-testid="forgot-otp"]')
      expect(otpInput.attributes('inputmode')).toBe('numeric')
      expect(otpInput.attributes('autocomplete')).toBe('one-time-code')
      expect(otpInput.attributes('maxlength')).toBe('6')
      const label = wrapper.find(`label[for="${otpInput.attributes('id')}"]`)
      expect(label.exists()).toBe(true)
      expect(label.text()).toBe('Verification code')
    })
  })

  it('has a "Back to login" link', async () => {
    const wrapper = await mountView()
    const back = wrapper.find('[data-testid="back-to-login"]')
    expect(back.exists()).toBe(true)
    expect(back.attributes('href')).toBe('/login')
  })
})
