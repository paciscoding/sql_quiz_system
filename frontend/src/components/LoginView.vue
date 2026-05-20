<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

const router = useRouter()
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
    
    router.push('/dashboard')
  } catch (err: any) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-shell">
    <div class="card login-card">
      <h2 style="margin: 0 0 var(--space-1) 0;">SQL Quiz System</h2>
      <p class="muted" style="margin: 0 0 var(--space-4) 0;">Sign in to continue</p>

      <form @submit.prevent="handleLogin">
        <div class="field">
          <label>Username</label>
          <input class="input" v-model="username" autocomplete="username" />
        </div>
        <div class="field">
          <label>Password</label>
          <input class="input" v-model="password" type="password" autocomplete="current-password" />
        </div>

        <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="loading">
          {{ loading ? 'Signing in...' : 'Login' }}
        </button>

        <p v-if="error" style="margin-top: var(--space-3); text-align: center;">
          <span class="badge badge-danger">{{ error }}</span>
        </p>

        <p class="muted" style="margin-top: var(--space-4); text-align: center; font-size: 0.8rem;">
          Default admin: admin / admin123
        </p>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-shell {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: calc(100vh - 80px);
  padding: var(--space-4);
  background: var(--color-surface);
}
.login-card {
  width: 100%;
  max-width: 400px;
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  padding: var(--space-6);
}
.login-card h2 {
  color: var(--color-text);
  font-weight: 800;
  letter-spacing: -0.01em;
  font-size: 1.8rem;
  line-height: 1.2;
  margin-bottom: var(--space-1);
}
.muted {
  color: var(--color-text-muted);
  font-weight: 500;
  font-size: 0.9rem;
}
.field {
  margin-bottom: var(--space-4);
}
.label {
  display: block;
  font-weight: 700;
  margin-bottom: 0.5rem;
  color: var(--color-text);
}
.input {
  width: 100%;
  padding: 0.6rem;
  border: 2px solid var(--color-border);
  font-family: var(--font-mono);
  font-weight: 600;
  font-size: 1rem;
  background: var(--color-surface);
  color: var(--color-text);
}
.btn-primary {
  background: var(--color-primary);
  color: var(--color-surface);
  border: 2px solid var(--color-border);
  font-weight: 700;
  padding: var(--space-3);
  cursor: pointer;
  transition: none;
}
.btn-primary:hover:not(:disabled) {
  background: #333333;
}
.btn-primary:active {
  transform: translateY(1px);
}
.badge-danger {
  background: var(--color-danger-bg);
  color: var(--color-danger);
  border: 1px solid var(--color-danger);
  padding: var(--space-2) var(--space-4);
  font-weight: 700;
}
</style>
