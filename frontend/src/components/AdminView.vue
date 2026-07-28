<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useConfirm } from '../services/confirm_service'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import {
  createStaff,
  resetPassword,
  resendCredential,
  getPasswordEvents,
  type StaffRole,
  type PasswordEvent,
} from '../services/auth'
import Spinner from './Spinner.vue'
import EmptyState from './EmptyState.vue'
import Skeleton from './Skeleton.vue'
import BulkImportStudents from './BulkImportStudents.vue'
import AddStudentForm from './AddStudentForm.vue'
import EmailReveal from './EmailReveal.vue'

const { notify } = useNotifications()
const { confirm } = useConfirm()

// --- Create staff (lecturer/admin) — server generates the password ---
const newStaff = ref<{ username: string; role: StaffRole; email: string }>({
  username: '',
  role: 'LECTURER',
  email: '',
})
const staffReveal = ref<{ id: string; username: string; email: string; password: string } | null>(null)
// Inline email preview + real re-send state for the staff credential reveal.
const showEmailPreview = ref(false)
const resendNotice = ref('')

// The exact credential email the backend sends (mirrors SMTPEmailSender.SendCredential).
const emailPreview = computed(() => {
  if (!staffReveal.value) return null
  const { username, email, password } = staffReveal.value
  return {
    to: email,
    subject: 'Your SQL Quiz System credentials',
    body:
      `Hello,\n\n` +
      `An account password has been set for username "${username}".\n\n` +
      `Password: ${password}\n\n` +
      `Please sign in and change it if you wish.\n`,
  }
})

// --- Per-user reset-password reveal ---
const resetReveal = ref<{ username: string; password: string } | null>(null)

// --- Per-user password history ---
const history = ref<{
  userId: string
  username: string
  key_configured: boolean
  events: PasswordEvent[]
} | null>(null)

const users = ref<any[]>([])
const listLoading = ref(false)
const editingUser = ref<any>(null)


// Form validation for create staff (TODO-038)
const newStaffForm = useFormValidation({
  username: { rules: [required('Username is required')], initial: '' },
})
watch(() => newStaff.value.username, (v) => { newStaffForm.values.username = v })

async function copyToClipboard(text: string) {
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

const { inFlight: resendLoading, run: resendCredentialEmail } = useSingleFlight(async () => {
  if (!staffReveal.value) return
  try {
    const res = await resendCredential(staffReveal.value.id)
    resendNotice.value = `Credential email sent to ${res.emailed_to}.`
    notify(`Credential email sent to ${res.emailed_to}.`, 'success')
  } catch (err) {
    resendNotice.value = ''
    handleApiError(err, { fallback: 'Failed to resend credential email' })
  }
})

const fetchUsers = async () => {
  listLoading.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/users/all', {
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) throw new Error('Failed to fetch users')
    users.value = await response.json()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to fetch users' })
  } finally {
    listLoading.value = false
  }
}

const { inFlight: createStaffLoading, run: createStaffAction } = useSingleFlight(async () => {
  if (!newStaffForm.validateAll()) return
  try {
    const username = newStaff.value.username
    const email = newStaff.value.email.trim()
    const res = await createStaff({
      username,
      role: newStaff.value.role,
      ...(email ? { email } : {}),
    })
    staffReveal.value = { id: res.id, username, email, password: res.generated_password }
    showEmailPreview.value = false
    resendNotice.value = ''
    notify(`Account ${username} created successfully.`, 'success')
    newStaff.value = { username: '', role: 'LECTURER', email: '' }
    newStaffForm.reset()
    fetchUsers()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create account' })
  }
})

const { inFlight: resetPasswordLoading, run: resetPasswordAction } = useSingleFlight(async (user: any) => {
  const ok = await confirm({
    title: `Reset password for ${user.username}?`,
    body: 'A new password will be generated and shown once. The user\'s existing sessions will be signed out.',
    confirmLabel: 'Reset password',
    danger: true,
  })
  if (!ok) return
  try {
    const res = await resetPassword(user.id)
    resetReveal.value = { username: user.username, password: res.generated_password }
    notify(`Password reset for ${user.username}.`, 'success')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to reset password' })
  }
})

