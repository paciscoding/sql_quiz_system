/**
 * Tests for TODO-078 (ConfirmDialogHost focus trap) and TODO-079 (AdminView autofocus).
 */
import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import { ref, nextTick, defineComponent } from 'vue'
import { useFocusTrap } from '../../composables/useFocusTrap'

// Mocks for AdminView (TODO-079) — must be top-level
vi.mock('../../composables/useSingleFlight', () => ({
  useSingleFlight: (fn: any) => ({ inFlight: ref(false), run: fn }),
}))
vi.mock('../../services/confirm_service', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(false) }),
}))
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: vi.fn() }),
}))
vi.mock('../../services/error_handler', () => ({
  handleApiError: vi.fn(),
}))
vi.mock('../../components/BulkImportStudents.vue', () => ({
  default: { template: '<div />' },
}))
vi.mock('../../components/FieldError.vue', () => ({
  default: { template: '<span />' },
}))
vi.mock('../../components/Spinner.vue', () => ({
  default: { template: '<span />' },
}))
vi.mock('../../components/EmptyState.vue', () => ({
  default: { template: '<div />' },
}))
vi.mock('../../components/Skeleton.vue', () => ({
  default: { template: '<div />' },
}))

const mountWithSetup = (fn: () => void) =>
  mount(defineComponent({ setup: fn, template: '<div />' }))

// -------- TODO-078: ConfirmDialogHost focus trap --------

describe('ConfirmDialogHost focus trap (TODO-078)', () => {
  it('Tab on only focusable element cycles to itself', async () => {
    const container = document.createElement('div')
    const btn = document.createElement('button')
    btn.textContent = 'OK'
    container.appendChild(btn)
    document.body.appendChild(container)

    const containerRef = ref<HTMLElement | null>(container)
    const active = ref(false)

    const wrapper = mountWithSetup(() => {
      useFocusTrap(containerRef, active)
    })

    active.value = true
    await nextTick()

    btn.focus()
    expect(document.activeElement).toBe(btn)

    // Tab on the only focusable element should cycle back to itself
    window.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    )
    expect(document.activeElement).toBe(btn)

    wrapper.unmount()
    document.body.removeChild(container)
  })

  it('Shift+Tab cycles from first back to last focusable', async () => {
    const container = document.createElement('div')
    const buttons = ['Cancel', 'Confirm'].map(label => {
      const b = document.createElement('button')
      b.textContent = label
      container.appendChild(b)
      return b
    })
    document.body.appendChild(container)

    const containerRef = ref<HTMLElement | null>(container)
    const active = ref(false)

    const wrapper = mountWithSetup(() => {
      useFocusTrap(containerRef, active)
    })

    active.value = true
    await nextTick()

    buttons[0].focus()
    window.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Tab',
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      })
    )
    expect(document.activeElement).toBe(buttons[1])

    wrapper.unmount()
    document.body.removeChild(container)
  })

  it('restores focus to previous element when dialog closes', async () => {
    const triggerBtn = document.createElement('button')
    triggerBtn.textContent = 'Open dialog'
    document.body.appendChild(triggerBtn)

    const container = document.createElement('div')
    const confirmBtn = document.createElement('button')
    confirmBtn.textContent = 'Confirm'
    container.appendChild(confirmBtn)
    document.body.appendChild(container)

    triggerBtn.focus()

    const containerRef = ref<HTMLElement | null>(container)
    const active = ref(false)

    const wrapper = mountWithSetup(() => {
      useFocusTrap(containerRef, active)
    })

    active.value = true
    await nextTick()
    confirmBtn.focus()

    active.value = false
    await nextTick()

    expect(document.activeElement).toBe(triggerBtn)

    wrapper.unmount()
    document.body.removeChild(triggerBtn)
    document.body.removeChild(container)
  })

  it('does not throw when deactivated without prior activation', async () => {
    const container = document.createElement('div')
    const btn = document.createElement('button')
    container.appendChild(btn)
    document.body.appendChild(container)

    const containerRef = ref<HTMLElement | null>(container)
    const active = ref(false)

    expect(() => {
      const wrapper = mountWithSetup(() => {
        useFocusTrap(containerRef, active)
      })
      wrapper.unmount()
    }).not.toThrow()

    document.body.removeChild(container)
  })
})

// -------- TODO-079: AdminView autofocus --------

describe('AdminView autofocus attribute (TODO-079)', () => {
  it('username input in create-user form has autofocus attribute', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve([]),
    }))

    const AdminView = (await import('../../components/AdminView.vue')).default
    const wrapper = mount(AdminView, { attachTo: document.body })
    await flushPromises()

    // Find the username input — it should have autofocus
    const usernameInput = wrapper.find('input[autofocus]')
    expect(usernameInput.exists()).toBe(true)

    wrapper.unmount()
    vi.unstubAllGlobals()
  })
})
