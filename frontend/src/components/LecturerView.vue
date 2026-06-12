<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import BulkImportStudents from './BulkImportStudents.vue'
import StudentMultiSelect from './StudentMultiSelect.vue'
import SqlEditor from './SqlEditor.vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useConfirm } from '../services/confirm_service'
import { useNotifications } from '../services/notification_service'
import { handleApiError, toApiError } from '../services/error_handler'
import Spinner from './Spinner.vue'
import EmptyState from './EmptyState.vue'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import Skeleton from './Skeleton.vue'
import { useKeyboardShortcut } from '../composables/useKeyboardShortcut'
import { formatSql } from '../utils/format_sql'
import { UI_COPY } from '../constants/ui_copy'
import { setExamIntroduction } from '../services/exam'
import IntroImagesEditor from './IntroImagesEditor.vue'

interface Examination {
  id: number
  title: string
  is_active: boolean
  effective_state: string
  scheduled_start_at: string | null
  scheduled_end_at: string | null
  activated_at: string | null
  archived_at: string | null
  introduction: string
}

interface Student {
  id: number
  username: string
}

interface CaptureStatus {
  status: 'captured' | 'failed' | 'pending' | ''
  error: string
  capturedAt: string | null
}

const { notify } = useNotifications()
const { confirm } = useConfirm()

const blankQuestion = () => ({
  id: null as number | null,
  exam_id: null as number | null,
  target_table: '',
  correct_sql: '',
  requirement: '',
  type: 'QUERY',
  points: 10,
  strict_mode: {
    ordering: false,
    attributes: false
  }
})

const question = ref(blankQuestion())

// Form validation for question fields (TODO-038)
const questionValidation = useFormValidation({
  points: { rules: [required('Points is required')], initial: 10 },
  requirement: { rules: [required('Requirement is required')], initial: '' },
  correct_sql: { rules: [required('Correct SQL is required')], initial: '' },
})

// Keep validation values in sync with question reactive object
watch(() => question.value.points, (v) => { questionValidation.values.points = v })
watch(() => question.value.requirement, (v) => { questionValidation.values.requirement = v })
watch(() => question.value.correct_sql, (v) => { questionValidation.values.correct_sql = v })

const lastCapture = ref<CaptureStatus>({ status: '', error: '', capturedAt: null })
const examQuestions = ref<any[]>([])
const sortKey = ref<'id'|'type'|'points'|null>(null)
const sortDir = ref<'asc'|'desc'>('asc')
const filterText = ref('')

const displayedQuestions = computed(() => {
  let list = examQuestions.value.slice()
  if (filterText.value.trim()) {
    const q = filterText.value.toLowerCase()
    list = list.filter(x => (x.requirement || '').toLowerCase().includes(q))
  }
  if (sortKey.value) {
    const k = sortKey.value
    const dir = sortDir.value === 'asc' ? 1 : -1
    list.sort((a, b) => {
      const av = k === 'type' ? (a.question_type || '') : (a[k] ?? 0)
      const bv = k === 'type' ? (b.question_type || '') : (b[k] ?? 0)
      return av < bv ? -dir : av > bv ? dir : 0
    })
  }
  return list
})

function toggleSort(key: 'id'|'type'|'points') {
  if (sortKey.value === key) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortKey.value = key
    sortDir.value = 'asc'
  }
}

const fetchExamQuestions = async () => {
  if (question.value.exam_id == null) {
    examQuestions.value = []
    return
  }
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${question.value.exam_id}/questions`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      examQuestions.value = await response.json()
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam questions' })
  }
}

watch(() => question.value.exam_id, (newId) => {
  if (newId !== null) {
    fetchExamQuestions()
  } else {
    examQuestions.value = []
  }
})

const { inFlight: deleteQuestionLoading, run: deleteQuestion } = useSingleFlight(async (id: number) => {
  const ok = await confirm({ title: 'Delete this question?', body: 'This cannot be undone.', confirmLabel: 'Delete', danger: true })
  if (!ok) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/questions/${id}`, {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      notify('Question deleted', 'success')
      fetchExamQuestions()
    } else {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to delete')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to delete question' })
  }
})

const isEditMode = ref(false)

const nextFreeId = computed(() => {
  if (!examQuestions.value || examQuestions.value.length === 0) return 1
  return Math.max(...examQuestions.value.map((q: any) => q.id)) + 1
})

const editQuestion = (q: any) => {
  isEditMode.value = true
  question.value = {
    id: q.id,
    exam_id: q.exam_id,
    target_table: q.target_table || '',
    correct_sql: q.correct_sql || '',
    requirement: q.requirement || '',
    type: q.question_type || 'QUERY',
    points: q.points || 0,
    strict_mode: {
      ordering: q.strict_mode?.ordering || false,
      attributes: q.strict_mode?.attributes || false
    }
  }
  lastCapture.value = {
    status: q.expected_result_status || '',
    error: q.capture_error || '',
    capturedAt: q.expected_result_captured_at || null
  }
  previewResult.value = { status: '', rows: [], columns: [], error: '' }
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function onQuestionDropdownChange(event: Event) {
  const val = (event.target as HTMLSelectElement).value
  if (val === '') {
    // Create new question
    isEditMode.value = false
    question.value = {
      id: nextFreeId.value,
      exam_id: question.value.exam_id,
      target_table: '',
      correct_sql: '',
      requirement: '',
      type: 'QUERY',
      points: 10,
      strict_mode: { ordering: false, attributes: false }
    }
    lastCapture.value = { status: '', error: '', capturedAt: null }
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
  } else {
    const q = examQuestions.value.find((q: any) => q.id === Number(val))
    if (q) editQuestion(q)
  }
}

interface PreviewResult {
  status: 'ok' | 'error' | ''
  rows: Record<string, any>[]
  columns: string[]
  error: string
}
const previewResult = ref<PreviewResult>({ status: '', rows: [], columns: [], error: '' })
const previewLoading = ref(false)
const questionFormRef = ref<HTMLElement | null>(null)

useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, scope: questionFormRef, handler: () => previewCorrectSQL() })

