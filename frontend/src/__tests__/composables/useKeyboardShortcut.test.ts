import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { useKeyboardShortcut } from '../../composables/useKeyboardShortcut'

const mountWithSetup = (fn: () => void) => mount(defineComponent({ setup: fn, template: '<div />' }))

function dispatchKeydown(target: EventTarget, options: KeyboardEventInit) {
  target.dispatchEvent(
    new KeyboardEvent('keydown', {
      bubbles: true,
      cancelable: true,
      ...options,
    })
  )
}

describe('useKeyboardShortcut', () => {
  it('calls the handler when combo matches', () => {
    const handler = vi.fn()
    const wrapper = mountWithSetup(() => {
      useKeyboardShortcut({ combo: 'Ctrl+Enter', handler })
    })

    dispatchKeydown(window, { key: 'Enter', ctrlKey: true })

    expect(handler).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not call the handler from an input when whenTyping is false by default', () => {
    const handler = vi.fn()
    const input = document.createElement('input')
    document.body.appendChild(input)
    const wrapper = mountWithSetup(() => {
      useKeyboardShortcut({ combo: 'Ctrl+Enter', handler })
    })

    dispatchKeydown(input, { key: 'Enter', ctrlKey: true })

    expect(handler).not.toHaveBeenCalled()
    wrapper.unmount()
    input.remove()
  })

  it('calls the handler from an input when whenTyping is true', () => {
    const handler = vi.fn()
    const input = document.createElement('input')
    document.body.appendChild(input)
    const wrapper = mountWithSetup(() => {
      useKeyboardShortcut({ combo: 'Ctrl+Enter', handler, whenTyping: true })
    })

    dispatchKeydown(input, { key: 'Enter', ctrlKey: true })

    expect(handler).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    input.remove()
  })

  it('supports Escape key binding', () => {
    const handler = vi.fn()
    const wrapper = mountWithSetup(() => {
      useKeyboardShortcut({ combo: 'Escape', handler })
    })

    dispatchKeydown(window, { key: 'Escape' })

    expect(handler).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not call the handler when the target is outside the scope', () => {
    const handler = vi.fn()
    const scopeEl = document.createElement('section')
    const insideButton = document.createElement('button')
    const outsideButton = document.createElement('button')
    scopeEl.appendChild(insideButton)
    document.body.append(scopeEl, outsideButton)

    const wrapper = mountWithSetup(() => {
      const scope = ref<HTMLElement | null>(scopeEl)
      useKeyboardShortcut({ combo: 'Ctrl+Enter', handler, scope })
    })

    dispatchKeydown(outsideButton, { key: 'Enter', ctrlKey: true })
    expect(handler).not.toHaveBeenCalled()

    dispatchKeydown(insideButton, { key: 'Enter', ctrlKey: true })
    expect(handler).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    scopeEl.remove()
    outsideButton.remove()
  })
})
