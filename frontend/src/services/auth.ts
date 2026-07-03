import { reactive, computed } from 'vue'
import { clearAllSchemaWindowState } from '../utils/schemaWindowState'

export interface AuthUser {
  id?: number
  username: string
  role: 'ADMIN' | 'LECTURER' | 'STUDENT'
}

interface AuthState {
  token: string | null
  user: AuthUser | null
}

// Module-level singleton — created once, shared across all useAuth() calls
const authState = reactive<AuthState>({
  token: null,
  user: null,
})

// ---------------------------------------------------------------------------
// Password / credential provisioning API (spec §5, §8)
// ---------------------------------------------------------------------------

export type StaffRole = 'LECTURER' | 'ADMIN'

export interface CreateStaffRequest {
  username: string
  role: StaffRole
  /** Optional email for credential delivery (spec §3 — column is dormant/nullable). */
  email?: string
}
export interface CreateStaffResponse {
  id: number
  generated_password: string
}

export interface AddStudentRequest {
  student_number: string
  student_id_ext: string
  sec_lec: string
  sec_lab: string
  first_name: string
  last_name: string
  /** Optional email for credential delivery (spec §3 — column is dormant/nullable). */
  email?: string
}
export interface AddStudentResponse {
  id: number
  created: boolean
  default_password: string
}

export interface ResetPasswordResponse {
  generated_password: string
}

export interface PasswordEvent {
  source: string
  changed_by_username: string
  created_at: string
  new_password: string
}
export interface PasswordEventsResponse {
  key_configured: boolean
  events: PasswordEvent[]
}

export interface ForgotPasswordResponse {
  message: string
}

export interface ChangePasswordRequest {
  old_password: string
  new_password: string
}
export interface ChangePasswordResponse {
  token: string
}

function authHeaders(): Record<string, string> {
  const token = localStorage.getItem('sql_quiz_token')
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (token) headers['Authorization'] = `Bearer ${token}`
  return headers
}

async function parseOrThrow<T>(res: Response, fallback: string): Promise<T> {
  if (!res.ok) {
    let message = fallback
    try {
      const body = await res.json()
      if (body && (body.error || body.message)) message = body.error || body.message
    } catch {
      // non-JSON error body — keep fallback
    }
    throw new Error(message)
  }
  return (await res.json()) as T
}

/** POST /api/v1/users — create a lecturer/admin (server generates the password). */
export async function createStaff(payload: CreateStaffRequest): Promise<CreateStaffResponse> {
  const res = await fetch('/api/v1/users', {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify(payload),
  })
  return parseOrThrow<CreateStaffResponse>(res, 'Failed to create account')
}

/** POST /api/v1/students — single student create/upsert (default password = student number). */
export async function addStudent(payload: AddStudentRequest): Promise<AddStudentResponse> {
  const res = await fetch('/api/v1/students', {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify(payload),
  })
  return parseOrThrow<AddStudentResponse>(res, 'Failed to add student')
}

/**
 * POST /api/v1/forgot-password — request a password reset for a username.
 * Triggers a verification-code (OTP) email to the address on file. The backend
 * ALWAYS returns a generic message and never reveals whether the account
 * exists; the caller must surface the returned message verbatim.
 */
export async function forgotPassword(username: string): Promise<ForgotPasswordResponse> {
  const res = await fetch('/api/v1/forgot-password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username }),
  })
  return parseOrThrow<ForgotPasswordResponse>(res, 'Failed to submit request')
}

/**
 * POST /api/v1/forgot-password/verify — submit the emailed verification code.
 * On success the backend emails a NEW generated password to the address on
 * file. Any failure (wrong code, expired, too many attempts, unknown username)
 * returns the same generic 400 {"error": "invalid or expired code"} so the
 * cause is never distinguishable from the client.
 */
export async function verifyForgotPassword(username: string, otp: string): Promise<ForgotPasswordResponse> {
  const res = await fetch('/api/v1/forgot-password/verify', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, otp }),
  })
  return parseOrThrow<ForgotPasswordResponse>(res, 'Failed to verify code')
}

/** POST /api/v1/users/:id/reset-password — admin-generated new password (shown once). */
export async function resetPassword(id: number): Promise<ResetPasswordResponse> {
  const res = await fetch(`/api/v1/users/${id}/reset-password`, {
    method: 'POST',
    headers: authHeaders(),
  })
  return parseOrThrow<ResetPasswordResponse>(res, 'Failed to reset password')
}

/** GET /api/v1/users/:id/password-events — admin changelog for a user. */
export async function getPasswordEvents(id: number): Promise<PasswordEventsResponse> {
  const res = await fetch(`/api/v1/users/${id}/password-events`, {
    headers: authHeaders(),
  })
  return parseOrThrow<PasswordEventsResponse>(res, 'Failed to load password history')
}

/**
 * POST /api/v1/users/password — self-service change. Backend returns a fresh JWT
 * (token_version bumped, §5b); store it so the current session stays valid.
 * NOTE: there is currently no change-password UI in the frontend; this method
 * exists so the contract is wired and the token swap is handled correctly.
 */
export async function changePassword(payload: ChangePasswordRequest): Promise<ChangePasswordResponse> {
  const res = await fetch('/api/v1/users/password', {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify(payload),
  })
  const data = await parseOrThrow<ChangePasswordResponse>(res, 'Failed to change password')
  if (data.token) {
    localStorage.setItem('sql_quiz_token', data.token)
    authState.token = data.token
  }
  return data
}

export function useAuth() {
  const token = computed(() => authState.token)
  const user = computed(() => authState.user)
  const isLoggedIn = computed(() => !!authState.token && !!authState.user)

  function login(newToken: string, newUser: AuthUser): void {
    authState.token = newToken
    authState.user = newUser
    localStorage.setItem('sql_quiz_token', newToken)
    localStorage.setItem('sql_quiz_user', JSON.stringify(newUser))
  }

  function logout(): void {
    authState.token = null
    authState.user = null
    localStorage.removeItem('sql_quiz_token')
    localStorage.removeItem('sql_quiz_user')
    clearAllSchemaWindowState()
  }

  function hydrateFromStorage(): void {
    const storedToken = localStorage.getItem('sql_quiz_token')
    const storedUserRaw = localStorage.getItem('sql_quiz_user')
    if (storedToken && storedUserRaw) {
      try {
        const storedUser = JSON.parse(storedUserRaw) as AuthUser
        if (storedUser && storedUser.role) {
          authState.token = storedToken
          authState.user = storedUser
        }
      } catch {
        // invalid stored data — stay logged out
      }
    }
  }

  return { token, user, isLoggedIn, login, logout, hydrateFromStorage }
}
