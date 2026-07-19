/**
 * Router ADMIN wildcard tests — TODO-061 (ARCH-020)
 *
 * These tests extend the existing router.test.ts coverage with the new
 * ADMIN wildcard behavior introduced by TODO-061:
 *   - ADMIN can access any route unless meta.adminBlocked=true.
 *   - /practice has adminBlocked:true to prevent analytics pollution.
 *   - LECTURER and STUDENT roles are unaffected.
 */
import { beforeEach, describe, expect, it } from 'vitest'
import router from '../router'
import { useAuth, type AuthUser } from '../services/auth'

const START_PATH = '/__router-admin-wildcard-test__'

function loginAs(role: AuthUser['role']) {
  useAuth().login(`${role.toLowerCase()}-token`, {
    id: '99',
    username: role.toLowerCase(),
    role,
  })
}

describe('router ADMIN wildcard (TODO-061)', () => {
  beforeEach(async () => {
    useAuth().logout()
    localStorage.clear()
    await router.push('/')
    await router.push(START_PATH)
  })

  it('logged-in ADMIN navigating to /lecturer is allowed', async () => {
    loginAs('ADMIN')
    await router.push('/lecturer')
    expect(router.currentRoute.value.name).toBe('lecturer')
  })

  it('logged-in ADMIN navigating to /admin is allowed', async () => {
    loginAs('ADMIN')
    await router.push('/admin')
    expect(router.currentRoute.value.name).toBe('admin')
  })

  it('logged-in ADMIN navigating to /student is allowed (NEW wildcard behavior)', async () => {
    loginAs('ADMIN')
    await router.push('/student')
    expect(router.currentRoute.value.name).toBe('student')
  })

  it('logged-in ADMIN navigating to /student/results is allowed (read-only, adminBlocked=false)', async () => {
    loginAs('ADMIN')
    await router.push('/student/results')
    expect(router.currentRoute.value.name).toBe('student-results')
  })

  it('logged-in ADMIN navigating to /lecturer/practice is allowed', async () => {
    loginAs('ADMIN')
    await router.push('/lecturer/practice')
    expect(router.currentRoute.value.name).toBe('lecturer-practice')
  })

  it('logged-in ADMIN navigating to /practice is redirected to /403 (adminBlocked=true)', async () => {
    loginAs('ADMIN')
    await router.push('/practice')
    expect(router.currentRoute.value.path).toBe('/403')
    expect(router.currentRoute.value.name).toBe('forbidden')
  })

  it('logged-in LECTURER navigating to /admin is forbidden (wildcard does not apply to LECTURER)', async () => {
    loginAs('LECTURER')
    await router.push('/admin')
    expect(router.currentRoute.value.path).toBe('/403')
  })

  it('logged-in LECTURER navigating to /student is forbidden', async () => {
    loginAs('LECTURER')
    await router.push('/student')
    expect(router.currentRoute.value.path).toBe('/403')
  })

  it('logged-in STUDENT navigating to /lecturer is forbidden', async () => {
    loginAs('STUDENT')
    await router.push('/lecturer')
    expect(router.currentRoute.value.path).toBe('/403')
  })

  it('logged-in STUDENT navigating to /admin is forbidden', async () => {
    loginAs('STUDENT')
    await router.push('/admin')
    expect(router.currentRoute.value.path).toBe('/403')
  })
})
