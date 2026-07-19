<script setup lang="ts">
import { ref, onMounted, computed, watch, nextTick } from 'vue'
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
  setCategoryIntroduction,
  ApiError,
  type PracticeQuestion,
  type PracticeCategory
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

const { notify } = useNotifications()
const { confirm } = useConfirm()

const questions = ref<PracticeQuestion[]>([])
const categories = ref<PracticeCategory[]>([])
const form = ref<Partial<PracticeQuestion>>({
  question_type: 'QUERY',
  points: 1.0,
  strict_ordering: false,
  strict_attrs: false,
  strict_column_order: false
})
const previewRows = ref<any[]>([])
const previewCols = ref<string[]>([])
const previewLoading = ref(false)
const previewError = ref('')

// --- Category management panel state ---
const showCategoryPanel = ref(false)
const categoryPanelRef = ref<HTMLElement | null>(null)
const newCategoryName = ref('')

// The panel renders as the last card on the page (below the create/edit form),
// so on open we scroll it into view — otherwise clicking the button appears to
// do nothing because the panel opens below the fold.
const toggleCategoryPanel = async () => {
  showCategoryPanel.value = !showCategoryPanel.value
  if (showCategoryPanel.value) {
    await nextTick()
    categoryPanelRef.value?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }
}
const importCategoryId = ref<string | null>(null)
const importSql = ref('')

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

const editQuestion = (q: PracticeQuestion) => {
  form.value = { ...q }
}

const formRef = ref<HTMLElement | null>(null)

const resetForm = () => {
  form.value = {
    category_id: categories.value[0]?.id,
    question_type: 'QUERY',
    points: 1.0,
    strict_ordering: false,
    strict_attrs: false,
    strict_column_order: false
  }
  previewRows.value = []
  previewCols.value = []
  previewError.value = ''
}

function newPracticeQuestion() {
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
    await loadQuestions()
    clearDraft()
    resetForm()
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
    <!-- Section A: Question List -->
    <div class="card" style="margin-bottom: var(--space-4);">
      <h2>Practice Questions</h2>
      <div v-if="questions.length === 0" class="muted">No questions yet.</div>
      <table v-else class="data-table">
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


    <!-- Section B: Manage Categories -->
    <div v-if="showCategoryPanel" class="card" ref="categoryPanelRef">
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <h2 style="margin: 0;">{{ UI_COPY.MANAGE_CATEGORIES }}</h2>
        <button class="btn btn-secondary btn-sm" @click="showCategoryPanel = false">Close</button>
      </div>
      <p class="muted">Each category owns its own database (schema), kept separate from exams. Import a <code>.sql</code> per category.</p>

      <!-- New category -->
      <div style="display: flex; gap: var(--space-2); margin-bottom: var(--space-4);">
        <input v-model="newCategoryName" class="input" placeholder="New category name" style="flex: 1;" @keyup.enter="addCategory" />
        <button class="btn btn-primary" :disabled="addCategoryLoading || !newCategoryName.trim()" @click="addCategory">
          {{ UI_COPY.NEW_CATEGORY }}
        </button>
      </div>

      <!-- Per-category card stack -->
      <div v-if="categories.length > 0">
        <div v-for="c in categories" :key="c.id" class="category-card">
          <!-- Card header -->
          <div style="display: flex; justify-content: space-between; align-items: center;">
            <div style="display: flex; align-items: baseline; gap: var(--space-2); flex-wrap: wrap;">
              <h3 style="margin: 0;">{{ c.name }}</h3>
              <code>{{ c.schema_name }}</code>
              <span class="muted" style="font-size: 0.8rem;">{{ c.question_count }} questions</span>
            </div>
            <div style="display: flex; gap: var(--space-1); flex-wrap: wrap;">
              <button class="btn btn-secondary btn-sm" @click="doRenameCategory(c)">{{ UI_COPY.RENAME_CATEGORY }}</button>
              <button class="btn btn-secondary btn-sm" @click="openImport(c)">{{ UI_COPY.IMPORT_CATEGORY_DB }}</button>
              <button class="btn btn-secondary btn-sm" @click="doDeleteCategory(c)" style="border-color: var(--color-danger); color: var(--color-danger);">{{ UI_COPY.DELETE_CATEGORY }}</button>
            </div>
          </div>

          <!-- Database schema sub-panel -->
          <div class="cat-subpanel">
            <span class="cat-subpanel-label">Database schema</span>
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
            <p v-else class="muted" style="margin: 0;">Import a .sql to (re)load this category's database.</p>
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
        </div>
      </div>
      <p v-else class="muted">No categories yet. Create one above.</p>
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
          <div style="display: flex; gap: var(--space-2); align-items: center;">
            <select v-model="form.category_id" class="input" style="flex: 1;">
              <option v-if="categories.length === 0" :value="undefined" disabled>No categories — create one</option>
              <option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
            <button type="button" class="btn btn-secondary btn-sm" @click="toggleCategoryPanel">
              {{ UI_COPY.MANAGE_CATEGORIES }}
            </button>
          </div>
        </div>
        <div class="field">
          <label>Question Type</label>
          <select v-model="form.question_type" class="input">
            <option value="QUERY">QUERY</option>
            <option value="DML">DML</option>
          </select>
        </div>
        <div class="field">
          <label>Title <span class="muted">(optional — shown to students instead of a category ordinal)</span></label>
          <input v-model="form.title" class="input" placeholder="e.g. Employees hired after 2020" />
        </div>
        <div class="field">
          <label>Target Table (DML only)</label>
          <input v-model="form.target_table" class="input" placeholder="e.g. employee" />
        </div>
        <div v-if="showDraftBanner" class="draft-banner">
          A draft from your previous session is available.
          <button type="button" class="btn btn-sm" @click="acceptDraft">Restore</button>
          <button type="button" class="btn btn-sm" @click="dismissDraft">Discard</button>
        </div>
        <div class="field" style="grid-column: span 2;">
          <label>Requirement</label>
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
        <div class="field" style="display: flex; flex-direction: row; gap: var(--space-4); align-items: flex-end;">
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

.category-card {
  border: 2px solid var(--color-border);
  border-left: 4px solid var(--color-primary);
  padding: var(--space-4);
  margin-bottom: var(--space-4);
  background: var(--color-surface);
}
.category-card:nth-of-type(even) {
  background: var(--color-surface-muted);
}
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