const previewCorrectSQL = async () => {
  if (!question.value.correct_sql) {
    notify('Enter the Correct SQL before previewing', 'error')
    return
  }
  if (question.value.type === 'DML' && !question.value.target_table) {
    notify('Target table is required to preview a DML question', 'error')
    return
  }
  previewLoading.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/preview', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        type: question.value.type,
        target_table: question.value.target_table,
        correct_sql: question.value.correct_sql
      })
    })
    const data = await response.json().catch(() => ({} as any))
    if (!response.ok) {
      throw new Error(data.error || 'Preview failed')
    }
    if (data.status === 'error') {
      previewResult.value = { status: 'error', rows: [], columns: [], error: data.error || 'SQL error' }
    } else {
      previewResult.value = {
        status: 'ok',
        rows: data.rows || [],
        columns: data.columns || [],
        error: ''
      }
    }
  } catch (err) {
    previewResult.value = { status: 'error', rows: [], columns: [], error: (err as Error).message || 'Preview failed' }
    handleApiError(err, { fallback: 'Preview failed' })
  } finally {
    previewLoading.value = false
  }
}

onMounted(() => {
  fetchExams()
  fetchStudents()
})

const exams = ref<Examination[]>([])
const newExamTitle = ref('')
const selectedExamId = ref<number | null>(null)

const activeExam = computed(() => exams.value.find(e => e.id === selectedExamId.value))

// Exam description editor state
const examIntroductionText = ref('')
const examIntroSaving = ref(false)

// Pre-populate the description textarea when the selected exam changes.
watch(selectedExamId, (id) => {
  if (id !== null) {
    const exam = exams.value.find(e => e.id === id)
    examIntroductionText.value = exam?.introduction ?? ''
  } else {
    examIntroductionText.value = ''
  }
})

const saveExamIntroduction = async () => {
  if (!selectedExamId.value) return
  examIntroSaving.value = true
  try {
    await setExamIntroduction(selectedExamId.value, examIntroductionText.value)
    notify('Exam description saved', 'success')
    // Refresh so the updated intro appears if exams are re-fetched
    await fetchExams()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save exam description' })
  } finally {
    examIntroSaving.value = false
  }
}

const studentsList = ref<Student[]>([])
const selectedStudentIds = ref<number[]>([])

// --- Autosave for question requirement (TODO-059) ---
const requirementDraftKey = computed(() =>
  'sql_quiz_draft.lecturer.questionRequirement.exam-' + (question.value.exam_id ?? 'none')
)
const showDraftBanner = ref(false)

let requirementDebounceTimer: number | null = null
watch(() => question.value.requirement, (val) => {
  if (requirementDebounceTimer != null) window.clearTimeout(requirementDebounceTimer)
  requirementDebounceTimer = window.setTimeout(() => {
    const key = requirementDraftKey.value
    if (val && val.length >= 10) {
      try { localStorage.setItem(key, val) } catch { /* quota */ }
    } else {
      localStorage.removeItem(key)
    }
    requirementDebounceTimer = null
  }, 600)
})

watch(requirementDraftKey, (key) => {
  showDraftBanner.value = !!localStorage.getItem(key)
}, { immediate: true })

const acceptDraft = () => {
  const draft = localStorage.getItem(requirementDraftKey.value)
  if (draft) question.value.requirement = draft
  showDraftBanner.value = false
}
const dismissDraft = () => {
  localStorage.removeItem(requirementDraftKey.value)
  showDraftBanner.value = false
}

// Update question.exam_id and fetch questions whenever the selected exam changes.
watch(selectedExamId, (id) => {
  if (id !== null) {
    question.value.exam_id = id
    fetchExamQuestions()
  } else {
    question.value.exam_id = null
    examQuestions.value = []
  }
})

const fetchStudents = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/users/students', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      studentsList.value = (await response.json()) || []
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load students' })
  }
}

const fetchExams = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/exams', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      exams.value = (await response.json()) || []
    } else {
      throw await toApiError(response, 'Failed to fetch exams')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to fetch exams' })
  }
}

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
      newExamTitle.value = ''
      fetchExams()
      notify('Exam created successfully', 'success')
    } else {
      throw await toApiError(response, 'Failed to create exam')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create exam' })
  }
})

