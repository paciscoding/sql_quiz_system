<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import SqlEditor from './SqlEditor.vue'
import EmptyState from './EmptyState.vue'
import ResultTable from './ResultTable.vue'
import SchemaIntroduction from './SchemaIntroduction.vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useExamStream } from '../composables/useExamStream'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import Spinner from './Spinner.vue'
import { useKeyboardShortcut } from '../composables/useKeyboardShortcut'
import { formatSql } from '../utils/format_sql'
import { UI_COPY } from '../constants/ui_copy'
import { getExamIntroduction, type ExamIntroduction } from '../services/exam'
import SchemaImageWindow from './SchemaImageWindow.vue'
import { listExamIntroImages, type IntroImageMetadata } from '../services/schemaImages'

const { notify } = useNotifications()

// ─── Phase state machine ────────────────────────────────────────────────────
// 'select'  – student picks an exam from their enrolled list
// 'waiting' – exam not yet active; SSE stream open, Start button gated
// 'active'  – exam is active; student interacting with questions
type Phase = 'select' | 'waiting' | 'active'
const phase = ref<Phase>('select')

// ─── Interfaces ─────────────────────────────────────────────────────────────
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
  is_active: boolean
  effective_state: string
  scheduled_start_at: string | null
  scheduled_end_at: string | null
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

// ─── SELECT phase state ──────────────────────────────────────────────────────
const enrolledExams = ref<Examination[]>([])
const selectedExam = ref<Examination | null>(null)
const selectedExamId = computed(() => selectedExam.value?.id ?? null)

// ─── SSE / WAITING phase state ───────────────────────────────────────────────
const { effectiveState, connected, close: closeStream } = useExamStream(selectedExamId)

// ─── ACTIVE phase sub-view ───────────────────────────────────────────────────
// 'intro'     – the mandatory first page: curriculum schema + lecturer description
// 'questions' – the question list and editor
type ActiveView = 'intro' | 'questions'
const activeView = ref<ActiveView>('intro')

// examIntro holds the fetched intro data (null until fetched or if unavailable)
const examIntro = ref<ExamIntroduction | null>(null)
const examIntroLoading = ref(false)
const examIntroImages = ref<IntroImageMetadata[]>([])

async function fetchExamIntro(examId: number) {
  examIntroLoading.value = true
  try {
    const [intro, imgs] = await Promise.all([
      getExamIntroduction(examId),
      listExamIntroImages(examId).catch(() => [] as IntroImageMetadata[])
    ])
    examIntro.value = intro
    examIntroImages.value = imgs
  } catch (_) {
    // Non-fatal: intro unavailable or not yet imported — show empty state
    examIntro.value = null
    examIntroImages.value = []
  } finally {
    examIntroLoading.value = false
  }
}

// ─── ACTIVE phase state ──────────────────────────────────────────────────────
const questions = ref<Question[]>([])
const selectedQuestionId = ref<number | null>(null)
const sqlText = ref('')
const result = ref<ValidationResult | null>(null)
const error = ref('')
const previewLoading = ref(false)
const previewResult = ref<PreviewResult | null>(null)
const previewStale = ref(false)
const answerStatus = ref<Record<number, boolean>>({})
const progressMap = ref<Record<number, { answered: boolean; correct: boolean }>>({})
const editorWrapRef = ref<HTMLElement | null>(null)

const selectedQuestion = computed<Question | null>(
  () => questions.value.find(q => q.id === selectedQuestionId.value) || null
)
const currentQuestion = computed(() => selectedQuestion.value)

const currentIndex = computed(() =>
  questions.value.findIndex(q => q.id === selectedQuestionId.value))

const answeredCount = computed(() => {
  const merged = { ...progressMap.value }
  for (const [k, v] of Object.entries(answerStatus.value)) {
    merged[Number(k)] = { answered: true, correct: v }
  }
  return Object.values(merged).filter(v => v.answered).length
})
const correctCount = computed(() => {
  const merged = { ...progressMap.value }
  for (const [k, v] of Object.entries(answerStatus.value)) {
    merged[Number(k)] = { answered: true, correct: v }
  }
  return Object.values(merged).filter(v => v.correct).length
})
const progressPct = computed(() =>
  questions.value.length === 0 ? 0 : Math.round((answeredCount.value / questions.value.length) * 100))

