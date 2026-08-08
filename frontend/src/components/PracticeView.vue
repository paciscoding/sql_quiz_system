<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import SqlEditor from './SqlEditor.vue'
import BackLink from './BackLink.vue'
import ResultTable from './ResultTable.vue'
import SchemaIntroWindow from './SchemaIntroWindow.vue'
import SchemaImageWindow from './SchemaImageWindow.vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import Spinner from './Spinner.vue'
import { useKeyboardShortcut } from '../composables/useKeyboardShortcut'
import { formatSql } from '../utils/format_sql'
import { formatDate, formatRelative } from '../utils/format_date'
import { UI_COPY } from '../constants/ui_copy'
import {
  listQuestions,
  submit,
  preview,
  listMyAttempts,
  getCategoryIntroduction,
  PracticeUnavailableError,
  type PracticeQuestion,
  type PracticeAttempt,
  type ValidationResult,
  type CategoryIntroduction
} from '../services/practice'
import { listCategoryIntroImages, type IntroImageMetadata } from '../services/schemaImages'

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
const editorWrapRef = ref<HTMLElement | null>(null)
const previewLoading = ref(false)
const questionListCollapsed = ref(false)
const attemptsCollapsed = ref(false)

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
      handleApiError(err, { fallback: 'Failed to load practice questions.' })
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

const { inFlight: submitLoading, run: doSubmit } = useSingleFlight(async () => {
  if (!selectedQuestion.value || !sqlText.value.trim()) return
  result.value = null
  try {
    await doPreview()
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
      handleApiError(err, { fallback: 'Submit failed.' })
    }
  }
})

useKeyboardShortcut({ combo: 'Ctrl+Enter', whenTyping: true, scope: editorWrapRef, handler: () => doSubmit() })

const doPreview = async () => {
  if (!selectedQuestion.value || !sqlText.value.trim()) return
  previewLoading.value = true
  previewRows.value = []
  previewCols.value = []
  try {
    const res = await preview(selectedQuestion.value.id, sqlText.value)
    previewRows.value = res.rows
    previewCols.value = res.columns
  } catch (err) {
    if (err instanceof PracticeUnavailableError) {
      practiceUnavailable.value = true
    } else {
      handleApiError(err, { fallback: 'Preview failed.' })
    }
  } finally {
    previewLoading.value = false
  }
}

const badgeClassFor = (s: string) => s === 'correct' ? 'badge-success' : s === 'syntax_error' ? 'badge-danger' : 'badge-warning'
const statusLabel = (s: string) => s === 'correct' ? 'Passed' : s === 'syntax_error' ? 'SQL Error' : 'Incorrect'

const truncate = (str: string, len: number) => {
  if (str.length <= len) return str
  return str.substring(0, len) + '...'
}

const categoryNameFor = (q: PracticeQuestion) => q.category_name || q.category || 'Uncategorized'

// Groups every question by category, uncapped — the per-category list is
// scrollable in the template (ARCH-009) instead of truncating questions past
// a fixed count, so lecturers can add more than 5 to a category and students
// still reach all of them.
const groupedQuestions = computed(() => {
  const groups: Record<string, PracticeQuestion[]> = {}
  questions.value.forEach(q => {
    const key = categoryNameFor(q)
    if (!groups[key]) {
      groups[key] = []
    }
    groups[key].push(q)
  })
  return groups
})

// questionLabel returns the title when set, else a '#N' ordinal that is
// relative to the question's position within its category group — never the
// raw opaque id/token.
const questionLabel = (q: PracticeQuestion): string => {
  if (q.title) return q.title
  const group = groupedQuestions.value[categoryNameFor(q)] || []
  const idx = group.findIndex(item => item.id === q.id)
  return '#' + (idx >= 0 ? idx + 1 : 1)
}

// Client-side id -> label map, used to give Attempt History rows a question
// identity (attempts only carry practice_question_id, an opaque token).
const questionLabelMap = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {}
  Object.values(groupedQuestions.value).forEach(group => {
    group.forEach(q => { map[q.id] = questionLabel(q) })
  })
  return map
})

