<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import type { Examination, CaptureStatus, PreviewResult, AutoSeedPlan } from './types'
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
import {
  setExamIntroduction,
  importExamCurriculumPreview,
  listExamCategories,
  createExamCategory,
  renameExamCategory,
  deleteExamCategory,
  importExamCategorySchema,
  importExamCategoryPreviewSchema,
  setExamCategoryIntroduction,
  type ExamCategory
} from '../../services/exam'
import { ApiError } from '../../services/practice'
import { listExamIntroImages, examImageBlobUrl, type IntroImageMetadata } from '../../services/schemaImages'
import CollapsibleCard from '../CollapsibleCard.vue'
import { useAuthoringLayout } from '../../composables/useAuthoringLayout'

const props = defineProps<{ exam: Examination }>()
const emit = defineEmits<{ changed: [] }>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

// Same side/bottom dock as PracticeAuthoring.vue, under its own 'exam' key so
// the two authoring pages keep independent preferences. Geometry lives in the
// global .authoring-* rules in style.css.
const {
  mode: layoutMode,
  collapsed: categoriesPanelCollapsed,
  setMode: setLayoutMode,
  toggleCollapsed: toggleCategoriesPanel,
} = useAuthoringLayout('exam')

// Dual schema is no longer a lecturer-facing choice: every exam question is
// authored with dual_schema = true (see blankQuestion / editQuestion). The
// per-question toggle and its "no decoy curriculum yet" badge are gone; an
// exam with no decoy curriculum still grades correctly (previews just fall
// back to the grading schema), and the decoy-import block below reports
// whether a decoy exists.
const examHasDecoy = computed(() => props.exam.has_preview_schema ?? false)

const blankQuestion = () => ({
  id: null as number | null,
  // Backend-computed per-exam position (see sortKey below). Display only, and
  // never sent back on save — 0 until the question is loaded from the server.
  display_no: 0,
  exam_id: null as string | null,
  category_id: null as string | null,
  target_table: '',
  correct_sql: '',
  requirement: '',
  type: 'QUERY',
  dml_kind: '',
  seed_sql: '',
  points: 10,
  // Always true — dual schema is enabled for every question and hidden from
  // the lecturer.
  dual_schema: true,
  strict_mode: {
    ordering: false,
    attributes: false,
    column_order: false
  }
})

const question = ref(blankQuestion())

// Dirty-check guard (TODO-061): mirrors PracticeAuthoring.vue so switching to
// another question or starting a new one warns before silently discarding
// in-progress edits.
const loadedQuestionSnapshot = ref(JSON.stringify(question.value))
const isQuestionFormDirty = computed(() => JSON.stringify(question.value) !== loadedQuestionSnapshot.value)

async function confirmDiscardQuestionIfDirty(): Promise<boolean> {
  if (!isQuestionFormDirty.value) return true
  return confirm({
    title: 'Discard unsaved changes?',
    body: 'This question has edits that have not been saved yet. Switching now will lose them.',
    confirmLabel: 'Discard',
    danger: true,
  })
}

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

// Auto-seeded row (ARCH-042): read-only, backend-derived plan for a DML
// question whose model answer matches no existing row. Kept entirely
// separate from `question` (which mirrors the create/update request body) so
// it can never accidentally be sent back to the backend.
const autoSeedPlan = ref<AutoSeedPlan | null>(null)
const autoSeedStatement = computed(() => {
  const plan = autoSeedPlan.value
  if (!plan) return ''
  const cols = plan.columns.map(c => c.name).join(', ')
  const vals = plan.columns.map(c => `'${c.value}'`).join(', ')
  return `INSERT INTO ${plan.table} (${cols}) VALUES (${vals})`
})

// The wire format keeps question_type as 'QUERY' | 'DML' (the database CHECK
// constraint is deliberately unchanged, ARCH-043); the four-way authoring
// choice is question_type + dml_kind. This computed is the single place the
// two representations are converted, in both directions.
const questionKind = computed<string>({
  get() {
    if (question.value.type === 'QUERY') return 'QUERY'
    return question.value.dml_kind || 'DML'
  },
  set(v: string) {
    if (v === 'QUERY') {
      question.value.type = 'QUERY'
      question.value.dml_kind = ''
      question.value.seed_sql = ''
      return
    }
    question.value.type = 'DML'
    question.value.dml_kind = v
    // ARCH-044: the seed field only exists for UPDATE and DELETE. Clearing it
    // here means a hidden field can never be silently persisted.
    if (v !== 'UPDATE' && v !== 'DELETE') question.value.seed_sql = ''
  },
})