const { inFlight: toggleExamActiveLoading, run: toggleExamActive } = useSingleFlight(async (id: number, active: boolean) => {
  const token = localStorage.getItem('sql_quiz_token')
  // Phase 3 has its own dedicated endpoint that drops the per-exam DML sandbox
  // exactly once. Phase 1 activation goes through /activate with {active:true}.
  const url = active
    ? `/api/v1/lecturer/exams/${id}/activate`
    : `/api/v1/lecturer/exams/${id}/deactivate`
  const init: RequestInit = {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    }
  }
  if (active) {
    init.body = JSON.stringify({ active: true })
  }
  try {
    const response = await fetch(url, init)
    if (response.ok) {
      fetchExams()
      notify(`Exam ${active ? 'activated' : 'deactivated'}`, 'success')
    } else {
      const errData = await response.json()
      handleApiError(new Error(errData.error || 'Failed to update exam status'), { fallback: 'Failed to update exam status' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to update exam status' })
  }
})

// Per-exam activation mode state (keyed by exam id)
const examActivationMode = ref<Record<number, 'instant' | 'scheduled'>>({})
const examScheduledStart = ref<Record<number, string>>({})
const examScheduledEnd = ref<Record<number, string>>({})
const examDeactivateEndAt = ref<Record<number, string>>({})
const examActionLoading = ref<Record<number, boolean>>({})

function toISOString(localStr: string): string {
  if (!localStr) return ''
  return new Date(localStr).toISOString()
}

function effectiveStateBadgeClass(state: string): string {
  switch (state) {
    case 'active':    return 'success-btn'
    case 'scheduled': return 'badge-info'
    case 'ended':     return 'fail-btn'
    default:          return ''
  }
}

async function activateExam(id: number) {
  const token = localStorage.getItem('sql_quiz_token')
  const mode = examActivationMode.value[id] || 'instant'
  let body: any = { active: true, mode: 'instant' }
  if (mode === 'scheduled') {
    const startAt = examScheduledStart.value[id]
    if (!startAt) { notify('Please set a start time', 'error'); return }
    body = {
      active: true,
      mode: 'scheduled',
      scheduled_start_at: toISOString(startAt),
      scheduled_end_at: examScheduledEnd.value[id] ? toISOString(examScheduledEnd.value[id]) : undefined
    }
  }
  examActionLoading.value[id] = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${id}/activate`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify(body)
    })
    if (resp.ok) {
      notify('Exam activated', 'success')
      fetchExams()
    } else {
      const e = await resp.json()
      handleApiError(new Error(e.error || 'Failed to activate'), { fallback: 'Failed to activate exam' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to activate exam' })
  } finally {
    examActionLoading.value[id] = false
  }
}

async function deactivateExamInstant(id: number) {
  const ok = await confirm({
    title: 'Deactivate exam?',
    body: 'This will drop the exam sandbox immediately. Students cannot submit after this.',
    confirmLabel: 'Deactivate',
    danger: true
  })
  if (!ok) return
  const token = localStorage.getItem('sql_quiz_token')
  examActionLoading.value[id] = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${id}/deactivate`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ mode: 'instant' })
    })
    if (resp.ok) {
      notify('Exam deactivated', 'success')
      fetchExams()
    } else {
      const e = await resp.json()
      handleApiError(new Error(e.error || 'Failed to deactivate'), { fallback: 'Failed to deactivate exam' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to deactivate exam' })
  } finally {
    examActionLoading.value[id] = false
  }
}

async function deactivateExamScheduled(id: number) {
  const endAt = examDeactivateEndAt.value[id]
  if (!endAt) { notify('Please set an end time', 'error'); return }
  const token = localStorage.getItem('sql_quiz_token')
  examActionLoading.value[id] = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${id}/deactivate`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ mode: 'scheduled', scheduled_end_at: toISOString(endAt) })
    })
    if (resp.ok) {
      notify('Exam end time set', 'success')
      fetchExams()
    } else {
      const e = await resp.json()
      handleApiError(new Error(e.error || 'Failed to set end time'), { fallback: 'Failed to set end time' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to set end time' })
  } finally {
    examActionLoading.value[id] = false
  }
}

interface RosterStudent {
  student_id: number
  username: string
  student_number: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  assigned_at: string
}

const rosterStudents = ref<RosterStudent[]>([])

const fetchRoster = async () => {
  if (!selectedExamId.value) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${selectedExamId.value}/students`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      const data = await resp.json()
      rosterStudents.value = data.students || []
    }
  } catch (err) {
    // silently ignore
  }
}

// Fetch roster when selected exam changes
watch(selectedExamId, (id) => {
  rosterStudents.value = []
  if (id) fetchRoster()
})

const { inFlight: assignStudentsLoading, run: assignStudents } = useSingleFlight(async () => {
  if (!selectedExamId.value || selectedStudentIds.value.length === 0) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${selectedExamId.value}/students`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ student_ids: selectedStudentIds.value }),
    })
    if (response.ok) {
      notify('Students enrolled successfully', 'success')
      selectedStudentIds.value = []
      fetchRoster()
    } else {
      const errData = await response.json()
      handleApiError(new Error(errData.error || 'Failed to enroll students'), { fallback: 'Failed to enroll students' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to enroll students' })
  }
})

async function removeStudentFromRoster(studentId: number, studentName: string) {
  const ok = await confirm({
    title: 'Remove from roster?',
    body: studentName,
    confirmLabel: 'Remove',
    danger: true
  })
  if (!ok) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${selectedExamId.value}/students/${studentId}`, {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      notify('Student removed from roster', 'success')
      fetchRoster()
    } else {
      const e = await resp.json()
      handleApiError(new Error(e.error || 'Failed to remove student'), { fallback: 'Failed to remove student' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to remove student' })
  }
}

const { inFlight: saveQuestionLoading, run: saveQuestion } = useSingleFlight(async () => {
  if (question.value.exam_id == null) {
    notify('Please pick an Exam ID before saving a question', 'error')
    return
  }
  // In create-new mode the ID is auto-assigned (the dropdown offers only
  // existing questions or "create new"), so resolve it here rather than relying
  // on the dropdown's @change having fired. Edit mode keeps the existing ID.
  if (!isEditMode.value) {
    question.value.id = nextFreeId.value
  }
  if (question.value.id == null || question.value.id <= 0) {
    notify('Please enter a valid Question ID', 'error')
    return
  }
  questionValidation.markTouched('correct_sql')
  if (!questionValidation.validateAll()) return

  // Warn before overwriting an existing question when in create-new mode
  if (!isEditMode.value && examQuestions.value.some((q: any) => q.id === question.value.id)) {
    const ok = await confirm({
      title: 'Overwrite existing question?',
      body: `Question #${question.value.id} already exists and will be overwritten.`,
      confirmLabel: 'Overwrite',
      danger: true
    })
    if (!ok) return
  }

  const token = localStorage.getItem('sql_quiz_token')

  try {
    const response = await fetch('/api/v1/lecturer/question', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify(question.value),
    })

    const data = await response.json().catch(() => ({} as any))
    if (!response.ok) {
      throw new Error(data.error || 'Failed to save question')
    }

    // Surface Phase 0 capture outcome so the lecturer knows whether the question
    // is ready to ship without having to inspect the DB.
    lastCapture.value = {
      status: data.expected_result_status || '',
      error: data.capture_error || '',
      capturedAt: data.expected_result_captured_at || null
    }

    if (lastCapture.value.status === 'failed') {
      notify(`Question saved, but capture failed: ${lastCapture.value.error}`, 'error')
    } else if (lastCapture.value.status === 'captured') {
      notify('Question saved and Correct Result captured', 'success')
    } else {
      notify(data.message || 'Question saved', 'success')
    }

    fetchExamQuestions()
    localStorage.removeItem(requirementDraftKey.value)

    // Reset for the next question to avoid silent ON CONFLICT overwrites.
    const lastExamId = question.value.exam_id
    question.value = blankQuestion()
    question.value.exam_id = lastExamId
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save question' })
  }
})

const importSQL = ref('')

const { inFlight: importSchemaLoading, run: handleImportSchema } = useSingleFlight(async () => {
  if (!importSQL.value) return

  // Confirm dangerous destructive action before posting
  const ok = await confirm({
    title: 'Import schema — destructive action',
    body: 'This will drop and recreate all public tables and data. This cannot be undone. Proceed?',
    confirmLabel: 'Import',
    danger: true
  })
  if (!ok) return

  const token = localStorage.getItem('sql_quiz_token')

  try {
    const response = await fetch('/api/v1/lecturer/import-schema', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ sql: importSQL.value }),
    })

    if (!response.ok) {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to import schema')
    }

    const data = await response.json()
    notify(data.message, 'success')
    importSQL.value = ''
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to import schema' })
  }
})

