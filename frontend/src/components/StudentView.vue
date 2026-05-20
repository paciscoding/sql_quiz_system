<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import SqlEditor from './SqlEditor.vue'
import ResultTable from './ResultTable.vue'
import { useNotifications } from '../services/notification_service'

const { notify } = useNotifications()

interface ValidationResult {
  status: string
  score: number
  feedback: string
  is_correct: boolean
}

interface PreviewResult {
  status: 'correct' | 'incorrect' | 'syntax_error'
  feedback: string
  rows: Record<string, any>[]
  columns: string[]
  row_count: number
  truncated: boolean
}

interface Examination {
  id: number
  title: string
}

interface Question {
  id: number
  points: number
  type: string
  requirement: string
  target_table: string
  expected_result?: Record<string, any>[] | null
  expected_columns?: string[] | null
}

const exams = ref<Examination[]>([])
const selectedExamId = ref<number | null>(null)
const questions = ref<Question[]>([])
const selectedQuestionId = ref<number | null>(null)

const sqlText = ref('')
const result = ref<ValidationResult | null>(null)
const loading = ref(false)
const error = ref('')

const previewLoading = ref(false)
const previewResult = ref<PreviewResult | null>(null)
const previewStale = ref(false)

const selectedQuestion = computed<Question | null>(
  () => questions.value.find(q => q.id === selectedQuestionId.value) || null
)
const currentQuestion = computed(() => selectedQuestion.value)

// Clear the previous result whenever the student picks a different question

// so the three-state feedback always reflects the current selection.
watch(selectedQuestionId, () => {
  result.value = null
  sqlText.value = ''
  previewResult.value = null
  previewStale.value = false
})

watch(sqlText, () => { if (previewResult.value !== null) previewStale.value = true })

onMounted(async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/quiz/exams', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) exams.value = await response.json()
  } catch (err) {}
})

