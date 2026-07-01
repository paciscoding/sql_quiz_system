/**
 * Regression tests for TODO-SESS — global 401 fetch interceptor + forceLogout.
 *
 * session.ts has a module-level `handling` boolean guard that is reset in a
 * `.finally()` on the router.push promise. To get a fresh `handling = false`
 * for every test we use vi.resetModules() + dynamic imports inside each test
 * (or inside beforeEach). All three dependency modules are mocked so we can
 * spy on logout, notify, and router.push without touching real infrastructure.
 *
 * window.fetch is saved and restored in beforeEach/afterEach to prevent leaks
 * across tests (installAuthFetchInterceptor monkeypatches window.fetch).
 */

import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build a Response-like object that satisfies the interceptor's status check. */
function makeResponse(status: number): Response {
  return { status } as unknown as Response
}

/** Return a fetch mock that immediately resolves with the given Response. */
function makeFetchStub(res: Response) {
  return vi.fn().mockResolvedValue(res)
}

// ---------------------------------------------------------------------------
// Per-test module reload machinery
//
// Because `handling` is module-level state, we reload the module before each
// test so that guard is always false at test start.
// ---------------------------------------------------------------------------

type SessionModule = typeof import('../../services/session')

// Mocks are hoisted to file top by Vitest so they apply to dynamic imports too.
vi.mock('../../services/auth', () => ({
  useAuth: vi.fn(),
}))

vi.mock('../../services/notification_service', () => ({
  useNotifications: vi.fn(),
}))

vi.mock('../../router', () => ({
  default: {
    push: vi.fn(),
    currentRoute: { value: { name: 'home', fullPath: '/lecturer/practice' } },
  },
}))

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