// ─── Watchers ────────────────────────────────────────────────────────────────
watch(selectedQuestionId, () => {
  result.value = null
  sqlText.value = ''
  previewResult.value = null
  previewStale.value = false
})

watch(sqlText, () => { if (previewResult.value !== null) previewStale.value = true })

// When the selected exam changes, reset progress state
watch(selectedExamId, async (id) => {
  answerStatus.value = {}
  progressMap.value = {}
  questions.value = []
  selectedQuestionId.value = null
  if (id !== null) {
    try {
      const token = localStorage.getItem('sql_quiz_token')
      const res = await fetch(`/api/v1/quiz/results/${id}`, {
        headers: { 'Authorization': `Bearer ${token}` }
      })
      if (res.ok) {
        const data = await res.json()
        const map: Record<number, { answered: boolean; correct: boolean }> = {}
        if (Array.isArray(data)) {
          for (const item of data) {
            if (item.question_id != null) {
              map[item.question_id] = { answered: true, correct: !!item.is_correct }
            }
          }
        }
        progressMap.value = map
      }
    } catch (_) { /* silently ignore */ }
  }
})

// ─── Badge helpers ───────────────────────────────────────────────────────────
function effectiveBadgeClass(state: string): string {
  switch (state) {
    case 'active':    return 'badge-success'
    case 'scheduled': return 'badge-info'
    case 'ended':     return 'badge-warning'
    default:          return ''
  }
}

// ─── SELECT phase actions ─────────────────────────────────────────────────────
onMounted(async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/quiz/exams?include_inactive=true', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) enrolledExams.value = await response.json()
  } catch (err) {}
})

function selectExam(exam: Examination) {
  selectedExam.value = exam
  if (exam.effective_state === 'active') {
    phase.value = 'active'
    activeView.value = 'intro'
    fetchQuestions()
    fetchExamIntro(exam.id)
  } else {
    phase.value = 'waiting'
  }
}

function goBackToSelect() {
  closeStream()
  selectedExam.value = null
  questions.value = []
  selectedQuestionId.value = null
  result.value = null
  sqlText.value = ''
  previewResult.value = null
  phase.value = 'select'
  activeView.value = 'intro'
  examIntro.value = null
}

// ─── WAITING phase actions ────────────────────────────────────────────────────
function startExam() {
  if (effectiveState.value !== 'active') return
  phase.value = 'active'
  activeView.value = 'intro'
  fetchQuestions()
  if (selectedExamId.value !== null) fetchExamIntro(selectedExamId.value)
}

// ─── ACTIVE phase actions ────────────────────────────────────────────────────
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

const { inFlight: submitLoading, run: submitAnswer } = useSingleFlight(async () => {
  if (!sqlText.value) {
    error.value = 'Please provide a SQL query.'
    return
  }
  if (!selectedQuestionId.value) {
    error.value = 'Please select a question.'
    return
  }

  error.value = ''
  result.value = null
  const token = localStorage.getItem('sql_quiz_token')

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
    if (selectedQuestionId.value !== null) {
      const qid = selectedQuestionId.value
      const isCorrect = result.value?.is_correct ?? false
      answerStatus.value = {
        ...answerStatus.value,
        [qid]: isCorrect,
      }
      progressMap.value = {
        ...progressMap.value,
        [qid]: { answered: true, correct: isCorrect },
      }
    }
    if (result.value?.status === 'correct') {
      notify('Correct answer!', 'success')
    } else if (result.value?.status === 'syntax_error') {
      notify('Syntax error in your SQL.', 'error')
    } else {
      notify('Incorrect answer.', 'error')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to submit answer' })
  }
})

useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, scope: editorWrapRef, handler: () => submitAnswer() })

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
  } catch (err) {
    handleApiError(err, { fallback: 'Test run failed' })
  } finally {
    previewLoading.value = false
  }
}

const badgeClassFor = (s: string) => s === 'correct' ? 'badge-success' : s === 'syntax_error' ? 'badge-danger' : 'badge-warning'
const statusLabel = (s: string) => s === 'correct' ? 'Passed' : s === 'syntax_error' ? 'SQL Error' : 'Incorrect'
</script>

