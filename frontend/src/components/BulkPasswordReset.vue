<script setup lang="ts">
// Admin-only bulk password reset (ARCH-031/032/033). A dedicated, four-state
// page: select targets -> confirm -> run (cursor loop) -> results (CSV /
// printable table / cut slips). See the file-level ARCH-032 constraints this
// component MUST satisfy:
//   - this file never touches any browser persistence API directly (the
//     student/exam lookups and the reset call all go through services/auth.ts,
//     which owns the auth token) — results live in plain component state only;
//   - no re-fetch of a generated password — `results` below is populated ONLY
//     from bulkResetPasswords() responses, held in plain component state, and
//     dropped by "Clear results" / onUnmounted;
//   - no email trigger, no console-logging of a password.
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import StudentMultiSelect from './StudentMultiSelect.vue'
import Spinner from './Spinner.vue'
import EmptyState from './EmptyState.vue'
import FieldError from './FieldError.vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { handleApiError } from '../services/error_handler'
import { useConfirm } from '../services/confirm_service'
import { toCsv, downloadCsv } from '../utils/csv'
import {
  bulkResetPasswords,
  listStudents,
  listExamsForSelection,
  getExamRosterCount,
  type StudentSummary,
  type ExamSummary,
  type BulkResetResultRow,
  type BulkResetFailureRow,
  type BulkResetPasswordsRequest,
} from '../services/auth'

type Mode = 'ids' | 'all_students' | 'exam_roster'
type Stage = 'select' | 'confirm' | 'running' | 'results'

const stage = ref<Stage>('select')
const mode = ref<Mode>('ids')

// --- STATE A: selection data ---
const studentsLoading = ref(false)
const studentsList = ref<StudentSummary[]>([])
const selectedStudentIds = ref<string[]>([])

const examsLoading = ref(false)
const examsList = ref<ExamSummary[]>([])
const selectedExamId = ref('')
const examRosterLoading = ref(false)
const examRosterCount = ref<number | null>(null)

async function fetchStudents() {
  studentsLoading.value = true
  try {
    studentsList.value = await listStudents()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load students' })
  } finally {
    studentsLoading.value = false
  }
}

async function fetchExams() {
  examsLoading.value = true
  try {
    examsList.value = await listExamsForSelection()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load examinations' })
  } finally {
    examsLoading.value = false
  }
}

async function onExamSelected() {
  examRosterCount.value = null
  if (!selectedExamId.value) return
  examRosterLoading.value = true
  try {
    examRosterCount.value = await getExamRosterCount(selectedExamId.value)
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam roster' })
  } finally {
    examRosterLoading.value = false
  }
}

onMounted(() => {
  fetchStudents()
  fetchExams()
})

const targetCount = computed<number>(() => {
  if (mode.value === 'ids') return selectedStudentIds.value.length
  if (mode.value === 'all_students') return studentsList.value.length
  return examRosterCount.value ?? 0
})

const canContinue = computed<boolean>(() => {
  if (mode.value === 'ids') return selectedStudentIds.value.length > 0
  if (mode.value === 'all_students') return studentsList.value.length > 0
  return !!selectedExamId.value && (examRosterCount.value ?? 0) > 0
})

const selectedExamTitle = computed(() => examsList.value.find((e) => e.id === selectedExamId.value)?.title ?? '')

function goToConfirm() {
  if (!canContinue.value) return
  stage.value = 'confirm'
}

function backToSelect() {
  stage.value = 'select'
}

// --- STATE B: confirmation gate ---
const confirmText = ref('')

// --- STATE C/D: run + results ---
const results = ref<BulkResetResultRow[]>([])
const failures = ref<BulkResetFailureRow[]>([])
const runError = ref('')
const processedSoFar = ref(0)
const remainingCount = ref(0)

const { inFlight: resetRunning, run: runReset } = useSingleFlight(async () => {
  if (confirmText.value !== 'RESET') return

  stage.value = 'running'
  results.value = []
  failures.value = []
  runError.value = ''
  processedSoFar.value = 0
  remainingCount.value = 0

  let afterUserId: string | undefined
  try {
    // eslint-disable-next-line no-constant-condition
    while (true) {
      const payload: BulkResetPasswordsRequest = { mode: mode.value, confirm: 'RESET' }
      if (mode.value === 'ids') payload.user_ids = selectedStudentIds.value
      if (mode.value === 'exam_roster') payload.exam_id = selectedExamId.value
      if (afterUserId) payload.after_user_id = afterUserId

      const resp = await bulkResetPasswords(payload)
      results.value.push(...resp.results)
      failures.value.push(...resp.failures)
      processedSoFar.value += resp.processed
      remainingCount.value = resp.remaining

      if (!resp.next_after_user_id) break
      afterUserId = resp.next_after_user_id
    }
  } catch (err) {
    // Stop the loop and surface the error, but KEEP every row already
    // accumulated above — those students really were reset.
    runError.value = handleApiError(err, { fallback: 'Bulk reset stopped partway through the batch' })
  } finally {
    stage.value = 'results'
  }
})

