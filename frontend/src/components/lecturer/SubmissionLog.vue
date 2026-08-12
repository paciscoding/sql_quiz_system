<script setup lang="ts">
// Admin/Lecturer Submission Log: an in-app, on-demand, filterable browser
// over every attempt in app.submissions (exam) / app.practice_attempts
// (practice). This is NOT the emailed CSV.gz exporter (services/
// submission_export.go) — it never touches that watermark/table and adds no
// migration. Self-loads its own filter data; takes no required props so it
// can be dropped straight into a route.
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import {
  listSubmissionLog, getSubmissionLogDetail, downloadSubmissionLogCsv
} from '../../services/submissionLog'
import type {
  SubmissionLogSource, SubmissionLogOutcome, SubmissionLogRow,
  ExamSubmissionLogRow, PracticeSubmissionLogRow, SubmissionLogDetail
} from '../../services/submissionLog'
import { handleApiError, toApiError } from '../../services/error_handler'
import EmptyState from '../EmptyState.vue'
import Spinner from '../Spinner.vue'
import Icon from '../Icon.vue'

interface ExamOption { id: string; title: string }

// --- Filter state ---
const source = ref<SubmissionLogSource>('exam')
const examId = ref<string>('')
const studentSearch = ref('')          // free-text; resolved to student_id below
const studentId = ref<string>('')
const questionId = ref<string>('')     // kept as string for the input; parsed on send
const outcome = ref<SubmissionLogOutcome>('all')
const countedOnly = ref(false)
const fromDate = ref('')
const toDate = ref('')

const limit = ref(50)
const offset = ref(0)

const exams = ref<ExamOption[]>([])

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

// --- Results state ---
const rows = ref<SubmissionLogRow[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

function isExamRow(r: SubmissionLogRow): r is ExamSubmissionLogRow {
  return source.value === 'exam'
}

function toRFC3339(dateOnly: string, endOfDay: boolean): string | undefined {
  if (!dateOnly) return undefined
  return endOfDay ? `${dateOnly}T23:59:59Z` : `${dateOnly}T00:00:00Z`
}

async function fetchRows() {
  loading.value = true
  error.value = null
  try {
    const resp = await listSubmissionLog({
      source: source.value,
      examId: examId.value || undefined,
      studentId: studentId.value || undefined,
      questionId: questionId.value ? Number(questionId.value) : undefined,
      outcome: outcome.value,
      countedOnly: countedOnly.value,
      from: toRFC3339(fromDate.value, false),
      to: toRFC3339(toDate.value, true),
      limit: limit.value,
      offset: offset.value
    })
    rows.value = resp.rows || []
    total.value = resp.total || 0
  } catch (err) {
    error.value = handleApiError(err, { fallback: 'Failed to load submission log' })
    rows.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

// Debounce free-text search inputs (student, question id) so we don't fire a
// request on every keystroke; other filters (selects/checkboxes/dates) apply
// immediately.
let debounceTimer: number | null = null
function scheduleFetch(immediate = false) {
  if (debounceTimer != null) window.clearTimeout(debounceTimer)
  if (immediate) {
    fetchRows()
    return
  }
  debounceTimer = window.setTimeout(() => {
    debounceTimer = null
    fetchRows()
  }, 400)
}

// studentSearch is treated as the opaque student_id / username / student
// number the lecturer pastes or types; the backend resolves matching, so we
// forward it verbatim as student_id when it looks like an id, else let the
// backend's own filter semantics apply. To keep the contract exact (the
// endpoint takes student_id, an opaque id), we mirror the text box straight
// into studentId — lecturers copy the id from elsewhere in the app (e.g. the
// results table) rather than typing a name here.
watch(studentSearch, (val) => {
  studentId.value = val.trim()
  offset.value = 0
  scheduleFetch()
})

watch(questionId, () => { offset.value = 0; scheduleFetch() })

watch([source, examId, outcome, countedOnly, fromDate, toDate], () => {
  offset.value = 0
  scheduleFetch(true)
})

watch(offset, () => scheduleFetch(true))

onMounted(() => {
  fetchExams()
  fetchRows()
})

onBeforeUnmount(() => {
  if (debounceTimer != null) window.clearTimeout(debounceTimer)
})

// --- Pagination ---
const page = computed(() => Math.floor(offset.value / limit.value) + 1)
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / limit.value)))
const canPrev = computed(() => offset.value > 0)
const canNext = computed(() => offset.value + limit.value < total.value)

function prevPage() { if (canPrev.value) offset.value = Math.max(0, offset.value - limit.value) }
function nextPage() { if (canNext.value) offset.value = offset.value + limit.value }

