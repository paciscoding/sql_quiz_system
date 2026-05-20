<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import SqlEditor from './SqlEditor.vue'
import ResultTable from './ResultTable.vue'
import { useNotifications } from '../services/notification_service'
import {
  listQuestions,
  submit,
  preview,
  listMyAttempts,
  PracticeUnavailableError,
  type PracticeQuestion,
  type PracticeAttempt,
  type ValidationResult
} from '../services/practice'

const { notify } = useNotifications()

// State
const questions = ref<PracticeQuestion[]>([])
const selectedQuestion = ref<PracticeQuestion | null>(null)
const sqlText = ref('')
const result = ref<ValidationResult | null>(null)
const previewRows = ref<Record<string, any>[]>([])
const previewCols = ref<string[]>([])
const attempts = ref<PracticeAttempt[]>([])
const practiceUnavailable = ref(false)
const loading = ref(false)
const previewLoading = ref(false)

// Initialization
const loadData = async () => {
  practiceUnavailable.value = false
  try {
    const [qs, atts] = await Promise.all([listQuestions(), listMyAttempts()])
    questions.value = qs
    attempts.value = atts
  } catch (err) {
    if (err instanceof PracticeUnavailableError) {
      practiceUnavailable.value = true
    } else {
      notify('Failed to load practice questions.', 'error')
    }
  }
}

onMounted(loadData)

// Reset state on question change
watch(selectedQuestion, () => {
  sqlText.value = ''
  result.value = null
  previewRows.value = []
  previewCols.value = []
})

const selectQuestion = (q: PracticeQuestion) => {
  selectedQuestion.value = q
}

const doSubmit = async () => {
  if (!selectedQuestion.value || !sqlText.value.trim()) return
  loading.value = true
  result.value = null
  try {
    const res = await submit(selectedQuestion.value.id, sqlText.value)
    result.value = res
    // Refresh attempts
    try {
      attempts.value = await listMyAttempts()
    } catch { /* ignore */ }
    
    if (res.status === 'correct') {
      notify('Correct!', 'success')
    } else if (res.status === 'syntax_error') {
      notify('Syntax error in your SQL.', 'error')
    } else {
      notify('Incorrect answer.', 'error')
    }
  } catch (err) {
    if (err instanceof PracticeUnavailableError) {
      practiceUnavailable.value = true
    } else {
      notify((err as Error).message || 'Submit failed.', 'error')
    }
  } finally {
    loading.value = false
  }
}

const doPreview = async () => {
  if (!selectedQuestion.value || !sqlText.value.trim()) return
  previewLoading.value = true
  previewRows.value = []
  previewCols.value = []
  try {
    const res = await preview(selectedQuestion.value.id, sqlText.value)
    previewRows.value = res.rows
    previewCols.value = res.columns
    if (res.status === 'correct') {
      notify('Preview: Correct!', 'success')
    } else if (res.status === 'syntax_error') {
      notify('Preview: Syntax error.', 'error')
    } else {
      notify('Preview: Incorrect.', 'error')
    }
  } catch (err) {
    if (err instanceof PracticeUnavailableError) {
      practiceUnavailable.value = true
    } else {
      notify((err as Error).message || 'Preview failed.', 'error')
    }
  } finally {
    previewLoading.value = false
  }
}

const badgeClassFor = (s: string) => s === 'correct' ? 'badge-success' : s === 'syntax_error' ? 'badge-danger' : 'badge-warning'
const statusLabel = (s: string) => s === 'correct' ? 'Passed' : s === 'syntax_error' ? 'SQL Error' : 'Incorrect'

const formatDate = (dateStr: string) => {
  return new Date(dateStr).toLocaleString()
}

const truncate = (str: string, len: number) => {
  if (str.length <= len) return str
  return str.substring(0, len) + '...'
}

const groupedQuestions = computed(() => {
  const groups: Record<string, PracticeQuestion[]> = {}
  questions.value.forEach(q => {
    if (!groups[q.category]) {
      groups[q.category] = []
    }
    if (groups[q.category].length < 5) {
      groups[q.category].push(q)
    }
  })
  return groups
})
</script>

