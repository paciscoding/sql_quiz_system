<script setup lang="ts">
// Displays an email masked by default (shoulder-surfing privacy) with an eye
// toggle to reveal and a copy button. Copy always copies the real address,
// regardless of masked/revealed state.
import { ref, computed } from 'vue'

const props = defineProps<{ value?: string | null }>()

const revealed = ref(false)
const copied = ref(false)

const hasValue = computed(() => !!props.value && props.value.trim().length > 0)

// Masked form: keep the first local-part char and the domain, dot out the rest —
// enough to recognise an address you already know without exposing it fully.
const masked = computed(() => {
  const v = (props.value ?? '').trim()
  const at = v.indexOf('@')
  if (at < 1) return '•'.repeat(Math.max(v.length, 3))
  const local = v.slice(0, at)
  const domain = v.slice(at)
  const head = local[0]
  return `${head}${'•'.repeat(Math.max(local.length - 1, 2))}${domain}`
})

const display = computed(() => (revealed.value ? (props.value ?? '') : masked.value))

async function copy() {
  if (!hasValue.value) return
  try {
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(props.value!.trim())
    } else {
      // Fallback for insecure contexts / older browsers.
      const ta = document.createElement('textarea')
      ta.value = props.value!.trim()
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    copied.value = true
    setTimeout(() => { copied.value = false }, 1500)
  } catch {
    // Clipboard blocked — leave state unchanged.
  }
}
</script>

<template>
  <span v-if="!hasValue" class="muted">—</span>
  <span v-else class="email-reveal">
    <code class="email-reveal__value" :title="revealed ? 'Email revealed' : 'Email hidden'">{{ display }}</code>
    <button
      type="button"
      class="email-reveal__btn"
      :aria-label="revealed ? 'Hide email' : 'Reveal email'"
      :title="revealed ? 'Hide' : 'Reveal'"
      @click="revealed = !revealed"
    >{{ revealed ? '🙈' : '👁' }}</button>
    <button
      type="button"
      class="email-reveal__btn"
      :aria-label="copied ? 'Copied' : 'Copy email'"
      :title="copied ? 'Copied' : 'Copy'"
      @click="copy"
    >{{ copied ? '✓' : '⧉' }}</button>
  </span>
</template>

<style scoped>
.email-reveal {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.email-reveal__value {
  font-size: 0.82rem;
  color: var(--color-text);
  white-space: nowrap;
}
.email-reveal__btn {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  padding: 1px 6px;
  font-size: 0.8rem;
  line-height: 1.3;
  cursor: pointer;
  transition: background 0.1s ease;
}
.email-reveal__btn:hover { background: var(--color-surface-muted); }
</style>
