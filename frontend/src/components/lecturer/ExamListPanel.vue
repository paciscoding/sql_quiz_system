<script setup lang="ts">
import { ref } from 'vue'
import type { Examination } from './types'
import ExamCard from './ExamCard.vue'
import Spinner from '../Spinner.vue'
import EmptyState from '../EmptyState.vue'
import { useSingleFlight } from '../../composables/useSingleFlight'
import { useNotifications } from '../../services/notification_service'
import { handleApiError, toApiError } from '../../services/error_handler'

defineProps<{ exams: Examination[] }>()

const emit = defineEmits<{
  open: [id: number]
  changed: []
}>()

const { notify } = useNotifications()

const newExamTitle = ref('')

const { inFlight: createExamLoading, run: createExam } = useSingleFlight(async () => {
  if (!newExamTitle.value) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/exams', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ title: newExamTitle.value }),
    })
    if (response.ok) {
      notify('Exam created successfully', 'success')
      newExamTitle.value = ''
      emit('changed')
    } else {
      throw await toApiError(response, 'Failed to create exam')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create exam' })
  }
})
</script>

<template>
  <div class="lecturer-form card">
    <h2>Examination Events</h2>

    <div class="form-grid">
      <div class="input-group">
        <label>New Exam Title:</label>
        <input
          type="text"
          v-model="newExamTitle"
          placeholder="Midterm 2026"
          aria-label="New exam title"
        />
      </div>
      <button
        @click="createExam"
        :disabled="createExamLoading || !newExamTitle"
        class="secondary-btn"
        style="margin-top: 1.8rem;"
      >
        <Spinner v-if="createExamLoading" size="sm" inline />{{ createExamLoading ? 'Creating' : 'Create Exam' }}
      </button>
    </div>

    <EmptyState
      v-if="exams.length === 0"
      :compact="true"
      title="No exams yet"
      body="Create your first exam above."
    />
    <div class="exam-list" v-if="exams.length > 0">
      <h3>Existing Exams</h3>
      <ExamCard
        v-for="e in exams"
        :exam="e"
        :key="e.id"
        @open="emit('open', e.id)"
        @changed="emit('changed')"
        @deleted="emit('changed')"
      />
    </div>
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

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-5);
}

.input-group {
  display: flex;
  flex-direction: column;
}

label {
  font-weight: 700;
  margin-bottom: 0.4rem;
  font-size: 0.95rem;
  color: var(--color-text);
}

input {
  padding: 0.6rem;
  border: 2px solid var(--color-border);
  border-radius: 0;
  font-weight: 600;
  font-family: var(--font-mono);
  background: var(--color-surface);
  color: var(--color-text);
}

.exam-list {
  margin-top: var(--space-4);
}

h2 {
  color: var(--color-text);
  font-weight: 700;
  margin: 0 0 var(--space-3) 0;
  font-size: 1.1rem;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}

h3 {
  margin: 0 0 var(--space-3) 0;
  font-size: 1rem;
  font-weight: 700;
  color: var(--color-text);
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
