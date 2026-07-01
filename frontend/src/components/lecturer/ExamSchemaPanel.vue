<script setup lang="ts">
import { ref } from 'vue'
import type { Examination } from './types'
import { importExamCurriculum } from '../../services/exam'
import { ApiError } from '../../services/practice'
import { handleApiError } from '../../services/error_handler'
import { useNotifications } from '../../services/notification_service'
import { useConfirm } from '../../services/confirm_service'
import { useSingleFlight } from '../../composables/useSingleFlight'
import { UI_COPY } from '../../constants/ui_copy'
import SqlEditor from '../SqlEditor.vue'
import Spinner from '../Spinner.vue'

const props = defineProps<{ exam: Examination }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

const importSQL = ref('')

const { inFlight: loading, run: doImport } = useSingleFlight(async () => {
  if (!importSQL.value.trim()) return

  try {
    await importExamCurriculum(props.exam.id, importSQL.value, false)
    notify('Curriculum imported', 'success')
    importSQL.value = ''
    emit('changed')
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      // If the server message indicates the exam is active, do not offer
      // a replace — retrying with confirm_replace would still 409.
      if (err.message.toLowerCase().includes('active')) {
        handleApiError(err, { fallback: 'Cannot import curriculum for an active exam' })
        return
      }
      // Schema already exists but exam is not active — offer replace.
      const ok = await confirm({
        title: 'Replace existing curriculum?',
        body: UI_COPY.REPLACE_EXAM_SCHEMA_CONFIRM,
        confirmLabel: 'Replace',
        danger: true,
      })
      if (!ok) return
      try {
        await importExamCurriculum(props.exam.id, importSQL.value, true)
        notify('Curriculum replaced', 'success')
        importSQL.value = ''
        emit('changed')
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to import curriculum' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to import curriculum' })
  }
})
</script>

<template>
  <div class="lecturer-form card">
    <div>
      <h2>Initialize Exam Curriculum</h2>
      <p class="desc">
        Paste the SQL schema script for this exam (e.g. company.sql) to create the per-exam
        tables and seed data. Legacy exams start with an empty curriculum — import here to
        set one up.
      </p>
    </div>
    <SqlEditor v-model="importSQL" height="300px" style="margin-bottom: 1rem;" />
    <button
      class="secondary-btn"
      :disabled="loading || !importSQL.trim()"
      @click="doImport"
    >
      <Spinner v-if="loading" size="sm" :inline="true" />
      {{ loading ? 'Importing...' : UI_COPY.IMPORT_EXAM_SCHEMA }}
    </button>
  </div>
</template>

<style scoped>
.card {
  background: var(--color-surface);
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-1);
  margin-bottom: var(--space-4);
}

.lecturer-form {
  background: var(--color-surface);
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.full-width {
  grid-column: span 2;
}

.desc {
  color: var(--color-text-muted);
  margin-bottom: var(--space-3);
  font-size: 0.88rem;
}

.secondary-btn {
  padding: var(--space-2) var(--space-4);
  background-color: var(--color-surface);
  color: var(--color-text);
  border: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  margin-bottom: var(--space-4);
  transition: none;
}

.secondary-btn:hover {
  background-color: var(--color-surface-muted);
}

.secondary-btn:active {
  transform: translateY(1px);
}

.secondary-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
