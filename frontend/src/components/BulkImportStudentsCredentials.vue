<script setup lang="ts">
// Admin-only ROTATING credential import: unlike BulkImportStudents.vue (which
// leaves an existing student's password untouched), every row in this file —
// new AND returning students — gets a brand-new server-generated password
// and is signed out of any current session. The confirm gate below follows
// BulkPasswordReset.vue's inline-typed-phrase pattern (services/confirm_service's
// ConfirmOptions has no text-input field, so a dialog can't hold the phrase).
import { computed, onUnmounted, ref, watch } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import { useFormValidation } from '../composables/useFormValidation'
import { useConfirm } from '../services/confirm_service'
import { toCsv, downloadCsv } from '../utils/csv'
import FieldError from './FieldError.vue'
import Spinner from './Spinner.vue'

interface ImportError {
  row: number
  student_number: string
  message: string
}

interface RotatedAccount {
  row: number
  student_number: string
  student_id_ext: string
  email: string
  password: string
  outcome: 'created' | 'updated'
  email_status: 'queued' | 'no_email' | 'not_configured'
}

interface RotatingImportReport {
  total: number
  created: number
  updated: number
  skipped: number
  // Server-typed as always-present ([] on a clean/all-skipped import — see
  // student_import_service.go), but declared optional here defensively: the
  // component must not trust the wire format and throw on a stray null.
  errors?: ImportError[] | null
  accounts?: RotatedAccount[] | null
  job_id: string
}

const emit = defineEmits<{ (e: 'job-started', jobId: string): void }>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

const selectedFile = ref<File | null>(null)
const confirmPhrase = ref('')
const topLevelError = ref('')
const result = ref<RotatingImportReport | null>(null)

// Exact-match gate, same rule as BulkPasswordReset's RESET. The server checks
// it again on every request — this input is convenience, not security.
const confirmed = computed(() => confirmPhrase.value === 'ROTATE')

// Defensive accessors: the server always sends [] (never null) for a clean or
// all-skipped import (see student_import_service.go), but a nil/absent array
// must never be able to throw during render regardless — a TypeError here
// used to abort the whole results subtree mid-patch, leaving the admin with a
// green success toast and NO table, right after every student in the file was
// already rotated and signed out. Every other read of accounts/errors in this
// component goes through these two computed refs, not result.value directly.
const resultAccounts = computed<RotatedAccount[]>(() => result.value?.accounts ?? [])
const resultErrors = computed<ImportError[]>(() => result.value?.errors ?? [])

// True exactly while a batch of unrecoverable, one-time-shown passwords is
// sitting in this tab's memory. Drives BOTH the beforeunload guard below and
// the "are you sure" gate in front of the two actions that would wipe them
// silently (a second import, picking a different file) — same discipline as
// BulkPasswordReset.vue's hasHeldResults.
const hasHeldResults = computed(() => resultAccounts.value.length > 0)

// Known server error codes mapped to sentences an admin can act on. Anything
// not in this map (e.g. a file-parsing message) is already a human-readable
// sentence from the backend and is shown as-is via the fallback.
//
// confirmation_required is currently unreachable through this UI: submitImport
// always sends confirm=ROTATE verbatim (hardcoded below) and the button stays
// disabled until `confirmed` is true, so this component can never produce a
// request the server would reject on that check. Kept anyway, deliberately,
// as a defensive mapping: the server re-validates `confirm` on every request
// regardless of what the UI already gated (by design — see
// ImportStudentsWithCredentials), so it remains the authority and could still
// return this code (a future UI bug, a direct API call, etc.).
const KNOWN_ERROR_CODES: Record<string, string> = {
  confirmation_required: 'Confirmation phrase did not match. Please retype ROTATE and try again.',
}

function friendlyImportError(code: string | undefined, fallback: string): string {
  if (!code) return fallback
  return KNOWN_ERROR_CODES[code] ?? code
}