<template>
  <div class="practice-shell">
    <!-- Unavailability banner -->
    <div v-if="practiceUnavailable" class="card unavailability-banner">
      <div style="display: flex; align-items: center; justify-content: space-between;">
        <p style="margin: 0; color: var(--color-danger); font-weight: 500;">
          Practice unavailable while an exam is active.
        </p>
        <button class="btn btn-secondary" @click="loadData">Retry</button>
      </div>
    </div>

    <div class="pane-grid">
      <!-- Question List -->
      <div class="card question-list-card">
        <h3 style="margin-top: 0;">Practice Categories</h3>
        <div class="question-list">
          <div v-for="(qs, cat) in groupedQuestions" :key="cat" class="category-group">
            <h4 class="category-title">{{ cat }}</h4>
            <div 
              v-for="q in qs" 
              :key="q.id" 
              class="question-item"
              :class="{ active: selectedQuestion?.id === q.id }"
              @click="selectQuestion(q)"
            >
              <div class="question-item-header">
                <span class="q-id">#{{ q.id }}</span>
                <span class="badge" :class="q.question_type === 'QUERY' ? 'badge-info' : 'badge-warning'">
                  {{ q.question_type }}
                </span>
                <span class="points">{{ q.points }} pts</span>
              </div>
              <div class="requirement-preview">{{ truncate(q.requirement, 60) }}</div>
            </div>
          </div>
          <p v-if="questions.length === 0" class="muted">No practice questions available.</p>
        </div>
      </div>

      <!-- Editor Section -->
      <div class="card editor-card">
        <div v-if="selectedQuestion">
          <div class="selected-header">
            <h3 style="margin: 0;">Question Details</h3>
            <span class="badge" :class="selectedQuestion.question_type === 'QUERY' ? 'badge-info' : 'badge-warning'">
              {{ selectedQuestion.question_type }}
            </span>

            <!-- points -->
            <div>
              <label class="muted small">Points</label>
              <p style="margin: 0;">{{ selectedQuestion.points }}</p>
            </div>
            <div v-if="selectedQuestion.target_table">
              <label class="muted small">Target Table</label>
              <p style="margin: 0;"><code>{{ selectedQuestion.target_table }}</code></p>
            </div>
          </div>
         
          <!--
          <div class="detail-grid">
            <div>
              <label class="muted small">Points</label>
              <p style="margin: 0;">{{ selectedQuestion.points }}</p>
            </div>
            <div v-if="selectedQuestion.target_table">
              <label class="muted small">Target Table</label>
              <p style="margin: 0;"><code>{{ selectedQuestion.target_table }}</code></p>
            </div>
          </div>
          -->

          <div class="requirement-box">
            <p style="white-space: pre-wrap; margin: 0;">{{ selectedQuestion.requirement }}</p>
          </div>

          <h3 style="margin: var(--space-4) 0 var(--space-2) 0;">Your SQL</h3>
          <SqlEditor v-model="sqlText" height="240px" />

          <div class="actions">
            <button 
              class="btn btn-secondary" 
              :disabled="loading || previewLoading || practiceUnavailable" 
              @click="doPreview"
            >
              {{ previewLoading ? 'Running...' : 'Preview' }}
            </button>
            <button 
              class="btn btn-primary" 
              :disabled="loading || previewLoading || practiceUnavailable" 
              @click="doSubmit"
            >
              {{ loading ? 'Submitting...' : 'Submit Answer' }}
            </button>
            <p v-if="practiceUnavailable" class="muted unavailability-msg">
              Practice is unavailable while an exam is active.
            </p>
          </div>

          <!-- Preview Result Panel -->
          <div v-if="previewRows.length > 0" class="result-panel preview-result">
            <div class="result-header">
              <span class="badge" :class="badgeClassFor(result?.status || 'incorrect')">
                {{ statusLabel(result?.status || 'incorrect') }}
              </span>
              <span class="muted small">Preview Result</span>
            </div>
            <ResultTable :rows="previewRows" :columns="previewCols" max-height="320px" />
          </div>

          <!-- Submit Result Panel -->
          <div v-if="result" class="result-panel submit-result" :class="result.status">
            <div class="result-header">
              <span class="badge" :class="badgeClassFor(result.status)">
                {{ statusLabel(result.status) }}
              </span>
              <span class="muted small">Final Result</span>
              <span class="score" style="margin-left: auto;">Score: {{ result.score }}</span>
            </div>
            <p class="feedback">{{ result.feedback }}</p>
          </div>
        </div>
        <div v-else class="empty-state">
          <p class="muted">Select a question from the list to start practicing.</p>
        </div>
      </div>
    </div>

    <!-- Attempts History -->
    <div class="card attempts-card">
      <h3 style="margin-top: 0;">Attempt History</h3>
      <div class="attempts-list">
        <div v-for="a in attempts" :key="a.id" class="attempt-item">
          <div class="attempt-main">
            <code class="attempt-sql">{{ truncate(a.sql_text, 80) }}</code>
            <span class="badge" :class="a.is_correct ? 'badge-success' : 'badge-warning'">
              {{ a.is_correct ? 'Correct' : 'Incorrect' }}
            </span>
          </div>
          <div class="attempt-meta">
            <span class="muted small">{{ formatDate(a.submitted_at) }}</span>
          </div>
        </div>
        <p v-if="attempts.length === 0" class="muted">No attempts yet.</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.practice-shell {
  max-width: 1200px;
  margin: var(--space-5) auto;
  padding: 0 var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.card {
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-5);
  border-radius: 0;
}