// --- Per-category Introduction panel ---
// categoryIntroCache maps category_id -> fetched intro (avoids re-fetching)
const categoryIntroCache = ref<Record<string, CategoryIntroduction>>({})
const activeCategoryIntroId = ref<string | null>(null)
const catIntroImages = ref<IntroImageMetadata[]>([])

const activeIntro = computed<CategoryIntroduction | null>(() =>
  activeCategoryIntroId.value !== null
    ? (categoryIntroCache.value[activeCategoryIntroId.value] ?? null)
    : null
)

const introLoading = ref(false)

const openCategoryIntro = async (q: PracticeQuestion) => {
  const catId = q.category_id
  if (!catId) return
  activeCategoryIntroId.value = catId
  introLoading.value = true
  try {
    const [data, imgs] = await Promise.all([
      categoryIntroCache.value[catId]
        ? Promise.resolve(categoryIntroCache.value[catId])
        : getCategoryIntroduction(catId),
      listCategoryIntroImages(catId).catch(() => [] as IntroImageMetadata[])
    ])
    categoryIntroCache.value = { ...categoryIntroCache.value, [catId]: data }
    catIntroImages.value = imgs
  } catch (err) {
    if (err instanceof PracticeUnavailableError) {
      practiceUnavailable.value = true
    } else {
      handleApiError(err, { fallback: 'Failed to load category introduction.' })
    }
    activeCategoryIntroId.value = null
  } finally {
    introLoading.value = false
  }
}

const closeCategoryIntro = () => {
  activeCategoryIntroId.value = null
}

// Derive a representative question for intro lookup from the category group
const firstQuestionForCategory = (cat: string): PracticeQuestion | null => {
  const qs = groupedQuestions.value[cat]
  return qs && qs.length > 0 ? qs[0] : null
}

async function copyToClipboard(text: string) {
  try {
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      notify('Copied.', 'success')
    } else {
      notify('Clipboard not available in this browser.', 'error')
    }
    RefillData(text)
  } catch {
    notify('Failed to copy text.', 'error')
  }
}

function RefillData(text: string) {
  sqlText.value = text
}

</script>

