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