interface ConsolidatedScore {
  student_id: number
  username: string
  student_number: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  exam_scores: Record<string, number>
  practice_total: number
  grand_total: number
}

const consolidatedScores = ref<ConsolidatedScore[]>([])
const loadingScores = ref(false)

const fetchScores = async () => {
  loadingScores.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/scores', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      consolidatedScores.value = await response.json()
    } else {
      handleApiError(new Error('Failed to fetch scores'), { fallback: 'Failed to fetch scores' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to fetch scores' })
  } finally {
    loadingScores.value = false
  }
  // Also fetch per-exam max scores for the denominators
  fetchExamMaxScores()
}

const examMaxScores = ref<Record<string, number>>({})

const fetchExamMaxScores = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch('/api/v1/lecturer/exams/max-scores', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      const data = await resp.json()
      examMaxScores.value = data.max_scores || {}
    }
  } catch (err) {
    // Non-fatal: just display '?' for max
  }
}

const exportCSV = () => {
  const token = localStorage.getItem('sql_quiz_token')
  window.open(`/api/v1/lecturer/export-scores?token=${token}`, '_blank')
}

const allExamTitles = computed(() => {
  const titles = new Set<string>()
  consolidatedScores.value.forEach(s => {
    Object.keys(s.exam_scores).forEach(t => titles.add(t))
  })
  return Array.from(titles).sort()
})