<template>
  <div class="practice-shell">
    <div class="page-header">
      <h1>Practice</h1>
      <p class="page-subtitle">Browse a practices, review its schema, and submit SQL against the practice sandbox.</p>
    </div>

    <BackLink to="/student" label="Back to Student Dashboard" />
    <!-- Unavailability banner -->
    <div v-if="practiceUnavailable" class="alert-warning" style="display:flex; align-items:center; justify-content:space-between; gap:var(--space-3);">
      <span>Practice is paused while an examination with focus mode is active.</span>
      <button class="btn btn-sm" @click="loadData">Retry</button>
    </div>

    <div class="pane-grid" :class="{ 'list-collapsed': questionListCollapsed }">
      <!-- Question List -->
      <div class="card question-list-card" :class="{ collapsed: questionListCollapsed }">
        <div class="card-header">
          <h3 v-if="!questionListCollapsed" style="margin: 0;">Practices</h3>
          <button
            class="collapse-btn"
            :title="questionListCollapsed ? 'Expand question list' : 'Collapse question list'"
            @click="questionListCollapsed = !questionListCollapsed"
          >{{ questionListCollapsed ? '›' : '‹' }}</button>
        </div>
        <div v-if="!questionListCollapsed" class="question-list">
          <div v-for="(qs, cat) in groupedQuestions" :key="cat" class="category-group">
            <div class="category-header">
              <h4 class="category-title" style="margin: 0; flex: 1;">{{ cat }}</h4>
              <button
                class="btn btn-secondary"
                style="font-size: 0.75rem; padding: 2px 8px;"
                :disabled="introLoading"
                @click="() => { const q = firstQuestionForCategory(String(cat)); if (q) openCategoryIntro(q) }"
                title="View category schema and description"
              >Schema</button>
            </div>
            <div class="question-items-scroll">
              <div
                v-for="q in qs"
                :key="q.id"
                class="question-item"
                :class="{ active: selectedQuestion?.id === q.id }"
                @click="selectQuestion(q)"
              >
                <div class="question-item-header">
                  <span class="q-id">{{ questionLabel(q) }}</span>
                  <span class="badge" :class="q.question_type === 'QUERY' ? 'badge-info' : 'badge-warning'">
                    {{ q.question_type }}
                  </span>
                  <span class="points">{{ q.points }} pts</span>
                </div>
                <div class="requirement-preview">{{ truncate(q.requirement, 60) }}</div>
              </div>
            </div>
          </div>
          <p v-if="questions.length === 0" class="muted">No practice questions available.</p>
        </div>
      </div>

      <!-- Category schema + introduction — floating windows (position:fixed), not
           inline layout, so they no longer force a full-width grid row. Opened by
           the per-category Schema button; each is independently draggable. -->
      <template v-if="activeCategoryIntroId !== null && activeIntro">
        <SchemaImageWindow
          v-if="catIntroImages.length > 0"
          scope="category"
          :owner-id="activeCategoryIntroId!"
          :images="catIntroImages"
        />
        <SchemaIntroWindow
          scope="category"
          :owner-id="activeCategoryIntroId!"
          :introduction="activeIntro.introduction"
          :tables="activeIntro.tables"
          :imported="activeIntro.imported"
          :category-name="activeIntro.category_name"
          mode="student"
          @close="closeCategoryIntro"
        />
      </template>

      <!-- Editor Section -->
      <div class="card editor-card" ref="editorWrapRef">
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

          <template v-if="selectedQuestion.question_type === 'QUERY' && selectedQuestion.expected_columns">
            <h4 style="margin: var(--space-4) 0 var(--space-2) 0;">Expected Result</h4>
            <ResultTable
              :rows="selectedQuestion.expected_result || []"
              :columns="selectedQuestion.expected_columns || []"
              max-height="200px"
            />
          </template>

          <h3 style="margin: var(--space-4) 0 var(--space-2) 0;">Your SQL</h3>
          <SqlEditor v-model="sqlText" height="240px" />

          <div class="actions">
            <button class="btn btn-secondary" :disabled="!sqlText" @click="sqlText = formatSql(sqlText)">Format SQL</button>
            <button
              class="btn btn-primary"
              :disabled="submitLoading || previewLoading || practiceUnavailable"
              @click="doSubmit"
            >
              <Spinner v-if="submitLoading || previewLoading" size="sm" inline :invert="true" />{{ submitLoading || previewLoading ? UI_COPY.SUBMIT_ANSWER_LOADING : UI_COPY.SUBMIT_ANSWER }}
            </button>
            <p v-if="practiceUnavailable" class="muted unavailability-msg">
              Practice is unavailable while an exam is active.
            </p>
          </div>

          <!-- Query Result Rows -->
          <div v-if="previewRows.length > 0" class="result-panel preview-result">
            <div class="result-header">
              <span class="muted small">Query Result</span>
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
      <div class="card-header">
        <h3 style="margin: 0;">Attempt History</h3>
        <button
          class="collapse-btn"
          :title="attemptsCollapsed ? 'Expand attempt history' : 'Collapse attempt history'"
          @click="attemptsCollapsed = !attemptsCollapsed"
        >{{ attemptsCollapsed ? '▾' : '▴' }}</button>
      </div>
      <div v-if="!attemptsCollapsed" class="attempts-list">
        <div v-for="a in attempts" :key="a.id" class="attempt-item">
          <div class="attempt-main">
            <span class="attempt-question">{{ questionLabelMap[a.practice_question_id] || 'Unknown question' }}</span>
            <code class="attempt-sql">{{ truncate(a.sql_text, 80) }}</code>
            <button type="button" class="btn btn-sm" @click="copyToClipboard(a.sql_text)">Copy</button>
            <span class="badge" :class="a.is_correct ? 'badge-success' : 'badge-warning'">
              {{ a.is_correct ? 'Correct' : 'Incorrect' }}
            </span>
          </div>
          <div class="attempt-meta">
            <span :title="new Date(a.submitted_at).toISOString()">{{ formatDate(a.submitted_at) }} <span class="muted small">({{ formatRelative(a.submitted_at) }})</span></span>
          </div>
        </div>
        <p v-if="attempts.length === 0" class="muted">No attempts yet.</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.practice-shell {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}