const fileValidator = (file: File | null) => {
  if (!file) return 'Please select a file'
  if (!file.name.match(/\.(csv|xlsx)$/i)) return 'File must be .csv or .xlsx'
  return null
}
const fileForm = useFormValidation({ file: { rules: [fileValidator] } })

// Invariant: what the file input DISPLAYS and what a submit would UPLOAD can
// never disagree. The browser swaps the native input's shown filename and
// .files to the newly picked file BEFORE this handler runs — that happens
// synchronously as part of the OS file-pick and cannot be intercepted or
// deferred. So if the admin declines the discard prompt below, the input is
// already showing the new file while selectedFile.value (what submit
// actually uploads) still points at the old one. Left alone, that's a silent
// wrong-roster upload: the screen shows the new class, the request sends the
// old one. We restore the input to reflect the file that is actually staged
// so a decline is a true no-op from the admin's point of view.
const handleFileChange = async (event: Event) => {
  const input = event.target as HTMLInputElement
  const nextFile = input.files?.[0] ?? null
  const previouslyStaged = selectedFile.value

  if (hasHeldResults.value) {
    const ok = await confirm({
      title: 'Discard generated passwords?',
      body: 'These passwords are shown only once and cannot be re-viewed after this. Make sure they have been downloaded, copied, or delivered to students first.',
      confirmLabel: 'Discard',
      danger: true,
    })
    if (!ok) {
      restoreDisplayedFile(input, previouslyStaged)
      return
    }
  }

  selectedFile.value = nextFile
  fileForm.values.file = selectedFile.value
  topLevelError.value = ''
  result.value = null
}

// Rebuilds the input's FileList via DataTransfer so the previously staged
// file is what's shown again — a real no-op for the admin, and the preferred
// path. DataTransfer-based FileList construction is standard and supported
// by evergreen browsers. If reassigning `.files` isn't possible in a given
// host environment (DataTransfer missing, or the property isn't writable),
// fall back to clearing the input instead of leaving it pointed at a file
// that's no longer staged — and clear selectedFile/fileForm right along with
// it, so displayed/staged/uploaded can never drift apart from one another.
function restoreDisplayedFile(input: HTMLInputElement, file: File | null) {
  let restored = false
  if (typeof DataTransfer !== 'undefined') {
    try {
      const dt = new DataTransfer()
      if (file) dt.items.add(file)
      input.files = dt.files
      restored = true
    } catch {
      // Assignment blocked in this environment — fall through to clearing.
    }
  }

  if (restored) {
    selectedFile.value = file
    fileForm.values.file = file
  } else {
    input.value = ''
    selectedFile.value = null
    fileForm.values.file = null
  }
}

async function copyPassword(text: string) {
  try {
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      notify('Password copied to clipboard.', 'success')
    } else {
      notify('Clipboard not available in this browser.', 'error')
    }
  } catch {
    notify('Failed to copy password.', 'error')
  }
}

// Every password is shown exactly once. Rows whose email is missing — or whose
// delivery silently fails — are only recoverable from here, so EVERY account
// row goes into the CSV, not just the failed ones.
function downloadAllAsCsv() {
  const content = toCsv(
    ['student_id_ext', 'email', 'password'],
    resultAccounts.value.map(a => [a.student_id_ext, a.email, a.password]),
  )
  downloadCsv('rotated-credentials.csv', content)
}

// The email column must never claim delivery happened when it didn't:
// "not_configured" is rendered as a distinct, explicit "not sent" message,
// not folded into the same bucket as "queued" or silently miscounted as
// "no email" (which would imply the row itself has no address on file).
function emailStatusLabel(a: RotatedAccount): string {
  switch (a.email_status) {
    case 'queued':
      return `✉ queued — ${a.email}`
    case 'not_configured':
      return 'not sent — email is not configured on this server'
    default:
      return 'no email'
  }
}

