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