// --- CSV download ---
const downloadingCsv = ref(false)
async function onDownloadCsv() {
  downloadingCsv.value = true
  try {
    await downloadSubmissionLogCsv({
      source: source.value,
      examId: examId.value || undefined,
      studentId: studentId.value || undefined,
      questionId: questionId.value ? Number(questionId.value) : undefined,
      outcome: outcome.value,
      countedOnly: countedOnly.value,
      from: toRFC3339(fromDate.value, false),
      to: toRFC3339(toDate.value, true)
    })
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to download submission log CSV' })
  } finally {
    downloadingCsv.value = false
  }
}

// --- Detail panel ---
const detailOpen = ref(false)
const detailLoading = ref(false)
const detail = ref<SubmissionLogDetail | null>(null)

async function openDetail(row: SubmissionLogRow) {
  detailOpen.value = true
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await getSubmissionLogDetail(row.id, source.value)
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load submission detail' })
    detailOpen.value = false
  } finally {
    detailLoading.value = false
  }
}

function closeDetail() {
  detailOpen.value = false
  detail.value = null
}

function formatDate(iso: string): string {
  if (!iso) return '—'
  try { return new Date(iso).toLocaleString() } catch { return iso }
}

const prettyValidationDetails = computed(() => {
  if (!detail.value) return ''
  try { return JSON.stringify(detail.value.validation_details, null, 2) } catch { return String(detail.value.validation_details) }
})
</script>

