<script setup lang="ts">
import { ref } from 'vue'
import type { Examination } from './types'
import ExamQuestionsPanel from './ExamQuestionsPanel.vue'
import ExamRosterPanel from './ExamRosterPanel.vue'
import Icon from '../Icon.vue'

defineProps<{ exam: Examination }>()

const emit = defineEmits<{
  back: []
  changed: []
}>()

// The Schema tab was removed from the tab bar: the exam-category management
// panel (inside Questions) already handles per-category schema import, so a
// separate exam-level Schema navigation was redundant. ExamSchemaPanel.vue is
// left on disk, unreferenced, in case the standalone panel is wanted again.
const activeTab = ref<'questions' | 'roster'>('questions')
</script>

<template>
  <div :key="exam.id">
    <div class="workspace-header">
      <h2>{{ exam.title }}</h2>
      <button class="btn-sm" @click="emit('back')"><Icon name="arrow-left" /> Back to examinations</button>
    </div>

    <div class="tab-bar">
      <button :class="{ 'tab-active': activeTab === 'questions' }" @click="activeTab = 'questions'">Questions</button>
      <button :class="{ 'tab-active': activeTab === 'roster' }" @click="activeTab = 'roster'">Roster</button>
    </div>

    <ExamQuestionsPanel v-if="activeTab === 'questions'" :exam="exam" @changed="emit('changed')" />
    <ExamRosterPanel v-if="activeTab === 'roster'" :exam="exam" @changed="emit('changed')" />
  </div>
</template>

<style scoped>
.workspace-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--space-4);
}

h2 {
  color: var(--color-text);
  font-weight: 700;
  margin: 0;
  font-size: 1.3rem;
}

.tab-bar {
  display: inline-flex;
  gap: 0;
  background: var(--color-surface-muted);
  padding: 3px;
  margin-bottom: var(--space-4);
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  flex-wrap: wrap;
}

.tab-bar button {
  background: transparent;
  border: 1px solid transparent;
  padding: 6px 16px;
  cursor: pointer;
  color: var(--color-text-muted);
  font-size: 0.86rem;
  font-weight: 600;
  border-radius: var(--radius-sm);
  flex: 0 0 auto;
}

.tab-bar button:hover {
  color: var(--color-text);
}

.tab-bar button.tab-active {
  background: var(--color-surface);
  color: var(--color-primary);
  border-color: var(--color-border);
  box-shadow: var(--shadow-1);
}

.btn-sm {
  padding: 4px 12px;
  font-size: 0.8rem;
  font-weight: 700;
  cursor: pointer;
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
}

.btn-sm:hover {
  background: var(--color-surface-muted);
}

.btn-sm:active {
  transform: translateY(1px);
}

@media (max-width: 767.98px) {
  .tab-bar {
    overflow-x: auto;
    flex-wrap: nowrap;
  }

  .tab-bar button {
    flex: 0 0 auto;
    min-width: 110px;
  }

  .workspace-header {
    flex-direction: column;
    align-items: flex-start;
    gap: var(--space-3);
  }
}
</style>
