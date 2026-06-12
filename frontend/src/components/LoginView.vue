<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import Spinner from './Spinner.vue'
import { useAuth } from '../services/auth'

const router = useRouter()
const route = useRoute()
const { login } = useAuth()

const form = useFormValidation({
  username: { rules: [required('Username is required')], initial: '' },
  password: { rules: [required('Password is required')], initial: '' },
})

import { ref } from 'vue'
import { UI_COPY } from '../constants/ui_copy'
const error = ref('')

const { inFlight: loginLoading, run: handleLogin } = useSingleFlight(async () => {
  error.value = ''
  if (!form.validateAll()) return

  try {
    const response = await fetch('/api/v1/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: form.values.username, password: form.values.password })
    })

    if (!response.ok) {
      form.markTouched('username')
      form.markTouched('password')
      throw new Error('Invalid username or password')
    }

    const data = await response.json()
    login(data.token, data.user)
    
    router.push((route.query.redirect as string) || '/dashboard')
  } catch (err: any) {
    error.value = err.message
  }
})
</script>

<template>
  <div class="login-shell">
    <div class="login-card card">
      <div class="login-brand">
        <span class="brand-mark">SQ</span>
        <div>
          <h2>SQL Quiz Grader Portal</h2>
          <p class="muted">Sign in to continue.</p>
        </div>
      </div>

      <div class="alert-info" style="margin-bottom: var(--space-4);">
        <strong>Welcome.</strong>
        <span class="alert-body">Use the credentials issued by your instructor. If you have lost access, contact the course administrator.</span>
      </div>

      <form @submit.prevent="handleLogin" novalidate>
        <div class="field">
          <label>Username</label>
          <input
            class="input"
            v-model="form.values.username"
            autocomplete="username"
            autofocus
            :aria-invalid="form.touched.username && !!form.errors.username"
            :aria-describedby="'err-username'"
            @blur="form.markTouched('username')"
            @input="form.touched.username && form.validateField('username')"
          />
          <FieldError :message="form.touched.username ? form.errors.username : ''" id="err-username" />
        </div>
        <div class="field">
          <label>Password</label>
          <input
            class="input"
            v-model="form.values.password"
            type="password"
            autocomplete="current-password"
            :aria-invalid="form.touched.password && !!form.errors.password"
            :aria-describedby="'err-password'"
            @blur="form.markTouched('password')"
            @input="form.touched.password && form.validateField('password')"
          />
          <FieldError :message="form.touched.password ? form.errors.password : ''" id="err-password" />
        </div>

        <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="loginLoading">
          <Spinner v-if="loginLoading" size="sm" inline :invert="true" />{{ loginLoading ? UI_COPY.LOGIN_LOADING : UI_COPY.LOGIN }}
        </button>

        <p v-if="error" style="margin-top: var(--space-3); text-align: center;">
          <span class="badge badge-danger">{{ error }}</span>
        </p>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-shell {
  display: flex;
  justify-content: center;
  align-items: flex-start;
  min-height: calc(100vh - var(--layout-header-h) - 80px);
  padding: var(--space-7) var(--space-4);
}
.login-card {
  width: 100%;
  max-width: 420px;
  padding: var(--space-6);
}
.login-brand {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: var(--space-5);
  padding-bottom: var(--space-3);
  border-bottom: 1px solid var(--color-rule);
}
.login-brand .brand-mark {
  width: 44px; height: 44px;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--color-primary);
  color: #fff;
  border-radius: var(--radius);
  font-family: var(--font-mono);
  font-weight: 700;
  font-size: 0.95rem;
  letter-spacing: 0.04em;
}
.login-brand h2 { margin: 0; font-size: 1.2rem; }
.login-brand p  { margin: 2px 0 0 0; font-size: 0.85rem; }
</style>
