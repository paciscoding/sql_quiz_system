<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import SqlEditor from './SqlEditor.vue'
import EmptyState from './EmptyState.vue'
import Icon from './Icon.vue'
import ResultTable from './ResultTable.vue'
import SchemaIntroduction from './SchemaIntroduction.vue'
import CollapsibleCard from './CollapsibleCard.vue'
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
import SchemaIntroWindow from './SchemaIntroWindow.vue'
import { listExamIntroImages, type IntroImageMetadata } from '../services/schemaImages'
import { listExamAnnouncements, type ExamAnnouncement } from '../services/announcements'

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
  status: 'correct' | 'incorrect' | 'syntax_error' | 'preview'
  feedback: string
  rows: Record<string, any>[]
  columns: string[]
  row_count: number
  truncated: boolean
}

interface Examination {
  id: string
  title: string
  is_active: boolean
  effective_state: string
  scheduled_start_at: string | null
  scheduled_end_at: string | null
  activated_at?: string | null
}

interface Question {
  id: number
  points: number
  type: string
  requirement: string
  target_table: string
  expected_result?: Record<string, any>[] | null
  expected_columns?: string[] | null
  category_id?: string
  category_name?: string
}

// QuestionGroup buckets contiguous runs of `questions` sharing the same
// category (ARCH-030 (5)). `questions` is already category-ordered by the
// backend (category_id IS NULL last, name ASC, id ASC — TODO-053), so a
// single linear pass is enough to group it; no re-sorting happens here.
// `index` is each question's position in the flat `questions` array, kept so
// the <option> label stays the sequential Q-number (1-based), not a
// per-group-restarted count.
interface QuestionGroup {
  label: string
  items: { question: Question; index: number }[]
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

// The schema stays reachable while answering: the questions sub-view mounts the
// same intro data as a floating, draggable SchemaIntroWindow (the pattern
// PracticeView.vue already uses for category schemas) instead of forcing a
// switch back to the intro sub-view. Closing the window only hides it until the
// student reopens it from the header.
const schemaWindowOpen = ref(true)

async function fetchExamIntro(examId: string) {
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

// ─── Announcements ────────────────────────────────────────────────────────────
// Polled every 30s while an exam is selected and effectively active (phase
// === 'active'). Cached in localStorage per exam + activation stamp so a
// re-activation (new activated_at) discards the previous run's announcements
// client-side, matching the server's sent_at >= activated_at gating.
const announcements = ref<ExamAnnouncement[]>([])
const seenAnnouncementIds = ref<Set<number>>(new Set())
const unseenAnnouncementQueue = ref<ExamAnnouncement[]>([])
let announcementsTimer: ReturnType<typeof setInterval> | null = null

function announcementsCacheKey(examId: string) { return `exam_announcements:${examId}` }
function announcementsSeenKey(examId: string) { return `exam_announcements_seen:${examId}` }

function loadCachedAnnouncements(examId: string, activatedAt: string | null): ExamAnnouncement[] {
  try {
    const raw = localStorage.getItem(announcementsCacheKey(examId))
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (parsed.activated_at !== activatedAt) return []
    return Array.isArray(parsed.items) ? parsed.items : []
  } catch (_) { return [] }
}

function saveCachedAnnouncements(examId: string, activatedAt: string | null, items: ExamAnnouncement[]) {
  try {
    localStorage.setItem(announcementsCacheKey(examId), JSON.stringify({ activated_at: activatedAt, items }))
  } catch (_) { /* storage full/unavailable — non-fatal */ }
}

function loadSeenAnnouncementIds(examId: string, activatedAt: string | null): Set<number> {
  try {
    const raw = localStorage.getItem(announcementsSeenKey(examId))
    if (!raw) return new Set()
    const parsed = JSON.parse(raw)
    if (parsed.activated_at !== activatedAt) return new Set()
    return new Set(Array.isArray(parsed.ids) ? parsed.ids : [])
  } catch (_) { return new Set() }
}

function saveSeenAnnouncementIds(examId: string, activatedAt: string | null, ids: Set<number>) {
  try {
    localStorage.setItem(announcementsSeenKey(examId), JSON.stringify({ activated_at: activatedAt, ids: Array.from(ids) }))
  } catch (_) { /* storage full/unavailable — non-fatal */ }
}

async function fetchAnnouncements() {
  const exam = selectedExam.value
  if (!exam) return
  const activatedAt = exam.activated_at ?? null
  try {
    const data = await listExamAnnouncements(exam.id)
    announcements.value = data
    saveCachedAnnouncements(exam.id, activatedAt, data)
    const alreadyQueued = new Set(unseenAnnouncementQueue.value.map(a => a.id))
    for (const a of data) {
      if (!seenAnnouncementIds.value.has(a.id) && !alreadyQueued.has(a.id)) {
        unseenAnnouncementQueue.value.push(a)
      }
    }
  } catch (_) {
    // Fail silently: 403/404 just means the exam isn't currently active for
    // announcement purposes (or enrollment changed) — no toast spam, no
    // announcements shown.
  }
}

function stopAnnouncementsPoll() {
  if (announcementsTimer !== null) {
    clearInterval(announcementsTimer)
    announcementsTimer = null
  }
}

function startAnnouncementsPoll() {
  stopAnnouncementsPoll()
  announcementsTimer = setInterval(fetchAnnouncements, 30000)
}

function initAnnouncementsForExam(exam: Examination) {
  const activatedAt = exam.activated_at ?? null
  announcements.value = loadCachedAnnouncements(exam.id, activatedAt)
  seenAnnouncementIds.value = loadSeenAnnouncementIds(exam.id, activatedAt)
  unseenAnnouncementQueue.value = []
  fetchAnnouncements()
  startAnnouncementsPoll()
}

function resetAnnouncements() {
  stopAnnouncementsPoll()
  announcements.value = []
  seenAnnouncementIds.value = new Set()
  unseenAnnouncementQueue.value = []
}

function dismissAnnouncement(id: number) {
  seenAnnouncementIds.value.add(id)
  if (selectedExam.value) {
    saveSeenAnnouncementIds(selectedExam.value.id, selectedExam.value.activated_at ?? null, seenAnnouncementIds.value)
  }
  unseenAnnouncementQueue.value = unseenAnnouncementQueue.value.filter(a => a.id !== id)
}

onBeforeUnmount(stopAnnouncementsPoll)

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
const progressMap = ref<Record<number, { answered: boolean; correct: boolean; score?: number }>>({})
const editorWrapRef = ref<HTMLElement | null>(null)

const selectedQuestion = computed<Question | null>(
  () => questions.value.find(q => q.id === selectedQuestionId.value) || null
)
const currentQuestion = computed(() => selectedQuestion.value)

// Test Run exists only for exam QUERY questions. For INSERT/UPDATE/DELETE the
// student gets the verdict and nothing else — no expected result (the backend
// already strips expected_result/expected_columns for non-QUERY), and no preview
// of the table state. /api/v1/quiz/preview refuses non-QUERY questions outright,
// so this only hides a button that would 400.
const previewAllowed = computed(() => currentQuestion.value?.type === 'QUERY')

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

// hasCategorizedQuestions / questionGroups drive the optgroup-grouped question
// picker (ARCH-030 (5)). Presentation only — questions/currentIndex/progress
// above are untouched, and the flat <option> list renders exactly as before
// this feature when no question carries a category.
const hasCategorizedQuestions = computed(() => questions.value.some(q => q.category_id != null))

const questionGroups = computed<QuestionGroup[]>(() => {
  const groups: QuestionGroup[] = []
  questions.value.forEach((q, index) => {
    const label = q.category_id != null ? (q.category_name || 'Category') : 'Other'
    const last = groups[groups.length - 1]
    if (last && last.label === label) {
      last.items.push({ question: q, index })
    } else {
      groups.push({ label, items: [{ question: q, index }] })
    }
  })
  return groups
})

// mergedQuestionStatus combines answerStatus (optimistic, just-submitted) with
// progressMap (server-fetched via GET .../results) the same way
// answeredCount/correctCount above do, but also carries the score so the
// question dropdown and the selected-question status row can show it.
const mergedQuestionStatus = computed<Record<number, { answered: boolean; correct: boolean; score?: number }>>(() => {
  const merged: Record<number, { answered: boolean; correct: boolean; score?: number }> = { ...progressMap.value }
  for (const [k, v] of Object.entries(answerStatus.value)) {
    const qid = Number(k)
    const existing = merged[qid]
    merged[qid] = { answered: true, correct: v, score: existing?.score }
  }
  return merged
})

// optionStatusMarker renders the plain-text status marker used inside a
// native <option> label (SVG icons cannot appear there): a checkmark with the
// score for a correct answer, an X with the score for an incorrect one, or a
// middle dot for a question the student hasn't submitted yet.
function optionStatusMarker(q: Question): string {
  const status = mergedQuestionStatus.value[q.id]
  if (!status || !status.answered) return '· unanswered'
  const scoreText = status.score != null ? `${status.score}/${q.points}` : ''
  return (status.correct ? `✔ ${scoreText}` : `✖ ${scoreText}`).trim()
}

// selectedQuestionStatus drives the icon + colour-coded row shown next to the
// question dropdown for whichever question is currently selected.
const selectedQuestionStatus = computed(() => {
  const q = selectedQuestion.value
  if (!q) return null
  const status = mergedQuestionStatus.value[q.id]
  if (!status || !status.answered) {
    return { icon: 'circle-help', colorVar: 'var(--color-text-muted)', text: 'Not answered yet' }
  }
  const scoreText = status.score != null ? ` — ${status.score}/${q.points} pts` : ''
  return status.correct
    ? { icon: 'circle-check', colorVar: 'var(--color-success)', text: `Correct${scoreText}` }
    : { icon: 'circle-x', colorVar: 'var(--color-danger)', text: `Incorrect${scoreText}` }
})

// ─── Watchers ────────────────────────────────────────────────────────────────
watch(selectedQuestionId, () => {
  result.value = null
  sqlText.value = ''
  previewResult.value = null
  previewStale.value = false
})

watch(sqlText, () => { if (previewResult.value !== null) previewStale.value = true })

// Stop polling for announcements as soon as the exam is no longer effectively
// active (e.g. a lecturer ends it mid-session) — the backend would 403/404
// the poll anyway, but this avoids the pointless requests.
watch(effectiveState, (state) => {
  if (phase.value === 'active' && state !== 'active') stopAnnouncementsPoll()
})

// fetchResults hits GET /api/v1/quiz/exams/:id/results — the per-question
// score/correctness history for the selected exam — and rebuilds
// progressMap from it. Reused on exam selection and again after every
// successful submit so the dropdown/status row reflect the server's record,
// not just the just-submitted question.
const fetchResults = async () => {
  const id = selectedExamId.value
  if (id === null) return
  try {
    const token = localStorage.getItem('sql_quiz_token')
    const res = await fetch(`/api/v1/quiz/exams/${id}/results`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (res.ok) {
      const data = await res.json()
      const map: Record<number, { answered: boolean; correct: boolean; score?: number }> = {}
      if (Array.isArray(data)) {
        for (const item of data) {
          if (item.question_id != null) {
            map[item.question_id] = { answered: true, correct: !!item.is_correct, score: item.score }
          }
        }
      }
      progressMap.value = map
    }
  } catch (_) { /* silently ignore */ }
}

// When the selected exam changes, reset progress state
watch(selectedExamId, async (id) => {
  answerStatus.value = {}
  progressMap.value = {}
  questions.value = []
  selectedQuestionId.value = null
  if (id !== null) await fetchResults()
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
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load your exams.' })
  }
})

function selectExam(exam: Examination) {
  selectedExam.value = exam
  if (exam.effective_state === 'active') {
    phase.value = 'active'
    activeView.value = 'intro'
    fetchQuestions()
    fetchExamIntro(exam.id)
    initAnnouncementsForExam(exam)
  } else {
    phase.value = 'waiting'
  }
}

function goBackToSelect() {
  closeStream()
  resetAnnouncements()
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
  if (selectedExam.value) initAnnouncementsForExam(selectedExam.value)
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
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load questions.' })
  }
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

  // Show the student what their SQL actually returned, not just the verdict —
  // the same thing PracticeView.doSubmit does before submitting. The preview
  // runs in the read-only sandbox (ValidatePreview -> EvaluatePreview) and
  // persists nothing, so it cannot influence the grade that follows.
  //
  // QUERY only: an INSERT/UPDATE/DELETE question shows no preview at all, and
  // the endpoint 400s for it, so calling it here would only surface a spurious
  // error toast on every DML submit.
  if (previewAllowed.value) await testRun()

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
    // Refresh from the server so the score/status shown in the dropdown and
    // status row match the persisted record, not just the optimistic merge.
    await fetchResults()
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
          <Icon name="arrow-left" /> Back
        </button>
        <h3 style="margin-top: 0;">{{ selectedExam?.title }}</h3>
        <div style="display: flex; align-items: center; gap: var(--space-3); margin-bottom: var(--space-4);">
          <span class="muted">Status:</span>
          <span class="badge" :class="effectiveBadgeClass(effectiveState)">{{ effectiveState }}</span>
          <span class="muted" style="font-size: 0.8rem;" v-if="connected"><Icon name="circle-dot" /> Connected</span>
          <span class="muted" style="font-size: 0.8rem;" v-else><Icon name="circle" /> Reconnecting…</span>
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
        <button class="btn btn-secondary" @click="goBackToSelect"><Icon name="arrow-left" /> Back</button>
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
        <!-- Reopen control for the floating schema window; only meaningful in
             the questions sub-view, where that window lives. -->
        <button
          v-if="activeView === 'questions' && !schemaWindowOpen"
          class="btn btn-secondary"
          data-testid="student-schema-window-reopen"
          title="Show the schema panel again"
          @click="schemaWindowOpen = true"
        >Show schema panel</button>
        <div v-if="activeView === 'questions'" class="field" style="margin: 0; flex: 1;">
          <label>Question</label>
          <select class="input" v-model="selectedQuestionId">
            <option :value="null">-- Select question --</option>
            <template v-if="hasCategorizedQuestions">
              <optgroup v-for="(group, gi) in questionGroups" :key="gi" :label="group.label">
                <option v-for="item in group.items" :key="item.question.id" :value="item.question.id">
                  Q{{ item.index + 1 }} ({{ item.question.type }} - {{ item.question.points }} pts) — {{ optionStatusMarker(item.question) }}
                </option>
              </optgroup>
            </template>
            <template v-else>
              <option v-for="(q, i) in questions" :key="q.id" :value="q.id">
                Q{{ i + 1 }} ({{ q.type }} - {{ q.points }} pts) — {{ optionStatusMarker(q) }}
              </option>
            </template>
          </select>
          <div v-if="selectedQuestionStatus" class="question-status-row" :style="{ color: selectedQuestionStatus.colorVar }">
            <Icon :name="selectedQuestionStatus.icon" />
            <span>{{ selectedQuestionStatus.text }}</span>
          </div>
        </div>
      </div>

      <CollapsibleCard
        v-if="phase === 'active'"
        title="Announcements"
        :subtitle="announcements.length ? `${announcements.length} message${announcements.length === 1 ? '' : 's'}` : undefined"
        :default-open="announcements.length > 0"
        test-id="student-announcements"
      >
        <p v-if="announcements.length === 0" class="muted" style="margin: 0;">No announcements yet.</p>
        <ul v-else class="announcement-list">
          <li v-for="a in announcements" :key="a.id" class="announcement-item">
            <div class="announcement-item-meta">
              <strong>{{ a.lecturer_name }}</strong>
              <span class="muted announcement-item-time">{{ new Date(a.sent_at).toLocaleString() }}</span>
            </div>
            <p class="announcement-item-message">{{ a.message }}</p>
          </li>
        </ul>
      </CollapsibleCard>

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
              Proceed to Questions <Icon name="arrow-right" />
            </button>
          </div>
        </div>
      </template>

      <template v-else>

      <!-- Schema stays on screen while answering: floating windows
           (position: fixed), draggable/collapsible/minimizable, mounted for the
           whole questions sub-view rather than only on the intro page. -->
      <template v-if="schemaWindowOpen && selectedExam">
        <SchemaImageWindow
          v-if="examIntroImages.length > 0"
          scope="exam"
          :owner-id="selectedExam.id"
          :images="examIntroImages"
        />
        <SchemaIntroWindow
          scope="exam"
          :owner-id="selectedExam.id"
          :introduction="examIntro?.introduction ?? ''"
          :tables="examIntro?.tables ?? []"
          :imported="(examIntro?.tables?.length ?? 0) > 0"
          mode="student"
          @close="schemaWindowOpen = false"
        />
      </template>

      <div class="quiz-progress" v-if="questions.length > 0">
        <div class="qp-line">
          <span>Question <strong>{{ currentIndex >= 0 ? currentIndex + 1 : 1 }}</strong> of <strong>{{ questions.length }}</strong></span>
          <span class="muted">{{ answeredCount }} answered · {{ correctCount }} correct</span>
        </div>
        <div class="qp-bar"><div class="qp-fill" :style="{ transform: 'scaleX(' + (progressPct / 100) + ')' }" /></div>
      </div>

      <!-- Single column, two rows: Question Brief on top, the answering panel
           directly below it. Previously this switched to two side-by-side
           columns when the expected-result table was narrow (briefTwoCol),
           which pushed the SQL editor off to the right. -->
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
            <button v-if="previewAllowed" class="btn btn-secondary" :disabled="previewLoading || !selectedQuestionId" @click="testRun">
              {{ previewLoading ? UI_COPY.TEST_RUN_LOADING : UI_COPY.TEST_RUN }}
            </button>
            <button class="btn btn-primary" :disabled="submitLoading || !selectedQuestionId" @click="submitAnswer">
              <Spinner v-if="submitLoading" size="sm" inline :invert="true" />{{ submitLoading ? UI_COPY.SUBMIT_ANSWER_LOADING : UI_COPY.SUBMIT_ANSWER }}
            </button>
          </div>
        </div>
      </div>

      <div v-if="previewResult && previewAllowed" class="card result-panel" :class="previewResult.status" :style="{ opacity: previewStale ? 0.55 : 1 }">
        <div style="display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2);">
          <span class="muted" style="font-size: 0.8rem;">Test Run</span>
          <span v-if="previewStale" class="muted" style="font-size: 0.75rem; margin-left: auto;">Stale - run again to refresh</span>
        </div>
        <p v-if="previewResult.status === 'syntax_error'" style="margin: 0 0 var(--space-3) 0; font-weight: 600; color: var(--color-danger);">{{ previewResult.feedback }}</p>
        <!-- QUERY only by construction: previewAllowed gates this whole panel,
             so the old INSERT/UPDATE/DELETE row-count branch is gone with the
             DML test run itself. -->
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
        <p style="margin: 0; white-space: pre-line; overflow-wrap: break-word;">{{ result.feedback }}</p>
      </div>

      </template><!-- end v-else (questions sub-view) -->
    </template><!-- end ACTIVE phase -->

    <!-- Unseen-announcement dialog: reuses the app's modal/dialog markup
         convention (see ConfirmDialogHost.vue). Multiple unseen announcements
         queue up here — dismissing the front one reveals the next. -->
    <Teleport to="body">
      <div
        v-if="unseenAnnouncementQueue.length > 0"
        class="announcement-backdrop"
        role="presentation"
        @click.self="dismissAnnouncement(unseenAnnouncementQueue[0].id)"
      >
        <div
          class="announcement-dialog"
          role="alertdialog"
          aria-modal="true"
          aria-labelledby="announcement-dialog-title"
        >
          <h3 id="announcement-dialog-title" class="announcement-dialog-title">
            Announcement from {{ unseenAnnouncementQueue[0].lecturer_name }}
          </h3>
          <p class="muted announcement-dialog-time">{{ new Date(unseenAnnouncementQueue[0].sent_at).toLocaleString() }}</p>
          <p class="announcement-dialog-message">{{ unseenAnnouncementQueue[0].message }}</p>
          <div class="announcement-dialog-actions">
            <span v-if="unseenAnnouncementQueue.length > 1" class="muted" style="margin-right: auto; font-size: 0.8rem;">
              {{ unseenAnnouncementQueue.length - 1 }} more
            </span>
            <button type="button" class="btn btn-primary" @click="dismissAnnouncement(unseenAnnouncementQueue[0].id)">
              Got it
            </button>
          </div>
        </div>
      </div>
    </Teleport>

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
  grid-template-columns: 1fr;
  gap: var(--space-5);
}
/* Grid items default to min-width: auto, which lets a wide table's
   min-content size push the track (and the surrounding page) wider instead
   of scrolling inside its own card. min-width: 0 lets the card shrink to the
   track width so the table wrapper (ResultTable's .responsive-table-wrap,
   global in style.css) owns the horizontal overflow and the card's own
   background always covers the full visible width. */
.pane-grid > * {
  min-width: 0;
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
  min-width: 0;
}
.result-panel.correct       { background: var(--color-success-bg); color: var(--color-success); border-color: #c4dfba; }
.result-panel.incorrect     { background: var(--color-warning-bg); color: var(--color-warning); border-color: #ecd998; }
.result-panel.syntax_error  { background: var(--color-danger-bg); color: var(--color-danger); border-color: #e9bcbc; }
.result-panel p { font-weight: 600; margin: 0; }

@media (max-width: 768px) {
  .pane-grid { grid-template-columns: 1fr; }
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
  width: 100%;
  transform-origin: left center;
  background: #3a87ad;
  transition: transform .25s ease;
}
@media (prefers-reduced-motion: reduce) {
  .qp-fill { transition: none; }
}

/* ── Question status row (Task A) ─────────────────────────────────────── */
.question-status-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: var(--space-2);
  font-size: 0.85rem;
  font-weight: 600;
}

/* ── Announcements (Task B) ───────────────────────────────────────────── */
.announcement-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.announcement-item {
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: 10px 12px;
  background: var(--color-surface-sunk);
}
.announcement-item-meta {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  margin-bottom: 4px;
}
.announcement-item-time {
  font-size: 0.78rem;
}
.announcement-item-message {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: break-word;
}

.announcement-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10000;
  padding: var(--space-4);
}
.announcement-dialog {
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  border-radius: var(--radius);
  padding: var(--space-5);
  max-width: 480px;
  width: 100%;
  box-shadow: var(--shadow-2);
}
.announcement-dialog-title {
  margin: 0 0 var(--space-2) 0;
  font-weight: 800;
  font-size: 1.05rem;
}
.announcement-dialog-time {
  margin: 0 0 var(--space-3) 0;
  font-size: 0.8rem;
}
.announcement-dialog-message {
  margin: 0 0 var(--space-4) 0;
  white-space: pre-wrap;
  overflow-wrap: break-word;
}
.announcement-dialog-actions {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  justify-content: flex-end;
}
</style>
