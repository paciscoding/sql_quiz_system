<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import {
  lecturerListQuestions,
  upsertQuestion,
  deleteQuestion,
  lecturerPreview,
  listCategories,
  createCategory,
  renameCategory,
  deleteCategory,
  importCategorySchema,
  importCategoryPreviewSchema,
  setCategoryIntroduction,
  ApiError,
  type PracticeQuestion,
  type PracticeCategory,
  type AutoSeedPlan
} from '../services/practice'
import { UI_COPY } from '../constants/ui_copy'
import SqlEditor from './SqlEditor.vue'
import BackLink from './BackLink.vue'
import ResultTable from './ResultTable.vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useConfirm } from '../services/confirm_service'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import Spinner from './Spinner.vue'
import { useAutosave } from '../composables/useAutosave'
import IntroImagesEditor from './IntroImagesEditor.vue'
import CollapsibleCard from './CollapsibleCard.vue'
import { useAuthoringLayout } from '../composables/useAuthoringLayout'

const { notify } = useNotifications()
const { confirm } = useConfirm()

// Dock orientation for the Categories Management panel. BOTH modes are
// `position: fixed` overlays that never scroll away, at any viewport width —
// 'side' pins it to the right edge at full viewport height, 'bottom' to the
// bottom edge at full viewport width. Persisted per lecturer, along with the
// minimize state. Geometry lives in the global .authoring-* rules in
// style.css.
const {
  mode: layoutMode,
  collapsed: categoriesPanelCollapsed,
  setMode: setLayoutMode,
  toggleCollapsed: toggleCategoriesPanel,
} = useAuthoringLayout('practice')

const questions = ref<PracticeQuestion[]>([])
const categories = ref<PracticeCategory[]>([])
const form = ref<Partial<PracticeQuestion>>({
  question_type: 'QUERY',
  dml_kind: '',
  seed_sql: '',
  points: 1.0,
  strict_ordering: false,
  strict_attrs: false,
  strict_column_order: false,
  dual_schema: true
})
const previewRows = ref<any[]>([])
const previewCols = ref<string[]>([])
const previewLoading = ref(false)
const previewError = ref('')

// The auto-seed plan (ARCH-042) belongs to the *loaded* question, not the
// editable form: it must never round-trip through form.value, since
// saveQuestion sends form.value verbatim as the request body and auto_seed
// is derived server-side only. Kept in its own ref, set in editQuestion /
// cleared in resetForm, and rendered read-only beneath Correct SQL.
const loadedAutoSeed = ref<AutoSeedPlan | null>(null)

const autoSeedStatement = computed(() => {
  const seed = loadedAutoSeed.value
  if (!seed) return ''
  const cols = seed.columns.map(c => c.name).join(', ')
  const vals = seed.columns.map(c => `'${c.value}'`).join(', ')
  return `INSERT INTO ${seed.table} (${cols}) VALUES (${vals})`
})

// The wire format keeps question_type as 'QUERY' | 'DML' (the database CHECK
// constraint is deliberately unchanged, ARCH-043); the four-way authoring
// choice is question_type + dml_kind. This computed is the single place the
// two representations are converted, in both directions.
const questionKind = computed<string>({
  get() {
    if (form.value.question_type === 'QUERY') return 'QUERY'
    return form.value.dml_kind || 'DML'
  },
  set(v: string) {
    if (v === 'QUERY') {
      form.value.question_type = 'QUERY'
      form.value.dml_kind = ''
      form.value.seed_sql = ''
      return
    }
    form.value.question_type = 'DML'
    form.value.dml_kind = v
    // ARCH-044: the seed field only exists for UPDATE and DELETE. Clearing it
    // here means a hidden field can never be silently persisted.
    if (v !== 'UPDATE' && v !== 'DELETE') form.value.seed_sql = ''
  },
})

const seedApplies = computed(() => form.value.dml_kind === 'UPDATE' || form.value.dml_kind === 'DELETE')

// "Preview as student" (schema: 'preview') results — kept in a separate set of
// refs from the grading-schema Preview above so the lecturer can see both the
// student's decoy-schema view (A) and the grading-schema view (B) side by side.
const previewAsStudentRows = ref<any[]>([])
const previewAsStudentCols = ref<string[]>([])
const previewAsStudentLoading = ref(false)
const previewAsStudentError = ref('')

// --- Category management panel state ---
const newCategoryName = ref('')
const importCategoryId = ref<string | null>(null)
const importSql = ref('')

// Decoy (preview-only) schema import state — parallel to importCategoryId/importSql above.
const importPreviewCategoryId = ref<string | null>(null)
const importPreviewSql = ref('')