describe('session.ts (TODO-SESS)', () => {
  let originalFetch: typeof window.fetch

  // Spies we refresh each test
  let logoutSpy: ReturnType<typeof vi.fn>
  let notifySpy: ReturnType<typeof vi.fn>
  let routerPushSpy: ReturnType<typeof vi.fn>

  // Fresh module handle (reloaded per test to reset `handling` guard)
  let session: SessionModule

  beforeEach(async () => {
    // 1. Save the real fetch
    originalFetch = window.fetch

    // 2. Clear localStorage
    localStorage.clear()

    // 3. Reset module registry so `handling` starts at false
    vi.resetModules()

    // 4. Wire up fresh spies on the mocked modules
    logoutSpy = vi.fn()
    notifySpy = vi.fn()
    routerPushSpy = vi.fn().mockResolvedValue(undefined)

    const { useAuth } = await import('../../services/auth')
    const { useNotifications } = await import('../../services/notification_service')
    const routerModule = await import('../../router')

    vi.mocked(useAuth).mockReturnValue({ logout: logoutSpy } as unknown as ReturnType<typeof useAuth>)
    vi.mocked(useNotifications).mockReturnValue({ notify: notifySpy } as unknown as ReturnType<typeof useNotifications>)
    // Restore currentRoute to a non-login route for most tests
    routerModule.default.push = routerPushSpy as unknown as typeof routerModule.default.push
    ;(routerModule.default as { currentRoute: { value: { name: string; fullPath: string } } })
      .currentRoute.value = { name: 'home', fullPath: '/lecturer/practice' }

    // 5. Load the module under test AFTER mocks are configured
    session = await import('../../services/session')
  })

  afterEach(() => {
    // Restore the original fetch (the interceptor may have overwritten window.fetch)
    window.fetch = originalFetch
    vi.clearAllMocks()
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 1. 401 with token + non-login URL → triggers forceLogout + redirect
  // ─────────────────────────────────────────────────────────────────────────

  describe('interceptor: 401 with token on authenticated endpoint', () => {
    it('calls logout() and router.push(/login) with redirect query', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      const res401 = makeResponse(401)
      window.fetch = makeFetchStub(res401)

      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/some-protected-resource')

      // Wait for the push promise's .finally to settle
      await vi.waitFor(() => expect(logoutSpy).toHaveBeenCalledOnce())

      expect(logoutSpy).toHaveBeenCalledOnce()
      expect(notifySpy).toHaveBeenCalledOnce()
      expect(routerPushSpy).toHaveBeenCalledOnce()
      expect(routerPushSpy).toHaveBeenCalledWith({
        path: '/login',
        query: { redirect: '/lecturer/practice' },
      })
    })

    it('clears localStorage tokens as part of logout (auth.logout() side-effect)', async () => {
      // Plant tokens — auth.logout() in the real implementation removes them.
      // Here we wire the spy to actually clear storage so we can verify the
      // flow reaches logout.
      logoutSpy.mockImplementation(() => {
        localStorage.removeItem('sql_quiz_token')
        localStorage.removeItem('sql_quiz_user')
      })
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      localStorage.setItem('sql_quiz_user', '{"username":"alice","role":"LECTURER"}')

      window.fetch = makeFetchStub(makeResponse(401))
      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/lecturer/exams')

      await vi.waitFor(() => expect(logoutSpy).toHaveBeenCalledOnce())
      expect(localStorage.getItem('sql_quiz_token')).toBeNull()
      expect(localStorage.getItem('sql_quiz_user')).toBeNull()
    })

    it('returns the SAME 401 response object (identity preserved)', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      const res401 = makeResponse(401)
      window.fetch = makeFetchStub(res401)

      session.installAuthFetchInterceptor()
      const returned = await window.fetch('/api/v1/protected')

      expect(returned).toBe(res401)
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 2. 401 on /api/v1/login URL → does NOT trigger forceLogout
  // ─────────────────────────────────────────────────────────────────────────

  describe('interceptor: 401 on the login endpoint (bad credentials)', () => {
    it('does NOT call logout or router.push for /api/v1/login 401', async () => {
      localStorage.setItem('sql_quiz_token', 'some-token') // token present
      window.fetch = makeFetchStub(makeResponse(401))

      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/login')

      expect(logoutSpy).not.toHaveBeenCalled()
      expect(routerPushSpy).not.toHaveBeenCalled()
    })

    it('returns the 401 response object untouched for /api/v1/login', async () => {
      localStorage.setItem('sql_quiz_token', 'some-token')
      const res = makeResponse(401)
      window.fetch = makeFetchStub(res)

      session.installAuthFetchInterceptor()
      const returned = await window.fetch('/api/v1/login')

      expect(returned).toBe(res)
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 3. 401 with NO token in localStorage → does NOT trigger forceLogout
  // ─────────────────────────────────────────────────────────────────────────

  describe('interceptor: 401 with no token in localStorage', () => {
    it('does NOT call logout or router.push when localStorage has no token', async () => {
      // localStorage is already cleared in beforeEach
      window.fetch = makeFetchStub(makeResponse(401))

      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/some-resource')

      expect(logoutSpy).not.toHaveBeenCalled()
      expect(routerPushSpy).not.toHaveBeenCalled()
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 4. Non-401 (200) response → passes through, no forceLogout
  // ─────────────────────────────────────────────────────────────────────────

  describe('interceptor: non-401 response pass-through', () => {
    it('does NOT call logout or router.push for a 200 response', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      window.fetch = makeFetchStub(makeResponse(200))

      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/protected')

      expect(logoutSpy).not.toHaveBeenCalled()
      expect(routerPushSpy).not.toHaveBeenCalled()
    })

    it('returns the original 200 response object (status preserved)', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      const res200 = makeResponse(200)
      window.fetch = makeFetchStub(res200)

      session.installAuthFetchInterceptor()
      const returned = await window.fetch('/api/v1/protected')

      expect(returned).toBe(res200)
      expect(returned.status).toBe(200)
    })

    it('does NOT call logout or router.push for a 403 response', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      window.fetch = makeFetchStub(makeResponse(403))

      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/protected')

      expect(logoutSpy).not.toHaveBeenCalled()
      expect(routerPushSpy).not.toHaveBeenCalled()
    })

    it('does NOT call logout or router.push for a 500 response', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      window.fetch = makeFetchStub(makeResponse(500))

      session.installAuthFetchInterceptor()
      await window.fetch('/api/v1/protected')

      expect(logoutSpy).not.toHaveBeenCalled()
      expect(routerPushSpy).not.toHaveBeenCalled()
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 5. Response identity: interceptor returns the SAME response object (401)
  //    already covered in section 1 — extra cross-check here for clarity
  // ─────────────────────────────────────────────────────────────────────────

  // (covered by test "returns the SAME 401 response object" in section 1)

  // ─────────────────────────────────────────────────────────────────────────
  // 6. forceLogout does NOT double-push when already on the login route
  // ─────────────────────────────────────────────────────────────────────────

  describe('forceLogout: no double-push when already on /login', () => {
    it('does not call router.push when currentRoute.name is "login"', async () => {
      // Set the current route to 'login' on the mocked router
      const routerModule = await import('../../router')
      ;(routerModule.default as { currentRoute: { value: { name: string; fullPath: string } } })
        .currentRoute.value = { name: 'login', fullPath: '/login' }

      session.forceLogout()

      // logout and notify should still fire (those aren't gated on the route)
      expect(logoutSpy).toHaveBeenCalledOnce()
      expect(notifySpy).toHaveBeenCalledOnce()
      // But push must NOT be called
      expect(routerPushSpy).not.toHaveBeenCalled()
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 7. forceLogout: handling guard — re-entrant calls are dropped
  // ─────────────────────────────────────────────────────────────────────────

  describe('forceLogout: handling guard prevents re-entrant logout', () => {
    it('only logs out once even if forceLogout is called while handling is in flight', async () => {
      // Make router.push return a NEVER-resolving promise so `handling` stays true
      routerPushSpy.mockReturnValue(new Promise(() => {}))

      session.forceLogout() // sets handling = true, push never resolves
      session.forceLogout() // should be a no-op

      expect(logoutSpy).toHaveBeenCalledTimes(1)
      expect(notifySpy).toHaveBeenCalledTimes(1)
      expect(routerPushSpy).toHaveBeenCalledTimes(1)
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 8. Interceptor correctly resolves URL for Request object input
  // ─────────────────────────────────────────────────────────────────────────

  describe('interceptor: URL extraction from Request object', () => {
    it('triggers forceLogout when input is a Request object with a non-login URL', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      const requestObj = new Request('/api/v1/protected')
      const res401 = makeResponse(401)
      window.fetch = makeFetchStub(res401)

      session.installAuthFetchInterceptor()
      await window.fetch(requestObj)

      await vi.waitFor(() => expect(logoutSpy).toHaveBeenCalledOnce())
      expect(routerPushSpy).toHaveBeenCalledOnce()
    })

    it('does NOT trigger forceLogout when input is a Request object for /api/v1/login', async () => {
      localStorage.setItem('sql_quiz_token', 'valid-jwt')
      const requestObj = new Request('/api/v1/login')
      window.fetch = makeFetchStub(makeResponse(401))

      session.installAuthFetchInterceptor()
      await window.fetch(requestObj)

      expect(logoutSpy).not.toHaveBeenCalled()
    })
  })

  // ─────────────────────────────────────────────────────────────────────────
  // 9. forceLogout: default reason string
  // ─────────────────────────────────────────────────────────────────────────

  describe('forceLogout: default reason message', () => {
    it('notifies with the default expiry message when no reason is supplied', () => {
      session.forceLogout()

      expect(notifySpy).toHaveBeenCalledWith(
        'Your session has expired. Please sign in again.',
        'error'
      )
    })

    it('notifies with the custom reason when one is supplied', () => {
      session.forceLogout('Token revoked by admin.')

      expect(notifySpy).toHaveBeenCalledWith('Token revoked by admin.', 'error')
    })
  })
})