.unavailability-banner {
  border: 2px solid var(--color-danger) !important;
  background-color: var(--color-danger-bg) !important;
  color: var(--color-danger) !important;
}

.unavailability-banner p { color: var(--color-danger) !important; font-weight: 700 !important; }

.pane-grid {
  display: grid;
  grid-template-columns: 350px 1fr;
  gap: var(--space-6);
  align-items: start;
}

@media (max-width: 1000px) {
  .pane-grid {
    grid-template-columns: 1fr;
  }
}

.question-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.category-group {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.category-title {
  margin: 0;
  padding: var(--space-2) var(--space-4);
  background: var(--color-surface-muted);
  color: var(--color-text);
  font-size: 0.85rem;
  font-weight: 800;
  border: 2px solid var(--color-border);
}

.question-item {
  padding: var(--space-4);
  border: 2px solid var(--color-border);
  border-radius: 0;
  cursor: pointer;
  transition: none;
  background: var(--color-surface);
}

.question-item:hover {
  background-color: var(--color-surface-muted);
}

.question-item.active {
  background-color: var(--color-surface-muted);
  border-color: var(--color-primary);
}

.question-item-header {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-2);
}

.q-id {
  font-weight: 800;
  font-size: 1.2rem;
  color: var(--color-text);
}

.points {
  margin-left: auto;
  font-size: 0.85rem;
  font-weight: 700;
  color: var(--color-text-muted);
}

.requirement-preview {
  font-size: 0.9rem;
  color: var(--color-text);
  font-weight: 500;
}

.selected-header {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-bottom: var(--space-5);
  border-bottom: 2px solid var(--color-border);
  padding-bottom: var(--space-4);
}

.small {
  font-size: 0.75rem;
  font-weight: 700;
  color: var(--color-text-muted);
}

.requirement-box {
  padding: var(--space-5);
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  font-size: 1.1rem;
  line-height: 1.5;
  font-weight: 500;
  margin-bottom: var(--space-6);
}

.actions {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-top: var(--space-6);
  flex-wrap: wrap;
}

.btn {
  border: 2px solid var(--color-border);
  padding: var(--space-2) var(--space-6);
  font-weight: 700;
  cursor: pointer;
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

.unavailability-msg {
  margin: 0;
  font-size: 0.9rem;
  color: var(--color-danger);
  font-weight: 700;
}

.result-panel {
  margin-top: var(--space-8);
  padding: var(--space-6);
  border: 2px solid var(--color-border);
}

.result-panel.correct { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.result-panel.incorrect { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }
.result-panel.syntax_error { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }

.result-header {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-bottom: var(--space-4);
  border-bottom: 1px solid currentColor;
  padding-bottom: var(--space-2);
}

.feedback {
  margin: 0;
  font-size: 1.1rem;
  font-weight: 700;
}

.score {
  font-weight: 800;
  font-size: 1.2rem;
}

.empty-state {
  height: 400px;
  display: flex;
  align-items: center;
  justify-content: center;
  text-align: center;
  font-weight: 700;
  color: var(--color-text-muted);
  border: 2px dashed var(--color-border);
  font-size: 1.4rem;
  padding: var(--space-10);
}

.attempts-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.attempt-item {
  padding: var(--space-4);
  border: 2px solid var(--color-border);
  margin-bottom: var(--space-3);
  background: var(--color-surface);
}

.attempt-main {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-5);
  margin-bottom: var(--space-2);
}

.attempt-sql {
  font-family: var(--font-mono);
  font-size: 0.9rem;
  background: var(--color-surface-muted);
  padding: var(--space-1) var(--space-3);
  border: 1px solid var(--color-border);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  font-weight: 600;
}

.badge {
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-weight: 700;
  font-size: 0.75rem;
}
.badge-info { background: var(--color-primary); color: var(--color-surface); }
.badge-success { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.badge-warning { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }
.badge-danger { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
</style>