<template>
  <div class="student-shell">

    <div class="page-header">
      <h1>My Examination</h1>
      <p class="page-subtitle">
        <template v-if="phase === 'select'">Choose an enrolled examination to begin.</template>
        <template v-else-if="phase === 'waiting'">Waiting for the examination to open.</template>
        <template v-else>Examination in progress &mdash; review the schema and submit your answers.</template>
      </p>
    </div>

    <!-- ── SELECT phase ─────────────────────────────────────────────────── -->
    <template v-if="phase === 'select'">
      <div class="card">
        <h3 style="margin-top: 0;">Available Examinations</h3>
        <p class="muted" v-if="enrolledExams.length === 0">You are not enrolled in any examinations.</p>
        <div class="exam-grid" v-else>
          <div
            v-for="exam in enrolledExams"
            :key="exam.id"
            class="exam-card"
          >
            <div class="exam-card-header">
              <strong>{{ exam.title }}</strong>
              <span class="badge" :class="effectiveBadgeClass(exam.effective_state)">
                {{ exam.effective_state }}
              </span>
            </div>
            <div class="exam-card-meta muted" v-if="exam.scheduled_start_at">
              Opens: {{ new Date(exam.scheduled_start_at).toLocaleString() }}
            </div>
            <div class="exam-card-meta muted" v-if="exam.scheduled_end_at">
              Closes: {{ new Date(exam.scheduled_end_at).toLocaleString() }}
            </div>
            <button class="btn btn-primary" @click="selectExam(exam)" style="margin-top: var(--space-3);">
              Select
            </button>
          </div>
        </div>
      </div>
    </template>

    <!-- ── WAITING phase ────────────────────────────────────────────────── -->
    <template v-else-if="phase === 'waiting'">
      <div class="card waiting-panel">
        <button class="btn btn-secondary" style="margin-bottom: var(--space-4);" @click="goBackToSelect">
          ← Back
        </button>
        <h3 style="margin-top: 0;">{{ selectedExam?.title }}</h3>
        <div style="display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-4);">
          <span class="muted">Status:</span>
          <span class="badge" :class="effectiveBadgeClass(effectiveState)">{{ effectiveState }}</span>
          <span class="muted" style="font-size: 0.8rem;" v-if="connected">● Connected</span>
          <span class="muted" style="font-size: 0.8rem;" v-else>○ Reconnecting…</span>
        </div>
        <p class="muted" v-if="effectiveState !== 'active'">
          Waiting for the exam to start. The Start Exam button will become available when the exam is active.
        </p>
        <p class="muted" v-if="selectedExam?.scheduled_start_at">
          Scheduled start: {{ new Date(selectedExam.scheduled_start_at).toLocaleString() }}
        </p>
        <button
          class="btn btn-primary"
          :disabled="effectiveState !== 'active'"
          @click="startExam"
          style="margin-top: var(--space-4);"
        >
          Start Exam
        </button>
      </div>
    </template>

    <!-- ── ACTIVE phase ─────────────────────────────────────────────────── -->
    <template v-else>
      <div class="header-bar">
        <button class="btn btn-secondary" @click="goBackToSelect">← Back</button>
        <!-- Persistent intro / questions toggle — always visible in active phase -->
        <button
          class="btn"
          :class="activeView === 'intro' ? 'btn-primary' : 'btn-secondary'"
          @click="activeView = 'intro'"
          title="View curriculum schema and description"
        >Schema</button>
        <button
          class="btn"
          :class="activeView === 'questions' ? 'btn-primary' : 'btn-secondary'"
          @click="activeView = 'questions'"
          title="Go to questions"
        >Questions</button>
        <div v-if="activeView === 'questions'" class="field" style="margin: 0; flex: 1;">
          <label>Question</label>
          <select class="input" v-model="selectedQuestionId">
            <option :value="null">-- Select question --</option>
            <option v-for="q in questions" :key="q.id" :value="q.id">
              Q{{ q.id }} ({{ q.type }} - {{ q.points }} pts)
            </option>
          </select>
        </div>
      </div>

      <!-- Intro sub-view: shown when activeView === 'intro' -->
      <template v-if="activeView === 'intro'">
        <div class="card intro-view">
          <h3 style="margin-top: 0;">{{ selectedExam?.title }} — Exam Introduction</h3>
          <div v-if="examIntroLoading" class="muted" style="padding: var(--space-6); text-align: center;">Loading schema...</div>
          <template v-else>
            <SchemaImageWindow
              v-if="examIntroImages.length > 0"
              scope="exam"
              :owner-id="selectedExam!.id"
              :images="examIntroImages"
            />
            <SchemaIntroduction
              :introduction="examIntro?.introduction ?? ''"
              :tables="examIntro?.tables ?? []"
              :imported="(examIntro?.tables?.length ?? 0) > 0"
              mode="student"
            />
          </template>
          <div style="margin-top: var(--space-5);">
            <button class="btn btn-primary" @click="activeView = 'questions'">
              Proceed to Questions →
            </button>
          </div>
        </div>
      </template>

      <template v-else>

      <div class="quiz-progress" v-if="questions.length > 0">
        <div class="qp-line">
          <span>Question <strong>{{ currentIndex >= 0 ? currentIndex + 1 : 1 }}</strong> of <strong>{{ questions.length }}</strong></span>
          <span class="muted">{{ answeredCount }} answered · {{ correctCount }} correct</span>
        </div>
        <div class="qp-bar"><div class="qp-fill" :style="{ width: progressPct + '%' }" /></div>
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
          <EmptyState v-else :compact="true" title="No question selected" body="Pick a question to begin." />
        </div>

        <div class="card" ref="editorWrapRef">
          <h3 style="margin-top: 0;">Your SQL</h3>
          <SqlEditor v-model="sqlText" />
          <div style="display: flex; gap: var(--space-2); margin-top: var(--space-3);">
            <button class="btn btn-secondary" :disabled="!sqlText" @click="sqlText = formatSql(sqlText)">Format SQL</button>
            <button class="btn btn-secondary" :disabled="previewLoading || !selectedQuestionId" @click="testRun">
              {{ previewLoading ? UI_COPY.TEST_RUN_LOADING : UI_COPY.TEST_RUN }}
            </button>
            <button class="btn btn-primary" :disabled="submitLoading || !selectedQuestionId" @click="submitAnswer">
              <Spinner v-if="submitLoading" size="sm" inline :invert="true" />{{ submitLoading ? UI_COPY.SUBMIT_ANSWER_LOADING : UI_COPY.SUBMIT_ANSWER }}
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

      </template><!-- end v-else (questions sub-view) -->
    </template><!-- end ACTIVE phase -->

  </div>