// The actual import (validation already done, confirm dialog already
// resolved) is the only part guarded by useSingleFlight: importLoading
// (and the spinner it drives) must reflect ONLY this network round trip, not
// the discard-confirmation dialog above it. Wrapping the confirm() await in
// the same guarded region used to hold the spinner up the entire time the
// "discard generated passwords?" modal sat open, which lies about what the
// button is actually waiting on.
const { inFlight: importLoading, run: runImport } = useSingleFlight(async () => {
  // Re-checked here (not just by submitImport below) because this function is
  // the actual async boundary TypeScript narrows across; submitImport's own
  // check does not persist into a separately-declared callback.
  if (!selectedFile.value) return

  topLevelError.value = ''
  result.value = null

  const token = localStorage.getItem('sql_quiz_token')
  const formData = new FormData()
  formData.append('file', selectedFile.value)
  formData.append('confirm', 'ROTATE')

  try {
    const response = await fetch('/api/v1/students/import-credentials', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: formData,
    })
    const data = await response.json().catch(() => ({} as any))

    if (!response.ok) {
      if (response.status === 400) {
        topLevelError.value = friendlyImportError(data.error, 'File format rejected')
        return
      }
      throw new Error(data.error || 'Import failed')
    }

    result.value = data
    confirmPhrase.value = ''

    // The toast must reflect what actually happened, not what usually
    // happens: job_id is "" whenever no row in the file carried an email
    // address, and accounts can be empty when every row landed in errors[].
    if (data.job_id) {
      emit('job-started', data.job_id)
      notify('Import complete — passwords are being emailed.', 'success')
    } else if (data.accounts && data.accounts.length > 0) {
      notify('Import complete — no emails were queued. Copy the passwords below and deliver them by hand.', 'info')
    } else {
      notify('Import complete — nothing was imported. See the row errors below.', 'info')
    }
  } catch (err: any) {
    handleApiError(err, { fallback: 'Import failed' })
  }
})

// Owner-approved safeguard: warn, but let the admin override (never a hard
// block). Called from submitImport BEFORE runImport's actual upload, so the
// warning lands before the admin commits to the action rather than after
// every password has already been rotated. Returns true when the import
// should proceed.
//
//   - exam_active: true  -> a SECOND explicit confirmation naming the
//     consequence plainly (an exam is in progress; every student in this
//     file will be signed out immediately, including anyone sitting it).
//   - exam_active: false -> proceeds silently, exactly as before this
//     safeguard existed.
//   - the preflight request itself fails -> fails CLOSED: neither silently
//     proceeds as though no exam were active, nor blocks the admin outright.
//     Warns that exam status could not be checked and asks for the same
//     explicit confirmation, so an admin can still act in an emergency.
async function checkExamActiveAndConfirm(): Promise<boolean> {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/students/import-credentials/preflight', {
      headers: { Authorization: `Bearer ${token}` },
    })
    if (!response.ok) throw new Error('preflight check failed')
    const data = await response.json()

    if (data.exam_active !== true) return true

    return await confirm({
      title: 'An exam is currently in progress',
      body: 'Rotating passwords now will sign every student in this file out immediately — including anyone currently sitting the exam. This cannot be undone. Proceed anyway?',
      confirmLabel: 'Rotate anyway',
      danger: true,
    })
  } catch {
    return await confirm({
      title: 'Exam status could not be checked',
      body: 'We could not confirm whether an exam is currently in progress. If one is, rotating passwords now will sign every student in this file out immediately — including anyone currently sitting it. Proceed anyway?',
      confirmLabel: 'Proceed anyway',
      danger: true,
    })
  }
}

// The public entry point bound to the submit button. Validation and both
// confirmation dialogs run OUTSIDE the useSingleFlight guard on purpose (see
// runImport above) — only the network round trip itself sets
// importLoading/shows the spinner.
async function submitImport() {
  if (!fileForm.validateAll()) return
  if (!selectedFile.value || !confirmed.value) return

  if (hasHeldResults.value) {
    const ok = await confirm({
      title: 'Discard generated passwords?',
      body: 'These passwords are shown only once and cannot be re-viewed after this. Make sure they have been downloaded, copied, or delivered to students first.',
      confirmLabel: 'Discard',
      danger: true,
    })
    if (!ok) return
  }

  const okToProceed = await checkExamActiveAndConfirm()
  if (!okToProceed) return

  await runImport()
}