<template>
  <div class="card submission-log">
    <div class="log-header">
      <h2>Submission Log</h2>
      <button class="secondary-btn" style="margin: 0;" :disabled="downloadingCsv" @click="onDownloadCsv">
        <Spinner v-if="downloadingCsv" size="sm" inline />
        Download CSV
      </button>
    </div>

    <!-- Filter bar -->
    <div class="filter-bar">
      <div class="input-group">
        <label>Source</label>
        <div class="toggle-row" role="group" aria-label="Source">
          <button
            type="button"
            class="toggle-btn"
            :class="{ active: source === 'exam' }"
            @click="source = 'exam'"
          >Exam</button>
          <button
            type="button"
            class="toggle-btn"
            :class="{ active: source === 'practice' }"
            @click="source = 'practice'"
          >Practice</button>
        </div>
      </div>

      <div class="input-group" v-if="source === 'exam'">
        <label>Exam</label>
        <select v-model="examId">
          <option value="">All exams</option>
          <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }}</option>
        </select>
      </div>

      <div class="input-group">
        <label>Student ID</label>
        <input type="text" v-model="studentSearch" placeholder="Paste student id…" />
      </div>

      <div class="input-group">
        <label>Question ID</label>
        <input type="number" v-model="questionId" placeholder="Any" min="0" />
      </div>

      <div class="input-group">
        <label>Outcome</label>
        <select v-model="outcome">
          <option value="all">All</option>
          <option value="correct">Correct</option>
          <option value="incorrect">Incorrect</option>
        </select>
      </div>

      <div class="input-group input-group--checkbox">
        <label class="checkbox-label">
          <input type="checkbox" v-model="countedOnly" />
          Counted attempts only
        </label>
      </div>

      <div class="input-group">
        <label>From</label>
        <input type="date" v-model="fromDate" />
      </div>

      <div class="input-group">
        <label>To</label>
        <input type="date" v-model="toDate" />
      </div>
    </div>

    <!-- Legend -->
    <p class="legend">
      <span class="badge badge-success"><Icon name="circle-check" size="12" /> Counted</span>
      is the attempt that decides this student's score: best score wins, and ties are
      broken by the most recent attempt.
    </p>

    <!-- Results -->
    <Spinner v-if="loading" inline />
    <EmptyState
      v-else-if="!loading && rows.length === 0"
      title="No submissions found"
      body="No attempts match the current filters."
    />
    <template v-else>
      <div class="preview-table-wrap sticky-table-head">
        <table class="preview-table">
          <thead>
            <tr v-if="source === 'exam'">
              <th>Exam</th>
              <th>Student</th>
              <th>Std #</th>
              <th>Q#</th>
              <th>Requirement</th>
              <th>Attempt</th>
              <th>Counted</th>
              <th>Outcome</th>
              <th>Score</th>
              <th>Submitted</th>
              <th>SQL preview</th>
            </tr>
            <tr v-else>
              <th>Student</th>
              <th>Std #</th>
              <th>Category</th>
              <th>Requirement</th>
              <th>Attempt</th>
              <th>Counted</th>
              <th>Outcome</th>
              <th>Submitted</th>
              <th>SQL preview</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="row in rows" :key="row.id">
              <tr
                class="log-row"
                :class="{ 'log-row--counted': row.is_counted }"
                @click="openDetail(row)"
              >
                <template v-if="isExamRow(row)">
                  <td>{{ (row as ExamSubmissionLogRow).exam_title }}</td>
                  <td>{{ (row as ExamSubmissionLogRow).username }}</td>
                  <td>{{ (row as ExamSubmissionLogRow).student_number || '—' }}</td>
                  <td>{{ (row as ExamSubmissionLogRow).question_display_no }}</td>
                  <td class="truncate-cell">{{ (row as ExamSubmissionLogRow).requirement }}</td>
                  <td>{{ row.attempt_no }}</td>
                  <td>
                    <span v-if="row.is_counted" class="badge badge-success">
                      <Icon name="circle-check" size="12" /> Counted
                    </span>
                    <span v-else class="badge badge-neutral">—</span>
                  </td>
                  <td>
                    <span class="badge" :class="row.is_correct ? 'badge-success' : 'badge-danger'">
                      <Icon :name="row.is_correct ? 'check' : 'x'" size="12" />
                      {{ row.is_correct ? 'Correct' : 'Incorrect' }}
                    </span>
                  </td>
                  <td>{{ (row as ExamSubmissionLogRow).score.toFixed(2) }} / {{ (row as ExamSubmissionLogRow).points.toFixed(2) }}</td>
                  <td>{{ formatDate(row.submitted_at) }}</td>
                  <td class="truncate-cell mono">{{ row.sql_preview }}</td>
                </template>
                <template v-else>
                  <td>{{ (row as PracticeSubmissionLogRow).username }}</td>
                  <td>{{ (row as PracticeSubmissionLogRow).student_number || '—' }}</td>
                  <td>{{ (row as PracticeSubmissionLogRow).category_name }}</td>
                  <td class="truncate-cell">{{ (row as PracticeSubmissionLogRow).requirement }}</td>
                  <td>{{ row.attempt_no }}</td>
                  <td>
                    <span v-if="row.is_counted" class="badge badge-success">
                      <Icon name="circle-check" size="12" /> Counted
                    </span>
                    <span v-else class="badge badge-neutral">—</span>
                  </td>
                  <td>
                    <span class="badge" :class="row.is_correct ? 'badge-success' : 'badge-danger'">
                      <Icon :name="row.is_correct ? 'check' : 'x'" size="12" />
                      {{ row.is_correct ? 'Correct' : 'Incorrect' }}
                    </span>
                  </td>
                  <td>{{ formatDate(row.submitted_at) }}</td>
                  <td class="truncate-cell mono">{{ row.sql_preview }}</td>
                </template>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

      <div class="pagination">
        <span class="muted">
          Showing {{ rows.length === 0 ? 0 : offset + 1 }}–{{ offset + rows.length }} of {{ total }}
          (page {{ page }} / {{ pageCount }})
        </span>
        <div class="pagination-controls">
          <button class="secondary-btn" style="margin: 0;" :disabled="!canPrev" @click="prevPage">Prev</button>
          <button class="secondary-btn" style="margin: 0;" :disabled="!canNext" @click="nextPage">Next</button>
        </div>
      </div>
    </template>

    <!-- Detail modal -->
    <div v-if="detailOpen" class="detail-overlay" @click.self="closeDetail">
      <div class="detail-panel card" role="dialog" aria-modal="true" aria-label="Submission detail">
        <div class="detail-header">
          <h3>Submission Detail</h3>
          <button class="icon-btn" aria-label="Close" @click="closeDetail"><Icon name="x" /></button>
        </div>
        <Spinner v-if="detailLoading" inline />
        <div v-else-if="detail" class="detail-body">
          <dl class="detail-meta">
            <template v-if="source === 'exam'">
              <dt>Exam</dt><dd>{{ detail.exam_title }}</dd>
              <dt>Question</dt><dd>#{{ detail.question_display_no }} — {{ detail.requirement }}</dd>
              <dt>Score</dt><dd>{{ Number(detail.score).toFixed(2) }} / {{ Number(detail.points).toFixed(2) }}</dd>
            </template>
            <template v-else>
              <dt>Category</dt><dd>{{ detail.category_name }}</dd>
              <dt>Question</dt><dd>{{ detail.requirement }}</dd>
            </template>
            <dt>Student</dt><dd>{{ detail.username }} ({{ detail.student_number || '—' }})</dd>
            <dt>Attempt</dt><dd>{{ detail.attempt_no }}<span v-if="detail.is_counted"> — counted</span></dd>
            <dt>Outcome</dt><dd>{{ detail.is_correct ? 'Correct' : 'Incorrect' }}</dd>
            <dt>Submitted</dt><dd>{{ formatDate(detail.submitted_at) }}</dd>
          </dl>
          <div class="detail-section">
            <h4>SQL</h4>
            <pre class="sql-block">{{ detail.sql_text }}</pre>
          </div>
          <div class="detail-section">
            <h4>Validation details</h4>
            <pre class="sql-block">{{ prettyValidationDetails }}</pre>
          </div>
        </div>
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