const seedApplies = computed(
  () => question.value.dml_kind === 'UPDATE' || question.value.dml_kind === 'DELETE',
)

const examQuestions = ref<any[]>([])
// Sorting/labelling is on display_no (the backend's per-exam 1-based position,
// gap-free and restarting at 1 per exam) rather than the raw global id, which
// keeps climbing after questions or whole exams are deleted. q.id is still the
// key for edit/delete/lookup — only what the lecturer READS changed.
const sortKey = ref<'display_no'|'type'|'points'|null>(null)
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
      // Same `type` vs `question_type` wire-name caveat as editQuestion below.
      const av = k === 'type' ? (a.type || a.question_type || '') : (a[k] ?? 0)
      const bv = k === 'type' ? (b.type || b.question_type || '') : (b[k] ?? 0)
      return av < bv ? -dir : av > bv ? dir : 0
    })
  }
  return list
})

function toggleSort(key: 'display_no'|'type'|'points') {
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
    if (!response.ok) {
      throw new Error(`Failed to load exam questions (${response.status})`)
    }
    examQuestions.value = await response.json()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam questions' })
  }
}

watch(() => props.exam.id, fetchExamQuestions, { immediate: true })

// --- Exam Categories (ARCH-029 / ARCH-034 (3) lower panel) ---
// Always-rendered "Exam Categories" management panel. Mirrors the category
// management block in PracticeAuthoring.vue, adapted for exam.ts's two-arg
// (examId, categoryId, ...) service signatures and ARCH-036's exam-category
// delete semantics (refuse-if-non-empty, no force/detach retry).
const categories = ref<ExamCategory[]>([])

const loadCategories = async () => {
  try {
    categories.value = await listExamCategories(props.exam.id)
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam categories' })
  }
}

watch(() => props.exam.id, loadCategories, { immediate: true })

const newCategoryName = ref('')

const { inFlight: addCategoryLoading, run: addCategory } = useSingleFlight(async () => {
  const name = newCategoryName.value.trim()
  if (!name) return
  try {
    const cat = await createExamCategory(props.exam.id, name)
    notify(`Category "${cat.name}" created`, 'success')
    newCategoryName.value = ''
    await loadCategories()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create category' })
  }
})

const doRenameCategory = async (cat: ExamCategory) => {
  const next = window.prompt('Rename category', cat.name)
  if (next === null) return
  const name = next.trim()
  if (!name || name === cat.name) return
  try {
    await renameExamCategory(props.exam.id, cat.id, name)
    notify('Category renamed', 'success')
    await loadCategories()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to rename category' })
  }
}

// Mirrors the backend's examCategoryQuestionCountRe
// (exam_category_controller.go), which wraps the attached-question count into
// the 409 error text ("N question(s) still attached to this category"). Used
// to surface the count without needing a new field on the shared ApiError
// class (which only carries status + message).
const attachedQuestionCountRe = /(\d+) question\(s\)/

// ARCH-036: exam-category delete has NO force/detach escape hatch. A 409
// means the category still has questions attached; the lecturer must
// reassign or delete those questions first — there is no "delete anyway"
// retry here (unlike PracticeAuthoring's force-delete flow).
const doDeleteCategory = async (cat: ExamCategory) => {
  const ok = await confirm({ title: `Delete "${cat.name}"?`, body: 'The category and its imported database schema will be removed. This cannot be undone.', confirmLabel: 'Delete', danger: true })
  if (!ok) return
  try {
    await deleteExamCategory(props.exam.id, cat.id)
    notify('Category deleted', 'success')
    await loadCategories()
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      const match = attachedQuestionCountRe.exec(err.message)
      const count = match ? match[1] : null
      notify(
        count
          ? `Cannot delete "${cat.name}": ${count} question(s) are still assigned to it. Reassign or delete those questions first.`
          : `Cannot delete "${cat.name}": it still has questions assigned. Reassign or delete those questions first.`,
        'error'
      )
      return
    }
    handleApiError(err, { fallback: 'Failed to delete category' })
  }
}

// --- Category database schema import (grading schema) ---
const catSchemaImportCategoryId = ref<string | null>(null)
const catSchemaImportSql = ref('')
const catSchemaImportFileName = ref('')

const openCatSchemaImport = (cat: ExamCategory) => {
  catSchemaImportCategoryId.value = cat.id
  catSchemaImportSql.value = ''
  catSchemaImportFileName.value = ''
}

const cancelCatSchemaImport = () => {
  catSchemaImportCategoryId.value = null
  catSchemaImportSql.value = ''
  catSchemaImportFileName.value = ''
}

