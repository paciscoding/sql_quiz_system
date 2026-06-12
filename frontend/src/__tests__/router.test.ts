import { beforeEach, describe, expect, it } from 'vitest'
import router from '../router'
import { useAuth, type AuthUser } from '../services/auth'

const START_PATH = '/__router-test-start__'

function loginAs(role: AuthUser['role']) {
  useAuth().login(`${role.toLowerCase()}-token`, {
    id: 1,
    username: role.toLowerCase(),
    role,
  })
}

describe('router beforeEach guard', () => {
  beforeEach(async () => {
    useAuth().logout()
    localStorage.clear()
    await router.push('/')
    await router.push(START_PATH)
  })

  it('unauthenticated user navigating to /student is redirected to /login with redirect param', async () => {
    await router.push('/student')

    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe('/student')
  })

  it('unauthenticated user navigating to /login is allowed through', async () => {
    await router.push('/login')

    expect(router.currentRoute.value.name).toBe('login')
  })

  it('unauthenticated user navigating to /403 is allowed through (Rule 1 short-circuits at !isLoggedIn)', async () => {
    await router.push('/403')

    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('unauthenticated user navigating to /nonexistent-bogus-url reaches not-found', async () => {
    await router.push('/nonexistent-bogus-url')

    expect(router.currentRoute.value.name).toBe('not-found')
  })

  it('logged-in STUDENT navigating to /student is allowed', async () => {
    loginAs('STUDENT')

    await router.push('/student')

    expect(router.currentRoute.value.name).toBe('student')
  })

  it('logged-in STUDENT navigating to /admin is redirected to /403 (Rule 3 + Rule 1 exception lets forbidden through)', async () => {
    loginAs('STUDENT')

    await router.push('/admin')

    expect(router.currentRoute.value.name).toBe('forbidden')
    expect(router.currentRoute.value.path).toBe('/403')
  })

  it('logged-in user navigating to /nonexistent-bogus-url reaches not-found (TODO-033 fix)', async () => {
    loginAs('STUDENT')

    await router.push('/nonexistent-bogus-url')

    expect(router.currentRoute.value.name).toBe('not-found')
  })

  it('logged-in user navigating to /login without force=1 is bounced to /dashboard', async () => {
    loginAs('STUDENT')

    await router.push('/login')

    expect(router.currentRoute.value.path).toBe('/student')
  })

  it('logged-in user navigating to /login?force=1 is allowed through', async () => {
    loginAs('STUDENT')

    await router.push('/login?force=1')

    expect(router.currentRoute.value.name).toBe('login')
  })

  it('logged-in LECTURER navigating to /dashboard is redirected to /lecturer', async () => {
    loginAs('LECTURER')

    await router.push('/dashboard')

    expect(router.currentRoute.value.path).toBe('/lecturer')
  })
})
