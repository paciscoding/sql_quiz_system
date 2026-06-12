/**
 * Tests for TODO-065, TODO-066, TODO-084, TODO-085
 * Proves that Ctrl+Enter shortcuts fire ONLY when focus is inside the scope ref.
 */
import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { useKeyboardShortcut } from '../composables/useKeyboardShortcut'

const mountWithSetup = (fn: () => void) =>
  mount(defineComponent({ setup: fn, template: '<div />' }))

function fire(target: EventTarget, key: string, opts: KeyboardEventInit = {}) {
  target.dispatchEvent(
    new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...opts })
  )
}

describe('useKeyboardShortcut – scope ref restricts Ctrl+Enter', () => {
  it('fires when event target is the scope element itself', () => {
    const handler = vi.fn()
    const scopeEl = document.createElement('div')
    document.body.appendChild(scopeEl)

    const wrapper = mountWithSetup(() => {
      const scope = ref<HTMLElement | null>(scopeEl)
      useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, handler, scope })
    })

    fire(scopeEl, 'Enter', { ctrlKey: true })
    expect(handler).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    scopeEl.remove()
  })

  it('fires when event target is a child inside the scope', () => {
    const handler = vi.fn()
    const scopeEl = document.createElement('div')
    const textarea = document.createElement('textarea')
    scopeEl.appendChild(textarea)
    document.body.appendChild(scopeEl)

    const wrapper = mountWithSetup(() => {
      const scope = ref<HTMLElement | null>(scopeEl)
      useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, handler, scope })
    })

    fire(textarea, 'Enter', { ctrlKey: true })
    expect(handler).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    scopeEl.remove()
  })

  it('does NOT fire when event target is outside the scope', () => {
    const handler = vi.fn()
    const scopeEl = document.createElement('div')
    const outsideEl = document.createElement('textarea')
    document.body.appendChild(scopeEl)
    document.body.appendChild(outsideEl)

    const wrapper = mountWithSetup(() => {
      const scope = ref<HTMLElement | null>(scopeEl)
      useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, handler, scope })
    })

    fire(outsideEl, 'Enter', { ctrlKey: true })
    expect(handler).not.toHaveBeenCalled()

    wrapper.unmount()
    scopeEl.remove()
    outsideEl.remove()
  })

  it('does NOT fire when scope ref is null (guard against unset ref)', () => {
    const handler = vi.fn()

    const wrapper = mountWithSetup(() => {
      const scope = ref<HTMLElement | null>(null)
      useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, handler, scope })
    })

    fire(window, 'Enter', { ctrlKey: true })
    expect(handler).not.toHaveBeenCalled()

    wrapper.unmount()
  })

  it('fires without scope when combo is pressed anywhere (sanity check)', () => {
    const handler = vi.fn()
    const wrapper = mountWithSetup(() => {
      useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: false, handler })
    })

    fire(window, 'Enter', { ctrlKey: true })
    expect(handler).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  it('scope check: two independent bindings with different scopes stay isolated', () => {
    const handlerA = vi.fn()
    const handlerB = vi.fn()
    const scopeA = document.createElement('div')
    const scopeB = document.createElement('div')
    const btnA = document.createElement('button')
    const btnB = document.createElement('button')
    scopeA.appendChild(btnA)
    scopeB.appendChild(btnB)
    document.body.append(scopeA, scopeB)

    const wrapper = mountWithSetup(() => {
      const refA = ref<HTMLElement | null>(scopeA)
      const refB = ref<HTMLElement | null>(scopeB)
      useKeyboardShortcut([
        { combo: 'Ctrl+Enter', whenTyping: false, handler: handlerA, scope: refA },
        { combo: 'Ctrl+Enter', whenTyping: false, handler: handlerB, scope: refB },
      ])
    })

    fire(btnA, 'Enter', { ctrlKey: true })
    expect(handlerA).toHaveBeenCalledTimes(1)
    expect(handlerB).not.toHaveBeenCalled()

    fire(btnB, 'Enter', { ctrlKey: true })
    expect(handlerB).toHaveBeenCalledTimes(1)
    expect(handlerA).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    scopeA.remove()
    scopeB.remove()
  })
})