</template>

<style scoped>
.student-shell {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}
.header-bar {
  display: flex;
  gap: var(--space-3);
  background: var(--color-surface);
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  align-items: center;
  flex-wrap: wrap;
  box-shadow: var(--shadow-1);
}
.pane-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-5);
}

.card {
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-5);
  border-radius: var(--radius);
  box-shadow: var(--shadow-1);
}

.card h3 {
  font-size: 1.02rem;
  font-weight: 700;
  margin-bottom: var(--space-3);
  color: var(--color-text);
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}

.waiting-panel { max-width: 620px; }
.intro-view    { max-width: 100%; }

.exam-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: var(--space-4);
}

.exam-card {
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-4);
  border-radius: var(--radius);
  display: flex;
  flex-direction: column;
  transition: border-color .12s ease, box-shadow .12s ease;
}
.exam-card:hover { border-color: var(--color-primary); box-shadow: var(--shadow-2); }

.exam-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}
.exam-card-header strong { font-size: 0.96rem; }

.exam-card-meta {
  font-size: 0.78rem;
  margin-bottom: 2px;
}

.result-panel {
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  margin-top: var(--space-3);
  padding: 10px 14px;
}
.result-panel.correct       { background: var(--color-success-bg); color: var(--color-success); border-color: #c4dfba; }
.result-panel.incorrect     { background: var(--color-warning-bg); color: var(--color-warning); border-color: #ecd998; }
.result-panel.syntax_error  { background: var(--color-danger-bg); color: var(--color-danger); border-color: #e9bcbc; }
.result-panel p { font-weight: 600; margin: 0; }

@media (max-width: 768px) {
  .pane-grid   { grid-template-columns: 1fr; }
  .header-bar  { flex-direction: column; align-items: stretch; }
}

.quiz-progress {
  background: var(--alert-info-bg);
  border: 1px solid var(--alert-info-border);
  border-radius: var(--radius);
  padding: 10px 14px;
  color: var(--alert-info-text);
}
.qp-line {
  display: flex;
  justify-content: space-between;
  font-size: 0.86rem;
  margin-bottom: 6px;
}
.qp-line strong { color: #2b6987; }
.qp-bar {
  height: 6px;
  background: rgba(58,135,173,0.18);
  border-radius: 999px;
  overflow: hidden;
}
.qp-fill {
  height: 100%;
  background: #3a87ad;
  transition: width .25s ease;
}
</style>