.submission-log {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.log-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

h2 {
  color: var(--color-text);
  font-weight: 700;
  margin: 0 0 var(--space-3) 0;
  font-size: 1.1rem;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}

.filter-bar {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4);
  align-items: end;
}

.input-group {
  display: flex;
  flex-direction: column;
  min-width: 160px;
}

.input-group--checkbox {
  justify-content: flex-end;
}

label {
  font-weight: 700;
  margin-bottom: 0.4rem;
  font-size: 0.85rem;
  color: var(--color-text);
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-weight: 600;
  font-size: 0.9rem;
  cursor: pointer;
}

select, input[type='text'], input[type='number'], input[type='date'] {
  padding: 0.5rem;
  border: 2px solid var(--color-border);
  border-radius: 0;
  font-weight: 600;
  font-family: var(--font-mono);
  background: var(--color-surface);
  color: var(--color-text);
}

.toggle-row {
  display: flex;
  gap: var(--space-2);
}

.toggle-btn {
  background: var(--color-surface);
  color: var(--color-text);
  border: 1px solid var(--color-border);
  padding: 0.5rem var(--space-3);
  cursor: pointer;
  font-weight: 700;
}

.toggle-btn.active {
  background: var(--color-primary);
  color: #fff;
  border-color: var(--color-primary-strong);
}

.legend {
  font-size: 0.85rem;
  color: var(--color-text-muted);
  margin: 0;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}

.badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.preview-table-wrap { margin-top: 10px; overflow-x: auto; border: 1px solid var(--color-border); border-radius: var(--radius); background: var(--color-surface); min-width: 0; }
.preview-table { width: 100%; border-collapse: collapse; font-size: 0.86rem; background: var(--color-surface); color: var(--color-text); }
.preview-table th, .preview-table td { border-bottom: 1px solid var(--color-border-soft); padding: 6px 10px; text-align: left; }
.preview-table th { background: var(--color-surface-sunk); color: var(--color-text); font-weight: 700; font-size: 0.74rem; text-transform: uppercase; letter-spacing: 0.04em; }
.preview-table tr:nth-child(even) td { background: var(--color-surface-muted); }

.log-row { cursor: pointer; }
.log-row:hover td { background: var(--color-primary-bg); }
.log-row--counted td:first-child { border-left: 3px solid var(--color-success); }

.truncate-cell {
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mono {
  font-family: var(--font-mono);
  font-size: 0.8rem;
}

.pagination {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.pagination-controls {
  display: flex;
  gap: var(--space-2);
}

.secondary-btn {
  padding: var(--space-2) var(--space-4);
  background-color: var(--color-surface);
  color: var(--color-text);
  border: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  transition: none;
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
}

.secondary-btn:hover:not(:disabled) {
  background-color: var(--color-surface-muted);
}

.secondary-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.muted {
  color: var(--color-text-muted);
  font-size: 0.85rem;
}

/* Detail modal */
.detail-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 200;
  padding: var(--space-4);
}

.detail-panel {
  width: 100%;
  max-width: 720px;
  max-height: 85vh;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  min-width: 0;
}

.detail-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.detail-header h3 {
  margin: 0;
  color: var(--color-text);
}

.icon-btn {
  background: transparent;
  border: 1px solid var(--color-border);
  cursor: pointer;
  padding: var(--space-1) var(--space-2);
  color: var(--color-text);
  display: inline-flex;
}

.icon-btn:hover {
  background: var(--color-surface-muted);
}

.detail-meta {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--space-1) var(--space-3);
  margin: 0;
  font-size: 0.9rem;
}

.detail-meta dt {
  font-weight: 700;
  color: var(--color-text-muted);
}

.detail-meta dd {
  margin: 0;
  color: var(--color-text);
}

.detail-section h4 {
  margin: 0 0 var(--space-2) 0;
  font-size: 0.85rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-text-muted);
}

.sql-block {
  background: var(--color-surface-sunk);
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-3);
  overflow-x: auto;
  font-family: var(--font-mono);
  font-size: 0.82rem;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--color-text);
  min-width: 0;
}

@media (max-width: 767.98px) {
  .log-header {
    flex-direction: column;
    align-items: stretch;
    gap: var(--space-3);
  }
  .filter-bar {
    flex-direction: column;
  }
}
</style>
