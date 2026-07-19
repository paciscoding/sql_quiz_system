import { beforeEach, describe, expect, it } from 'vitest'
import { useAuth, type AuthUser } from '../services/auth'

describe('useAuth', () => {
  beforeEach(() => {
    useAuth().logout()
    localStorage.clear()
  })

  describe('login()', () => {
    it('stores the token and user, then marks the session as logged in', () => {
      const auth = useAuth()
      const token = 'test-token'
      const user: AuthUser = {
        id: '1',
        username: 'alice',
        role: 'LECTURER',
      }

      auth.login(token, user)

      expect(auth.isLoggedIn.value).toBe(true)
      expect(auth.token.value).toBe(token)
      expect(auth.user.value).toEqual(user)
      expect(localStorage.getItem('sql_quiz_token')).toBe(token)
      expect(localStorage.getItem('sql_quiz_user')).toBe(JSON.stringify(user))
    })
  })

  describe('logout()', () => {
    it('clears the token and user from state and storage', () => {
      const auth = useAuth()
      const user: AuthUser = {
        username: 'alice',
        role: 'STUDENT',
      }

      auth.login('test-token', user)
      auth.logout()

      expect(auth.isLoggedIn.value).toBe(false)
      expect(auth.token.value).toBeNull()
      expect(auth.user.value).toBeNull()
      expect(localStorage.getItem('sql_quiz_token')).toBeNull()
      expect(localStorage.getItem('sql_quiz_user')).toBeNull()
    })
  })

  describe('hydrateFromStorage()', () => {
    it('restores the token and user from localStorage', () => {
      const auth = useAuth()
      const user: AuthUser = {
        username: 'bob',
        role: 'STUDENT',
      }

      localStorage.setItem('sql_quiz_token', 'tok')
      localStorage.setItem('sql_quiz_user', JSON.stringify(user))

      auth.hydrateFromStorage()

      expect(auth.isLoggedIn.value).toBe(true)
      expect(auth.token.value).toBe('tok')
      expect(auth.user.value?.username).toBe('bob')
    })

    it('does nothing when localStorage is empty', () => {
      const auth = useAuth()

      auth.hydrateFromStorage()

      expect(auth.isLoggedIn.value).toBe(false)
    })

    it('does not throw and stays logged out when stored user JSON is corrupted', () => {
      const auth = useAuth()

      localStorage.setItem('sql_quiz_token', 'tok')
      localStorage.setItem('sql_quiz_user', 'not-valid-json{{{{')

      expect(() => auth.hydrateFromStorage()).not.toThrow()
      expect(auth.isLoggedIn.value).toBe(false)
    })

    it('stays logged out when only a token is present', () => {
      const auth = useAuth()

      localStorage.setItem('sql_quiz_token', 'tok')

      auth.hydrateFromStorage()

      expect(auth.isLoggedIn.value).toBe(false)
    })

    it('stays logged out when only a user is present', () => {
      const auth = useAuth()
      const user: AuthUser = {
        username: 'bob',
        role: 'STUDENT',
      }

      localStorage.setItem('sql_quiz_user', JSON.stringify(user))

      auth.hydrateFromStorage()

      expect(auth.isLoggedIn.value).toBe(false)
    })
  })
})