const loadCatSchemaImportFile = (event: Event) => {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  if (!/\.sql$/i.test(file.name)) {
    notify('File must be a .sql file', 'error')
    input.value = ''
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    catSchemaImportSql.value = typeof reader.result === 'string' ? reader.result : ''
    catSchemaImportFileName.value = file.name
  }
  reader.onerror = () => notify('Failed to read the file', 'error')
  reader.readAsText(file)
  input.value = '' // reset so re-selecting the same file re-fires @change
}

const { inFlight: catSchemaImportLoading, run: doCatSchemaImport } = useSingleFlight(async () => {
  if (catSchemaImportCategoryId.value === null || !catSchemaImportSql.value.trim()) return
  const id = catSchemaImportCategoryId.value
  try {
    await importExamCategorySchema(props.exam.id, id, catSchemaImportSql.value, false)
    notify('Database imported', 'success')
    cancelCatSchemaImport()
    await loadCategories()
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      const ok = await confirm({ title: 'Replace existing database?', body: 'This category already has a database. Importing will drop the existing tables and replace them. Continue?', confirmLabel: 'Replace', danger: true })
      if (!ok) return
      try {
        await importExamCategorySchema(props.exam.id, id, catSchemaImportSql.value, true)
        notify('Database replaced', 'success')
        cancelCatSchemaImport()
        await loadCategories()
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to import database' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to import database' })
  }
})

// --- Category decoy database schema import (preview only) — parallel to the
// grading-schema import above, same 409-then-confirm-replace flow, targeting
// importExamCategoryPreviewSchema instead of importExamCategorySchema. ---
const catDecoyImportCategoryId = ref<string | null>(null)
const catDecoyImportSql = ref('')
const catDecoyImportFileName = ref('')

const openCatDecoyImport = (cat: ExamCategory) => {
  catDecoyImportCategoryId.value = cat.id
  catDecoyImportSql.value = ''
  catDecoyImportFileName.value = ''
}

const cancelCatDecoyImport = () => {
  catDecoyImportCategoryId.value = null
  catDecoyImportSql.value = ''
  catDecoyImportFileName.value = ''
}

const loadCatDecoyImportFile = (event: Event) => {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  if (!/\.sql$/i.test(file.name)) {
    notify('File must be a .sql file', 'error')
    input.value = ''
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    catDecoyImportSql.value = typeof reader.result === 'string' ? reader.result : ''
    catDecoyImportFileName.value = file.name
  }
  reader.onerror = () => notify('Failed to read the file', 'error')
  reader.readAsText(file)
  input.value = '' // reset so re-selecting the same file re-fires @change
}

const { inFlight: catDecoyImportLoading, run: doCatDecoyImport } = useSingleFlight(async () => {
  if (catDecoyImportCategoryId.value === null || !catDecoyImportSql.value.trim()) return
  const id = catDecoyImportCategoryId.value
  try {
    const res = await importExamCategoryPreviewSchema(props.exam.id, id, catDecoyImportSql.value, false)
    notify('Decoy schema imported', 'success')
    if (res.parity_warning) notify(res.parity_warning, 'error')
    cancelCatDecoyImport()
    await loadCategories()
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      const ok = await confirm({ title: 'Replace existing decoy schema?', body: 'This category already has a decoy schema. Importing will drop the existing tables and replace them. Continue?', confirmLabel: 'Replace', danger: true })
      if (!ok) return
      try {
        const res2 = await importExamCategoryPreviewSchema(props.exam.id, id, catDecoyImportSql.value, true)
        notify('Decoy schema replaced', 'success')
        if (res2.parity_warning) notify(res2.parity_warning, 'error')
        cancelCatDecoyImport()
        await loadCategories()
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to import decoy schema' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to import decoy schema' })
  }
})

// Per-category introduction editor state (keyed by category id). NO
// intro-images sub-panel for exam categories (ARCH-026 (6) / ARCH-030 scope
// them out entirely) — exam-level intro images stay in the upper panel.
const categoryIntroEdits = ref<Record<string, string>>({})
const categoryIntroSaving = ref<Record<string, boolean>>({})

watch(categories, (cats) => {
  cats.forEach(c => {
    if (!(c.id in categoryIntroEdits.value)) {
      categoryIntroEdits.value[c.id] = c.introduction ?? ''
    }
  })
})

const saveCategoryIntro = async (cat: ExamCategory) => {
  categoryIntroSaving.value[cat.id] = true
  try {
    await setExamCategoryIntroduction(props.exam.id, cat.id, categoryIntroEdits.value[cat.id] ?? '')
    notify('Description saved', 'success')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save description' })
  } finally {
    categoryIntroSaving.value[cat.id] = false
  }
}

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