const { confirm } = useConfirm()

async function clearResults() {
  if (hasHeldResults.value) {
    const ok = await confirm({
      title: 'Clear generated passwords?',
      body: 'These passwords are shown only once and cannot be re-viewed after this. Make sure they have been downloaded, printed, or delivered to students first.',
      confirmLabel: 'Clear',
      danger: true,
    })
    if (!ok) return
  }
  results.value = []
  failures.value = []
  runError.value = ''
  confirmText.value = ''
  stage.value = 'select'
}

// --- beforeunload guard while results are held in memory ---
function beforeUnloadGuard(e: BeforeUnloadEvent) {
  e.preventDefault()
  e.returnValue = ''
}

const hasHeldResults = computed(() => results.value.length > 0 || failures.value.length > 0)

// Registered the instant a row lands (mid-loop, not just once the loop
// finishes) and removed the instant every row is gone — "Clear results" or
// unmount — so the guard's lifetime always matches exactly whether a
// generated password currently exists only in this tab's memory.
watch(hasHeldResults, (held) => {
  if (held) window.addEventListener('beforeunload', beforeUnloadGuard)
  else window.removeEventListener('beforeunload', beforeUnloadGuard)
})

// --- CSV / print artifacts ---
function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

function filenameTimestamp(d: Date): string {
  return `${d.getFullYear()}${pad2(d.getMonth() + 1)}${pad2(d.getDate())}-${pad2(d.getHours())}${pad2(d.getMinutes())}`
}

function downloadResultsCsv() {
  const header = ['username', 'student_number', 'first_name', 'last_name', 'sec_lec', 'sec_lab', 'password']
  const rows = results.value.map((r) => [r.username, r.student_number, r.first_name, r.last_name, r.sec_lec, r.sec_lab, r.password])
  const csv = toCsv(header, rows)
  downloadCsv(`password-reset-${mode.value}-${filenameTimestamp(new Date())}.csv`, csv)
}

const printMode = ref<'none' | 'table' | 'slips'>('none')
const printGeneratedAt = ref('')

const modeLabelForPrint = computed(() => {
  if (mode.value === 'exam_roster') return `Exam roster — ${selectedExamTitle.value}`
  if (mode.value === 'all_students') return 'All students'
  return 'Selected students'
})

async function printResultsTable() {
  printMode.value = 'table'
  printGeneratedAt.value = new Date().toLocaleString()
  document.body.classList.add('bulk-print-active', 'bulk-print-table')
  await nextTick()
  window.print()
}

async function printResultsSlips() {
  printMode.value = 'slips'
  printGeneratedAt.value = new Date().toLocaleString()
  document.body.classList.add('bulk-print-active', 'bulk-print-slips')
  await nextTick()
  window.print()
}

function onAfterPrint() {
  document.body.classList.remove('bulk-print-active', 'bulk-print-table', 'bulk-print-slips')
  printMode.value = 'none'
}

onMounted(() => {
  window.addEventListener('afterprint', onAfterPrint)
})

onUnmounted(() => {
  window.removeEventListener('afterprint', onAfterPrint)
  window.removeEventListener('beforeunload', beforeUnloadGuard)
})
</script>

