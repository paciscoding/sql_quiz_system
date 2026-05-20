<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useNotifications } from '../services/notification_service'
import BulkImportStudents from './BulkImportStudents.vue'

const { notify } = useNotifications()
const newUser = ref({
  username: '',
  password: '',
  role: 'STUDENT'
})

const users = ref<any[]>([])
const loading = ref(false)
const listLoading = ref(false)
const editingUser = ref<any>(null)

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
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    listLoading.value = false
  }
}

const createUser = async () => {
  loading.value = true
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
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

const deleteUser = async (id: number) => {
  if (!confirm('Are you sure you want to delete this user?')) return
  
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
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

const startEdit = (user: any) => {
  editingUser.value = { ...user }
}

const cancelEdit = () => {
  editingUser.value = null
}

const saveUser = async () => {
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
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

onMounted(fetchUsers)
</script>

<template>
  <div style="max-width: 1200px; margin: var(--space-6) auto; padding: 0 var(--space-4); display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-6);">
    <!-- Create User -->
    <div>
      <div class="card" style="margin-bottom: var(--space-6);">
        <h2 style="margin: 0 0 var(--space-1) 0;">Create Account</h2>
        <p class="muted" style="margin: 0 0 var(--space-4) 0;">Manually create accounts.</p>

        <form @submit.prevent="createUser">
          <div class="field">
            <label>Username</label>
            <input class="input" v-model="newUser.username" required />
          </div>
          <div class="field">
            <label>Password</label>
            <input class="input" type="password" v-model="newUser.password" required />
          </div>
          <div class="field">
            <label>Role</label>
            <select class="input" v-model="newUser.role">
              <option value="STUDENT">Student</option>
              <option value="LECTURER">Lecturer</option>
              <option value="ADMIN">Admin</option>
            </select>
          </div>

          <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="loading">
            Create Account
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
        
        <div v-if="listLoading" class="muted">Loading users...</div>
        <div v-else class="table-container">
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
                      <button class="btn btn-sm btn-primary" @click="saveUser">Save</button>
                      <button class="btn btn-sm" @click="cancelEdit">Cancel</button>
                    </template>
                    <template v-else>
                      <button class="btn btn-sm" @click="startEdit(user)">Edit</button>
                      <button class="btn btn-sm btn-danger" @click="deleteUser(user.id)">Delete</button>
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
</template>

<style scoped>
.table-container {
  overflow-x: auto;
  border: 2px solid var(--color-border);
  margin-top: var(--space-4);
}
.table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}
.table th, .table td {
  padding: var(--space-3);
  text-align: left;
  border: 1px solid var(--color-border);
  font-size: 0.9rem;
}
.table th {
  background: var(--color-surface-muted);
  color: var(--color-text);
  font-weight: 700;
}
.input-sm {
  padding: 4px 8px;
  font-size: 0.85rem;
  border: 1px solid var(--color-border);
}
.btn-sm {
  padding: 6px 12px;
  font-size: 0.8rem;
  font-weight: 700;
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
  cursor: pointer;
}
.btn-sm:active {
  transform: translateY(1px);
}
.badge {
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-size: 0.75rem;
  font-weight: 700;
  display: inline-block;
}
.badge.admin { background: var(--color-text); color: var(--color-surface); }
.badge.lecturer { background: var(--color-surface-muted); color: var(--color-text); }
.badge.student { background: var(--color-surface); color: var(--color-text); }

.btn-danger {
  background: var(--color-danger);
  color: var(--color-surface);
  border-color: var(--color-danger);
}
.btn-primary {
  background: var(--color-primary);
  color: var(--color-surface);
}
.btn-primary:hover {
  background: #333333;
}

.card {
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  padding: var(--space-6);
  border-radius: 0;
}

h2 {
  font-weight: 800;
  letter-spacing: -0.01em;
}
</style>