const editQuestion = async (q: any) => {
  if (!(await confirmDiscardQuestionIfDirty())) return
  isEditMode.value = true
  question.value = {
    id: q.id,
    display_no: q.display_no ?? 0,
    exam_id: q.exam_id,
    category_id: q.category_id ?? null,
    target_table: q.target_table || '',
    correct_sql: q.correct_sql || '',
    requirement: q.requirement || '',
    // The exam-question wire format serializes the kind as `type`
    // (models.Question, submission.go) — NOT `question_type`, which is the
    // practice-question field name. Reading only `question_type` here silently
    // fell back to 'QUERY', so editing a DML question showed (and re-saved) it
    // as a QUERY. Both names are accepted so either shape loads correctly.
    type: q.type || q.question_type || 'QUERY',
    dml_kind: q.dml_kind || '',
    seed_sql: q.seed_sql || '',
    points: q.points || 0,
    // Forced on regardless of what is stored, so re-saving an older
    // single-schema question upgrades it.
    dual_schema: true,
    strict_mode: {
      ordering: q.strict_mode?.ordering || false,
      attributes: q.strict_mode?.attributes || false,
      column_order: q.strict_mode?.column_order || false
    }
  }
  loadedQuestionSnapshot.value = JSON.stringify(question.value)
  autoSeedPlan.value = q.auto_seed ?? null
  lastCapture.value = {
    status: q.expected_result_status || '',
    error: q.capture_error || '',
    capturedAt: q.expected_result_captured_at || null
  }
  previewResult.value = { status: '', rows: [], columns: [], error: '' }
  previewAsStudentResult.value = { status: '', rows: [], columns: [], error: '' }
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function onQuestionDropdownChange(event: Event) {
  const val = (event.target as HTMLSelectElement).value
  if (val === '') {
    if (!(await confirmDiscardQuestionIfDirty())) return
    // Create new question — id=0 tells the backend to auto-assign the PK on
    // insert (repository/postgres_questions.go UpsertQuestion: q.ID==0 => INSERT).
    isEditMode.value = false
    question.value = {
      id: 0,
      display_no: 0,
      exam_id: props.exam.id,
      category_id: null,
      target_table: '',
      correct_sql: '',
      requirement: '',
      type: 'QUERY',
      dml_kind: '',
      seed_sql: '',
      points: 10,
      dual_schema: true,
      strict_mode: { ordering: false, attributes: false, column_order: false }
    }
    loadedQuestionSnapshot.value = JSON.stringify(question.value)
    autoSeedPlan.value = null
    lastCapture.value = { status: '', error: '', capturedAt: null }
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
    previewAsStudentResult.value = { status: '', rows: [], columns: [], error: '' }
  } else {
    const q = examQuestions.value.find((q: any) => q.id === Number(val))
    if (q) await editQuestion(q)
  }
}

useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, scope: questionFormRef, handler: () => previewCorrectSQL() })

// Shared request body for both preview variants below (ARCH-028 (5)). Sends
// category_id only when the form currently has a category selected — "None
// (exam-level schema)" leaves question.value.category_id null, so the field
// is omitted entirely and the backend's zero-value CategoryID falls back to
// the exam curriculum exactly as it did before categories existed. Sending an
// explicit null instead would fail the category's custom UnmarshalJSON
// (models.ExamCategoryID expects an opaque id string, not null).
function previewRequestBody(extra: Record<string, unknown> = {}) {
  const body: Record<string, unknown> = {
    exam_id: props.exam.id,
    type: question.value.type,
    target_table: question.value.target_table,
    correct_sql: question.value.correct_sql,
    ...extra
  }
  if (question.value.category_id) {
    body.category_id = question.value.category_id
  }
  return body
}

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
      body: JSON.stringify(previewRequestBody())
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
      body: JSON.stringify(previewRequestBody({ schema: 'preview' }))
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
  const clash = isEditMode.value ? undefined : examQuestions.value.find((q: any) => q.id === question.value.id)
  if (clash) {
    const ok = await confirm({
      title: 'Overwrite existing question?',
      body: `Question Q${clash.display_no} already exists and will be overwritten.`,
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
      // display_no is a read-only, per-read projection — never echo it back.
      body: JSON.stringify({ ...question.value, display_no: undefined, exam_id: props.exam.id }),
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
    loadCategories() // question_count on the affected category has changed
    localStorage.removeItem(requirementDraftKey.value)
    emit('changed')

    // Reset for the next question to avoid silent ON CONFLICT overwrites.
    question.value = blankQuestion()
    question.value.exam_id = props.exam.id
    loadedQuestionSnapshot.value = JSON.stringify(question.value)
    autoSeedPlan.value = null
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
    previewAsStudentResult.value = { status: '', rows: [], columns: [], error: '' }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save question' })
  }
})
</script>