const activeTab = ref<'schema'|'questions'|'exams'|'assignments'|'results'>(
  (localStorage.getItem('lecturerTab') as any) || 'questions'
)
watch(activeTab, (v) => {
  localStorage.setItem('lecturerTab', v)
  if (v === 'results') {
    fetchScores()
  }
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Examination Authoring</h1>
      <p class="page-subtitle">Manage schemas, author questions, schedule exams, and review consolidated student results.</p>
    </div>

    <div class="tab-bar">
      <button :class="{ 'tab-active': activeTab === 'schema' }" @click="activeTab = 'schema'">Schema</button>
      <button :class="{ 'tab-active': activeTab === 'questions' }" @click="activeTab = 'questions'">Questions</button>
      <button :class="{ 'tab-active': activeTab === 'exams' }" @click="activeTab = 'exams'">Exams</button>
      <button :class="{ 'tab-active': activeTab === 'assignments' }" @click="activeTab = 'assignments'">{{ UI_COPY.EXAM_ROSTER }}</button>
      <button :class="{ 'tab-active': activeTab === 'results' }" @click="activeTab = 'results'">Results</button>
    </div>

    <div v-show="activeTab === 'schema'" class="lecturer-form card">
      <h2>Initialize Exam Database</h2>
      <p class="desc">Paste the full SQL schema script (e.g. company.sql) to set up the base tables and data.</p>
      <SqlEditor v-model="importSQL" height="300px" style="margin-bottom: 1rem;" />
      <button @click="handleImportSchema" :disabled="importSchemaLoading || !importSQL" class="secondary-btn">
<Spinner v-if="importSchemaLoading" size="sm" inline />{{ importSchemaLoading ? 'Importing' : 'Import Schema & Data' }}
      </button>
    </div>

    <div v-show="activeTab === 'questions'" class="lecturer-form card">
      <h2>Manage Questions</h2>
      
      <div class="context-selector">
        <div class="input-group">
          <label>Working Exam:</label>
          <select v-model.number="selectedExamId">
            <option :value="null">-- Select an Exam to Author Questions --</option>
            <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }} (ID: {{ e.id }})</option>
          </select>
        </div>
        
        <div v-if="activeExam" class="exam-context-status">
          <div class="status-info">
            <span class="status-label">Status:</span>
            <span class="badge" :class="activeExam.is_active ? 'captured' : 'failed'">
              {{ activeExam.is_active ? 'Active' : 'Inactive' }}
            </span>
          </div>
          <button 
            @click="toggleExamActive(activeExam.id, !activeExam.is_active)" 
            :class="activeExam.is_active ? 'fail-btn' : 'success-btn'"
            class="toggle-btn"
            :disabled="toggleExamActiveLoading"
          >
            {{ activeExam.is_active ? UI_COPY.DEACTIVATE_EXAM : UI_COPY.ACTIVATE_EXAM }}
          </button>
        </div>
      </div>

      <EmptyState v-if="!selectedExamId" title="No exam selected" body="Select an exam from the dropdown above to start authoring questions." />

      <template v-else>
        <div ref="questionFormRef">
        <div class="form-grid">
          <div class="input-group">
            <label>Question ID:</label>
            <select @change="onQuestionDropdownChange" :value="isEditMode ? question.id : ''" >
              <option value="">Create new question (ID: {{ nextFreeId }})</option>
              <option v-for="q in examQuestions" :key="q.id" :value="q.id">
                Q{{ q.id }} — {{ (q.requirement || '').slice(0, 50) }}
              </option>
            </select>
            <span v-if="isEditMode" class="badge badge-info" style="margin-top: var(--space-1); font-size: 0.75rem;">Editing #{{ question.id }}</span>
            <span v-else class="muted" style="font-size: 0.75rem; margin-top: var(--space-1);">New question (ID will be {{ nextFreeId }})</span>
          </div>

          <div class="input-group">
            <label>Exam ID:</label>
            <input type="number" v-model.number="question.exam_id" disabled />
          </div>

          <div class="input-group">
            <label>Type:</label>
            <select v-model="question.type">
              <option value="QUERY">Query (SELECT)</option>
              <option value="DML">DML (Insert/Update/Delete)</option>
            </select>
          </div>

          <div class="input-group" v-if="question.type === 'DML'">
            <label>Target Table:</label>
            <input type="text" v-model="question.target_table" placeholder="e.g. employee" />
          </div>

          <div class="input-group">
            <label>Points:</label>
            <input
              type="number"
              v-model.number="question.points"
              :aria-invalid="questionValidation.touched.points && !!questionValidation.errors.points"
              :aria-describedby="'points-error'"
              @blur="questionValidation.markTouched('points')"
            />
            <FieldError id="points-error" :message="questionValidation.touched.points ? questionValidation.errors.points : ''" />
          </div>
        </div>

        <div v-if="showDraftBanner" class="draft-banner">
          A draft from your previous session is available.
          <button type="button" class="btn btn-sm" @click="acceptDraft">Restore</button>
          <button type="button" class="btn btn-sm" @click="dismissDraft">Discard</button>
        </div>
        <div class="input-group full-width">
          <label>Question Requirement:</label>
          <textarea
            v-model="question.requirement"
            rows="3"
            placeholder="Describe what the student should do"
            :aria-invalid="questionValidation.touched.requirement && !!questionValidation.errors.requirement"
            aria-describedby="err-requirement"
            @blur="questionValidation.markTouched('requirement')"
          ></textarea>
          <FieldError :message="questionValidation.touched.requirement ? questionValidation.errors.requirement : ''" id="err-requirement" />
        </div>

        <div class="input-group full-width">
          <label>Correct SQL:</label>
          <SqlEditor v-model="question.correct_sql" height="200px" />
          <FieldError id="correct-sql-error" :message="questionValidation.touched.correct_sql ? questionValidation.errors.correct_sql : ''" />
          <button
            @click="question.correct_sql = formatSql(question.correct_sql)"
            :disabled="!question.correct_sql"
            class="secondary-btn"
            style="margin-top: 0.5rem; width: auto; padding: 0.5rem 1rem; margin-right: 0.5rem;"
          >Format SQL</button>
          <button
            @click="previewCorrectSQL"
            :disabled="previewLoading || !question.correct_sql"
            class="secondary-btn"
            style="margin-top: 0.5rem; width: auto; padding: 0.5rem 1rem;"
          >
            {{ previewLoading ? 'Running...' : 'Preview Correct SQL Result' }}
          </button>
        </div>

        <div v-if="previewResult.status" class="preview-block" :class="previewResult.status">
          <strong v-if="previewResult.status === 'ok'">Preview ({{ previewResult.rows.length }} row{{ previewResult.rows.length === 1 ? '' : 's' }})</strong>
          <strong v-else>Preview failed</strong>
          <div v-if="previewResult.status === 'error'" class="preview-error">{{ previewResult.error }}</div>
          <div v-else-if="previewResult.rows.length === 0" class="preview-empty">(no rows returned)</div>
          <div v-else class="preview-table-wrap sticky-table-head">
            <table class="preview-table">
              <thead>
                <tr><th v-for="c in previewResult.columns" :key="c">{{ c }}</th></tr>
              </thead>
              <tbody>
                <tr v-for="(row, i) in previewResult.rows" :key="i">
                  <td v-for="c in previewResult.columns" :key="c">{{ row[c] }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="strict-mode-section">
          <h3>Strict Mode Options</h3>
          <div class="checkbox-group">
            <label><input type="checkbox" v-model="question.strict_mode.ordering" /> Row Ordering <span class="hint">(rows must match the teacher's order; off means set-equality)</span></label>
            <label><input type="checkbox" v-model="question.strict_mode.attributes" /> Exact Attributes <span class="hint">(column names must match)</span></label>
          </div>
        </div>

        <button @click="saveQuestion" :disabled="saveQuestionLoading" class="primary-btn">
          <Spinner v-if="saveQuestionLoading" size="sm" inline :invert="true" />{{ saveQuestionLoading ? 'Saving' : 'Save Question' }}
        </button>

        <div v-if="lastCapture.status" class="capture-status" :class="lastCapture.status">
          <strong v-if="lastCapture.status === 'captured'">Correct Result captured</strong>
          <strong v-else-if="lastCapture.status === 'failed'">Capture failed</strong>
          <strong v-else>Status: {{ lastCapture.status }}</strong>
          <div v-if="lastCapture.error" class="capture-error">{{ lastCapture.error }}</div>
          <div v-if="lastCapture.capturedAt" class="capture-time">Captured at: {{ lastCapture.capturedAt }}</div>
        </div>
        </div><!-- /questionFormRef -->

        <!-- Exam Description editor — shown whenever an exam is selected -->
        <div v-if="selectedExamId" style="margin-top: var(--space-5); border-top: 2px solid var(--color-border); padding-top: var(--space-5);">
          <h3 style="margin: 0 0 var(--space-2) 0;">Exam Description</h3>
          <p class="muted" style="margin: 0 0 var(--space-2) 0; font-size: 0.85rem;">This text is shown to enrolled students on the intro page before they begin the exam.</p>
          <textarea
            v-model="examIntroductionText"
            class="input"
            rows="4"
            style="width: 100%; resize: vertical;"
            placeholder="Describe the exam's database structure or any context students should know..."
          ></textarea>
          <div style="margin-top: var(--space-2);">
            <button class="btn-sm" :disabled="examIntroSaving" @click="saveExamIntroduction">
              {{ examIntroSaving ? 'Saving...' : 'Save Description' }}
            </button>
          </div>
          <IntroImagesEditor scope="exam" :owner-id="selectedExamId!" />
        </div>

        <EmptyState v-if="selectedExamId && examQuestions.length === 0" :compact="true" title="No questions in this exam yet" body="Use the form above to add the first question." />
        <div v-if="examQuestions.length > 0" class="questions-list-section" style="margin-top: 2rem; border-top: 2px solid var(--color-border); padding-top: 1rem;">
          <h3 style="margin-bottom: 1rem;">Questions in Exam {{ question.exam_id }} <span class="muted" style="font-size: 0.8rem; font-weight: 400;">({{ displayedQuestions.length }} of {{ examQuestions.length }})</span></h3>
          <input v-model="filterText" placeholder="Filter by requirement..." class="input" style="margin-bottom: var(--space-2); width: 100%;" />
          <div class="preview-table-wrap sticky-table-head">
            <table class="preview-table">
              <thead>
                <tr>
                  <th @click="toggleSort('id')" style="cursor: pointer;">ID {{ sortKey === 'id' ? (sortDir === 'asc' ? '↑' : '↓') : '' }}</th>
                  <th @click="toggleSort('type')" style="cursor: pointer;">Type {{ sortKey === 'type' ? (sortDir === 'asc' ? '↑' : '↓') : '' }}</th>
                  <th @click="toggleSort('points')" style="cursor: pointer;">Points {{ sortKey === 'points' ? (sortDir === 'asc' ? '↑' : '↓') : '' }}</th>
                  <th>Requirement</th>
                  <th>Capture</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="q in displayedQuestions" :key="q.id">
                  <td>{{ q.id }}</td>
                  <td>{{ q.question_type }}</td>
                  <td>{{ q.points }}</td>
                  <td style="max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">
                    {{ q.requirement }}
                  </td>
                  <td>
                    <span class="badge" :class="q.expected_result_status">
                      {{ q.expected_result_status }}
                    </span>
                  </td>
                  <td>
                    <div style="display: flex; gap: 4px;">
                      <button class="btn-sm" @click="editQuestion(q)">Edit</button>
                      <button class="btn-sm btn-danger" :disabled="deleteQuestionLoading" @click="deleteQuestion(q.id)"><Spinner v-if="deleteQuestionLoading" size="sm" inline />Delete</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>

    <div v-show="activeTab === 'exams'" class="lecturer-form card">
      <h2>Examination Events</h2>
      <div class="form-grid">
        <div class="input-group">
          <label>New Exam Title:</label>
          <input type="text" v-model="newExamTitle" placeholder="Midterm 2026" aria-label="New exam title" />
        </div>
        <button @click="createExam" :disabled="createExamLoading" class="secondary-btn" style="margin-top: 1.8rem;">
          <Spinner v-if="createExamLoading" size="sm" inline />{{ createExamLoading ? 'Creating' : 'Create Exam' }}
        </button>
      </div>

      <EmptyState v-if="exams.length === 0" :compact="true" title="No exams yet" body="Create your first exam above." />
      <div class="exam-list" v-if="exams.length > 0">
        <h3>Existing Exams</h3>
        <div v-for="e in exams" :key="e.id" class="exam-item exam-item-extended">
          <div class="exam-item-header">
            <span>{{ e.title }} (ID: {{ e.id }})</span>
            <span class="badge" :class="effectiveStateBadgeClass(e.effective_state)" style="font-size: 0.75rem; padding: 2px 8px;">
              {{ e.effective_state || (e.is_active ? 'active' : 'inactive') }}
            </span>
          </div>
          <div class="exam-item-meta muted" v-if="e.scheduled_start_at">
            Starts: {{ new Date(e.scheduled_start_at).toLocaleString() }}
          </div>
          <div class="exam-item-meta muted" v-if="e.scheduled_end_at">
            Ends: {{ new Date(e.scheduled_end_at).toLocaleString() }}
          </div>
          <!-- Activation controls for inactive/scheduled exams -->
          <div v-if="e.effective_state === 'inactive' || e.effective_state === 'scheduled' || !e.is_active" class="exam-activation-controls">
            <label style="display: flex; align-items: center; gap: 0.5rem; cursor: pointer;">
              <input type="radio" :name="'mode-' + e.id" value="instant"
                :checked="(examActivationMode[e.id] || 'instant') === 'instant'"
                @change="examActivationMode[e.id] = 'instant'" />
              Activate now
            </label>
            <label style="display: flex; align-items: center; gap: 0.5rem; cursor: pointer;">
              <input type="radio" :name="'mode-' + e.id" value="scheduled"
                :checked="examActivationMode[e.id] === 'scheduled'"
                @change="examActivationMode[e.id] = 'scheduled'" />
              Schedule activation
            </label>
            <template v-if="examActivationMode[e.id] === 'scheduled'">
              <div style="display: flex; gap: 1rem; flex-wrap: wrap; margin-top: 0.5rem;">
                <div class="input-group" style="flex: 1; min-width: 200px;">
                  <label>Start time:</label>
                  <input type="datetime-local" v-model="examScheduledStart[e.id]" />
                </div>
                <div class="input-group" style="flex: 1; min-width: 200px;">
                  <label>End time (optional):</label>
                  <input type="datetime-local" v-model="examScheduledEnd[e.id]" />
                </div>
              </div>
            </template>
            <button class="success-btn" :disabled="examActionLoading[e.id]" @click="activateExam(e.id)" style="margin-top: 0.5rem;">
              Activate
            </button>
          </div>
          <!-- Deactivation controls for active/ended exams -->
          <div v-else-if="e.is_active" class="exam-activation-controls">
            <div style="display: flex; gap: 0.5rem; flex-wrap: wrap;">
              <button class="fail-btn" :disabled="examActionLoading[e.id]" @click="deactivateExamInstant(e.id)">
                Deactivate Now
              </button>
              <div style="display: flex; align-items: center; gap: 0.5rem;">
                <input type="datetime-local" v-model="examDeactivateEndAt[e.id]" style="border: 1px solid var(--color-border); padding: 0.3rem;" />
                <button class="secondary-btn" :disabled="examActionLoading[e.id]" @click="deactivateExamScheduled(e.id)">
                  Set End Time
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-show="activeTab === 'assignments'" class="lecturer-form card">
      <h2>Enroll Students in Exam</h2>
      <div class="form-grid">
        <div class="input-group">
          <label>Select Exam:</label>
          <select v-model.number="selectedExamId">
            <option :value="null">-- Select Exam --</option>
            <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }} (ID: {{ e.id }})</option>
          </select>
        </div>
        <div class="input-group full-width">
          <label>Select Students:</label>
          <StudentMultiSelect v-model="selectedStudentIds" :students="studentsList" />
        </div>
      </div>
      <button @click="assignStudents" :disabled="assignStudentsLoading || !selectedExamId || selectedStudentIds.length === 0" class="primary-btn" style="width: auto; padding: 0.5rem 1rem;">
  <Spinner v-if="assignStudentsLoading" size="sm" inline :invert="true" />{{ assignStudentsLoading ? 'Enrolling' : `Enroll ${selectedStudentIds.length} Students` }}
      </button>

      <details class="import-roster-panel">
        <summary>Import Roster</summary>
        <BulkImportStudents />
      </details>

      <!-- Roster table -->
      <div v-if="selectedExamId" style="margin-top: var(--space-5);">
        <h3>Enrolled: {{ rosterStudents.length }} student{{ rosterStudents.length !== 1 ? 's' : '' }}</h3>
        <EmptyState v-if="rosterStudents.length === 0" :compact="true" title="No students enrolled" body="Use the form above to enroll students." />
        <table v-else class="roster-table" style="width: 100%; border-collapse: collapse;">
          <thead>
            <tr>
              <th>Student #</th>
              <th>Name</th>
              <th>Sec Lec / Lab</th>
              <th>Enrolled At</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in rosterStudents" :key="s.student_id">
              <td>{{ s.student_number || s.username }}</td>
              <td>{{ s.first_name }} {{ s.last_name }}</td>
              <td>{{ s.sec_lec }} / {{ s.sec_lab }}</td>
              <td>{{ new Date(s.assigned_at).toLocaleDateString() }}</td>
              <td>
                <button
                  class="btn-sm fail-btn"
                  :disabled="activeExam?.effective_state === 'active'"
                  :title="activeExam?.effective_state === 'active' ? 'Cannot remove during active exam' : 'Remove from roster'"
                  @click="removeStudentFromRoster(s.student_id, s.first_name + ' ' + s.last_name)"
                >
                  Remove
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-show="activeTab === 'results'" class="lecturer-form card">
      <div class="results-header">
        <h2>Consolidated Student Performance</h2>
        <button @click="exportCSV" class="secondary-btn" style="margin: 0;">Export Scores (CSV)</button>
      </div>

      <Skeleton v-if="loadingScores" variant="table-row" :rows="5" :cols="6" />
      <EmptyState v-else-if="!loadingScores && consolidatedScores.length === 0" title="No scores yet" body="Once students submit answers, their scores appear here." />
      <div v-else class="preview-table-wrap sticky-table-head">
        <table class="preview-table">
          <thead>
            <tr>
              <th>Std #</th>
              <th>Username</th>
              <th>Name</th>
              <th>Sec (Lec/Lab)</th>
              <th v-for="title in allExamTitles" :key="title">
                {{ title }}<br /><span class="muted" style="font-size: 0.75rem; font-weight: 400;">(max: {{ examMaxScores[title] !== undefined ? examMaxScores[title].toFixed(0) : '?' }})</span>
              </th>
              <th>Practice</th>
              <th>Total</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in consolidatedScores" :key="s.student_id">
              <td>{{ s.student_number || '—' }}</td>
              <td>{{ s.username }}</td>
              <td>{{ s.first_name }} {{ s.last_name }}</td>
              <td>{{ s.sec_lec }} / {{ s.sec_lab }}</td>
              <td v-for="title in allExamTitles" :key="title">
                <span>{{ (s.exam_scores[title] ?? 0).toFixed(2) }} / {{ examMaxScores[title] !== undefined ? examMaxScores[title].toFixed(0) : '?' }}</span>
                <br />
                <span class="muted" style="font-size: 0.8rem;">
                  {{ examMaxScores[title] && examMaxScores[title] > 0 ? Math.round(((s.exam_scores[title] ?? 0) / examMaxScores[title]) * 100) + '%' : '—' }}
                </span>
              </td>
              <td style="font-weight: 800;">{{ s.practice_total.toFixed(2) }}</td>
              <td style="font-weight: 900; color: var(--color-primary);">{{ s.grand_total.toFixed(2) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
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

.context-selector {
  display: flex;
  justify-content: space-between;
  align-items: flex-end;
  gap: var(--space-4);
  padding-bottom: var(--space-4);
  border-bottom: 1px solid var(--color-border-soft);
  margin-bottom: var(--space-4);
}

.context-selector .input-group {
  flex: 1;
}

.exam-context-status {
  display: flex;
  align-items: center;
  gap: var(--space-4);
}

.status-info {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.status-label {
  font-size: 0.75rem;
  font-weight: 700;
  margin-bottom: 4px;
}

.toggle-btn {
  white-space: nowrap;
}

.empty-state {
  padding: var(--space-7) 0;
  text-align: center;
  background: var(--color-surface-muted);
  border: 1px dashed var(--color-border);
  border-radius: var(--radius);
  font-weight: 500;
  font-size: 0.95rem;
  color: var(--color-text-muted);
}

.desc {
  color: var(--color-text-muted);
  margin-bottom: var(--space-3);
  font-size: 0.88rem;
}

.multi-select-box {
  max-height: 250px;
  overflow-y: auto;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-3);
  background: var(--color-surface);
}

.student-item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 0.5rem 0;
  border-bottom: 1px solid var(--color-border);
}

.student-item:last-child { border-bottom: none; }

.student-item label {
  margin-bottom: 0;
  cursor: pointer;
  font-weight: 700;
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

.success-btn {
  background-color: var(--color-success-bg);
  color: var(--color-success);
  border: 2px solid var(--color-success);
  padding: 0.5rem var(--space-4);
  cursor: pointer;
  font-weight: 700;
  transition: none;
}

.fail-btn {
  background-color: var(--color-danger-bg);
  color: var(--color-danger);
  border: 2px solid var(--color-danger);
  padding: 0.5rem var(--space-4);
  cursor: pointer;
  font-weight: 700;
  transition: none;
}

.success-btn:active, .fail-btn:active {
  transform: translateY(1px);
}

.exam-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 14px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  margin-bottom: var(--space-2);
  background: var(--color-surface);
}
.exam-item:hover { border-color: var(--color-primary); background: var(--color-primary-bg); }

.exam-list { margin-top: var(--space-4); }

.import-roster-panel {
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-4);
  margin-top: var(--space-4);
  background: var(--color-surface-muted);
}

