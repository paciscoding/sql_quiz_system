<script setup lang="ts">
import { ref } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { forgotPassword, verifyForgotPassword } from '../services/auth'
import Spinner from './Spinner.vue'

// The generic message shown when the backend cannot be reached, so the UI
// never behaves differently based on whether an account exists.
const GENERIC_MESSAGE =
  'If an account with that username exists and has an email on file, a verification code has been sent.'

// The generic OTP failure message. The backend collapses every failure cause
// (wrong code, expired, too many attempts, unknown username) into one error;
// the UI must do the same and never show anything cause-specific.
const GENERIC_OTP_ERROR =
  'Invalid or expired code. Please check the code or request a new one.'

const DEFAULT_SUCCESS_MESSAGE =
  'Verification successful. A new password has been sent to your email.'

type Step = 'request' | 'verify' | 'done'
const step = ref<Step>('request')

const username = ref('')
const message = ref('')
const otp = ref('')
const otpError = ref('')
const successMessage = ref('')

const { inFlight: submitting, run: submit } = useSingleFlight(async () => {
  // SECURITY: this flow must never reveal whether the account exists. We treat
  // any outcome — success, error response, or network failure — as "submitted"
  // and always advance to the code-entry step with the same generic message.
  try {
    const res = await forgotPassword(username.value.trim())
    message.value = res?.message || GENERIC_MESSAGE
  } catch {
    // Network/parse/error response — still show the generic message so the
    // presence or absence of an account is not observable from the UI.
    message.value = GENERIC_MESSAGE
  }
  otp.value = ''
  otpError.value = ''
  step.value = 'verify'
})

const { inFlight: verifying, run: verify } = useSingleFlight(async () => {
  otpError.value = ''
  try {
    const res = await verifyForgotPassword(username.value.trim(), otp.value.trim())
    successMessage.value = res?.message || DEFAULT_SUCCESS_MESSAGE
    step.value = 'done'
  } catch {
    // SECURITY: one generic error for every failure cause — never reveal
    // whether the code was wrong, expired, throttled, or the account unknown.
    otpError.value = GENERIC_OTP_ERROR
  }
})

function requestNewCode(): void {
  // Back to step 1 with the username prefilled so the user can resend.
  otp.value = ''
  otpError.value = ''
  message.value = ''
  step.value = 'request'
}
</script>

<template>
  <div class="login-shell">
    <div class="login-card card">
      <div class="login-brand">
        <span class="brand-mark">SQ</span>
        <div>
          <h2>Reset your password</h2>
          <p class="muted">Enter your username and we'll email a verification code if an account exists.</p>
        </div>
      </div>

      <!-- Step 1: request a verification code -->
      <template v-if="step === 'request'">
        <div class="alert-info" style="margin-bottom: var(--space-4);">
          <strong>Heads up.</strong>
          <span class="alert-body">For your security we don't confirm whether an account exists. Check the email on file for a verification code after submitting.</span>
        </div>

        <form data-testid="forgot-password-form" @submit.prevent="submit" novalidate>
          <div class="field">
            <label for="forgot-username">Username</label>
            <input
              id="forgot-username"
              class="input"
              data-testid="forgot-username"
              v-model="username"
              autocomplete="username"
              autofocus
            />
          </div>

          <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="submitting">
            <Spinner v-if="submitting" size="sm" inline :invert="true" />{{ submitting ? 'Sending' : 'Send verification code' }}
          </button>
        </form>
      </template>

      <!-- Step 2: enter the emailed verification code -->
      <template v-else-if="step === 'verify'">
        <div
          v-if="message"
          data-testid="forgot-message"
          class="alert-info"
          style="margin-bottom: var(--space-4);"
        >
          <span class="alert-body">{{ message }}</span>
        </div>

        <p class="muted" style="margin-top: 0;">
          Enter the 6-digit code for <strong data-testid="forgot-otp-username">{{ username.trim() }}</strong>.
        </p>

        <form data-testid="forgot-otp-form" @submit.prevent="verify" novalidate>
          <div class="field">
            <label for="forgot-otp">Verification code</label>
            <input
              id="forgot-otp"
              class="input"
              data-testid="forgot-otp"
              v-model="otp"
              inputmode="numeric"
              autocomplete="one-time-code"
              maxlength="6"
              autofocus
            />
          </div>

          <div
            v-if="otpError"
            role="alert"
            data-testid="forgot-otp-error"
            class="alert-info"
            style="margin-bottom: var(--space-4);"
          >
            <span class="alert-body">{{ otpError }}</span>
          </div>

          <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="verifying">
            <Spinner v-if="verifying" size="sm" inline :invert="true" />{{ verifying ? 'Verifying' : 'Verify code' }}
          </button>
        </form>

        <p style="margin-top: var(--space-4); text-align: center;">
          <button
            type="button"
            class="back-link link-button"
            data-testid="forgot-request-new-code"
            @click="requestNewCode"
          >Request a new code</button>
        </p>
      </template>

      <!-- Step 3: done — a new password has been emailed -->
      <template v-else>
        <div
          data-testid="forgot-success"
          class="alert-info"
          style="margin-bottom: var(--space-4);"
        >
          <strong>Done.</strong>
          <span class="alert-body">{{ successMessage }}</span>
        </div>
        <p style="text-align: center;">
          <RouterLink to="/login" class="btn btn-primary" style="display: inline-block;">Back to login</RouterLink>
        </p>
      </template>

      <p style="margin-top: var(--space-4); text-align: center;">
        <RouterLink to="/login" class="back-link" data-testid="back-to-login">Back to login</RouterLink>
      </p>
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
.login-card {
  width: 100%;
  max-width: 520px;
  margin-inline: auto;
  padding: var(--space-6);
}
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
  .login-shell { padding: var(--space-5) var(--space-3); }
  .login-card { padding: var(--space-5); }
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
.back-link {
  font-size: 0.88rem;
  color: var(--color-primary-strong, var(--color-primary));
  text-decoration: underline;
}
.back-link:hover { text-decoration: none; }
.link-button {
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
  font: inherit;
  font-size: 0.88rem;
}
</style>
