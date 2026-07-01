import { useAuth } from './auth'
import { useNotifications } from './notification_service'
import router from '../router'

let handling = false

export function forceLogout(reason?: string): void {
  if (handling) return
  handling = true

  useAuth().logout()
  useNotifications().notify(
    reason ?? 'Your session has expired. Please sign in again.',
    'error'
  )

  if (router.currentRoute.value.name !== 'login') {
    router
      .push({ path: '/login', query: { redirect: router.currentRoute.value.fullPath } })
      .finally(() => {
        handling = false
      })
  } else {
    handling = false
  }
}

export function installAuthFetchInterceptor(): void {
  const originalFetch = window.fetch.bind(window)

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const res = await originalFetch(input, init)

    if (res.status === 401) {
      let url: string
      if (typeof input === 'string') {
        url = input
      } else if (input instanceof URL) {
        url = input.href
      } else {
        url = (input as Request).url
      }

      if (localStorage.getItem('sql_quiz_token') && !url.includes('/api/v1/login')) {
        forceLogout()
      }
    }

    return res
  }
}