// Per-category introduction editor state (keyed by category id)
const categoryIntroEdits = ref<Record<string, string>>({})
const categoryIntroSaving = ref<Record<string, boolean>>({})

// Pre-populate introduction edits from the loaded category list
watch(categories, (cats) => {
  cats.forEach(c => {
    if (!(c.id in categoryIntroEdits.value)) {
      categoryIntroEdits.value[c.id] = c.introduction ?? ''
    }
  })
})

const saveCategoryIntro = async (cat: PracticeCategory) => {
  categoryIntroSaving.value[cat.id] = true
  try {
    await setCategoryIntroduction(cat.id, categoryIntroEdits.value[cat.id] ?? '')
    notify('Description saved', 'success')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save description' })
  } finally {
    categoryIntroSaving.value[cat.id] = false
  }
}

const categoryNameById = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {}
  categories.value.forEach(c => { map[c.id] = c.name })
  return map
})

// The client-side "dual_schema on but this category has no decoy schema"
// check (dual-schema spec §6.1) lived here as `selectedCategoryHasDecoy`. It
// went away with the dual-schema toggle it annotated — with no lecturer-facing
// control there is nothing to warn about at author time. The backend still
// returns dual_schema_warning on save, which saveQuestion surfaces.

const loadCategories = async () => {
  try {
    categories.value = await listCategories()
    // Default the form to the first category when creating a new question.
    if (!form.value.category_id && categories.value.length > 0) {
      form.value.category_id = categories.value[0].id
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load categories' })
  }
}

// --- Autosave for requirement textarea (TODO-060) ---
const requirementRef = computed({
  get: () => form.value.requirement ?? '',
  set: (v: string) => { form.value.requirement = v }
})

const { hasDraft, restoreDraft, clearDraft } = useAutosave('sql_quiz_draft.practiceAuthoring.requirement', requirementRef)
const showDraftBanner = ref(false)

const acceptDraft = () => { restoreDraft(); showDraftBanner.value = false }
const dismissDraft = () => { clearDraft(); showDraftBanner.value = false }


// Form validation for practice question (TODO-038)
const practiceForm = useFormValidation({
  requirement: { rules: [required('Requirement is required')], initial: '' },
  correct_sql: { rules: [required('Correct SQL is required')], initial: '' },
})
watch(() => form.value.requirement, (v) => { practiceForm.values.requirement = v ?? '' })
watch(() => form.value.correct_sql, (v) => { practiceForm.values.correct_sql = v ?? '' })

const loadQuestions = async () => {
  try {
    questions.value = await lecturerListQuestions()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load questions' })
  }
}

// Dirty-check guard (TODO-061): loadedFormSnapshot pins the form's shape at
// the moment it was last loaded/reset; isFormDirty compares against it so
// switching to another question or starting a new one can warn before
// silently discarding in-progress edits (e.g. a half-written correct_sql).
const loadedFormSnapshot = ref(JSON.stringify(form.value))
const isFormDirty = computed(() => JSON.stringify(form.value) !== loadedFormSnapshot.value)

async function confirmDiscardIfDirty(): Promise<boolean> {
  if (!isFormDirty.value) return true
  return confirm({
    title: 'Discard unsaved changes?',
    body: 'This question has edits that have not been saved yet. Switching now will lose them.',
    confirmLabel: 'Discard',
    danger: true,
  })
}

const editQuestion = async (q: PracticeQuestion) => {
  if (!(await confirmDiscardIfDirty())) return
  // auto_seed is display-only and deliberately excluded from form.value (see
  // loadedAutoSeed above) so it can never be re-sent in the save payload.
  const { auto_seed, ...editable } = q
  // dml_kind/seed_sql default to '' so a question saved before those columns
  // existed carries a clean slate rather than undefined, and so an edit
  // round-trip never inherits the previously-loaded question's seed.
  form.value = { dml_kind: '', seed_sql: '', ...editable }
  loadedAutoSeed.value = auto_seed ?? null
  loadedFormSnapshot.value = JSON.stringify(form.value)
}

const formRef = ref<HTMLElement | null>(null)

const resetForm = () => {
  form.value = {
    category_id: categories.value[0]?.id,
    question_type: 'QUERY',
    dml_kind: '',
    seed_sql: '',
    points: 1.0,
    strict_ordering: false,
    strict_attrs: false,
    strict_column_order: false,
    dual_schema: false
  }
  loadedAutoSeed.value = null
  previewRows.value = []
  previewCols.value = []
  previewError.value = ''
  previewAsStudentRows.value = []
  previewAsStudentCols.value = []
  previewAsStudentError.value = ''
  loadedFormSnapshot.value = JSON.stringify(form.value)
}
const resetSubmissionForm = () => {
  form.value = {
    category_id: form.value.category_id,
    question_type: 'QUERY',
    dml_kind: '',
    seed_sql: '',
    points: 1.0,
    strict_ordering: false,
    strict_attrs: false,
    strict_column_order: false,
    dual_schema: false
  }
  loadedAutoSeed.value = null
  previewRows.value = []
  previewCols.value = []
  previewError.value = ''
  previewAsStudentRows.value = []
  previewAsStudentCols.value = []
  previewAsStudentError.value = ''
  loadedFormSnapshot.value = JSON.stringify(form.value)
}


async function newPracticeQuestion() {
  if (!(await confirmDiscardIfDirty())) return
  resetForm()
  formRef.value?.scrollIntoView({ behavior: 'smooth' })
}

const { inFlight: saveQuestionLoading, run: saveQuestion } = useSingleFlight(async () => {
  practiceForm.markTouched('correct_sql')
  if (!practiceForm.validateAll()) return
  if (!form.value.category_id) {
    notify('Please select a category. Create one first if none exist.', 'error')
    return
  }
  try {
    const res = await upsertQuestion(form.value)
    if (res.expected_result_status === 'captured') {
      notify('Question saved and result captured', 'success')
    } else if (res.expected_result_status === 'failed') {
      notify(`Question saved, but capture failed: ${res.capture_error}`, 'error')
    } else {
      notify('Question saved', 'success')
    }
    // Non-blocking warnings from the backend — never prevent the save flow below.
    if (res.table_warning) notify(res.table_warning, 'error')
    if (res.dual_schema_warning) notify(res.dual_schema_warning, 'error')
    if (res.mixed_dual_warning) notify(res.mixed_dual_warning, 'error')
    await loadQuestions()
    clearDraft()
    resetSubmissionForm()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to save question' })
  }
})

const { inFlight: deleteQuestionLoading, run: handleDelete } = useSingleFlight(async (id: string) => {
  const ok = await confirm({ title: 'Delete this practice question?', body: 'This cannot be undone.', confirmLabel: 'Delete', danger: true })
  if (!ok) return
  try {
    await deleteQuestion(id)
    notify('Question deleted', 'success')
    await loadQuestions()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to delete question' })
  }
})

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
  } catch (err) {
    previewError.value = (err as Error).message || 'Preview failed'
    handleApiError(err, { fallback: 'Preview failed' })
  } finally {
    previewLoading.value = false
  }
}

