<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useConfirm } from '../services/confirm_service'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import Spinner from './Spinner.vue'
import EmptyState from './EmptyState.vue'
import Skeleton from './Skeleton.vue'
import BulkImportStudents from './BulkImportStudents.vue'

const { notify } = useNotifications()
const { confirm } = useConfirm()
const newUser = ref({
  username: '',
  password: '',
  role: 'STUDENT'
})

const users = ref<any[]>([])
const listLoading = ref(false)
const editingUser = ref<any>(null)


// Form validation for create user (TODO-038)
const newUserForm = useFormValidation({
  username: { rules: [required('Username is required')], initial: '' },
  password: { rules: [required('Password is required')], initial: '' },
})
watch(() => newUser.value.username, (v) => { newUserForm.values.username = v })
watch(() => newUser.value.password, (v) => { newUserForm.values.password = v })

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

const { inFlight: createUserLoading, run: createUser } = useSingleFlight(async () => {
  if (!newUserForm.validateAll()) return
  const token = localStorage.getItem('sql_quiz_token')

  try {
    const response = await fetch('/api/v1/users', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify(newUser.value),
    })

    if (!response.ok) {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to create user')
    }

    notify(`User ${newUser.value.username} created successfully.`, 'success')
    newUser.value = { username: '', password: '', role: 'STUDENT' }
    fetchUsers()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to create user' })
  }
})

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
      <div class="card" style="margin-bottom: var(--space-6);">
        <h2 style="margin: 0 0 var(--space-1) 0;">Create Account</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">Manually create accounts.</p>

        <form @submit.prevent="createUser" novalidate>
          <div class="field">
            <label>Username</label>
            <input class="input" v-model="newUser.username" required autofocus
              :aria-invalid="newUserForm.touched.username && !!newUserForm.errors.username"
              aria-describedby="err-username"
              @blur="newUserForm.markTouched('username')"
            />
            <FieldError :message="newUserForm.touched.username ? newUserForm.errors.username : ''" id="err-username" />
          </div>
          <div class="field">
            <label>Password</label>
            <input class="input" type="password" v-model="newUser.password" required
              :aria-invalid="newUserForm.touched.password && !!newUserForm.errors.password"
              aria-describedby="err-password"
              @blur="newUserForm.markTouched('password')"
            />
            <FieldError :message="newUserForm.touched.password ? newUserForm.errors.password : ''" id="err-password" />
          </div>
          <div class="field">
            <label>Role</label>
            <select class="input" v-model="newUser.role">
              <option value="STUDENT">Student</option>
              <option value="LECTURER">Lecturer</option>
              <option value="ADMIN">Admin</option>
            </select>
          </div>

          <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="createUserLoading">
            <Spinner v-if="createUserLoading" size="sm" inline :invert="true" />{{ createUserLoading ? 'Creating' : 'Create Account' }}
          </button>
        </form>
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
</style>