.import-roster-panel summary {
  cursor: pointer;
  font-weight: 800;
  color: var(--color-text);
  font-size: 1.1rem;
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

.full-width {
  grid-column: span 2;
}

label {
  font-weight: 700;
  margin-bottom: 0.4rem;
  font-size: 0.95rem;
  color: var(--color-text);
}

input, select, textarea {
  padding: 0.6rem;
  border: 2px solid var(--color-border);
  border-radius: 0;
  font-weight: 600;
  font-family: var(--font-mono);
  background: var(--color-surface);
  color: var(--color-text);
}

textarea {
  min-height: 120px;
}

.strict-mode-section {
  border-top: 2px solid var(--color-border);
  padding-top: var(--space-5);
}

.checkbox-group {
  display: flex;
  gap: var(--space-6);
}

.checkbox-group label {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-weight: 700;
  cursor: pointer;
}

.primary-btn {
  padding: var(--space-3) var(--space-4);
  background-color: var(--color-primary);
  color: var(--color-surface);
  border: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  font-size: 1rem;
  transition: none;
}

.primary-btn:hover {
  background-color: #333333;
}

.primary-btn:active {
  transform: translateY(1px);
}

.capture-status {
  margin-top: var(--space-3);
  padding: 10px 14px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  font-weight: 500;
}
.capture-status.captured { background: var(--color-success-bg); color: var(--color-success); border-color: #c4dfba; }
.capture-status.failed   { background: var(--color-danger-bg);  color: var(--color-danger);  border-color: #e9bcbc; }

.preview-block {
  margin-top: var(--space-3);
  padding: 10px 14px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
}
.preview-block.ok    { background: var(--color-surface); color: var(--color-text); }
.preview-block.error { background: var(--color-danger-bg); color: var(--color-danger); border-color: #e9bcbc; }
.preview-error { margin-top: 6px; font-family: var(--font-mono); font-size: 0.82rem; white-space: pre-wrap; }
.preview-table-wrap { margin-top: 10px; overflow-x: auto; border: 1px solid var(--color-border); border-radius: var(--radius); background: var(--color-surface); }
.preview-table { width: 100%; border-collapse: collapse; font-size: 0.86rem; background: var(--color-surface); color: var(--color-text); }
.preview-table th, .preview-table td { border-bottom: 1px solid var(--color-border-soft); padding: 6px 10px; text-align: left; }
.preview-table th { background: var(--color-surface-sunk); color: var(--color-text); font-weight: 700; font-size: 0.74rem; text-transform: uppercase; letter-spacing: 0.04em; }
.preview-table tr:nth-child(even) td { background: var(--color-surface-muted); }

.results-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--space-4);
}

.badge {
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-size: 0.75rem;
  font-weight: 700;
  display: inline-block;
}
.badge.captured { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.badge.failed { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
.badge.pending { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }

.btn-sm {
  padding: 4px 12px;
  font-size: 0.8rem;
  font-weight: 700;
  cursor: pointer;
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
}
.btn-sm:hover { background: var(--color-surface-muted); }
.btn-sm:active { transform: translateY(1px); }

.btn-danger {
  background: var(--color-danger);
  color: var(--color-surface);
  border-color: var(--color-danger);
}

h2 {
  color: var(--color-text);
  font-weight: 700;
  margin: 0 0 var(--space-3) 0;
  font-size: 1.1rem;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
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
.tab-bar button:hover { color: var(--color-text); }
.tab-bar button.tab-active {
  background: var(--color-surface);
  color: var(--color-primary);
  border-color: var(--color-border);
  box-shadow: var(--shadow-1);
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

  .context-selector {
    flex-direction: column;
    align-items: stretch;
  }

  .exam-context-status {
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
  }

  .results-header {
    flex-direction: column;
    align-items: stretch;
  }
}

.draft-banner {
  border: 1px dashed var(--color-border);
  background: var(--color-surface-muted);
  font-family: var(--font-mono);
  padding: var(--space-2) var(--space-3);
  display: flex;
  gap: var(--space-2);
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: var(--space-2);
  font-size: 0.85rem;
}
</style>
