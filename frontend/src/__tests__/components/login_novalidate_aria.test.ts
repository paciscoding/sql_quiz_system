/**
 * Tests for TODO-083
 * Verifies LoginView has novalidate on the form and ARIA attributes on inputs.
 */
import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import { createRouter, createMemoryHistory } from 'vue-router'

// Mock all complex dependencies
vi.mock('../../services/auth', () => ({
  useAuth: () => ({
    login: vi.fn(),
  }),
}))
vi.mock('../../composables/useSingleFlight', () => ({
  useSingleFlight: (fn: any) => ({ inFlight: { value: false }, run: fn }),
}))
vi.mock('../../components/FieldError.vue', () => ({
  default: { template: '<span />' },
}))
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
      { path: '/dashboard', component: { template: '<div />' } },
    ],
  })
}

describe('LoginView novalidate + ARIA (TODO-083)', () => {
  it('form element has novalidate attribute', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const form = wrapper.find('form')
    expect(form.exists()).toBe(true)
    expect(form.attributes('novalidate')).toBeDefined()

    wrapper.unmount()
  })

  it('username input has aria-invalid attribute', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const usernameInput = wrapper.find('input[autocomplete="username"]')
    expect(usernameInput.exists()).toBe(true)
    // aria-invalid is bound as a boolean attr — present in DOM (may be "false" or absent)
    // The key is the binding exists in the template
    const ariaInvalid = usernameInput.attributes('aria-invalid')
    // When untouched, should be "false" (falsy binding)
    expect(ariaInvalid).toBeDefined()

    wrapper.unmount()
  })

  it('username input has aria-describedby pointing to error element', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const usernameInput = wrapper.find('input[autocomplete="username"]')
    expect(usernameInput.attributes('aria-describedby')).toBe('err-username')

    wrapper.unmount()
  })

  it('password input has aria-describedby pointing to error element', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const passwordInput = wrapper.find('input[type="password"]')
    expect(passwordInput.exists()).toBe(true)
    expect(passwordInput.attributes('aria-describedby')).toBe('err-password')

    wrapper.unmount()
  })

  it('labels are programmatically associated with inputs via for/id', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const labels = wrapper.findAll('label')
    const withFor = labels.filter((l) => l.attributes('for'))
    // Both the username and password labels must have a `for` attribute
    expect(withFor.length).toBeGreaterThanOrEqual(2)

    for (const label of withFor) {
      const forId = label.attributes('for')!
      const target = wrapper.find(`#${forId}`)
      expect(target.exists()).toBe(true)
      expect(target.element.tagName.toLowerCase()).toBe('input')
    }

    // Explicit assertions for the two specific inputs
    const usernameInput = wrapper.find('input[autocomplete="username"]')
    const passwordInput = wrapper.find('input[type="password"]')
    const usernameId = usernameInput.attributes('id')
    const passwordId = passwordInput.attributes('id')
    expect(usernameId).toBeTruthy()
    expect(passwordId).toBeTruthy()
    const usernameLabel = wrapper.find(`label[for="${usernameId}"]`)
    const passwordLabel = wrapper.find(`label[for="${passwordId}"]`)
    expect(usernameLabel.exists()).toBe(true)
    expect(passwordLabel.exists()).toBe(true)

    wrapper.unmount()
  })

  it('login error is announced via role="alert"', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false })
    vi.stubGlobal('fetch', fetchMock)

    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    await wrapper.find('input[autocomplete="username"]').setValue('user')
    await wrapper.find('input[type="password"]').setValue('pass')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    const alert = wrapper.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('Invalid')

    vi.unstubAllGlobals()
    wrapper.unmount()
  })

  it('password show/hide toggle switches input type', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const pw = () => wrapper.find('#login-password')
    expect(pw().attributes('type')).toBe('password')

    await wrapper.find('.pw-toggle').trigger('click')
    await flushPromises()
    expect(pw().attributes('type')).toBe('text')

    await wrapper.find('.pw-toggle').trigger('click')
    await flushPromises()
    expect(pw().attributes('type')).toBe('password')

    wrapper.unmount()
  })

  it('both field labels show a visible required marker', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    expect(wrapper.find('label[for="login-username"]').text()).toContain('*')
    expect(wrapper.find('label[for="login-password"]').text()).toContain('*')

    wrapper.unmount()
  })

  it('shows a "Forgot password?" link pointing to /forgot-password', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    const link = wrapper.find('[data-testid="forgot-password-link"]')
    expect(link.exists()).toBe(true)
    expect(link.text().toLowerCase()).toContain('forgot password')
    // RouterLink renders an <a href="/forgot-password">
    expect(link.attributes('href')).toBe('/forgot-password')

    wrapper.unmount()
  })

  it('renders without errors', async () => {
    const router = makeRouter()
    await router.push('/login')
    const LoginView = (await import('../../components/LoginView.vue')).default
    const wrapper = mount(LoginView, {
      global: { plugins: [router] },
    })
    await flushPromises()

    expect(wrapper.exists()).toBe(true)
    wrapper.unmount()
  })
})
