<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useConfirm } from '../services/confirm_service'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import {
  createStaff,
  addStudent,
  resetPassword,
  getPasswordEvents,
  type StaffRole,
  type PasswordEvent,
} from '../services/auth'
import Spinner from './Spinner.vue'
import EmptyState from './EmptyState.vue'
import Skeleton from './Skeleton.vue'
import BulkImportStudents from './BulkImportStudents.vue'

const { notify } = useNotifications()
const { confirm } = useConfirm()

// --- Create staff (lecturer/admin) — server generates the password ---
const newStaff = ref<{ username: string; role: StaffRole; email: string }>({
  username: '',
  role: 'LECTURER',
  email: '',
})
const staffReveal = ref<{ username: string; password: string } | null>(null)
const staffEmailNotice = ref(false)

// --- Add student (roster fields) — default password = student number ---
const newStudent = ref({
  student_number: '',
  student_id_ext: '',
  sec_lec: '',
  sec_lab: '',
  first_name: '',
  last_name: '',
  email: '',
})
const studentReveal = ref<{ student_number: string; username: string; password: string } | null>(null)

// --- Per-user reset-password reveal ---
const resetReveal = ref<{ username: string; password: string } | null>(null)

// --- Per-user password history ---
const history = ref<{
  userId: number
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

// Form validation for add student
const newStudentForm = useFormValidation({
  student_number: { rules: [required('Student number is required')], initial: '' },
  student_id_ext: { rules: [required('Student ID is required')], initial: '' },
})
watch(() => newStudent.value.student_number, (v) => { newStudentForm.values.student_number = v })
watch(() => newStudent.value.student_id_ext, (v) => { newStudentForm.values.student_id_ext = v })

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

function sendCredentialEmail() {
  // Email delivery is a backend stub for now (spec §7). Surface the pending state.
  staffEmailNotice.value = true
  notify('Email delivery pending — this is not wired up yet.', 'info')
}

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
    staffReveal.value = { username, password: res.generated_password }
    staffEmailNotice.value = false
    notify(`Account ${username} created successfully.`, 'success')
    newStaff.value = { username: '', role: 'LECTURER', email: '' }
    newStaffForm.reset()
    fetchUsers()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create account' })
  }
})

const { inFlight: addStudentLoading, run: addStudentAction } = useSingleFlight(async () => {
  if (!newStudentForm.validateAll()) return
  try {
    const { email, ...rosterFields } = newStudent.value
    const trimmedEmail = email.trim()
    const res = await addStudent({
      ...rosterFields,
      ...(trimmedEmail ? { email: trimmedEmail } : {}),
    })
    studentReveal.value = {
      student_number: newStudent.value.student_number,
      username: newStudent.value.student_id_ext,
      password: res.default_password,
    }
    notify(`Student ${newStudent.value.student_number} ${res.created ? 'created' : 'updated'}.`, 'success')
    newStudent.value = {
      student_number: '',
      student_id_ext: '',
      sec_lec: '',
      sec_lab: '',
      first_name: '',
      last_name: '',
      email: '',
    }
    newStudentForm.reset()
    fetchUsers()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to add student' })
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

const { inFlight: deleteUserLoading, run: deleteUser } = useSingleFlight(async (id: number) => {
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

onMounted(fetchUsers)
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Account Administration</h1>
      <p class="page-subtitle">Create, edit, and bulk-import student/lecturer accounts for the grader system.</p>
    </div>
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
            <label for="staff-email-input">Email <span class="muted">(optional)</span></label>
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
            <button type="button" class="btn btn-sm" data-testid="staff-email" @click="sendCredentialEmail">Email</button>
            <button type="button" class="btn btn-sm" @click="staffReveal = null">Dismiss</button>
          </div>
          <p v-if="staffEmailNotice" data-testid="staff-email-notice" class="reveal-notice">
            Email delivery pending — SMTP is not wired up yet.
          </p>
        </div>
      </div>

      <!-- Add Student -->
      <div class="card" style="margin-bottom: var(--space-6);">
        <h2 style="margin: 0 0 var(--space-1) 0;">Add Student</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">
          Single student add — identical to a one-row roster import. Login username = Student ID; default password = student number.
        </p>

        <form data-testid="add-student-form" @submit.prevent="addStudentAction" novalidate>
          <div class="field">
            <label>Student Number</label>
            <input class="input" data-testid="student-number" v-model="newStudent.student_number" required
              :aria-invalid="newStudentForm.touched.student_number && !!newStudentForm.errors.student_number"
              aria-describedby="err-student-number"
              @blur="newStudentForm.markTouched('student_number')"
            />
            <FieldError :message="newStudentForm.touched.student_number ? newStudentForm.errors.student_number : ''" id="err-student-number" />
          </div>
          <div class="field">
            <label>Student ID <span class="muted">(login username)</span></label>
            <input class="input" data-testid="student-id-ext" v-model="newStudent.student_id_ext" required
              :aria-invalid="newStudentForm.touched.student_id_ext && !!newStudentForm.errors.student_id_ext"
              aria-describedby="err-student-id-ext"
              @blur="newStudentForm.markTouched('student_id_ext')"
            />
            <FieldError :message="newStudentForm.touched.student_id_ext ? newStudentForm.errors.student_id_ext : ''" id="err-student-id-ext" />
          </div>
          <div class="field field-row">
            <div>
              <label>Lecture Section</label>
              <input class="input" data-testid="student-sec-lec" v-model="newStudent.sec_lec" />
            </div>
            <div>
              <label>Lab Section</label>
              <input class="input" data-testid="student-sec-lab" v-model="newStudent.sec_lab" />
            </div>
          </div>
          <div class="field field-row">
            <div>
              <label>First Name</label>
              <input class="input" data-testid="student-first-name" v-model="newStudent.first_name" />
            </div>
            <div>
              <label>Last Name</label>
              <input class="input" data-testid="student-last-name" v-model="newStudent.last_name" />
            </div>
          </div>
          <div class="field">
            <label for="student-email-input">Email <span class="muted">(optional)</span></label>
            <input id="student-email-input" class="input" type="email" data-testid="student-email-input"
              v-model="newStudent.email" autocomplete="email" placeholder="name@example.com" />
          </div>

          <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="addStudentLoading">
            <Spinner v-if="addStudentLoading" size="sm" inline :invert="true" />{{ addStudentLoading ? 'Adding' : 'Add Student' }}
          </button>
        </form>

        <div v-if="studentReveal" data-testid="student-password-reveal" class="reveal-panel">
          <p class="reveal-title">Login username: <strong>{{ studentReveal.username }}</strong></p>
          <p class="reveal-title">Default password for <strong>{{ studentReveal.student_number }}</strong>:</p>
          <code class="reveal-secret">{{ studentReveal.password }}</code>
          <div class="reveal-actions">
            <button type="button" class="btn btn-sm" @click="copyToClipboard(studentReveal.password)">Copy</button>
            <button type="button" class="btn btn-sm" @click="studentReveal = null">Dismiss</button>
          </div>
        </div>
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
                  <div style="display: flex; gap: 8px;">
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
  gap: var(--space-2);
  align-items: center;
  margin-top: var(--space-2);
}
.reveal-notice {
  margin: var(--space-2) 0 0 0;
  font-size: 0.85rem;
  color: var(--color-text-muted);
}
</style>
