import { reactive, computed } from 'vue'
import { clearAllSchemaWindowState } from '../utils/schemaWindowState'

export interface AuthUser {
  id?: string
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
  id: string
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
  id: string
  created: boolean
  /** Server-generated password, present only when a new row was created. */
  generated_password?: string
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
export async function resetPassword(id: string): Promise<ResetPasswordResponse> {
  const res = await fetch(`/api/v1/users/${id}/reset-password`, {
    method: 'POST',
    headers: authHeaders(),
  })
  return parseOrThrow<ResetPasswordResponse>(res, 'Failed to reset password')
}

export interface BulkResetResultRow {
  user_id: string
  username: string
  student_number: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  /** Plaintext, shown to the admin exactly once — this response is the ONLY
   * channel it ever travels on (ARCH-031/032); never persisted client-side. */
  password: string
  /** "" when no address is on file. */
  email: string
  /** Absent on a normal (non-emailing) reset — this endpoint does not send
   * mail unless the request opted in with send_email. */
  email_status?: 'queued' | 'no_email' | 'not_configured'
}
export interface BulkResetFailureRow {
  user_id: string
  username: string
  error: string
}
export interface BulkResetResponse {
  mode: 'ids' | 'all_students' | 'exam_roster'
  processed: number
  succeeded: number
  failed: number
  remaining: number
  next_after_user_id: string | null
  key_configured: boolean
  results: BulkResetResultRow[]
  failures: BulkResetFailureRow[]
  /** Progress-job id for this batch's credential emails; "" when nothing is
   * being sent. Each paginated batch starts its OWN job, so a multi-batch run
   * yields several ids — track them all (see useEmailJobStatus). */
  job_id: string
}

export interface BulkResetPasswordsRequest {
  mode: 'ids' | 'all_students' | 'exam_roster'
  user_ids?: string[]
  exam_id?: string
  /** Must be the literal string "RESET", on every request including
   * continuation batches — the server refuses with 400 otherwise (ARCH-032 S3). */
  confirm: string
  after_user_id?: string
  batch_size?: number
  /** Opt IN to emailing each new password. Omitted/false keeps this endpoint's
   * long-standing behaviour: reset, show, print — never send. */
  send_email?: boolean
}

/** POST /api/v1/users/bulk-reset-password — admin-only bulk reset (ARCH-031). */
export async function bulkResetPasswords(payload: BulkResetPasswordsRequest): Promise<BulkResetResponse> {
  const res = await fetch('/api/v1/users/bulk-reset-password', {
    method: 'POST',
    headers: authHeaders(),
    body: JSON.stringify(payload),
  })
  return parseOrThrow<BulkResetResponse>(res, 'Failed to bulk-reset passwords')
}

// ---------------------------------------------------------------------------
// Read-only lookups for BulkPasswordReset.vue's selection screen. Defined
// here (rather than inline fetch() calls in the component) so the component
// never touches localStorage/sessionStorage directly — it goes through
// authHeaders()/parseOrThrow() like every other call in this file (ARCH-032:
// the component file must contain zero references to browser storage APIs).
// ---------------------------------------------------------------------------

export interface StudentSummary {
  id: string
  username: string
}

/** GET /api/v1/users/students — the full student roster (Admin, Lecturer). */
export async function listStudents(): Promise<StudentSummary[]> {
  const res = await fetch('/api/v1/users/students', { headers: authHeaders() })
  return parseOrThrow<StudentSummary[]>(res, 'Failed to load students')
}

export interface ExamSummary {
  id: string
  title: string
}

/** GET /api/v1/lecturer/exams — examinations list (Admin, Lecturer). */
export async function listExamsForSelection(): Promise<ExamSummary[]> {
  const res = await fetch('/api/v1/lecturer/exams', { headers: authHeaders() })
  return parseOrThrow<ExamSummary[]>(res, 'Failed to load examinations')
}

/** GET /api/v1/lecturer/exams/:id/students — roster size preview for exam_roster mode. */
export async function getExamRosterCount(examId: string): Promise<number> {
  const res = await fetch(`/api/v1/lecturer/exams/${examId}/students`, { headers: authHeaders() })
  const data = await parseOrThrow<{ count: number }>(res, 'Failed to load exam roster')
  return data.count ?? 0
}

export interface ResendCredentialResponse {
  emailed_to: string
}

/** POST /api/v1/users/:id/resend-credential — re-email the user's current credential. */
export async function resendCredential(id: string): Promise<ResendCredentialResponse> {
  const res = await fetch(`/api/v1/users/${id}/resend-credential`, {
    method: 'POST',
    headers: authHeaders(),
  })
  return parseOrThrow<ResendCredentialResponse>(res, 'Failed to resend credential email')
}

/** GET /api/v1/users/:id/password-events — admin changelog for a user. */
export async function getPasswordEvents(id: string): Promise<PasswordEventsResponse> {
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
