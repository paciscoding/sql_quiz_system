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
const showPassword = ref(false)

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
          <label for="login-username">Username <span class="req" aria-hidden="true">*</span></label>
          <input
            id="login-username"
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
          <label for="login-password">Password <span class="req" aria-hidden="true">*</span></label>
          <div class="pw-wrap">
            <input
              id="login-password"
              class="input"
              v-model="form.values.password"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="current-password"
              :aria-invalid="form.touched.password && !!form.errors.password"
              :aria-describedby="'err-password'"
              @blur="form.markTouched('password')"
              @input="form.touched.password && form.validateField('password')"
            />
            <button
              type="button"
              class="pw-toggle btn btn-ghost"
              :aria-label="showPassword ? 'Hide password' : 'Show password'"
              @click="showPassword = !showPassword"
            >{{ showPassword ? 'Hide' : 'Show' }}</button>
          </div>
          <FieldError :message="form.touched.password ? form.errors.password : ''" id="err-password" />
        </div>

        <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="loginLoading">
          <Spinner v-if="loginLoading" size="sm" inline :invert="true" />{{ loginLoading ? UI_COPY.LOGIN_LOADING : UI_COPY.LOGIN }}
        </button>

        <p v-if="error" role="alert" style="margin-top: var(--space-3); text-align: center;">
          <span class="badge badge-danger">{{ error }}</span>
        </p>

        <p style="margin-top: var(--space-4); text-align: center;">
          <RouterLink
            to="/forgot-password"
            class="forgot-link"
            data-testid="forgot-password-link"
          >Forgot password?</RouterLink>
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
  min-height: calc(100dvh - var(--layout-header-h) - 80px);
  padding: var(--space-7) var(--space-4);
}
.req { color: var(--color-danger); }
.forgot-link {
  font-size: 0.88rem;
  color: var(--color-primary-strong, var(--color-primary));
  text-decoration: underline;
}
.forgot-link:hover { text-decoration: none; }
.pw-wrap { display: flex; gap: var(--space-2); }
.pw-wrap .input { flex: 1; }
.login-card {
  width: 100%;
  max-width: 520px;
  margin-inline: auto;
  padding: var(--space-6);
}
/* Desktop: give the card a roomier footprint so it doesn't read as a mobile column. */
@media (min-width: 768px) {
  .login-shell {
    align-items: center;
    padding: var(--space-7) var(--space-5);
  }
  .login-card {
    max-width: 560px;
    padding: var(--space-7);
  }
}
@media (max-width: 767.98px) {
  .login-shell {
    padding: var(--space-5) var(--space-3);
  }
  .login-card {
    padding: var(--space-5);
  }
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
