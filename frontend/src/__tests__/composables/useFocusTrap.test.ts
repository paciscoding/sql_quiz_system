import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { defineComponent, nextTick, ref } from 'vue'
import { useFocusTrap } from '../../composables/useFocusTrap'

const mountWithSetup = (fn: () => void) =>
  mount(
    defineComponent({
      setup: fn,
      template: '<div />',
    })
  )

describe('useFocusTrap', () => {
  it('exports useFocusTrap function', () => {
    expect(typeof useFocusTrap).toBe('function')
  })

  it('does not throw when active is false', () => {
    expect(() => {
      const wrapper = mountWithSetup(() => {
        const containerRef = ref<HTMLElement | null>(null)
        const active = ref(false)

        useFocusTrap(containerRef, active)
      })

      wrapper.unmount()
    }).not.toThrow()
  })

  it('does not throw when active is true but containerRef is null', () => {
    expect(() => {
      const wrapper = mountWithSetup(() => {
        const containerRef = ref<HTMLElement | null>(null)
        const active = ref(true)

        useFocusTrap(containerRef, active)
      })

      wrapper.unmount()
    }).not.toThrow()
  })

  it('Tab on last focusable element cycles to first', async () => {
    const container = document.createElement('div')
    const buttons = ['A', 'B', 'C'].map(label => {
      const btn = document.createElement('button')
      btn.textContent = label
      container.appendChild(btn)
      return btn
    })
    document.body.appendChild(container)

    const containerRef = ref<HTMLElement | null>(container)
    const active = ref(false)
    const wrapper = mountWithSetup(() => {
      useFocusTrap(containerRef, active)
    })

    active.value = true
    await nextTick()

    buttons[2].focus()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))

    expect(document.activeElement).toBe(buttons[0])

    wrapper.unmount()
    document.body.removeChild(container)
  })

  it('Shift+Tab on first focusable element cycles to last', async () => {
    const container = document.createElement('div')
    const buttons = ['A', 'B', 'C'].map(label => {
      const btn = document.createElement('button')
      btn.textContent = label
      container.appendChild(btn)
      return btn
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
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))

    expect(document.activeElement).toBe(buttons[2])

    wrapper.unmount()
    document.body.removeChild(container)
  })

  it('focus is restored to previousActiveElement on deactivation', async () => {
    const outsideBtn = document.createElement('button')
    outsideBtn.textContent = 'outside'
    document.body.appendChild(outsideBtn)

    const container = document.createElement('div')
    const innerBtn = document.createElement('button')
    innerBtn.textContent = 'inner'
    container.appendChild(innerBtn)
    document.body.appendChild(container)

    outsideBtn.focus()

    const containerRef = ref<HTMLElement | null>(container)
    const active = ref(false)
    const wrapper = mountWithSetup(() => {
      useFocusTrap(containerRef, active)
    })

    active.value = true
    await nextTick()

    innerBtn.focus()

    active.value = false
    await nextTick()

    expect(document.activeElement).toBe(outsideBtn)

    wrapper.unmount()
    document.body.removeChild(outsideBtn)
    document.body.removeChild(container)
  })
})