const { inFlight: historyLoading, run: openHistory } = useSingleFlight(async (user: any) => {
  try {
    const res = await getPasswordEvents(user.id)
    history.value = {
      userId: user.id,
      username: user.username,
      key_configured: res.key_configured,
      events: res.events || [],
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load password history' })
  }
})

function closeHistory() {
  history.value = null
}

const { inFlight: deleteUserLoading, run: deleteUser } = useSingleFlight(async (id: string) => {
  const ok = await confirm({ title: 'Delete this user?', body: 'This cannot be undone.', confirmLabel: 'Delete', danger: true })
  if (!ok) return
  
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/users/${id}`, {
      method: 'DELETE',
      headers: {
        'Authorization': `Bearer ${token}`
      }
    })
    if (!response.ok) throw new Error('Failed to delete user')
    notify('User deleted successfully', 'success')
    fetchUsers()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to delete user' })
  }
})

const startEdit = (user: any) => {
  editingUser.value = { ...user }
}

const cancelEdit = () => {
  editingUser.value = null
}

const { inFlight: saveUserLoading, run: saveUser } = useSingleFlight(async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/users/${editingUser.value.id}`, {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify(editingUser.value)
    })
    if (!response.ok) throw new Error('Failed to update user')
    notify('User updated successfully', 'success')
    editingUser.value = null
    fetchUsers()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to update user' })
  }
})

// --- Submission-log export controls ---
const exportSettings = ref<{
  enabled: boolean
  last_export_at: string
  interval: string
  recipient: string
} | null>(null)
const exportBusy = ref(false)

const authHeader = () => ({ Authorization: `Bearer ${localStorage.getItem('sql_quiz_token')}` })

const fetchExportSettings = async () => {
  try {
    const res = await fetch('/api/v1/export/settings', { headers: authHeader() })
    if (res.status === 503) { exportSettings.value = null; return } // email/export not configured
    if (!res.ok) throw new Error('Failed to load export settings')
    exportSettings.value = await res.json()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load export settings' })
  }
}

const toggleExport = async () => {
  if (!exportSettings.value) return
  const next = !exportSettings.value.enabled
  exportBusy.value = true
  try {
    const res = await fetch('/api/v1/export/settings', {
      method: 'PUT',
      headers: { ...authHeader(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: next }),
    })
    if (!res.ok) throw new Error('Failed to update export toggle')
    exportSettings.value.enabled = next
    notify(`Submission export ${next ? 'enabled' : 'disabled'}.`, 'success')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to update export toggle' })
  } finally {
    exportBusy.value = false
  }
}

const runExportNow = async () => {
  exportBusy.value = true
  try {
    const res = await fetch('/api/v1/export/run-now', { method: 'POST', headers: authHeader() })
    const data = await res.json()
    if (!res.ok) throw new Error(data?.error || 'Export run failed')
    notify(
      data.sent
        ? `Export sent: ${data.submission_count} submissions, ${data.practice_count} practice attempts.`
        : 'Export ran — nothing new to send.',
      'success'
    )
    await fetchExportSettings()
  } catch (err) {
    handleApiError(err, { fallback: 'Export run failed' })
  } finally {
    exportBusy.value = false
  }
}

