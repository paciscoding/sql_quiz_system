<script setup lang="ts">
import { ref } from 'vue'

const newUser = ref({
  username: '',
  password: '',
  role: 'STUDENT'
})

const loading = ref(false)
const message = ref('')
const error = ref('')

const createUser = async () => {
  loading.value = true
  message.value = ''
  error.value = ''

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

    message.value = `User ${newUser.value.username} created successfully.`
    newUser.value = { username: '', password: '', role: 'STUDENT' }
  } catch (err: any) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="admin-panel">
    <h2>Account Generation</h2>
    <p>Create new Lecturer or Student accounts.</p>

    <div class="form-card">
      <div class="input-group">
        <label>Username:</label>
        <input v-model="newUser.username" type="text" placeholder="e.g. 64010123 or aj_lecturer" />
      </div>

      <div class="input-group">
        <label>Password:</label>
        <input v-model="newUser.password" type="text" placeholder="Set temporary password" />
      </div>

      <div class="input-group">
        <label>Role:</label>
        <select v-model="newUser.role">
          <option value="LECTURER">Lecturer (Admin only)</option>
          <option value="STUDENT">Student</option>
        </select>
      </div>

      <button @click="createUser" :disabled="loading" class="primary-btn">
        {{ loading ? 'Generating...' : 'Generate Account' }}
      </button>

      <div v-if="message" class="success-box">{{ message }}</div>
      <div v-if="error" class="error-box">{{ error }}</div>
    </div>
  </div>
</template>

<style scoped>
.admin-panel { background: white; padding: 2rem; border-radius: 8px; box-shadow: 0 2px 4px rgba(0,0,0,0.05); }
.form-card { display: flex; flex-direction: column; gap: 1.5rem; margin-top: 1rem; }
.input-group { display: flex; flex-direction: column; }
label { font-weight: bold; margin-bottom: 0.5rem; }
input, select { padding: 0.8rem; border: 1px solid #cbd5e0; border-radius: 4px; }
.primary-btn {
  padding: 1rem; background: #3182ce; color: white; border: none; border-radius: 4px; font-weight: bold; cursor: pointer;
}
.success-box { padding: 1rem; background: #f0fff4; color: #38a169; border-radius: 4px; }
.error-box { padding: 1rem; background: #fff5f5; color: #e53e3e; border-radius: 4px; }
</style>
