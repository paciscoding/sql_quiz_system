<script setup lang="ts">
import { ref, onMounted } from 'vue'
import {
  lecturerListQuestions,
  upsertQuestion,
  deleteQuestion,
  resetSandbox,
  lecturerPreview,
  type PracticeQuestion
} from '../services/practice'
import SqlEditor from './SqlEditor.vue'
import ResultTable from './ResultTable.vue'
import { useNotifications } from '../services/notification_service'

const { notify } = useNotifications()

const questions = ref<PracticeQuestion[]>([])
const form = ref<Partial<PracticeQuestion>>({
  category: 'General',
  question_type: 'QUERY',
  points: 1.0,
  strict_ordering: false,
  strict_attrs: false
})
const previewRows = ref<any[]>([])
const previewCols = ref<string[]>([])
const lastResetAt = ref<string | null>(null)
const loading = ref(false)
const previewLoading = ref(false)
const previewError = ref('')

const loadQuestions = async () => {
  try {
    questions.value = await lecturerListQuestions()
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

const editQuestion = (q: PracticeQuestion) => {
  form.value = { ...q }
}

const resetForm = () => {
  form.value = {
    category: 'General',
    question_type: 'QUERY',
    points: 1.0,
    strict_ordering: false,
    strict_attrs: false
  }
  previewRows.value = []
  previewCols.value = []
  previewError.value = ''
}

const saveQuestion = async () => {
  loading.value = true
  try {
    const res = await upsertQuestion(form.value)
    if (res.expected_result_status === 'captured') {
      notify('Question saved and result captured', 'success')
    } else if (res.expected_result_status === 'failed') {
      notify(`Question saved, but capture failed: ${res.capture_error}`, 'error')
    } else {
      notify('Question saved', 'success')
    }
    await loadQuestions()
    resetForm()
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

const handleDelete = async (id: number) => {
  if (!window.confirm('Are you sure you want to delete this question?')) return
  try {
    await deleteQuestion(id)
    notify('Question deleted', 'success')
    await loadQuestions()
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

const handlePreview = async () => {
  previewLoading.value = true
  previewError.value = ''
  try {
    const res = await lecturerPreview({
      type: form.value.question_type ?? 'QUERY',
      target_table: form.value.target_table ?? '',
      correct_sql: form.value.correct_sql ?? ''
    })
    if (res.status === 'error') {
      previewError.value = res.error || 'SQL execution failed'
      previewRows.value = []
      previewCols.value = []
    } else {
      previewRows.value = res.rows || []
      previewCols.value = res.columns || []
    }
  } catch (err: any) {
    previewError.value = err.message
    notify(err.message, 'error')
  } finally {
    previewLoading.value = false
  }
}

const handleResetSandbox = async () => {
  if (!window.confirm('Reset sandbox? This drops and recreates sandbox_practice schema.')) return
  try {
    const res = await resetSandbox()
    lastResetAt.value = res.reset_at
    notify('Sandbox reset successfully', 'success')
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

onMounted(() => {
  loadQuestions()
})
</script>

<template>
  <div class="practice-authoring">
    <!-- Section A: Question List -->
    <div class="card" style="margin-bottom: var(--space-4);">
      <h2>Practice Questions</h2>
      <div v-if="questions.length === 0" class="muted">No questions yet.</div>
      <table v-else class="data-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Category</th>
            <th>Requirement</th>
            <th>Type</th>
            <th>Points</th>
            <th>Status</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="q in questions" :key="q.id">
            <td>{{ q.id }}</td>
            <td style="font-weight: 700; font-size: 0.8rem;">{{ q.category }}</td>
            <td>{{ q.requirement }}</td>
            <td>
              <span class="badge badge-info">{{ q.question_type }}</span>
            </td>
            <td>{{ q.points }}</td>
            <td>
              <span 
                class="badge" 
                :class="{
                  'badge-success': q.expected_result_status === 'captured',
                  'badge-warning': q.expected_result_status === 'pending',
                  'badge-danger': q.expected_result_status === 'failed'
                }"
              >
                {{ q.expected_result_status }}
              </span>
            </td>
            <td>
              <div style="display: flex; gap: var(--space-1);">
                <button class="btn btn-secondary btn-sm" @click="editQuestion(q)">Edit</button>
                <button class="btn btn-secondary btn-sm" @click="handleDelete(q.id)">Delete</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Section B: Create/Edit Form -->
    <div class="card" style="margin-bottom: var(--space-4);">
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: var(--space-4);">
        <h2>{{ form.id ? 'Edit' : 'Create' }} Question</h2>
        <button class="btn btn-secondary" @click="resetForm">Reset Form</button>
      </div>

      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4);">
        <div class="field">
          <label>Category</label>
          <input v-model="form.category" class="input" placeholder="e.g. Basic Query operation" />
        </div>
        <div class="field">
          <label>Question Type</label>
          <select v-model="form.question_type" class="input">
            <option value="QUERY">QUERY</option>
            <option value="DML">DML</option>
          </select>
        </div>
        <div class="field">
          <label>Target Table (DML only)</label>
          <input v-model="form.target_table" class="input" placeholder="e.g. employee" />
        </div>
        <div class="field" style="grid-column: span 2;">
          <label>Requirement</label>
          <textarea v-model="form.requirement" class="input" rows="3"></textarea>
        </div>
        <div class="field">
          <label>Points</label>
          <input type="number" v-model.number="form.points" class="input" step="0.1" />
        </div>
        <div class="field" style="display: flex; flex-direction: row; gap: var(--space-4); align-items: flex-end;">
          <label style="display: flex; align-items: center; gap: var(--space-1); cursor: pointer; font-weight: 700; margin-bottom: 0;">
            <input type="checkbox" v-model="form.strict_ordering" /> Strict Ordering
          </label>
          <label style="display: flex; align-items: center; gap: var(--space-1); cursor: pointer; font-weight: 700; margin-bottom: 0;">
            <input type="checkbox" v-model="form.strict_attrs" /> Strict Attrs
          </label>
        </div>
      </div>

      <div class="field" style="margin-top: var(--space-4);">
        <label>Correct SQL</label>
        <SqlEditor 
          :modelValue="form.correct_sql ?? ''" 
          @update:modelValue="form.correct_sql = $event" 
          height="200px" 
        />
      </div>

      <div style="display: flex; gap: var(--space-2); margin-top: var(--space-4);">
        <button 
          class="btn btn-primary" 
          @click="saveQuestion" 
          :disabled="loading"
        >
          {{ loading ? 'Saving...' : 'Save Question' }}
        </button>
        <button 
          class="btn btn-secondary" 
          @click="handlePreview" 
          :disabled="previewLoading"
        >
          {{ previewLoading ? 'Running...' : 'Preview' }}
        </button>
      </div>

      <!-- Preview Results -->
      <div v-if="previewError" class="badge badge-danger" style="margin-top: var(--space-4); display: block; padding: var(--space-2); font-family: monospace;">
        {{ previewError }}
      </div>
      <div v-if="previewRows.length > 0" style="margin-top: var(--space-4);">
        <h3>Preview Results</h3>
        <ResultTable :rows="previewRows" :columns="previewCols" />
      </div>
    </div>

    <!-- Section C: Reset Sandbox -->
    <div class="card">
      <h2>Reset Practice Sandbox</h2>
      <p class="muted">Drops and recreates sandbox_practice schema, re-cloning from public.*.</p>
      <div style="display: flex; align-items: center; gap: var(--space-4);">
        <button class="btn btn-secondary" @click="handleResetSandbox" style="border-color: var(--color-danger); color: var(--color-danger);">
          Reset Sandbox
        </button>
        <span v-if="lastResetAt" class="muted">Last reset: {{ lastResetAt }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.practice-authoring {
  max-width: 1000px;
  margin: 0 auto;
  padding: var(--space-4);
}
.card {
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-6);
  border-radius: 0;
}
h2 {
  margin-top: 0;
  margin-bottom: var(--space-4);
  font-size: 1.5rem;
  font-weight: 800;
  letter-spacing: -0.01em;
}
h3 {
  font-size: 1.1rem;
  margin-bottom: var(--space-2);
  font-weight: 800;
}
.data-table {
  border: 2px solid var(--color-border);
}
.data-table th, .data-table td {
  border: 1px solid var(--color-border);
  font-weight: 600;
}
.data-table th {
  background: var(--color-surface-muted);
  color: var(--color-text);
  font-weight: 700;
}
.input {
  border: 2px solid var(--color-border);
  border-radius: 0;
  padding: 0.6rem;
  font-family: var(--font-mono);
  font-weight: 600;
  background: var(--color-surface);
  color: var(--color-text);
}
.btn {
  border: 2px solid var(--color-border);
  font-weight: 700;
  transition: none;
}
.btn:active {
  transform: translateY(1px);
}
.btn-primary {
  background: var(--color-primary);
  color: var(--color-surface);
}
.btn-primary:hover:not(:disabled) {
  background: #333333;
}
.btn-secondary {
  background: var(--color-surface);
  color: var(--color-text);
}
.btn-secondary:hover:not(:disabled) {
  background: var(--color-surface-muted);
}
.btn-sm {
  padding: 4px 8px;
  font-size: 0.85rem;
}
.badge {
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-weight: 700;
}
</style>
