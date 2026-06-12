import { onBeforeUnmount, watch } from 'vue'
import type { Ref } from 'vue'

const FOCUSABLE_SELECTOR = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'

export function useFocusTrap(containerRef: Ref<HTMLElement | null>, active: Ref<boolean>): void {
  let previousActiveElement: Element | null = null
  let keydownHandler: ((e: KeyboardEvent) => void) | null = null

  const deactivate = () => {
    if (keydownHandler) {
      window.removeEventListener('keydown', keydownHandler)
      keydownHandler = null
    }

    ;(previousActiveElement as HTMLElement | null)?.focus()
    previousActiveElement = null
  }

  const activate = () => {
    deactivate()
    previousActiveElement = document.activeElement

    const container = containerRef.value
    if (!container) return

    const focusable = Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR)) as HTMLElement[]
    if (focusable.length === 0) return

    const first = focusable[0]
    const last = focusable[focusable.length - 1]

    keydownHandler = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return

      if (e.shiftKey) {
        if (document.activeElement === first) {
          e.preventDefault()
          last.focus()
        }
        return
      }

      if (document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }

    window.addEventListener('keydown', keydownHandler)
  }

  watch(active, (val) => {
    if (val) activate()
    else deactivate()
  })

  onBeforeUnmount(() => {
    deactivate()
  })
}