// Registered the instant results with accounts land and removed the instant
// they're gone (second import, new file selection, or unmount) — matching
// BulkPasswordReset.vue's lifetime discipline: the listener must not outlive
// the secrets, and must not leak past this component's life.
function beforeUnloadGuard(e: BeforeUnloadEvent) {
  e.preventDefault()
  e.returnValue = ''
}

watch(hasHeldResults, (held) => {
  if (held) window.addEventListener('beforeunload', beforeUnloadGuard)
  else window.removeEventListener('beforeunload', beforeUnloadGuard)
})

onUnmounted(() => {
  window.removeEventListener('beforeunload', beforeUnloadGuard)
})
</script>

<template>
  <div class="bulk-import card">
    <h2>Import &amp; Email Passwords</h2>
    <p class="desc">
      Same file format as the roster import, plus an Email column. Every student in the file —
      new and returning — gets a brand-new password emailed to them, and is signed out of any
      current session. Rows without an email address are still imported; copy their passwords below.
    </p>

    <div class="form-row">
      <label class="confirm-label" for="rotate-file">Roster file</label>
      <input
        id="rotate-file"
        type="file"
        accept=".csv,.xlsx"
        :disabled="importLoading"
        class="file-input"
        :aria-describedby="fileForm.touched.file && fileForm.errors.file ? 'err-rotate-file' : undefined"
        @change="handleFileChange"
      />
      <FieldError :message="fileForm.touched.file ? fileForm.errors.file : ''" id="err-rotate-file" />
    </div>

    <div class="form-row">
      <label class="confirm-label" for="rotate-confirm">Type <code>ROTATE</code> to confirm</label>
      <input
        id="rotate-confirm"
        v-model="confirmPhrase"
        type="text"
        class="file-input"
        data-testid="rotate-confirm-input"
        :disabled="importLoading"
        autocomplete="off"
      />
      <button
        type="button"
        class="primary-btn danger"
        data-testid="rotate-import-submit"
        :disabled="importLoading || !selectedFile || !confirmed"
        @click="submitImport"
      >
        <Spinner v-if="importLoading" size="sm" inline :invert="true" />{{ importLoading ? 'Importing' : 'Import & Email Passwords' }}
      </button>
    </div>

    <div v-if="topLevelError" class="error-box">{{ topLevelError }}</div>

    <div v-if="result !== null" role="status" aria-live="polite" class="sr-only">
      Import complete: {{ resultAccounts.length }} account{{ resultAccounts.length !== 1 ? 's' : '' }},
      {{ resultErrors.length }} row error{{ resultErrors.length !== 1 ? 's' : '' }}.
    </div>

    <div v-if="result !== null" class="results-section">
      <div class="summary-grid">
        <div class="summary-cell"><div class="summary-value">{{ result.total }}</div><div class="summary-label">Total</div></div>
        <div class="summary-cell"><div class="summary-value">{{ result.created }}</div><div class="summary-label">Created</div></div>
        <div class="summary-cell"><div class="summary-value">{{ result.updated }}</div><div class="summary-label">Rotated</div></div>
        <div class="summary-cell"><div class="summary-value">{{ result.skipped }}</div><div class="summary-label">Skipped</div></div>
      </div>

      <div v-if="resultAccounts.length > 0" class="created-section">
        <div class="section-head">
          <h3>Passwords ({{ resultAccounts.length }})</h3>
          <button type="button" class="copy-btn" data-testid="rotate-copy-csv" @click="downloadAllAsCsv">
            Copy all as CSV
          </button>
        </div>
        <p class="desc">Shown once. Download the CSV before leaving this page.</p>
        <div class="errors-table-wrap">
          <table class="errors-table" data-testid="rotated-accounts-table">
            <thead>
              <tr><th>Login ID</th><th>Outcome</th><th>Email</th><th>Password</th><th></th></tr>
            </thead>
            <tbody>
              <tr v-for="a in resultAccounts" :key="a.row">
                <td>{{ a.student_id_ext }}</td>
                <td>{{ a.outcome }}</td>
                <td>{{ emailStatusLabel(a) }}</td>
                <td><code class="pw-cell">{{ a.password }}</code></td>
                <td><button type="button" class="copy-btn" @click="copyPassword(a.password)">Copy</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-if="resultErrors.length > 0" class="errors-section">
        <h3>Row Errors ({{ resultErrors.length }})</h3>
        <div class="errors-table-wrap">
          <table class="errors-table">
            <thead><tr><th>Row</th><th>Student Number</th><th>Reason</th></tr></thead>
            <tbody>
              <tr v-for="e in resultErrors" :key="e.row">
                <td>{{ e.row }}</td><td>{{ e.student_number || '—' }}</td><td>{{ e.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-else class="no-errors-msg">No row errors.</div>
    </div>
  </div>
</template>

<style scoped>
.bulk-import { display: flex; flex-direction: column; gap: var(--space-4); }
.card { background: var(--color-surface); padding: var(--space-6); border: 2px solid var(--color-border); border-radius: 0; }
h2 { color: var(--color-text); margin: 0; font-weight: 800; }
h3 { margin: 0 0 var(--space-2) 0; font-size: 1rem; font-weight: 800; }
.desc { color: var(--color-text-muted); margin: 0; font-size: 0.9rem; }
.form-row { display: flex; align-items: center; gap: var(--space-4); flex-wrap: wrap; }
.confirm-label { font-size: 0.85rem; font-weight: 700; }
.file-input {
  flex: 1; min-width: 200px; padding: var(--space-2);
  border: 2px solid var(--color-border); border-radius: 0;
  background: var(--color-surface); color: var(--color-text);
  font-family: var(--font-mono); font-size: 0.9rem;
}
.primary-btn {
  padding: var(--space-2) var(--space-4); background-color: var(--color-primary);
  color: var(--color-surface); border: 2px solid var(--color-border);
  font-weight: 700; cursor: pointer; white-space: nowrap;
}
.primary-btn.danger { background-color: var(--color-danger); }
.primary-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.error-box {
  background-color: var(--color-danger-bg); color: var(--color-danger);
  padding: var(--space-4); border: 2px solid var(--color-danger); font-weight: 700;
}
.results-section { display: flex; flex-direction: column; gap: var(--space-4); }
.summary-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--space-4); }
.summary-cell { background: var(--color-surface); border: 2px solid var(--color-border); padding: var(--space-4); text-align: center; }
.summary-value { font-size: 1.8rem; font-weight: 800; color: var(--color-text); }
.summary-label { font-size: 0.75rem; color: var(--color-text-muted); margin-top: 0.2rem; font-weight: 700; }
.section-head { display: flex; align-items: center; justify-content: space-between; gap: var(--space-4); }
.created-section, .errors-section { display: flex; flex-direction: column; gap: var(--space-2); }
.errors-table-wrap { overflow-x: auto; border: 2px solid var(--color-border); }
.errors-table { width: 100%; border-collapse: collapse; font-size: 0.85rem; background: var(--color-surface); color: var(--color-text); }
.errors-table th, .errors-table td { border: 1px solid var(--color-border); padding: 0.5rem; text-align: left; }
.errors-table th { background: var(--color-surface-muted); font-weight: 700; }
.no-errors-msg {
  color: var(--color-success); background: var(--color-success-bg);
  padding: var(--space-2); border: 2px solid var(--color-success);
  font-weight: 700; text-align: center;
}
.pw-cell { font-family: var(--font-mono, monospace); font-weight: 700; user-select: all; }
.copy-btn {
  padding: 2px 10px; border: 1px solid var(--color-border);
  background: var(--color-surface); color: var(--color-text);
  font-weight: 700; cursor: pointer; white-space: nowrap;
}
.copy-btn:hover { background: var(--color-surface-muted); }
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
</style>