// Runs the same correct_sql against the decoy schema (schema: 'preview') so the
// lecturer can see exactly what the student sees on a dual-schema question.
// Unlike handlePreview, this must send category_id — the backend needs it to
// resolve the category's decoy schema.
const handlePreviewAsStudent = async () => {
  previewAsStudentLoading.value = true
  previewAsStudentError.value = ''
  try {
    const payload = {
      type: form.value.question_type ?? 'QUERY',
      target_table: form.value.target_table ?? '',
      correct_sql: form.value.correct_sql ?? '',
      category_id: form.value.category_id,
      schema: 'preview' as const
    }
    const res = await lecturerPreview(payload)
    if (res.status === 'error') {
      previewAsStudentError.value = res.error || 'SQL execution failed'
      previewAsStudentRows.value = []
      previewAsStudentCols.value = []
    } else {
      previewAsStudentRows.value = res.rows || []
      previewAsStudentCols.value = res.columns || []
    }
  } catch (err) {
    previewAsStudentError.value = (err as Error).message || 'Preview failed'
    handleApiError(err, { fallback: 'Preview failed' })
  } finally {
    previewAsStudentLoading.value = false
  }
}

// --- Category management handlers ---

const { inFlight: addCategoryLoading, run: addCategory } = useSingleFlight(async () => {
  const name = newCategoryName.value.trim()
  if (!name) return
  try {
    const cat = await createCategory(name)
    notify(`Category "${cat.name}" created`, 'success')
    newCategoryName.value = ''
    await loadCategories()
    form.value.category_id = cat.id
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create category' })
  }
})

const doRenameCategory = async (cat: PracticeCategory) => {
  const next = window.prompt('Rename category', cat.name)
  if (next === null) return
  const name = next.trim()
  if (!name || name === cat.name) return
  try {
    await renameCategory(cat.id, name)
    notify('Category renamed', 'success')
    await loadCategories()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to rename category' })
  }
}

