<script lang="ts">
// Module-scoped (NOT inside <script setup>'s per-instance setup()) so this
// counter is evaluated once per module load and persists across every
// CollapsibleCard instance, guaranteeing distinct ids. A counter declared in
// <script setup> is re-initialised to 0 on each component instantiation,
// which previously caused every card to render the same
// `collapsible-card-body-1` id -- see ARCH-034 (1) / TODO-078.
let uidCounter = 0
</script>

<script setup lang="ts">
import { ref } from 'vue'
import Icon from './Icon.vue'

/**
 * Shared disclosure primitive (ARCH-034 (1)): only the collapsible chrome is
 * extracted here -- a11y wiring (aria-expanded/aria-controls, button
 * semantics, chevron) is the part that rots when duplicated. Each consumer
 * (PracticeAuthoring.vue, ExamQuestionsPanel.vue) keeps its own category-card
 * body inside the default slot; this component owns no category-specific
 * logic.
 */
const props = withDefaults(defineProps<{
  title: string
  subtitle?: string
  defaultOpen?: boolean
  testId?: string
}>(), {
  subtitle: undefined,
  defaultOpen: false,
  testId: undefined,
})

const bodyId = `collapsible-card-body-${++uidCounter}`

const isOpen = ref(props.defaultOpen)

function toggle() {
  isOpen.value = !isOpen.value
}
</script>

<template>
  <div class="collapsible-card" :data-testid="testId">
    <div class="collapsible-card__header">
      <button
        type="button"
        class="collapsible-card__toggle"
        :aria-expanded="isOpen"
        :aria-controls="bodyId"
        @click="toggle"
      >
        <Icon :name="isOpen ? 'chevron-up' : 'chevron-down'" class="collapsible-card__chevron" />
        <span class="collapsible-card__titles">
          <span class="collapsible-card__title">{{ title }}</span>
          <span v-if="subtitle" class="collapsible-card__subtitle">{{ subtitle }}</span>
        </span>
        <span class="collapsible-card__meta"><slot name="header-meta" /></span>
      </button>
      <span class="collapsible-card__actions"><slot name="header-actions" /></span>
    </div>
    <div :id="bodyId" v-show="isOpen" class="collapsible-card__body">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.collapsible-card {
  border: 2px solid var(--color-border);
  border-left: 4px solid var(--color-primary);
  background: var(--color-surface);
  border-radius: var(--radius);
  margin-bottom: var(--space-4);
}
.collapsible-card__header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
}
.collapsible-card__toggle {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 1;
  min-width: 0;
  background: transparent;
  border: none;
  padding: 0;
  margin: 0;
  font: inherit;
  color: var(--color-text);
  cursor: pointer;
  text-align: left;
}
.collapsible-card__toggle:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}
.collapsible-card__chevron {
  flex: none;
}
.collapsible-card__titles {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.collapsible-card__title {
  font-weight: 700;
  font-size: 0.98rem;
}
.collapsible-card__subtitle {
  color: var(--color-text-muted);
  font-size: 0.85rem;
}
.collapsible-card__meta {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.collapsible-card__meta:empty {
  display: none;
}
.collapsible-card__actions {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  flex: none;
  flex-wrap: wrap;
}
.collapsible-card__body {
  padding: 0 var(--space-4) var(--space-4) var(--space-4);
}
</style>