const fetchQuestions = async () => {
  if (!selectedExamId.value) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/quiz/exams/${selectedExamId.value}/questions`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) questions.value = await response.json()
  } catch (err) {}
}

const submitAnswer = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  if (!sqlText.value) {
    error.value = 'Please provide a SQL query.'
    return
  }
  if (!selectedQuestionId.value) {
    error.value = 'Please select a question.'
    return
  }

  loading.value = true
  error.value = ''
  result.value = null

  try {
    const response = await fetch('/api/v1/quiz/submit', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        question_number: selectedQuestionId.value,
        sql_text: sqlText.value,
      }),
    })

    if (!response.ok) {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to submit answer')
    }

    result.value = await response.json()
    if (result.value?.status === 'correct') {
      notify('Correct answer!', 'success')
    } else if (result.value?.status === 'syntax_error') {
      notify('Syntax error in your SQL.', 'error')
    } else {
      notify('Incorrect answer.', 'error')
    }
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

const testRun = async () => {
  if (!selectedQuestionId.value) { notify('Select a question first', 'error'); return }
  if (!sqlText.value.trim())     { notify('SQL is empty', 'error'); return }

  previewLoading.value = true
  try {
    const token = localStorage.getItem('sql_quiz_token')
    const res = await fetch('/api/v1/quiz/preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ question_number: selectedQuestionId.value, sql_text: sqlText.value }),
    })
    if (res.status === 503) { notify('Server busy, please try again', 'error'); return }
    if (res.status === 504) { notify('Test run timed out', 'error'); return }
    if (!res.ok) {
      const data = await res.json().catch(() => ({} as any))
      notify(data.error || `HTTP ${res.status}`, 'error')
      return
    }
    const data: PreviewResult = await res.json()
    previewResult.value = data
    previewStale.value = false
  } catch (err: any) {
    notify(err?.message || 'Test run failed', 'error')
  } finally {
    previewLoading.value = false
  }
}

const badgeClassFor = (s: string) => s === 'correct' ? 'badge-success' : s === 'syntax_error' ? 'badge-danger' : 'badge-warning'
const statusLabel = (s: string) => s === 'correct' ? 'Passed' : s === 'syntax_error' ? 'SQL Error' : 'Incorrect'
</script>

<template>
  <div class="student-shell">
    <div class="header-bar">
      <div class="field" style="margin: 0; flex: 1;">
        <label>Examination</label>
        <select class="input" v-model="selectedExamId" @change="fetchQuestions">
          <option :value="null">-- Select exam --</option>
          <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }}</option>
        </select>
      </div>
      <div class="field" style="margin: 0; flex: 1;">
        <label>Question</label>
        <select class="input" v-model="selectedQuestionId">
          <option :value="null">-- Select question --</option>
          <option v-for="q in questions" :key="q.id" :value="q.id">
            Q{{ q.id }} ({{ q.type }} - {{ q.points }} pts)
          </option>
        </select>
      </div>
    </div>

    <div class="pane-grid">
      <div class="card">
        <h3 style="margin-top: 0;">Question Brief</h3>
        <div v-if="currentQuestion">
          <p>
            <span class="badge" :class="currentQuestion.type === 'QUERY' ? 'badge-info' : 'badge-warning'">
              {{ currentQuestion.type }}
            </span>
            <span class="muted" style="margin-left: var(--space-2);">{{ currentQuestion.points }} points</span>
          </p>
          <p style="white-space: pre-wrap;">{{ currentQuestion.requirement || '(no requirement text)' }}</p>
          <template v-if="currentQuestion.type === 'QUERY' && currentQuestion.expected_columns">
            <h4 style="margin-bottom: var(--space-2);">Expected Result</h4>
            <ResultTable
              :rows="currentQuestion.expected_result || []"
              :columns="currentQuestion.expected_columns || []"
              max-height="200px"
            />
          </template>
        </div>
        <p v-else class="muted">Select a question to see its brief.</p>
      </div>

      <div class="card">
        <h3 style="margin-top: 0;">Your SQL</h3>
        <SqlEditor v-model="sqlText" />
        <div style="display: flex; gap: var(--space-2); margin-top: var(--space-3);">
          <button class="btn btn-secondary" :disabled="previewLoading || !selectedQuestionId" @click="testRun">
            {{ previewLoading ? 'Running...' : 'Test Run' }}
          </button>
          <button class="btn btn-primary" :disabled="loading || !selectedQuestionId" @click="submitAnswer">
            {{ loading ? 'Submitting...' : 'Submit Answer' }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="previewResult" class="card result-panel" :class="previewResult.status" :style="{ opacity: previewStale ? 0.55 : 1 }">
      <div style="display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2);">
        <span class="badge" :class="badgeClassFor(previewResult.status)">{{ statusLabel(previewResult.status) }}</span>
        <span class="muted" style="font-size: 0.8rem;">Test Run</span>
        <span v-if="previewStale" class="muted" style="font-size: 0.75rem; margin-left: auto;">Stale - run again to refresh</span>
      </div>
      <p style="margin: 0 0 var(--space-3) 0;">{{ previewResult.feedback }}</p>
      <ResultTable :rows="previewResult.rows" :columns="previewResult.columns" max-height="320px" />
      <p v-if="previewResult.truncated" class="muted" style="font-size: 0.8rem; margin-top: var(--space-2);">
        Showing 200 of {{ previewResult.row_count }} rows.
      </p>
    </div>

    <div v-if="result" class="card result-panel" :class="result.status">
      <div style="display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2);">
        <span class="badge" :class="badgeClassFor(result.status)">{{ statusLabel(result.status) }}</span>
        <span class="muted" style="font-size: 0.8rem;">Final Submission</span>
        <span class="muted" style="margin-left: auto; font-size: 0.85rem;">Score: {{ result.score }} pts</span>
      </div>
      <p style="margin: 0;">{{ result.feedback }}</p>
    </div>
  </div>
</template>

<style scoped>
.student-shell {
  max-width: 1100px;
  margin: var(--space-5) auto;
  padding: 0 var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}
.header-bar {
  display: flex;
  gap: var(--space-4);
  background: var(--color-surface);
  padding: var(--space-4);
  border: 2px solid var(--color-border);
}
.pane-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-6);
}

.card {
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-5);
}

.card h3 {
  font-weight: 800;
  font-size: 1.25rem;
  margin-bottom: var(--space-4);
  color: var(--color-text);
  letter-spacing: -0.01em;
}
.result-panel {
  transition: none;
  border: 2px solid var(--color-border);
  margin-top: var(--space-4);
}
.result-panel.correct       { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.result-panel.incorrect     { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }
.result-panel.syntax_error  { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
.result-panel p { font-weight: 700; }

.btn {
  border: 2px solid var(--color-border);
  padding: var(--space-2) var(--space-4);
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

.badge {
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  font-weight: 700;
  font-size: 0.75rem;
}
.badge-info { background: var(--color-primary); color: var(--color-surface); }
.badge-warning { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }
.badge-success { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.badge-danger { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }

.input {
  border: 2px solid var(--color-border);
  padding: 0.6rem;
  font-weight: 600;
  font-family: var(--font-mono);
  background: var(--color-surface);
  color: var(--color-text);
}

@media (max-width: 768px) {
  .pane-grid   { grid-template-columns: 1fr; }
  .header-bar  { flex-direction: column; }
}
</style>