const doDeleteCategory = async (cat: PracticeCategory) => {
  const ok = await confirm({ title: `Delete "${cat.name}"?`, body: 'The category and its database will be removed.', confirmLabel: 'Delete', danger: true })
  if (!ok) return
  try {
    await deleteCategory(cat.id, false)
    notify('Category deleted', 'success')
    await Promise.all([loadCategories(), loadQuestions()])
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      const force = await confirm({ title: `Delete "${cat.name}" and its questions?`, body: UI_COPY.DELETE_CATEGORY_CONFIRM, confirmLabel: 'Delete everything', danger: true })
      if (!force) return
      try {
        await deleteCategory(cat.id, true)
        notify('Category and its questions deleted', 'success')
        await Promise.all([loadCategories(), loadQuestions()])
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to delete category' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to delete category' })
  }
}

const importFileName = ref('')

const openImport = (cat: PracticeCategory) => {
  importCategoryId.value = cat.id
  importSql.value = ''
  importFileName.value = ''
}

const cancelImport = () => {
  importCategoryId.value = null
  importSql.value = ''
  importFileName.value = ''
}

// loadSqlFile reads a chosen .sql file's text into importSql so the existing
// import flow (doImport) can send it. The backend takes SQL text, so this is a
// pure client-side read — no multipart upload needed.
const loadSqlFile = (event: Event) => {
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
    importSql.value = typeof reader.result === 'string' ? reader.result : ''
    importFileName.value = file.name
  }
  reader.onerror = () => notify('Failed to read the file', 'error')
  reader.readAsText(file)
  input.value = '' // reset so re-selecting the same file re-fires @change
}

const { inFlight: importLoading, run: doImport } = useSingleFlight(async () => {
  if (importCategoryId.value === null || !importSql.value.trim()) return
  const id = importCategoryId.value
  try {
    await importCategorySchema(id, importSql.value, false)
    notify('Database imported', 'success')
    cancelImport()
    await loadCategories()
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      const ok = await confirm({ title: 'Replace existing database?', body: UI_COPY.REPLACE_CATEGORY_DB_CONFIRM, confirmLabel: 'Replace', danger: true })
      if (!ok) return
      try {
        await importCategorySchema(id, importSql.value, true)
        notify('Database replaced', 'success')
        cancelImport()
        await loadCategories()
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to import database' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to import database' })
  }
})

// --- Decoy (preview-only) schema import — parallel to the grading-schema
// import above, same 409-then-confirm-replace flow, targeting
// importCategoryPreviewSchema instead of importCategorySchema. ---

const importPreviewFileName = ref('')

const openImportPreview = (cat: PracticeCategory) => {
  importPreviewCategoryId.value = cat.id
  importPreviewSql.value = ''
  importPreviewFileName.value = ''
}

const cancelImportPreview = () => {
  importPreviewCategoryId.value = null
  importPreviewSql.value = ''
  importPreviewFileName.value = ''
}

// Mirrors loadSqlFile for the decoy-schema file input.
const loadPreviewSqlFile = (event: Event) => {
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
    importPreviewSql.value = typeof reader.result === 'string' ? reader.result : ''
    importPreviewFileName.value = file.name
  }
  reader.onerror = () => notify('Failed to read the file', 'error')
  reader.readAsText(file)
  input.value = '' // reset so re-selecting the same file re-fires @change
}

const { inFlight: importPreviewLoading, run: doImportPreview } = useSingleFlight(async () => {
  if (importPreviewCategoryId.value === null || !importPreviewSql.value.trim()) return
  const id = importPreviewCategoryId.value
  try {
    const res = await importCategoryPreviewSchema(id, importPreviewSql.value, false)
    notify('Decoy schema imported', 'success')
    if (res.parity_warning) notify(res.parity_warning, 'error')
    cancelImportPreview()
    await loadCategories()
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      const ok = await confirm({ title: 'Replace existing decoy schema?', body: 'This category already has a decoy schema. Importing will drop the existing tables and replace them. Continue?', confirmLabel: 'Replace', danger: true })
      if (!ok) return
      try {
        const res2 = await importCategoryPreviewSchema(id, importPreviewSql.value, true)
        notify('Decoy schema replaced', 'success')
        if (res2.parity_warning) notify(res2.parity_warning, 'error')
        cancelImportPreview()
        await loadCategories()
      } catch (err2) {
        handleApiError(err2, { fallback: 'Failed to import decoy schema' })
      }
      return
    }
    handleApiError(err, { fallback: 'Failed to import decoy schema' })
  }
})

onMounted(() => {
  loadQuestions()
  loadCategories()
  showDraftBanner.value = hasDraft()
})
</script>

