<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import type { Examination, CaptureStatus, PreviewResult } from './types'
import SqlEditor from '../SqlEditor.vue'
import IntroImagesEditor from '../IntroImagesEditor.vue'
import FieldError from '../FieldError.vue'
import Spinner from '../Spinner.vue'
import EmptyState from '../EmptyState.vue'
import Icon from '../Icon.vue'
import { useSingleFlight } from '../../composables/useSingleFlight'
import { useConfirm } from '../../services/confirm_service'
import { useNotifications } from '../../services/notification_service'
import { handleApiError } from '../../services/error_handler'
import { useFormValidation, required } from '../../composables/useFormValidation'
import { useKeyboardShortcut } from '../../composables/useKeyboardShortcut'
import { formatSql } from '../../utils/format_sql'
import { setExamIntroduction, importExamCurriculumPreview } from '../../services/exam'
import { ApiError } from '../../services/practice'
import { listExamIntroImages, examImageBlobUrl, type IntroImageMetadata } from '../../services/schemaImages'

const props = defineProps<{ exam: Examination }>()
const emit = defineEmits<{ changed: [] }>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

// Independent client-side check (dual-schema spec §6.1): dual_schema on an
// exam with no decoy curriculum is allowed (grading falls back to schema B)
// but must be flagged to the lecturer with zero network round-trips. Purely
// derived from props.exam, so it reacts automatically to switching exams,
// toggling question.value.dual_schema, and loading a question into the form
// (editQuestion / onQuestionDropdownChange) — no extra watcher needed.
const examHasDecoy = computed(() => props.exam.has_preview_schema ?? false)

const blankQuestion = () => ({
  id: null as number | null,
  exam_id: null as string | null,
  target_table: '',
  correct_sql: '',
  requirement: '',
  type: 'QUERY',
  points: 10,
  dual_schema: false,
  strict_mode: {
    ordering: false,
    attributes: false,
    column_order: false
  }
})

const question = ref(blankQuestion())