.card {
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-5);
  border-radius: var(--radius);
  box-shadow: var(--shadow-1);
}

.pane-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: var(--space-5);
  align-items: start;
}

.pane-grid.list-collapsed {
  grid-template-columns: 1fr;
}

.question-list-card.collapsed {
  padding: var(--space-2);
}

.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}

.question-list-card.collapsed .card-header {
  margin-bottom: 0;
  justify-content: center;
}

.collapse-btn {
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
  font-weight: 800;
  font-size: 1rem;
  line-height: 1;
  padding: var(--space-1) var(--space-3);
  cursor: pointer;
}

.collapse-btn:hover {
  background: var(--color-surface-muted);
}

@media (max-width: 1000px) {
  .pane-grid,
  .pane-grid.list-collapsed {
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

.category-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 6px 10px;
  background: var(--color-surface-sunk);
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
}

.category-title {
  margin: 0;
  color: var(--color-text);
  font-size: 0.78rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

/* Caps the visible list at roughly 5 question-items' worth of height, then
   scrolls — categories with more than 5 questions stay fully reachable
   instead of the old hard truncation at 5. */
.question-items-scroll {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  max-height: 400px;
  overflow-y: auto;
}

.question-item {
  padding: 10px 12px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  cursor: pointer;
  transition: background-color .12s ease, border-color .12s ease;
  background: var(--color-surface);
}

.question-item:hover {
  background-color: var(--color-surface-muted);
}

.question-item.active {
  background-color: var(--color-primary-bg);
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
  margin-bottom: var(--space-4);
  border-bottom: 1px solid var(--color-border-soft);
  padding-bottom: var(--space-3);
}

.small {
  font-size: 0.72rem;
  font-weight: 600;
  color: var(--color-text-muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.requirement-box {
  padding: 12px 16px;
  background: #ffffff;
  border: 2px solid #1e40af;
  color: #1e40af;
  border-radius: var(--radius);
  font-size: 0.98rem;
  line-height: 1.55;
  margin-bottom: var(--space-5);
  white-space: pre-line;
}

.actions {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-top: var(--space-6);
  flex-wrap: wrap;
}

.result-panel {
  margin-top: var(--space-5);
  padding: 12px 16px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
}

.result-panel.correct       { background: var(--color-success-bg); color: var(--color-success); border-color: #c4dfba; }
.result-panel.incorrect     { background: var(--color-warning-bg); color: var(--color-warning); border-color: #ecd998; }
.result-panel.syntax_error  { background: var(--color-danger-bg); color: var(--color-danger); border-color: #e9bcbc; }

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
  white-space: pre-line;
  overflow-wrap: break-word;
}

.score {
  font-weight: 800;
  font-size: 1.2rem;
}

.empty-state {
  height: 360px;
  display: flex;
  align-items: center;
  justify-content: center;
  text-align: center;
  font-weight: 500;
  color: var(--color-text-muted);
  border: 1px dashed var(--color-border);
  border-radius: var(--radius);
  font-size: 1rem;
  padding: var(--space-7);
  background: var(--color-surface-muted);
}

.attempts-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.attempt-item {
  padding: 10px 12px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  background: var(--color-surface);
}

.attempt-main {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  margin-bottom: 4px;
}

.attempt-question {
  font-weight: 700;
  font-size: 0.85rem;
  color: var(--color-text);
}

.attempt-sql {
  font-family: var(--font-mono);
  font-size: 0.82rem;
  background: var(--color-surface-sunk);
  padding: 2px 8px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  color: var(--color-text);
}
</style>