<template>
  <div class="practice-authoring">
    <div class="page-header">
      <h1>Practice Authoring</h1>
      <p class="page-subtitle">Maintain practice categories, import per-category schemas, and author practice questions.</p>
    </div>
    <BackLink to="/lecturer" label="Back to Lecturer Dashboard" />

    <!-- Both sections stay mounted in both modes and keep their DOM order; only
         the wrapper class changes, so switching layouts never unmounts a
         half-filled import editor or collapses an open category card. -->
    <div class="authoring-layout" :class="`authoring-layout--${layoutMode}`">
    <!-- UPPER section: Practice Questions list + Create/Edit form. Behaviour unchanged from before the split. -->
    <section class="authoring-section authoring-section--upper">
      <!-- Section A: Question List -->
      <div class="card" style="margin-bottom: var(--space-4);">
        <h2>Practice Questions</h2>
        <div v-if="questions.length === 0" class="muted">No questions yet.</div>
        <!-- An 8-column table has a min-content width wider than a narrow
             viewport. Without this wrapper the table overflows its card and
             the whole page scrolls sideways. -->
        <div v-else class="responsive-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>ID</th>
              <th>Title</th>
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
              <td>{{ q.title || '—' }}</td>
              <td style="font-weight: 700; font-size: 0.8rem;">{{ q.category_name || categoryNameById[q.category_id] || q.category }}</td>
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
                  <button class="btn btn-secondary btn-sm" :disabled="deleteQuestionLoading" @click="handleDelete(q.id)">Delete</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        </div>
      </div>

      <!-- Section C: Create/Edit Form -->
      <div class="card" style="margin-bottom: var(--space-4);" ref="formRef">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: var(--space-4);">
          <div style="display: flex; align-items: center; gap: var(--space-3);">
            <h2 style="margin: 0;">{{ form.id ? 'Edit' : 'Create' }} Practice Question</h2>
            <span v-if="form.id" class="badge badge-info" style="font-size: 0.8rem;">Editing #{{ form.id }}</span>
            <span v-else class="muted" style="font-size: 0.85rem;">New question</span>
          </div>
          <div style="display: flex; gap: var(--space-2);">
            <!-- New Practice Question is disabled when already in create mode (no form.id) -->
            <button class="btn btn-primary" :disabled="!form.id" @click="newPracticeQuestion">
              New Practice Question
            </button>
            <button class="btn btn-secondary" @click="resetForm">Reset Form</button>
          </div>
        </div>

        <div style="display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-4);">
          <div class="field">
            <label>Category</label>
            <select v-model="form.category_id" class="input">
              <option v-if="categories.length === 0" :value="undefined" disabled>No categories — create one</option>
              <option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
          </div>
          <div class="field">
            <label>Question Type</label>
            <select v-model="questionKind" class="input">
              <option value="QUERY">QUERY</option>
              <option value="INSERT">INSERT</option>
              <option value="UPDATE">UPDATE</option>
              <option value="DELETE">DELETE</option>
              <option value="DML" disabled>DML (pick a kind)</option>
            </select>
          </div>
          <div class="field">
            <label>Question No.</label>
            <input v-model="form.title" class="input" placeholder="e.g. Employees hired after 2020" />
          </div>
          <div class="field" v-if="form.question_type === 'DML'">
            <label>Target Table (INSERT/UPDATE/DELETE only)</label>
            <input v-model="form.target_table" class="input" placeholder="e.g. employee" />
          </div>
          <div v-if="showDraftBanner" class="draft-banner">
            A draft from your previous session is available.
            <button type="button" class="btn btn-sm" @click="acceptDraft">Restore</button>
            <button type="button" class="btn btn-sm" @click="dismissDraft">Discard</button>
          </div>
          <div class="field" style="grid-column: span 2;">
            <label>Instructions</label>
            <textarea v-model="form.requirement" class="input" rows="3"
              :aria-invalid="practiceForm.touched.requirement && !!practiceForm.errors.requirement"
              aria-describedby="err-practice-req"
              @blur="practiceForm.markTouched('requirement')"
            ></textarea>
            <FieldError :message="practiceForm.touched.requirement ? practiceForm.errors.requirement : ''" id="err-practice-req" />
          </div>
          <div class="field">
            <label>Points</label>
            <input type="number" v-model.number="form.points" class="input" step="0.1" />
          </div>
          <div v-if="form.question_type !== 'DML'" class="field" style="display: flex; flex-direction: row; gap: var(--space-4); align-items: flex-end;">
            <label style="display: flex; align-items: center; gap: var(--space-1); cursor: pointer; font-weight: 700; margin-bottom: 0;">
              <input type="checkbox" v-model="form.strict_ordering" /> Strict Ordering
            </label>
            <label style="display: flex; align-items: center; gap: var(--space-1); cursor: pointer; font-weight: 700; margin-bottom: 0;">
              <input type="checkbox" v-model="form.strict_attrs" /> Strict Attrs
            </label>
            <label style="display: flex; align-items: center; gap: var(--space-1); cursor: pointer; font-weight: 700; margin-bottom: 0;">
              <input type="checkbox" v-model="form.strict_column_order" /> Strict Column Order
            </label>
          </div>
          <div v-else class="field muted" style="align-self: flex-end;">
            INSERT/UPDATE/DELETE answers are graded on the final table contents as an unordered set of rows, matched by column name. Ordering and column-order options do not apply.
          </div>
          <!-- Dual-schema control is hidden from the authoring form. The field
               itself still travels with the question (form.dual_schema, set by
               the defaults in resetForm / the loaded question) — only the
               lecturer-facing toggle and its "no decoy schema yet" warning are
               gone. Removed from the DOM rather than marked `hidden`: this div
               carried an inline `display: flex`, and an inline declaration
               beats the UA's `[hidden] { display: none }`, so the attribute
               alone left it fully visible. -->
        </div>

        <div class="field" style="margin-top: var(--space-4);">
          <label>Correct SQL</label>
          <SqlEditor
            :modelValue="form.correct_sql ?? ''"
            @update:modelValue="form.correct_sql = $event"
            height="200px"
          />
          <FieldError id="practice-correct-sql-error" :message="practiceForm.touched.correct_sql ? practiceForm.errors.correct_sql : ''" />
        </div>

        <div class="field" style="grid-column: span 2; margin-top: var(--space-4);" v-if="seedApplies">
          <label>Seed rows (sandbox only)</label>
          <textarea v-model="form.seed_sql" class="input" rows="4"
            placeholder="INSERT INTO employee (ssn, fname) VALUES ('999999999', 'Test');"></textarea>
          <small class="muted">
            These INSERT statements run inside a transaction that is always rolled back.
            They give your UPDATE/DELETE something to act on. They never modify the imported database.
          </small>
        </div>

        <div v-if="!form.seed_sql && loadedAutoSeed" class="field" style="margin-top: var(--space-4);">
          <label>Auto-seeded row</label>
          <code>{{ autoSeedStatement }}</code>
          <p class="muted" style="margin: 0;">
            This row does not exist in the category database. The system adds it inside a transaction that is rolled back, so the real database is unchanged.
          </p>
        </div>

        <div style="display: flex; gap: var(--space-2); margin-top: var(--space-4);">
          <button
            class="btn btn-primary"
            @click="saveQuestion"
            :disabled="saveQuestionLoading"
          >
            <Spinner v-if="saveQuestionLoading" size="sm" inline :invert="true" />{{ saveQuestionLoading ? 'Saving' : 'Save Question' }}
          </button>
          <button
            class="btn btn-secondary"
            @click="handlePreview"
            :disabled="previewLoading"
          >
            {{ previewLoading ? 'Running...' : 'Preview' }}
          </button>
          <button
            class="btn btn-secondary"
            @click="handlePreviewAsStudent"
            :disabled="previewAsStudentLoading"
          >
            {{ previewAsStudentLoading ? 'Running...' : 'Preview as Student' }}
          </button>
        </div>

        <!-- Preview Results (grading schema B — same as the student's submit-time verdict source) -->
        <div v-if="previewError" class="badge badge-danger" style="margin-top: var(--space-4); display: block; padding: var(--space-2); font-family: monospace;">
          {{ previewError }}
        </div>
        <div v-if="previewRows.length > 0" style="margin-top: var(--space-4);">
          <h3>Preview Results</h3>
          <ResultTable :rows="previewRows" :columns="previewCols" />
        </div>

        <!-- Preview-as-student Results (decoy schema A — what the student actually sees for a dual-schema question) -->
        <div v-if="previewAsStudentError" class="badge badge-danger" style="margin-top: var(--space-4); display: block; padding: var(--space-2); font-family: monospace;">
          {{ previewAsStudentError }}
        </div>
        <div v-if="previewAsStudentRows.length > 0" style="margin-top: var(--space-4);">
          <h3>Preview Results (as student sees — decoy schema)</h3>
          <ResultTable :rows="previewAsStudentRows" :columns="previewAsStudentCols" />
        </div>
      </div>
    </section>

    <!-- LOWER section: Categories Management. A `position: fixed` overlay in
         BOTH modes (see .authoring-section--lower.is-docked in style.css) —
         'side' docks it to the right edge at full viewport height, 'bottom'
         to the bottom edge at full viewport width. The dock switch sits in the
         panel header beside Minimize (not a separate page-level bar, and no
         longer an edge strip eating the content's width). Minimize is
         available in both modes: gating it on one mode left the other with no
         way to get the overlay out of the way. -->
    <section
      class="authoring-section authoring-section--lower is-docked"
      :class="[`dock-${layoutMode}`, { 'is-collapsed': categoriesPanelCollapsed }]"
      aria-labelledby="practice-categories-heading"
    >
      <div class="authoring-panel-shell" :class="`authoring-panel-shell--${layoutMode}`">
      <div class="card authoring-panel-content">
        <div class="authoring-floating-panel-header">
          <h2 id="practice-categories-heading">{{ UI_COPY.MANAGE_CATEGORIES }}</h2>
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
              class="btn btn-sm btn-secondary"
              data-testid="floating-categories-minimize"
              aria-label="Minimize categories panel"
              @click="toggleCategoriesPanel"
            >Minimize</button>
          </span>
        </div>
        <p class="muted">Each category owns its own database (schema), kept separate from exams. Import a <code>.sql</code> per category.</p>

        <!-- New category -->
        <div style="display: flex; gap: var(--space-2); margin-bottom: var(--space-4);">
          <input v-model="newCategoryName" class="input" placeholder="New category name" aria-label="New category name" style="flex: 1;" @keyup.enter="addCategory" />
          <button class="btn btn-primary" :disabled="addCategoryLoading || !newCategoryName.trim()" @click="addCategory">
            {{ UI_COPY.NEW_CATEGORY }}
          </button>
        </div>

        <!-- Per-category collapsible cards -->
        <div v-if="categories.length > 0">
          <CollapsibleCard v-for="c in categories" :key="c.id" :title="c.name" :test-id="'category-' + c.id">
            <template #header-meta>
              <code>{{ c.schema_name }}</code>
              <span class="muted" style="font-size: 0.8rem;">{{ c.question_count }} questions</span>
              <span v-if="c.has_preview_schema" class="badge badge-info">decoy</span>
            </template>
            <template #header-actions>
              <button class="btn btn-secondary btn-sm" @click="doRenameCategory(c)">{{ UI_COPY.RENAME_CATEGORY }}</button>
              <button class="btn btn-secondary btn-sm" @click="doDeleteCategory(c)" style="border-color: var(--color-danger); color: var(--color-danger);">{{ UI_COPY.DELETE_CATEGORY }}</button>
            </template>

            <!-- Database schema sub-panel -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Database schema (hidden schema)</span>
              <div v-if="importCategoryId === c.id">
                <h3>Import database into "{{ c.name }}"</h3>
                <p class="muted">Upload a <code>.sql</code> file or paste a SQL script, using unqualified names (no <code>public.</code> / <code>app.</code> prefixes).</p>
                <div style="display: flex; gap: var(--space-2); align-items: center; margin-bottom: var(--space-2);">
                  <label class="btn btn-secondary btn-sm" style="cursor: pointer; margin: 0;">
                    Choose .sql file
                    <input type="file" accept=".sql,text/plain,application/sql" @change="loadSqlFile" style="display: none;" />
                  </label>
                  <span v-if="importFileName" class="muted">Loaded: <code>{{ importFileName }}</code> — review or edit below, then import.</span>
                </div>
                <SqlEditor v-model="importSql" height="200px" />
                <div style="display: flex; gap: var(--space-2); margin-top: var(--space-2);">
                  <button class="btn btn-primary" :disabled="importLoading || !importSql.trim()" @click="doImport">
                    <Spinner v-if="importLoading" size="sm" inline :invert="true" />{{ importLoading ? 'Importing' : UI_COPY.IMPORT_CATEGORY_DB }}
                  </button>
                  <button class="btn btn-secondary" @click="cancelImport">Cancel</button>
                </div>
              </div>
              <div v-else style="display: flex; justify-content: space-between; align-items: center; gap: var(--space-2);">
                <p class="muted" style="margin: 0;">Import a .sql to (re)load this category's database.</p>
                <button class="btn btn-secondary btn-sm" @click="openImport(c)">{{ UI_COPY.IMPORT_CATEGORY_DB }}</button>
              </div>
            </div>

            <!-- Decoy database schema sub-panel (preview only — same tables/columns as
                 the grading schema above, different data; used by dual-schema questions
                 so the student's preview never leaks the grading data). -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Decoy database schema (student preview schema)</span>
              <div v-if="importPreviewCategoryId === c.id">
                <h3>Import decoy database into "{{ c.name }}"</h3>
                <p class="muted">Upload a <code>.sql</code> file or paste a SQL script with the same tables and columns as the grading database above, but different data. Students previewing a dual-schema question see this data instead of the real grading data.</p>
                <div style="display: flex; gap: var(--space-2); align-items: center; margin-bottom: var(--space-2);">
                  <label class="btn btn-secondary btn-sm" style="cursor: pointer; margin: 0;">
                    Choose .sql file
                    <input type="file" accept=".sql,text/plain,application/sql" @change="loadPreviewSqlFile" style="display: none;" />
                  </label>
                  <span v-if="importPreviewFileName" class="muted">Loaded: <code>{{ importPreviewFileName }}</code> — review or edit below, then import.</span>
                </div>
                <SqlEditor v-model="importPreviewSql" height="200px" />
                <div style="display: flex; gap: var(--space-2); margin-top: var(--space-2);">
                  <button class="btn btn-primary" :disabled="importPreviewLoading || !importPreviewSql.trim()" @click="doImportPreview">
                    <Spinner v-if="importPreviewLoading" size="sm" inline :invert="true" />{{ importPreviewLoading ? 'Importing' : 'Import Decoy Schema' }}
                  </button>
                  <button class="btn btn-secondary" @click="cancelImportPreview">Cancel</button>
                </div>
              </div>
              <div v-else style="display: flex; justify-content: space-between; align-items: center; gap: var(--space-2);">
                <p class="muted" style="margin: 0;">
                  {{ c.has_preview_schema ? 'Decoy schema loaded — used for dual-schema question previews.' : 'Import a decoy .sql (same structure, different data) to enable dual-schema previews for this category.' }}
                </p>
                <button class="btn btn-secondary btn-sm" @click="openImportPreview(c)">Import Decoy Schema</button>
              </div>
            </div>

            <!-- Description sub-panel -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Description (shown to students on the intro page)</span>
              <div style="display: flex; gap: var(--space-2); align-items: flex-end;">
                <textarea
                  v-model="categoryIntroEdits[c.id]"
                  class="input"
                  rows="3"
                  style="flex: 1; resize: vertical; min-height: 60px;"
                  :placeholder="'Write a description for the ' + c.name + ' category...'"
                  :aria-label="'Description for the ' + c.name + ' category'"
                ></textarea>
                <button
                  class="btn btn-primary btn-sm"
                  :disabled="categoryIntroSaving[c.id]"
                  @click="saveCategoryIntro(c)"
                  style="white-space: nowrap;"
                >{{ categoryIntroSaving[c.id] ? 'Saving...' : 'Save Description' }}</button>
              </div>
            </div>

            <!-- Introduction images sub-panel -->
            <div class="cat-subpanel">
              <span class="cat-subpanel-label">Introduction images</span>
              <IntroImagesEditor scope="category" :owner-id="c.id" />
            </div>
          </CollapsibleCard>
        </div>
        <p v-else class="muted">No categories yet. Create one above.</p>
      </div>
      </div><!-- /.authoring-panel-shell -->
    </section>
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
    >{{ UI_COPY.MANAGE_CATEGORIES }}</button>
  </div>
</template>

<style scoped>
.practice-authoring { /* sits inside .cms-main */ }
.card {
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-5);
  border-radius: var(--radius);
  box-shadow: var(--shadow-1);
}
h2 {
  margin-top: 0;
  margin-bottom: var(--space-3);
  font-size: 1.1rem;
  font-weight: 700;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}
h3 {
  font-size: 0.98rem;
  margin-bottom: 6px;
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
  grid-column: span 2;
}

/* .authoring-layout / .authoring-section--upper / --lower geometry lives in the
   global .authoring-* rules in style.css. Do NOT add a scoped rule for those
   selectors here: scoped styles compile to `.foo[data-v-x]` (specificity
   0,2,0) and are injected AFTER style.css, so even a single-class scoped rule
   ties the global two-class rules and wins on order. A scoped
   `.authoring-section { display: block }` used to live here and silently beat
   both `.is-docked { display: flex }` (breaking the flex chain the panel's
   internal scrolling depends on) and `.is-collapsed { display: none }`
   (making Minimize a no-op). It was also redundant — <section> is already
   display:block per the UA stylesheet. */
.cat-subpanel {
  border: 1px solid var(--color-border-soft);
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
</style>