onMounted(() => {
  fetchUsers()
  fetchExportSettings()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Account Administration</h1>
      <p class="page-subtitle">Create, edit, and bulk-import student/lecturer accounts for the grader system.</p>
    </div>

    <section v-if="exportSettings" class="card export-card">
      <h2 class="section-title">Submission-log export</h2>
      <p class="page-subtitle">
        Emails a compressed CSV of new exam + practice submissions to the admin address on a fixed interval.
      </p>
      <div class="export-row">
        <label class="switch">
          <input
            type="checkbox"
            :checked="exportSettings.enabled"
            :disabled="exportBusy"
            @change="toggleExport"
            data-testid="export-toggle"
          />
          <span>{{ exportSettings.enabled ? 'Enabled' : 'Disabled' }}</span>
        </label>
        <button type="button" class="btn btn-sm" :disabled="exportBusy" @click="runExportNow" data-testid="export-run-now">
          Run now
        </button>
      </div>
      <dl class="export-meta">
        <div><dt>Interval</dt><dd>{{ exportSettings.interval }}</dd></div>
        <div><dt>Recipient</dt><dd>{{ exportSettings.recipient || '— (ADMIN_EMAIL unset)' }}</dd></div>
        <div><dt>Last export</dt><dd>{{ exportSettings.last_export_at }}</dd></div>
      </dl>
    </section>

  <div class="admin-grid">
    <!-- Create User -->
    <div>
      <!-- Create Staff (Lecturer / Admin) -->
      <div class="card" style="margin-bottom: var(--space-6);">
        <h2 style="margin: 0 0 var(--space-1) 0;">Create Staff Account</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">
          Create a lecturer or admin. The system generates a password shown once below.
        </p>

        <form data-testid="create-staff-form" @submit.prevent="createStaffAction" novalidate>
          <div class="field">
            <label>Username</label>
            <input class="input" data-testid="staff-username" v-model="newStaff.username" required autofocus
              :aria-invalid="newStaffForm.touched.username && !!newStaffForm.errors.username"
              aria-describedby="err-staff-username"
              @blur="newStaffForm.markTouched('username')"
            />
            <FieldError :message="newStaffForm.touched.username ? newStaffForm.errors.username : ''" id="err-staff-username" />
          </div>
          <div class="field">
            <label>Role</label>
            <select class="input" data-testid="staff-role" v-model="newStaff.role">
              <option value="LECTURER">Lecturer</option>
              <option value="ADMIN">Admin</option>
            </select>
          </div>
          <div class="field">
            <label for="staff-email-input">Email <span class="muted">*</span></label>
            <input id="staff-email-input" class="input" type="email" data-testid="staff-email-input"
              v-model="newStaff.email" autocomplete="email" placeholder="name@example.com" />
          </div>

          <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="createStaffLoading">
            <Spinner v-if="createStaffLoading" size="sm" inline :invert="true" />{{ createStaffLoading ? 'Creating' : 'Create Staff Account' }}
          </button>
        </form>

        <!-- Generated password reveal -->
        <div v-if="staffReveal" data-testid="staff-password-reveal" class="reveal-panel">
          <p class="reveal-title">Password for <strong>{{ staffReveal.username }}</strong> (shown once):</p>
          <code class="reveal-secret">{{ staffReveal.password }}</code>
          <div class="reveal-actions">
            <button type="button" class="btn btn-sm" data-testid="staff-copy" @click="copyToClipboard(staffReveal.password)">Copy</button>
            <button type="button" class="btn btn-sm" data-testid="staff-email-preview-toggle"
              :aria-expanded="showEmailPreview" @click="showEmailPreview = !showEmailPreview"
              :disabled="!staffReveal.email">
              {{ showEmailPreview ? 'Hide email' : 'Preview email' }}
            </button>
            <button type="button" class="btn btn-sm" data-testid="staff-email-resend"
              :disabled="!staffReveal.email || resendLoading" @click="resendCredentialEmail">
              <Spinner v-if="resendLoading" size="sm" inline />{{ resendLoading ? 'Sending' : 'Re-send email' }}
            </button>
            <button type="button" class="btn btn-sm" @click="staffReveal = null">Dismiss</button>
          </div>
          <p v-if="!staffReveal.email" class="reveal-notice">
            No email on file for this account — add one on the create form to email credentials.
          </p>
          <!-- Inline preview of the exact email the backend sends -->
          <div v-if="showEmailPreview && emailPreview" data-testid="staff-email-preview" class="email-preview">
            <div class="email-preview-head">
              <span><strong>To:</strong> {{ emailPreview.to }}</span>
              <span><strong>Subject:</strong> {{ emailPreview.subject }}</span>
            </div>
            <pre class="email-preview-body">{{ emailPreview.body }}</pre>
          </div>
          <p v-if="resendNotice" data-testid="staff-email-notice" class="reveal-notice">{{ resendNotice }}</p>
        </div>
      </div>

      <!-- Add Student -->
      <div class="card" style="margin-bottom: var(--space-6);">
        <h2 style="margin: 0 0 var(--space-1) 0;">Add Student</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">
          Single student add — identical to a one-row roster import. Login username = Student ID; default password = student number.
        </p>

        <AddStudentForm @created="fetchUsers" />
      </div>

      <!-- Bulk Import -->
      <div class="card">
        <h2 style="margin: 0 0 var(--space-1) 0;">Bulk Roster Import</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">Import students from CSV or XLSX.</p>
        <BulkImportStudents />
      </div>
    </div>

    <!-- User List -->
    <div>
      <div class="card">
        <h2 style="margin: 0 0 var(--space-4) 0;">User Management</h2>

        <router-link to="/admin/bulk-reset" class="btn btn-sm" data-testid="bulk-reset-link" style="display: inline-block; margin-bottom: var(--space-4);">
          Bulk Reset Passwords
        </router-link>

        <!-- Reset-password reveal -->
        <div v-if="resetReveal" data-testid="reset-password-reveal" class="reveal-panel">
          <p class="reveal-title">New password for <strong>{{ resetReveal.username }}</strong> (shown once):</p>
          <code class="reveal-secret">{{ resetReveal.password }}</code>
          <div class="reveal-actions">
            <button type="button" class="btn btn-sm" @click="copyToClipboard(resetReveal.password)">Copy</button>
            <button type="button" class="btn btn-sm" @click="resetReveal = null">Dismiss</button>
          </div>
        </div>

        <!-- Password history drawer -->
        <div v-if="history" data-testid="password-history" class="reveal-panel history-panel">
          <div class="reveal-actions" style="justify-content: space-between;">
            <p class="reveal-title" style="margin: 0;">Password history — <strong>{{ history.username }}</strong></p>
            <button type="button" class="btn btn-sm" @click="closeHistory">Close</button>
          </div>
          <p v-if="!history.key_configured" data-testid="history-key-notice" class="reveal-notice">
            Password encryption key not configured — stored password values cannot be shown.
          </p>
          <template v-else>
            <EmptyState v-if="history.events.length === 0" title="No password events" body="Nothing has been recorded for this user yet." />
            <table v-else class="table" style="margin-top: var(--space-2);">
              <thead>
                <tr>
                  <th>Source</th>
                  <th>Changed by</th>
                  <th>When</th>
                  <th>Password</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(ev, i) in history.events" :key="i">
                  <td>{{ ev.source }}</td>
                  <td>{{ ev.changed_by_username }}</td>
                  <td>{{ ev.created_at }}</td>
                  <td><code>{{ ev.new_password }}</code></td>
                </tr>
              </tbody>
            </table>
          </template>
        </div>

        <Skeleton v-if="listLoading" variant="table-row" :rows="4" :cols="5" />
        <div v-else-if="!listLoading && users.length === 0">
          <EmptyState title="No users yet" body="Create the first account using the form on the left." />
        </div>
        <div v-else class="table-container sticky-table-head">
          <table class="table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Username</th>
                <th>Role</th>
                <th>Name</th>
                <th>Email</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="user in users" :key="user.id">
                <td>{{ user.id }}</td>
                <td>
                  <template v-if="editingUser?.id === user.id">
                    <input class="input input-sm" v-model="editingUser.username" />
                  </template>
                  <template v-else>{{ user.username }}</template>
                </td>
                <td>
                  <template v-if="editingUser?.id === user.id">
                    <select class="input input-sm" v-model="editingUser.role">
                      <option value="STUDENT">STUDENT</option>
                      <option value="LECTURER">LECTURER</option>
                      <option value="ADMIN">ADMIN</option>
                    </select>
                  </template>
                  <template v-else>
                    <span class="badge" :class="user.role.toLowerCase()">{{ user.role }}</span>
                  </template>
                </td>
                <td>
                  <template v-if="editingUser?.id === user.id">
                    <div style="display: flex; gap: 4px;">
                      <input class="input input-sm" v-model="editingUser.first_name" placeholder="First" />
                      <input class="input input-sm" v-model="editingUser.last_name" placeholder="Last" />
                    </div>
                  </template>
                  <template v-else>
                    {{ user.first_name }} {{ user.last_name }}
                  </template>
                </td>
                <td>
                  <EmailReveal :value="user.email" />
                </td>
                <td>
                  <div class="row-actions">
                    <template v-if="editingUser?.id === user.id">
                      <button class="btn btn-sm btn-primary" :disabled="saveUserLoading" @click="saveUser">Save</button>
                      <button class="btn btn-sm" @click="cancelEdit">Cancel</button>
                    </template>
                    <template v-else>
                      <button class="btn btn-sm" @click="startEdit(user)">Edit</button>
                      <button class="btn btn-sm" :data-testid="`reset-${user.id}`" :disabled="resetPasswordLoading" @click="resetPasswordAction(user)">Reset password</button>
                      <button class="btn btn-sm" :data-testid="`history-${user.id}`" :disabled="historyLoading" @click="openHistory(user)">History</button>
                      <button class="btn btn-sm btn-danger" :disabled="deleteUserLoading" @click="deleteUser(user.id)">Delete</button>
                    </template>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
  </div>
</template>

<style scoped>
.export-card {
  margin-bottom: var(--space-6);
}
.section-title {
  margin: 0 0 var(--space-1) 0;
  font-weight: 800;
  letter-spacing: -0.01em;
}
.export-row {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-top: var(--space-3);
}
.switch {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-size: 0.9rem;
  cursor: pointer;
}
.export-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4);
  margin: var(--space-3) 0 0 0;
}
.export-meta > div {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.export-meta dt {
  font-size: 0.74rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-text-muted);
}
.export-meta dd {
  margin: 0;
  font-size: 0.9rem;
}
.admin-grid {
  display: grid;
  grid-template-columns: 1fr 1.4fr;
  gap: var(--space-5);
}
@media (max-width: 1023.98px) {
  .admin-grid { grid-template-columns: 1fr; }
}
.table-container {
  overflow-x: auto;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  margin-top: var(--space-3);
  background: var(--color-surface);
}
.table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}
.table th, .table td {
  padding: 8px 12px;
  text-align: left;
  border-bottom: 1px solid var(--color-border-soft);
  font-size: 0.88rem;
}
.table th {
  background: var(--color-surface-sunk);
  color: var(--color-text);
  font-weight: 700;
  font-size: 0.74rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.table tr:nth-child(even) td { background: var(--color-surface-muted); }
.input-sm {
  padding: 4px 8px;
  font-size: 0.85rem;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.badge.admin    { background: #1e1c17;   color: #faf7f0; border-color: #1e1c17; }
.badge.lecturer { background: var(--color-primary-bg); color: var(--color-primary-strong); border-color: var(--color-primary); }
.badge.student  { background: var(--color-surface-muted); color: var(--color-text-muted); }

h2 {
  font-weight: 800;
  letter-spacing: -0.01em;
}

.field-row {
  display: flex;
  gap: var(--space-3);
}
.field-row > div { flex: 1; }

.reveal-panel {
  margin-top: var(--space-4);
  padding: var(--space-3);
  border: 1px solid var(--color-primary);
  background: var(--color-primary-bg);
  border-radius: var(--radius);
}
.history-panel {
  border-color: var(--color-border);
  background: var(--color-surface-sunk);
}
.reveal-title {
  margin: 0 0 var(--space-2) 0;
  font-size: 0.9rem;
}
.reveal-secret {
  display: inline-block;
  font-family: var(--font-mono, monospace);
  font-size: 1.05rem;
  font-weight: 700;
  padding: 4px 10px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  user-select: all;
}
.reveal-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  margin-top: var(--space-2);
}
.reveal-notice {
  margin: var(--space-2) 0 0 0;
  font-size: 0.85rem;
  color: var(--color-text-muted);
}

/* Per-row action buttons stack vertically so the User Management table stays
   within its column instead of overflowing horizontally. */
.row-actions {
  display: flex;
  flex-direction: column;
  gap: 6px;
  align-items: stretch;
}
.row-actions .btn { width: 100%; white-space: nowrap; }

/* Inline credential-email preview */
.email-preview {
  margin-top: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  overflow: hidden;
}
.email-preview-head {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border-soft);
  background: var(--color-surface-sunk);
  font-size: 0.82rem;
}
.email-preview-body {
  margin: 0;
  padding: var(--space-3);
  font-family: var(--font-mono, monospace);
  font-size: 0.82rem;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--color-text);
}
</style>