<template>
  <div class="bulk-reset-view">
    <h1>Bulk Reset Passwords</h1>
    <p class="muted">
      Reset a random, server-generated password for a batch of STUDENT accounts. Nothing is ever emailed on this
      path — the new passwords are shown here once, for you to hand out.
    </p>

    <!-- STATE A: select -->
    <div v-if="stage === 'select'" class="card">
      <h2>1. Select students</h2>

      <div class="mode-radios" role="radiogroup" aria-label="Selection mode">
        <label><input type="radio" value="ids" v-model="mode" /> Selected students</label>
        <label><input type="radio" value="all_students" v-model="mode" /> All students</label>
        <label><input type="radio" value="exam_roster" v-model="mode" /> By exam roster</label>
      </div>

      <div v-if="mode === 'ids'" class="mode-body">
        <Spinner v-if="studentsLoading" size="sm" inline label="Loading students" />
        <StudentMultiSelect v-else v-model="selectedStudentIds" :students="studentsList" />
      </div>

      <div v-else-if="mode === 'all_students'" class="mode-body well">
        <Spinner v-if="studentsLoading" size="sm" inline label="Loading students" />
        <p v-else data-testid="all-students-count">
          {{ studentsList.length }} student{{ studentsList.length !== 1 ? 's' : '' }} will be targeted.
        </p>
      </div>

      <div v-else class="mode-body input-group">
        <label for="bulk-reset-exam">Examination</label>
        <select id="bulk-reset-exam" v-model="selectedExamId" class="input" @change="onExamSelected">
          <option value="" disabled>Select an exam...</option>
          <option v-for="e in examsList" :key="e.id" :value="e.id">{{ e.title }}</option>
        </select>
        <p v-if="selectedExamId" class="muted" data-testid="exam-roster-count">
          <Spinner v-if="examRosterLoading" size="sm" inline label="Loading roster" />
          <template v-else>{{ examRosterCount ?? 0 }} student{{ (examRosterCount ?? 0) !== 1 ? 's' : '' }} enrolled.</template>
        </p>
      </div>

      <button type="button" class="btn btn-primary" :disabled="!canContinue" @click="goToConfirm">Continue</button>
    </div>

    <!-- STATE B: confirm -->
    <div v-else-if="stage === 'confirm'" class="card danger-panel">
      <h2>2. Confirm</h2>
      <p>
        This will reset the password for <strong>{{ targetCount }}</strong> student{{ targetCount !== 1 ? 's' : '' }}.
        Every one of them is signed out immediately. <strong>Nothing is emailed.</strong> The new passwords are
        shown only once, on this page — download the CSV or print before you leave.
      </p>

      <div class="input-group">
        <label for="bulk-reset-confirm-input">Type RESET to continue</label>
        <input
          id="bulk-reset-confirm-input"
          v-model="confirmText"
          class="input input-mono"
          autocomplete="off"
          data-testid="bulk-reset-confirm-input"
        />
      </div>

      <div class="confirm-actions">
        <button type="button" class="btn" @click="backToSelect">Back</button>
        <button
          type="button"
          class="btn btn-danger"
          data-testid="bulk-reset-submit"
          :disabled="confirmText !== 'RESET' || resetRunning"
          @click="runReset()"
        >
          <Spinner v-if="resetRunning" size="sm" inline :invert="true" />
          Reset {{ targetCount }} password{{ targetCount !== 1 ? 's' : '' }}
        </button>
      </div>
    </div>

    <!-- STATE C: running -->
    <div v-else-if="stage === 'running'" class="card running-panel">
      <h2>Resetting passwords...</h2>
      <Spinner size="lg" />
      <p data-testid="bulk-reset-progress">
        {{ processedSoFar }} done<template v-if="remainingCount > 0">, {{ remainingCount }} remaining</template>
      </p>
    </div>

    <!-- STATE D: results -->
    <div v-else class="card">
      <h2>Results</h2>
      <p v-if="runError" class="run-error-banner" role="alert" data-testid="bulk-reset-error">{{ runError }}</p>

      <div class="results-actions">
        <button type="button" class="btn btn-sm" :disabled="results.length === 0" @click="downloadResultsCsv">Download CSV</button>
        <button type="button" class="btn btn-sm" :disabled="results.length === 0" @click="printResultsTable">Print table</button>
        <button type="button" class="btn btn-sm" :disabled="results.length === 0" @click="printResultsSlips">Print slips</button>
        <button type="button" class="btn btn-sm btn-danger" data-testid="bulk-reset-clear" @click="clearResults">
          Clear results
        </button>
      </div>

      <EmptyState v-if="results.length === 0 && failures.length === 0" title="No results" body="Nothing was reset." />

      <template v-else>
        <h3>Reset ({{ results.length }})</h3>
        <div v-if="results.length > 0" class="responsive-table-wrap">
          <table class="data-table" data-testid="bulk-reset-results-table">
            <thead>
              <tr>
                <th>Username</th>
                <th>Student #</th>
                <th>Name</th>
                <th>Sec Lec / Lab</th>
                <th>Password</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in results" :key="r.user_id">
                <td>{{ r.username }}</td>
                <td>{{ r.student_number }}</td>
                <td>{{ r.first_name }} {{ r.last_name }}</td>
                <td>{{ r.sec_lec }} / {{ r.sec_lab }}</td>
                <td><code>{{ r.password }}</code></td>
              </tr>
            </tbody>
          </table>
        </div>

        <template v-if="failures.length > 0">
          <h3>Failed ({{ failures.length }})</h3>
          <div class="responsive-table-wrap">
            <table class="data-table" data-testid="bulk-reset-failures-table">
              <thead>
                <tr>
                  <th>Username</th>
                  <th>Error</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="f in failures" :key="f.user_id">
                  <td>{{ f.username }}</td>
                  <td><FieldError :message="f.error" /></td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </template>
    </div>

    <!-- Print-only artifacts. Always present in the DOM (so a test can find
         .slip elements without needing real print CSS), but hidden from the
         normal screen view by the base ".print-table, .print-slips { display:
         none }" rule below; made visible ONLY under @media print while a
         body.bulk-print-* class is set around window.print(). -->
    <div class="print-table">
      <div class="print-header">
        <h2>Password Reset — {{ modeLabelForPrint }}</h2>
        <p>{{ printGeneratedAt }}</p>
      </div>
      <table>
        <thead>
          <tr>
            <th>Username</th>
            <th>Student #</th>
            <th>Name</th>
            <th>Sec Lec / Lab</th>
            <th>Password</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in results" :key="r.user_id">
            <td>{{ r.username }}</td>
            <td>{{ r.student_number }}</td>
            <td>{{ r.first_name }} {{ r.last_name }}</td>
            <td>{{ r.sec_lec }} / {{ r.sec_lab }}</td>
            <td>{{ r.password }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="print-slips">
      <div v-for="r in results" :key="r.user_id" class="slip">
        <div class="slip-name">{{ r.first_name }} {{ r.last_name }}</div>
        <div class="slip-row">Student #: {{ r.student_number }}</div>
        <div class="slip-row">Username: {{ r.username }}</div>
        <div class="slip-password">{{ r.password }}</div>
        <div class="slip-note">Sign in and change this password. Keep it safe.</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bulk-reset-view {
  max-width: 900px;
  margin: 0 auto;
  padding: var(--space-5);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.mode-radios {
  display: flex;
  gap: var(--space-4);
  flex-wrap: wrap;
  margin: var(--space-3) 0;
}

.mode-radios label {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  font-weight: 700;
  cursor: pointer;
}

.mode-body {
  margin-bottom: var(--space-4);
}

.danger-panel {
  border-color: var(--color-danger);
  background: var(--color-danger-bg);
}

.confirm-actions,
.results-actions {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
  margin-top: var(--space-3);
}

.run-error-banner {
  background: var(--color-danger-bg);
  color: var(--color-danger);
  border: 1px solid var(--color-danger);
  border-radius: var(--radius);
  padding: var(--space-3);
  font-weight: 700;
}

.running-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-6) var(--space-5);
}

