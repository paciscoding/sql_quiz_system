<script setup lang="ts">
import { ref } from 'vue'

const emit = defineEmits(['login-success'])

const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

const handleLogin = async () => {
  loading.value = true
  error.value = ''

  try {
    const response = await fetch('/api/v1/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: username.value, password: password.value })
    })

    if (!response.ok) {
      throw new Error('Invalid username or password')
    }

    const data = await response.json()
    // Save to localStorage
    localStorage.setItem('sql_quiz_token', data.token)
    localStorage.setItem('sql_quiz_user', JSON.stringify(data.user))
    
    emit('login-success', data)
  } catch (err: any) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-card">
    <h2>Login to SQL Quiz</h2>
    <form @submit.prevent="handleLogin">
      <div class="input-group">
        <label>Username</label>
        <input v-model="username" type="text" required placeholder="Enter username" />
      </div>
      <div class="input-group">
        <label>Password</label>
        <input v-model="password" type="password" required placeholder="Enter password" />
      </div>
      <button type="submit" :disabled="loading" class="primary-btn">
        {{ loading ? 'Logging in...' : 'Login' }}
      </button>
      <div v-if="error" class="error-box">{{ error }}</div>
    </form>
    <div class="hint">Default Admin: admin / admin123</div>
  </div>
</template>

<style scoped>
.login-card {
  max-width: 400px;
  margin: 4rem auto;
  padding: 2rem;
  background: white;
  border-radius: 8px;
  box-shadow: 0 4px 6px rgba(0,0,0,0.1);
}

h2 { text-align: center; margin-bottom: 2rem; color: #2d3748; }

.input-group { margin-bottom: 1.5rem; display: flex; flex-direction: column; }

label { font-weight: bold; margin-bottom: 0.5rem; color: #4a5568; }

input { padding: 0.8rem; border: 1px solid #cbd5e0; border-radius: 4px; }

.primary-btn {
  width: 100%; padding: 1rem; background: #41b883; color: white;
  border: none; border-radius: 4px; font-weight: bold; cursor: pointer;
}

.error-box {
  margin-top: 1rem; padding: 0.8rem; background: #fff5f5; color: #c53030;
  border-radius: 4px; text-align: center;
}

.hint { margin-top: 1.5rem; font-size: 0.8rem; color: #718096; text-align: center; }
</style>
