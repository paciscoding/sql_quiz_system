<script setup lang="ts">
withDefaults(defineProps<{
  title: string
  body?: string
  compact?: boolean
}>(), {
  compact: false,
})
</script>

<template>
  <div class="empty-state" :class="{ 'empty-state--compact': compact }" role="status">
    <h3 class="empty-state__title">{{ title }}</h3>
    <p v-if="body" class="empty-state__body">{{ body }}</p>
    <div v-if="$slots.default" class="empty-state__actions"><slot /></div>
  </div>
</template>

<style scoped>
.empty-state {
  border: 1px dashed var(--color-border);
  padding: var(--space-6) var(--space-5);
  text-align: center;
  background: var(--color-surface-muted);
  color: var(--color-text);
  border-radius: var(--radius);
}
.empty-state--compact {
  padding: var(--space-4);
}
.empty-state__title {
  margin: 0 0 var(--space-2) 0;
  font-weight: 700;
  font-size: 1rem;
  color: var(--color-text);
}
.empty-state__body {
  margin: 0;
  color: var(--color-text-muted);
  font-size: 0.88rem;
}
.empty-state__actions {
  margin-top: var(--space-4);
  display: flex;
  gap: var(--space-2);
  justify-content: center;
  flex-wrap: wrap;
}
</style>
