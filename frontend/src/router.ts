import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import LoginView from './components/LoginView.vue'
import StudentView from './components/StudentView.vue'
import LecturerView from './components/LecturerView.vue'
import AdminView from './components/AdminView.vue'
import { useAuth } from './services/auth'

declare module 'vue-router' {
  interface RouteMeta {
    requiresAuth: boolean
    allowedRoles?: Array<'ADMIN' | 'LECTURER' | 'STUDENT'>
    /**
     * Block ADMIN wildcard access.
     * Default false. Use only for role-purist screens.
     */
    adminBlocked?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: LoginView,
    meta: { requiresAuth: false }
  },
  {
    path: '/',
    redirect: '/dashboard'
  },
  {
    path: '/student',
    name: 'student',
    component: StudentView,
    meta: { requiresAuth: true, allowedRoles: ['STUDENT'] }
  },
  {
    path: '/lecturer',
    name: 'lecturer',
    component: LecturerView,
    meta: { requiresAuth: true, allowedRoles: ['LECTURER'] }
  },
  {
    path: '/admin',
    name: 'admin',
    component: AdminView,
    meta: { requiresAuth: true, allowedRoles: ['ADMIN'] }
  },
  {
    path: '/practice',
    name: 'practice',
    component: () => import('./components/PracticeView.vue'),
    meta: { requiresAuth: true, allowedRoles: ['STUDENT'], adminBlocked: true }
  },
  {
    path: '/student/results',
    name: 'student-results',
    component: () => import('./components/StudentResults.vue'),
    meta: { requiresAuth: true, allowedRoles: ['STUDENT'] }
  },
  {
    path: '/lecturer/practice',
    name: 'lecturer-practice',
    component: () => import('./components/PracticeAuthoring.vue'),
    meta: { requiresAuth: true, allowedRoles: ['LECTURER'] }
  },
  {
    path: '/dashboard',
    name: 'dashboard',
    redirect: () => {
      const u = useAuth().user.value
      if (u?.role === 'ADMIN') return '/admin'
      if (u?.role === 'LECTURER') return '/lecturer'
      if (u?.role === 'STUDENT') return '/student'
      return '/login'
    }
  },
  {
    path: '/403',
    name: 'forbidden',
    component: () => import('./components/ForbiddenView.vue'),
    meta: { requiresAuth: false }
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('./components/NotFoundView.vue'),
    meta: { requiresAuth: false }
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

/**
 * Router guard rules:
 * 1. Logged-in users leave public pages for the dashboard unless force=1 or error pages.
 * 2. Anonymous users entering protected pages are sent to login with a redirect target.
 * 3. Protected role checks use route allowedRoles when present.
 * 4. ADMIN has wildcard access unless a route sets adminBlocked.
 */
router.beforeEach((to, _from, next) => {
  const { isLoggedIn, user } = useAuth()

  // Rule 1: public route AND logged in (without force=1) -> redirect to dashboard
  if (!to.meta.requiresAuth && isLoggedIn.value) {
    if (to.query.force === '1' || to.name === 'forbidden' || to.name === 'not-found') {
      next()
      return
    }
    next({ path: '/dashboard' })
    return
  }

  // Rule 2: protected route AND not logged in -> redirect to login with redirect param
  if (to.meta.requiresAuth && !isLoggedIn.value) {
    next({ path: '/login', query: { redirect: to.fullPath } })
    return
  }

  // Rule 3: role check
  if (to.meta.allowedRoles && user.value) {
    const role = user.value.role
    // ADMIN wildcard: allowed everywhere unless the route explicitly opts out.
    if (role === 'ADMIN' && !to.meta.adminBlocked) {
      next()
      return
    }
    if (!to.meta.allowedRoles.includes(role)) {
      next({ name: 'forbidden' })
      return
    }
  }

  next()
})

export default router
