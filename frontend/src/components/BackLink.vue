<script setup lang="ts">
withDefaults(defineProps<{
  to: string
  label?: string
  ariaLabel?: string
}>(), {
  label: 'Back',
})
</script>

<template>
  <router-link :to="to" :aria-label="ariaLabel || label || 'Back'" class="back-link">
    <span aria-hidden="true">&larr;</span> {{ label || 'Back' }}
  </router-link>
</template>

<style scoped>
.back-link {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-family: var(--font-mono);
  font-weight: 700;
  font-size: 0.85rem;
  color: var(--color-text);
  text-decoration: none;
  padding: 4px 8px;
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  margin-bottom: var(--space-3);
}
.back-link:hover {
  background: var(--color-surface-muted);
}
.back-link:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}
</style>