// Form validation for question fields
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
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/questions`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      examQuestions.value = await response.json()
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam questions' })
  }
}

watch(() => props.exam.id, fetchExamQuestions, { immediate: true })

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
      emit('changed')
    } else {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to delete')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to delete question' })
  }
})

const isEditMode = ref(false)

const previewResult = ref<PreviewResult>({ status: '', rows: [], columns: [], error: '' })
const previewLoading = ref(false)
const questionFormRef = ref<HTMLElement | null>(null)

// "Preview as student" (schema: 'preview') results — kept in a separate set
// of refs from previewResult above so the lecturer can see both the
// student's decoy-schema view (A) and the grading-schema view (B) side by side.
const previewAsStudentResult = ref<PreviewResult>({ status: '', rows: [], columns: [], error: '' })
const previewAsStudentLoading = ref(false)

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
    dual_schema: q.dual_schema || false,
    strict_mode: {
      ordering: q.strict_mode?.ordering || false,
      attributes: q.strict_mode?.attributes || false,
      column_order: q.strict_mode?.column_order || false
    }
  }
  lastCapture.value = {
    status: q.expected_result_status || '',
    error: q.capture_error || '',
    capturedAt: q.expected_result_captured_at || null
  }
  previewResult.value = { status: '', rows: [], columns: [], error: '' }
  previewAsStudentResult.value = { status: '', rows: [], columns: [], error: '' }
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

function onQuestionDropdownChange(event: Event) {
  const val = (event.target as HTMLSelectElement).value
  if (val === '') {
    // Create new question — id=0 tells the backend to auto-assign the PK on
    // insert (repository/postgres_questions.go UpsertQuestion: q.ID==0 => INSERT).
    isEditMode.value = false
    question.value = {
      id: 0,
      exam_id: props.exam.id,
      target_table: '',
      correct_sql: '',
      requirement: '',
      type: 'QUERY',
      points: 10,
      dual_schema: false,
      strict_mode: { ordering: false, attributes: false, column_order: false }
    }
    lastCapture.value = { status: '', error: '', capturedAt: null }
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
    previewAsStudentResult.value = { status: '', rows: [], columns: [], error: '' }
  } else {
    const q = examQuestions.value.find((q: any) => q.id === Number(val))
    if (q) editQuestion(q)
  }
}

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
        exam_id: props.exam.id,
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

// Runs the same correct_sql against the decoy curriculum (schema: 'preview')
// so the lecturer can see exactly what the student sees on a dual-schema
// question. Same body as previewCorrectSQL above, plus schema: 'preview'.
const previewAsStudent = async () => {
  if (!question.value.correct_sql) {
    notify('Enter the Correct SQL before previewing', 'error')
    return
  }
  if (question.value.type === 'DML' && !question.value.target_table) {
    notify('Target table is required to preview a DML question', 'error')
    return
  }
  previewAsStudentLoading.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/preview', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        exam_id: props.exam.id,
        type: question.value.type,
        target_table: question.value.target_table,
        correct_sql: question.value.correct_sql,
        schema: 'preview'
      })
    })
    const data = await response.json().catch(() => ({} as any))
    if (!response.ok) {
      throw new Error(data.error || 'Preview failed')
    }
    if (data.status === 'error') {
      previewAsStudentResult.value = { status: 'error', rows: [], columns: [], error: data.error || 'SQL error' }
    } else {
      previewAsStudentResult.value = {
        status: 'ok',
        rows: data.rows || [],
        columns: data.columns || [],
        error: ''
      }
    }
  } catch (err) {
    previewAsStudentResult.value = { status: 'error', rows: [], columns: [], error: (err as Error).message || 'Preview failed' }
    handleApiError(err, { fallback: 'Preview failed' })
  } finally {
    previewAsStudentLoading.value = false
  }
}

// --- Decoy curriculum (preview only) import — same tables/columns as the
// grading curriculum, different data; used by dual-schema questions so the
// student's preview never leaks the grading data. Mirrors the pattern in
// the read-only-reference ExamSchemaPanel.vue (SqlEditor + single-flight +
// 409-then-confirm-replace), replicated locally since that file is out of
// this TODO's scope. ---
const importPreviewSql = ref('')

const { inFlight: importPreviewLoading, run: doImportPreview } = useSingleFlight(async () => {
  if (!importPreviewSql.value.trim()) return
  try {
    const res = await importExamCurriculumPreview(props.exam.id, importPreviewSql.value, false)
    notify('Decoy curriculum imported', 'success')
    if (res.parity_warning) notify(res.parity_warning, 'error')
    importPreviewSql.value = ''
    emit('changed')
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      if (err.message.toLowerCase().includes('active')) {
        handleApiError(err, { fallback: 'Cannot import decoy curriculum for an active exam' })
        return
      }
      const ok = await confirm({
        title: 'Replace existing decoy curriculum?',
        body: 'This exam already has a decoy curriculum. Importing will drop the existing tables and replace them. Continue?',
        confirmLabel: 'Replace',
        danger: true,
      })
      if (!ok) return
      try {
        const res2 = await importExamCurriculumPreview(props.exam.id, importPreviewSql.value, true)
        notify('Decoy curriculum replaced', 'success')
        if (res2.parity_warning) notify(res2.parity_warning, 'error')
        importPreviewSql.value = ''
        emit('changed')
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to import decoy curriculum' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to import decoy curriculum' })
  }
})

// Exam description editor state
const examIntroductionText = ref('')
const examIntroSaving = ref(false)

// Pre-populate the description textarea when the exam prop changes.
watch(() => props.exam, e => { examIntroductionText.value = e?.introduction ?? '' }, { immediate: true })

const saveExamIntroduction = async () => {
  examIntroSaving.value = true
  try {
    await setExamIntroduction(props.exam.id, examIntroductionText.value)
    notify('Exam description saved', 'success')
    emit('changed')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save exam description' })
  } finally {
    examIntroSaving.value = false
  }
}

// --- Inline intro preview ---
const showIntroPreview = ref(false)
const previewImages = ref<IntroImageMetadata[]>([])
const previewThumbUrls = ref<Record<string, string>>({})

async function loadPreviewImages() {
  // Revoke stale blob URLs before re-fetching
  for (const url of Object.values(previewThumbUrls.value)) {
    URL.revokeObjectURL(url)
  }
  previewThumbUrls.value = {}
  previewImages.value = []
  try {
    const imgs = await listExamIntroImages(props.exam.id)
    previewImages.value = imgs
    await Promise.all(
      imgs.map(async (img) => {
        try {
          previewThumbUrls.value[img.id] = await examImageBlobUrl(props.exam.id, img.id)
        } catch {
          // Non-fatal: image fails to load
        }
      })
    )
  } catch {
    // Non-fatal: image list fails to load
  }
}

async function toggleIntroPreview() {
  showIntroPreview.value = !showIntroPreview.value
  if (showIntroPreview.value) {
    await loadPreviewImages()
  }
}

onUnmounted(() => {
  for (const url of Object.values(previewThumbUrls.value)) {
    URL.revokeObjectURL(url)
  }
  previewThumbUrls.value = {}
})

// --- Autosave for question requirement ---
const requirementDraftKey = computed(() =>
  'sql_quiz_draft.lecturer.questionRequirement.exam-' + props.exam.id
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

onUnmounted(() => {
  if (requirementDebounceTimer != null) window.clearTimeout(requirementDebounceTimer)
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

const { inFlight: saveQuestionLoading, run: saveQuestion } = useSingleFlight(async () => {
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
      body: JSON.stringify({ ...question.value, exam_id: props.exam.id }),
    })

    const data = await response.json().catch(() => ({} as any))
    if (!response.ok) {
      throw new Error(data.error || 'Failed to save question')
    }

    // Adopt the server-assigned real id (create sent id=0; the backend
    // auto-assigns the PK on insert and returns it here).
    if (data.id != null) question.value.id = data.id

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

    // Non-blocking warnings from the backend — never prevent the save flow below.
    if (data.table_warning) notify(data.table_warning, 'error')
    if (data.dual_schema_warning) notify(data.dual_schema_warning, 'error')
    if (data.mixed_dual_warning) notify(data.mixed_dual_warning, 'error')

    fetchExamQuestions()
    localStorage.removeItem(requirementDraftKey.value)
    emit('changed')

    // Reset for the next question to avoid silent ON CONFLICT overwrites.
    question.value = blankQuestion()
    question.value.exam_id = props.exam.id
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
    previewAsStudentResult.value = { status: '', rows: [], columns: [], error: '' }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save question' })
  }
})
</script>

<template>
  <div>
    <div ref="questionFormRef">
      <div class="form-grid">
        <div class="input-group">
          <label>Question ID:</label>
          <select @change="onQuestionDropdownChange" :value="isEditMode ? question.id : ''">
            <option value="">Create new question</option>
            <option v-for="q in examQuestions" :key="q.id" :value="q.id">
              Q{{ q.id }} — {{ (q.requirement || '').slice(0, 50) }}
            </option>
          </select>
          <span v-if="isEditMode" class="badge badge-info" style="margin-top: var(--space-1); font-size: 0.75rem;">Editing #{{ question.id }}</span>
          <span v-else class="muted" style="font-size: 0.75rem; margin-top: var(--space-1);">New question - ID assigned on save</span>
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

        <!-- Dual schema applies to both QUERY and DML, so it is not gated by
             question.type like the Target Table field above. -->
        <div class="input-group">
          <label><input type="checkbox" v-model="question.dual_schema" /> Dual schema (hidden grading)</label>
          <!-- Independent, client-only validation (spec §6.1): no network round-trip,
               purely reactive to question.value.dual_schema + examHasDecoy. -->
          <span v-if="question.dual_schema && !examHasDecoy" class="badge badge-warning" style="display: inline-block; width: fit-content; font-weight: 600; margin-top: var(--space-1);">
            Dual schema is enabled but this exam has no decoy curriculum yet — previews will use the grading schema until you upload one.
          </span>
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
          style="margin-top: 0.5rem; width: auto; padding: 0.5rem 1rem; margin-right: 0.5rem;"
        >
          {{ previewLoading ? 'Running...' : 'Preview Correct SQL Result' }}
        </button>
        <button
          @click="previewAsStudent"
          :disabled="previewAsStudentLoading || !question.correct_sql"
          class="secondary-btn"
          style="margin-top: 0.5rem; width: auto; padding: 0.5rem 1rem;"
        >
          {{ previewAsStudentLoading ? 'Running...' : 'Preview as Student' }}
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

      <!-- Preview-as-student results (decoy schema A — what the student
           actually sees for a dual-schema question). Kept as a separate
           panel from previewResult above so grading-schema (B) and
           decoy-schema (A) results are visible side by side. -->
      <div v-if="previewAsStudentResult.status" class="preview-block" :class="previewAsStudentResult.status">
        <strong v-if="previewAsStudentResult.status === 'ok'">Preview as student ({{ previewAsStudentResult.rows.length }} row{{ previewAsStudentResult.rows.length === 1 ? '' : 's' }})</strong>
        <strong v-else>Preview as student failed</strong>
        <div v-if="previewAsStudentResult.status === 'error'" class="preview-error">{{ previewAsStudentResult.error }}</div>
        <div v-else-if="previewAsStudentResult.rows.length === 0" class="preview-empty">(no rows returned)</div>
        <div v-else class="preview-table-wrap sticky-table-head">
          <table class="preview-table">
            <thead>
              <tr><th v-for="c in previewAsStudentResult.columns" :key="c">{{ c }}</th></tr>
            </thead>
            <tbody>
              <tr v-for="(row, i) in previewAsStudentResult.rows" :key="i">
                <td v-for="c in previewAsStudentResult.columns" :key="c">{{ row[c] }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="question.type !== 'DML'" class="strict-mode-section">
        <h3>Strict Mode Options</h3>
        <div class="checkbox-group">
          <label><input type="checkbox" v-model="question.strict_mode.ordering" /> Row Ordering <span class="hint">(rows must match the teacher's order; off means set-equality)</span></label>
          <label><input type="checkbox" v-model="question.strict_mode.attributes" /> Exact Attributes <span class="hint">(column names must match)</span></label>
          <label><input type="checkbox" v-model="question.strict_mode.column_order" /> Column Order <span class="hint">(student columns must appear in the teacher's exact left-to-right order)</span></label>
        </div>
      </div>
      <div v-else class="strict-mode-section muted">
        DML answers are graded on the final table contents as an unordered set of rows, matched by column name. Ordering and column-order options do not apply.
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

    <!-- Decoy curriculum (preview only) — same tables/columns as the exam's
         real curriculum, different data; used by dual-schema questions so
         the student's preview never leaks the grading data. Mirrors the
         SqlEditor + single-flight + 409-then-confirm-replace pattern used by
         the real-curriculum import in ExamSchemaPanel.vue, replicated
         locally since that file is out of this TODO's scope. -->
    <div style="margin-top: var(--space-5); border-top: 2px solid var(--color-border); padding-top: var(--space-5);">
      <h3 style="margin: 0 0 var(--space-2) 0;">Decoy curriculum (preview only)</h3>
      <p class="muted" style="margin: 0 0 var(--space-2) 0; font-size: 0.85rem;">
        Paste a SQL script with the same tables and columns as this exam's real curriculum, but different data. Students previewing a dual-schema question see this data instead of the real grading data.
      </p>
      <SqlEditor v-model="importPreviewSql" height="200px" style="margin-bottom: var(--space-2);" />
      <button
        class="secondary-btn"
        :disabled="importPreviewLoading || !importPreviewSql.trim()"
        @click="doImportPreview"
        style="margin-bottom: 0;"
      >
        <Spinner v-if="importPreviewLoading" size="sm" inline :invert="true" />{{ importPreviewLoading ? 'Importing...' : 'Import Decoy Curriculum' }}
      </button>
      <p v-if="examHasDecoy" class="muted" style="margin-top: var(--space-2); font-size: 0.85rem;">Decoy curriculum loaded — used for dual-schema question previews.</p>
    </div>

    <!-- Exam Description editor -->
    <div style="margin-top: var(--space-5); border-top: 2px solid var(--color-border); padding-top: var(--space-5);">
      <h3 style="margin: 0 0 var(--space-2) 0;">Exam Description</h3>
      <p class="muted" style="margin: 0 0 var(--space-2) 0; font-size: 0.85rem;">This text is shown to enrolled students on the intro page before they begin the exam.</p>
      <textarea
        v-model="examIntroductionText"
        class="input"
        rows="4"
        style="width: 100%; resize: vertical;"
        placeholder="Describe the exam's database structure or any context students should know..."
      ></textarea>
      <div style="margin-top: var(--space-2); display: flex; gap: var(--space-2); align-items: center;">
        <button class="btn-sm" :disabled="examIntroSaving" @click="saveExamIntroduction">
          {{ examIntroSaving ? 'Saving...' : 'Save Description' }}
        </button>
        <button class="btn-sm" @click="toggleIntroPreview">
          {{ showIntroPreview ? 'Hide preview' : 'Preview' }}
        </button>
      </div>
      <div v-if="showIntroPreview" class="intro-preview-panel">
        <div class="intro-preview-header">Student preview</div>
        <p v-if="examIntroductionText" class="intro-preview-text">{{ examIntroductionText }}</p>
        <p v-else class="intro-preview-placeholder">No description yet.</p>
        <div v-if="previewImages.length > 0" class="intro-preview-images">
          <img
            v-for="(img, idx) in previewImages"
            :key="img.id"
            :src="previewThumbUrls[img.id]"
            :alt="'Intro image ' + (idx + 1)"
            class="intro-preview-img"
          />
        </div>
      </div>
      <IntroImagesEditor scope="exam" :owner-id="props.exam.id" />
    </div>

    <EmptyState v-if="examQuestions.length === 0" :compact="true" title="No questions in this exam yet" body="Use the form above to add the first question." />
    <div v-if="examQuestions.length > 0" class="questions-list-section" style="margin-top: 2rem; border-top: 2px solid var(--color-border); padding-top: 1rem;">
      <h3 style="margin-bottom: 1rem;">Questions in Exam {{ props.exam.id }} <span class="muted" style="font-size: 0.8rem; font-weight: 400;">({{ displayedQuestions.length }} of {{ examQuestions.length }})</span></h3>
      <input v-model="filterText" placeholder="Filter by requirement..." class="input" style="margin-bottom: var(--space-2); width: 100%;" />
      <div class="preview-table-wrap sticky-table-head">
        <table class="preview-table">
          <thead>
            <tr>
              <th @click="toggleSort('id')" style="cursor: pointer;">ID <Icon v-if="sortKey === 'id'" :name="sortDir === 'asc' ? 'chevron-up' : 'chevron-down'" /></th>
              <th @click="toggleSort('type')" style="cursor: pointer;">Type <Icon v-if="sortKey === 'type'" :name="sortDir === 'asc' ? 'chevron-up' : 'chevron-down'" /></th>
              <th @click="toggleSort('points')" style="cursor: pointer;">Points <Icon v-if="sortKey === 'points'" :name="sortDir === 'asc' ? 'chevron-up' : 'chevron-down'" /></th>
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
  </div>
</template>

<style scoped>
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

.questions-list-section {
  margin-top: 2rem;
}

/* Inline intro preview panel */
.intro-preview-panel {
  margin-top: var(--space-3);
  padding: 12px 16px;
  border: 1px solid var(--alert-info-border, var(--color-border));
  border-radius: var(--radius);
  background: var(--alert-info-bg, var(--color-surface-muted));
}

.intro-preview-header {
  font-size: 0.72rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--color-text-muted);
  margin-bottom: var(--space-2);
}

.intro-preview-text {
  margin: 0;
  white-space: pre-wrap;
  font-size: 0.95rem;
  line-height: 1.55;
  color: var(--color-text);
}

.intro-preview-placeholder {
  margin: 0;
  color: var(--color-text-muted);
  font-style: italic;
  font-size: 0.88rem;
}

.intro-preview-images {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin-top: var(--space-3);
}

.intro-preview-img {
  max-width: 100%;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  display: block;
}
</style>
