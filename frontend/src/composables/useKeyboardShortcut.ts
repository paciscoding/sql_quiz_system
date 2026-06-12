import { onBeforeUnmount, onMounted, type Ref } from 'vue'

export interface ShortcutBinding {
  combo: string
  handler: (e: KeyboardEvent) => void
  whenTyping?: boolean
  scope?: Ref<HTMLElement | null>
}

interface ParsedShortcut {
  key: string
  ctrl: boolean
  alt: boolean
  shift: boolean
  meta: boolean
}

function isMacPlatform(): boolean {
  return typeof navigator !== 'undefined' && navigator.platform.includes('Mac')
}

function parseCombo(combo: string): ParsedShortcut {
  const parts = combo.split('+').map((part) => part.trim()).filter(Boolean)
  const key = parts.pop() ?? ''
  const parsed: ParsedShortcut = {
    key,
    ctrl: false,
    alt: false,
    shift: false,
    meta: false,
  }

  for (const modifier of parts) {
    const normalized = modifier.toLowerCase()

    if (normalized === 'ctrl' || normalized === 'control') {
      parsed.ctrl = true
    } else if (normalized === 'alt' || normalized === 'option') {
      parsed.alt = true
    } else if (normalized === 'shift') {
      parsed.shift = true
    } else if (normalized === 'meta' || normalized === 'cmd' || normalized === 'command') {
      parsed.meta = true
    } else if (normalized === 'mod') {
      if (isMacPlatform()) {
        parsed.meta = true
      } else {
        parsed.ctrl = true
      }
    }
  }

  return parsed
}

function keyMatches(eventKey: string, shortcutKey: string): boolean {
  return eventKey.toLowerCase() === shortcutKey.toLowerCase()
}

function modifiersMatch(event: KeyboardEvent, shortcut: ParsedShortcut): boolean {
  return (
    event.ctrlKey === shortcut.ctrl &&
    event.altKey === shortcut.alt &&
    event.shiftKey === shortcut.shift &&
    event.metaKey === shortcut.meta
  )
}

function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false

  if (target.closest('.cm-editor')) return true

  let current: Element | null = target
  while (current) {
    const tagName = current.tagName.toLowerCase()
    const contentEditable = current.getAttribute('contenteditable')

    if (tagName === 'input' || tagName === 'textarea') return true
    if (contentEditable != null && contentEditable.toLowerCase() !== 'false') return true

    current = current.parentElement
  }

  return false
}

function isInsideScope(target: EventTarget | null, scope: Ref<HTMLElement | null> | undefined): boolean {
  if (!scope) return true
  if (!scope.value || !(target instanceof Node)) return false

  return scope.value.contains(target)
}

function matchesBinding(event: KeyboardEvent, binding: ShortcutBinding): boolean {
  if (!isInsideScope(event.target, binding.scope)) return false
  if (!binding.whenTyping && isTypingTarget(event.target)) return false

  const parsed = parseCombo(binding.combo)
  return modifiersMatch(event, parsed) && keyMatches(event.key, parsed.key)
}

export function useKeyboardShortcut(bindings: ShortcutBinding | ShortcutBinding[]): void {
  const bindingList = Array.isArray(bindings) ? bindings : [bindings]

  function onKeydown(event: KeyboardEvent) {
    for (const binding of bindingList) {
      if (!matchesBinding(event, binding)) continue

      event.preventDefault()
      binding.handler(event)
    }
  }

  onMounted(() => {
    window.addEventListener('keydown', onKeydown)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('keydown', onKeydown)
  })
}
