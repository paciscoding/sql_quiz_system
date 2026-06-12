<script setup lang="ts">
withDefaults(defineProps<{
  size?: 'sm' | 'md' | 'lg'
  inline?: boolean
  label?: string
  invert?: boolean
}>(), {
  size: 'md',
  inline: false,
  label: 'Loading',
  invert: false,
})
</script>

<template>
  <span
    class="spinner"
    :class="['spinner--' + size, { 'spinner--inline': inline, 'spinner--invert': invert }]"
    role="status"
    :aria-label="label"
  >
    <span class="sr-only">{{ label }}</span>
  </span>
</template>

<style scoped>
.spinner {
  display: inline-block;
  border: 2px solid var(--color-border);
  border-top-color: transparent;
  border-radius: 0;
  animation: spinner-rot 0.8s linear infinite;
  box-sizing: border-box;
}
.spinner--inline {
  vertical-align: -2px;
  margin-right: 0.4rem;
}
.spinner--sm {
  width: 12px;
  height: 12px;
  border-width: 2px;
}
.spinner--md {
  width: 20px;
  height: 20px;
  border-width: 2px;
}
.spinner--lg {
  width: 36px;
  height: 36px;
  border-width: 3px;
}
.spinner--invert {
  border-color: var(--color-surface);
  border-top-color: transparent;
}
@keyframes spinner-rot {
  to { transform: rotate(360deg); }
}
@media (prefers-reduced-motion: reduce) {
  .spinner {
    animation: none;
    border-top-color: var(--color-border);
  }
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
</style>