h3 {
  margin: var(--space-4) 0 var(--space-2) 0;
  font-size: 1rem;
  font-weight: 700;
}
</style>

<style>
/* Print-only artifacts (ARCH-033 (4)). Unscoped and tightly namespaced under
   .print-table / .print-slips / body.bulk-print-* so nothing here leaks into
   any other view. Do NOT edit src/style.css for this — it stays global-css-free. */
.print-table,
.print-slips {
  display: none;
}

@media print {
  body.bulk-print-active > * {
    visibility: hidden;
  }

  body.bulk-print-table .print-table {
    display: block;
    visibility: visible;
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
  }
  body.bulk-print-table .print-table * {
    visibility: visible;
  }
  .print-table table {
    width: 100%;
    border-collapse: collapse;
  }
  .print-table th,
  .print-table td {
    border: 1px solid #000;
    padding: 4px 8px;
    text-align: left;
    font-size: 11pt;
  }

  body.bulk-print-slips .print-slips {
    display: grid;
    visibility: visible;
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    grid-template-columns: repeat(2, 1fr);
    grid-auto-rows: 20%;
    gap: 4mm;
  }
  body.bulk-print-slips .print-slips * {
    visibility: visible;
  }
  .print-slips .slip {
    border: 1px dashed #000;
    padding: 3mm;
    page-break-inside: avoid;
    break-inside: avoid;
    display: flex;
    flex-direction: column;
    justify-content: center;
    gap: 2mm;
    font-size: 10pt;
  }
  .print-slips .slip-name {
    font-weight: 700;
    font-size: 12pt;
  }
  .print-slips .slip-password {
    font-family: 'JetBrains Mono', ui-monospace, monospace;
    font-size: 13pt;
    font-weight: 700;
  }
  .print-slips .slip-note {
    font-size: 8pt;
  }
}
</style>