<template>
  <div>
    <!-- Both sections stay mounted in both modes and keep their DOM order; only
         the wrapper class changes, so switching layouts never unmounts a
         half-filled import editor or collapses an open category card. -->
    <div class="authoring-layout" :class="`authoring-layout--${layoutMode}`">
    <!-- UPPER section: everything this panel rendered before ARCH-034 (3) —
         question form, decoy curriculum import, exam description, exam intro
         images, questions list. Order and behaviour unchanged. -->
    <section class="authoring-section authoring-section--upper">
    <div ref="questionFormRef">
      <div class="form-grid">
        <div class="input-group">
          <label for="exam-question-select">Question ID:</label>
          <select id="exam-question-select" @change="onQuestionDropdownChange" :value="isEditMode ? question.id : ''">
            <option value="">Create new question</option>
            <option v-for="q in examQuestions" :key="q.id" :value="q.id">
              Q{{ q.display_no }} — {{ (q.requirement || '').slice(0, 50) }}
            </option>
          </select>
          <span v-if="isEditMode" class="badge badge-info" style="margin-top: var(--space-1); font-size: 0.75rem;">Editing Q{{ question.display_no }}</span>
          <span v-else class="muted" style="font-size: 0.75rem; margin-top: var(--space-1);">New question - ID assigned on save</span>
        </div>

        <div class="input-group">
          <label for="exam-question-type">Type:</label>
          <select id="exam-question-type" v-model="questionKind">
            <option value="QUERY">Query (SELECT)</option>
            <option value="INSERT">INSERT</option>
            <option value="UPDATE">UPDATE</option>
            <option value="DELETE">DELETE</option>
            <option value="DML" disabled>DML (pick a kind)</option>
          </select>
        </div>

        <!-- ARCH-034 (3)(c): the one behavioural addition to the upper panel —
             lets a lecturer put a question into an exam category. Without it
             exam categories would have no authoring entry point. -->
        <div class="input-group">
          <label for="exam-question-category">Category:</label>
          <select id="exam-question-category" v-model="question.category_id">
            <option :value="null">None (exam-level schema)</option>
            <option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </div>

        <div class="input-group" v-if="question.type === 'DML'">
          <label for="exam-question-target-table">Target Table:</label>
          <input id="exam-question-target-table" type="text" v-model="question.target_table" placeholder="e.g. employee" />
        </div>

        <div class="input-group">
          <label for="exam-question-points">Points:</label>
          <input
            id="exam-question-points"
            type="number"
            v-model.number="question.points"
            :aria-invalid="questionValidation.touched.points && !!questionValidation.errors.points"
            :aria-describedby="'points-error'"
            @blur="questionValidation.markTouched('points')"
          />
          <FieldError id="points-error" :message="questionValidation.touched.points ? questionValidation.errors.points : ''" />
        </div>

        <!-- No dual-schema toggle: it is always on and hidden from the
             lecturer. question.dual_schema is set to true by blankQuestion and
             by editQuestion, so every save carries it. -->
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

      <div class="input-group" v-if="seedApplies">
        <label for="exam-question-seed-sql">Seed rows (sandbox only):</label>
        <textarea id="exam-question-seed-sql" v-model="question.seed_sql" rows="4"
          placeholder="INSERT INTO employee (ssn, fname) VALUES ('999999999', 'Test');"></textarea>
        <small class="muted">
          These INSERT statements run inside a transaction that is always rolled back.
          They give your UPDATE/DELETE something to act on. They never modify the imported database.
        </small>
      </div>

      <!-- Auto-seeded row (ARCH-042): read-only, backend-derived. Never sent
           back in the create/update request body -- saveQuestion serializes
           `question`, which never carries autoSeedPlan. Gated on !question.seed_sql
           because the lecturer's own seed and the derived plan are mutually
           exclusive (ARCH-045) — they must never both show. -->
      <div v-if="!question.seed_sql && autoSeedPlan" class="auto-seed-panel">
        <strong>Auto-seeded row</strong>
        <div class="auto-seed-statement"><code>{{ autoSeedStatement }}</code></div>
        <p class="muted" style="margin: var(--space-2) 0 0 0; font-size: 0.85rem;">
          This row does not exist in the exam curriculum database. The system adds it inside a transaction that is rolled back, so the real database is unchanged.
        </p>
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

    <!-- The exam-level decoy-curriculum import and the exam description /
         intro-images editors used to sit inline here. They now live in the
         docked Exam Categories panel below, next to the per-category schema
         and description editors they mirror, so the authoring column holds
         only the question form and the questions list. -->

    <EmptyState v-if="examQuestions.length === 0" :compact="true" title="No questions in this exam yet" body="Use the form above to add the first question." />
    <div v-if="examQuestions.length > 0" class="questions-list-section" style="margin-top: 2rem; border-top: 2px solid var(--color-border); padding-top: 1rem;">
      <h3 style="margin-bottom: 1rem;">Questions in Exam {{ props.exam.id }} <span class="muted" style="font-size: 0.8rem; font-weight: 400;">({{ displayedQuestions.length }} of {{ examQuestions.length }})</span></h3>
      <input v-model="filterText" placeholder="Filter by requirement..." class="input" style="margin-bottom: var(--space-2); width: 100%;" />
      <div class="preview-table-wrap sticky-table-head">
        <table class="preview-table">
          <thead>
            <tr>
              <th @click="toggleSort('display_no')" style="cursor: pointer;">Q# <Icon v-if="sortKey === 'display_no'" :name="sortDir === 'asc' ? 'chevron-up' : 'chevron-down'" /></th>
              <th @click="toggleSort('type')" style="cursor: pointer;">Type <Icon v-if="sortKey === 'type'" :name="sortDir === 'asc' ? 'chevron-up' : 'chevron-down'" /></th>
              <th @click="toggleSort('points')" style="cursor: pointer;">Points <Icon v-if="sortKey === 'points'" :name="sortDir === 'asc' ? 'chevron-up' : 'chevron-down'" /></th>
              <th>Requirement</th>
              <th>Capture</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="q in displayedQuestions" :key="q.id">
              <td>{{ q.display_no }}</td>
              <td>{{ q.dml_kind || q.type || q.question_type }}</td>
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
    </section><!-- /authoring-section--upper -->

    <!-- LOWER section: Exam Categories, ALWAYS rendered — no show/hide toggle
         (ARCH-034 (3)(b)). Each category is a CollapsibleCard driven by the
         ARCH-029 service wrappers in services/exam.ts. NO intro-images
         sub-panel here — exam categories don't have them (ARCH-026 (6)).
         A `position: fixed` overlay in BOTH modes (see
         .authoring-section--lower.is-docked in style.css) — 'side' docks it
         to the right edge at full viewport height, 'bottom' to the bottom
         edge at full viewport width — with the dock switch in the panel
         header beside Minimize, available in either mode. -->
    <section
      class="authoring-section authoring-section--lower is-docked"
      :class="[`dock-${layoutMode}`, { 'is-collapsed': categoriesPanelCollapsed }]"
      aria-labelledby="exam-categories-heading"
    >
      <div class="authoring-panel-shell" :class="`authoring-panel-shell--${layoutMode}`">
      <!-- No inline divider here: inline styles beat every stylesheet, so one
           would draw a stray rule across the docked panel. The panel's own
           border comes from the .is-docked / .dock-* rules in style.css. -->
      <div class="authoring-panel-content">
        <div class="authoring-floating-panel-header">
          <h2 id="exam-categories-heading" style="margin: 0 0 var(--space-2) 0;">Exam Categories</h2>
          <span class="authoring-floating-panel-header-actions">
            <span class="authoring-layout-switch" role="group" aria-label="Categories panel dock">
              <button
                type="button"
                data-testid="layout-mode-side"
                title="Dock to the right edge"
                :aria-pressed="layoutMode === 'side'"
                @click="setLayoutMode('side')"
              >Side</button>
              <button
                type="button"
                data-testid="layout-mode-bottom"
                title="Dock to the bottom edge"
                :aria-pressed="layoutMode === 'bottom'"
                @click="setLayoutMode('bottom')"
              >Btm</button>
            </span>
            <button
              type="button"
              class="btn-sm"
              data-testid="floating-categories-minimize"
              aria-label="Minimize categories panel"
              @click="toggleCategoriesPanel"
            >Minimize</button>
          </span>
        </div>
        <p class="muted" style="margin: 0 0 var(--space-3) 0; font-size: 0.85rem;">Each category can own its own database, kept separate from the exam-level schema above. Questions with no category grade against the exam-level schema.</p>

        <!-- Exam-level settings, moved here from the authoring column so all
             schema/description management lives in this one docked panel.
             Collapsed by default (CollapsibleCard defaultOpen=false) — the
             per-category cards below stay the primary content. -->
        <CollapsibleCard title="Decoy curriculum (preview only)" test-id="exam-decoy-curriculum-card">
          <template #header-meta>
            <span v-if="examHasDecoy" class="badge badge-info">decoy</span>
          </template>
          <p class="muted" style="margin: 0 0 var(--space-2) 0; font-size: 0.85rem;">
            Paste a SQL script with the same tables and columns as this exam's real curriculum, but different data. Students previewing a question see this data instead of the real grading data.
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
          <p v-if="examHasDecoy" class="muted" style="margin-top: var(--space-2); font-size: 0.85rem;">Decoy curriculum loaded — used for student question previews.</p>
        </CollapsibleCard>

        <CollapsibleCard title="Exam description &amp; intro images" test-id="exam-description-card">
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
        </CollapsibleCard>

        <!-- New category -->
        <div style="display: flex; gap: var(--space-2); margin-bottom: var(--space-4);">
          <input v-model="newCategoryName" placeholder="New category name" aria-label="New category name" style="flex: 1;" @keyup.enter="addCategory" />
          <button class="primary-btn" style="margin-bottom: 0;" :disabled="addCategoryLoading || !newCategoryName.trim()" @click="addCategory">New Category</button>
        </div>

        <!-- Per-category collapsible cards -->
        <div v-if="categories.length > 0">
          <CollapsibleCard v-for="c in categories" :key="c.id" :title="c.name" :test-id="'exam-category-' + c.id">
            <template #header-meta>
              <code>{{ c.schema_name }}</code>
              <span class="muted" style="font-size: 0.8rem;">{{ c.question_count }} question{{ c.question_count === 1 ? '' : 's' }}</span>
              <span v-if="c.has_preview_schema" class="badge badge-info">decoy</span>
              <span v-if="!c.owns_schema" class="badge badge-warning">exam-level schema</span>
            </template>
            <template #header-actions>
              <button class="btn-sm" @click="doRenameCategory(c)">Rename</button>
              <button class="btn-sm btn-danger" @click="doDeleteCategory(c)">Delete</button>
            </template>

            <!-- Database schema sub-panel -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Database schema</span>
              <div v-if="catSchemaImportCategoryId === c.id">
                <h3>Import database into "{{ c.name }}"</h3>
                <p class="muted">Upload a <code>.sql</code> file or paste a SQL script, using unqualified names (no <code>public.</code> / <code>app.</code> prefixes).</p>
                <div style="display: flex; gap: var(--space-2); align-items: center; margin-bottom: var(--space-2);">
                  <label class="btn-sm" style="cursor: pointer; margin: 0;">
                    Choose .sql file
                    <input type="file" accept=".sql,text/plain,application/sql" @change="loadCatSchemaImportFile" style="display: none;" />
                  </label>
                  <span v-if="catSchemaImportFileName" class="muted">Loaded: <code>{{ catSchemaImportFileName }}</code> — review or edit below, then import.</span>
                </div>
                <SqlEditor v-model="catSchemaImportSql" height="200px" />
                <div style="display: flex; gap: var(--space-2); margin-top: var(--space-2);">
                  <button class="primary-btn" style="margin-bottom: 0;" :disabled="catSchemaImportLoading || !catSchemaImportSql.trim()" @click="doCatSchemaImport">
                    <Spinner v-if="catSchemaImportLoading" size="sm" inline :invert="true" />{{ catSchemaImportLoading ? 'Importing' : 'Import Database' }}
                  </button>
                  <button class="secondary-btn" style="margin-bottom: 0;" @click="cancelCatSchemaImport">Cancel</button>
                </div>
              </div>
              <div v-else style="display: flex; justify-content: space-between; align-items: center; gap: var(--space-2);">
                <p class="muted" style="margin: 0;">Import a .sql to (re)load this category's database.</p>
                <button class="btn-sm" @click="openCatSchemaImport(c)">Import Database</button>
              </div>
            </div>

            <!-- Decoy database schema sub-panel (preview only — same tables/columns as
                 the grading schema above, different data; used by dual-schema questions
                 so the student's preview never leaks the grading data). -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Decoy database schema (preview only)</span>
              <div v-if="catDecoyImportCategoryId === c.id">
                <h3>Import decoy database into "{{ c.name }}"</h3>
                <p class="muted">Upload a <code>.sql</code> file or paste a SQL script with the same tables and columns as the grading database above, but different data. Students previewing a dual-schema question see this data instead of the real grading data.</p>
                <div style="display: flex; gap: var(--space-2); align-items: center; margin-bottom: var(--space-2);">
                  <label class="btn-sm" style="cursor: pointer; margin: 0;">
                    Choose .sql file
                    <input type="file" accept=".sql,text/plain,application/sql" @change="loadCatDecoyImportFile" style="display: none;" />
                  </label>
                  <span v-if="catDecoyImportFileName" class="muted">Loaded: <code>{{ catDecoyImportFileName }}</code> — review or edit below, then import.</span>
                </div>
                <SqlEditor v-model="catDecoyImportSql" height="200px" />
                <div style="display: flex; gap: var(--space-2); margin-top: var(--space-2);">
                  <button class="primary-btn" style="margin-bottom: 0;" :disabled="catDecoyImportLoading || !catDecoyImportSql.trim()" @click="doCatDecoyImport">
                    <Spinner v-if="catDecoyImportLoading" size="sm" inline :invert="true" />{{ catDecoyImportLoading ? 'Importing' : 'Import Decoy Schema' }}
                  </button>
                  <button class="secondary-btn" style="margin-bottom: 0;" @click="cancelCatDecoyImport">Cancel</button>
                </div>
              </div>
              <div v-else style="display: flex; justify-content: space-between; align-items: center; gap: var(--space-2);">
                <p class="muted" style="margin: 0;">
                  {{ c.has_preview_schema ? 'Decoy schema loaded — used for dual-schema question previews.' : 'Import a decoy .sql (same structure, different data) to enable dual-schema previews for this category.' }}
                </p>
                <button class="btn-sm" @click="openCatDecoyImport(c)">Import Decoy Schema</button>
              </div>
            </div>

            <!-- Description sub-panel -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Description (shown to students on the intro page)</span>
              <div style="display: flex; gap: var(--space-2); align-items: flex-end;">
                <textarea
                  v-model="categoryIntroEdits[c.id]"
                  rows="3"
                  style="flex: 1; resize: vertical; min-height: 60px;"
                  :placeholder="'Write a description for the ' + c.name + ' category...'"
                  :aria-label="'Description for the ' + c.name + ' category'"
                ></textarea>
                <button
                  class="btn-sm"
                  :disabled="categoryIntroSaving[c.id]"
                  @click="saveCategoryIntro(c)"
                  style="white-space: nowrap;"
                >{{ categoryIntroSaving[c.id] ? 'Saving...' : 'Save Description' }}</button>
              </div>
            </div>
            <!-- NO intro-images sub-panel here (ARCH-026 (6) / ARCH-030 scope
                 exam-category intro images out entirely). -->
          </CollapsibleCard>
        </div>
        <p v-else class="muted">No categories yet. Create one above.</p>
      </div>
      </div><!-- /.authoring-panel-shell -->
    </section><!-- /authoring-section--lower -->
    </div><!-- /.authoring-layout -->

    <!-- Reopen tab: shown while the panel is minimized, in either dock. It
         sits on the edge the panel will return to. -->
    <button
      v-if="categoriesPanelCollapsed"
      type="button"
      class="authoring-floating-tab"
      :class="`authoring-floating-tab--${layoutMode}`"
      data-testid="floating-categories-reopen"
      @click="toggleCategoriesPanel"
    >Exam Categories</button>
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

.auto-seed-panel {
  margin-top: var(--space-3);
  padding: 10px 14px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  background: var(--color-surface-muted);
}
.auto-seed-statement {
  margin-top: 6px;
  font-family: var(--font-mono);
  font-size: 0.86rem;
  white-space: pre-wrap;
}

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

/* ARCH-034 (3)(d): two sibling sections, upper then lower. The divider and the
   side/bottom dock geometry come from the global .authoring-* rules in
   style.css. Do NOT add a scoped rule for those selectors here: scoped styles
   compile to `.foo[data-v-x]` (specificity 0,2,0) and are injected AFTER
   style.css, so even a single-class scoped rule ties the global two-class
   rules and wins on order. A scoped `.authoring-section { display: block }`
   used to live here and silently beat both `.is-docked { display: flex }`
   (breaking the flex chain the panel's internal scrolling depends on) and
   `.is-collapsed { display: none }` (making Minimize a no-op). It was also
   redundant — <section> is already display:block per the UA stylesheet. */
.cat-subpanel {
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-3);
  margin-top: var(--space-3);
}
.cat-subpanel > .cat-subpanel-label {
  font-size: 0.8rem;
  font-weight: 700;
  color: var(--color-text-muted);
  margin-bottom: var(--space-2);
  display: block;
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
