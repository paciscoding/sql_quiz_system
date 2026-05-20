import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import LoginView from './components/LoginView.vue'
import StudentView from './components/StudentView.vue'
import LecturerView from './components/LecturerView.vue'
import AdminView from './components/AdminView.vue'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: LoginView,
    meta: { public: true }
  },
  {
    path: '/',
    redirect: '/dashboard'
  },
  {
    path: '/student',
    name: 'student',
    component: StudentView,
    meta: { role: 'STUDENT' }
  },
  {
    path: '/lecturer',
    name: 'lecturer',
    component: LecturerView,
    meta: { role: 'LECTURER' }
  },
  {
    path: '/admin',
    name: 'admin',
    component: AdminView,
    meta: { role: 'ADMIN' }
  },
  {
    path: '/practice',
    name: 'practice',
    component: () => import('./components/PracticeView.vue'),
    meta: { role: 'STUDENT' }
  },
  {
    path: '/student/results',
    name: 'student-results',
    component: () => import('./components/StudentResults.vue'),
    meta: { role: 'STUDENT' }
  },
  {
    path: '/lecturer/practice',
    name: 'lecturer-practice',
    component: () => import('./components/PracticeAuthoring.vue'),
    meta: { role: 'LECTURER' }
  },
  {
    path: '/dashboard',
    name: 'dashboard',
    component: () => import('./App.vue'), // This will be handled by navigation guard
    beforeEnter: (_to, _from, next) => {
      const user = JSON.parse(localStorage.getItem('sql_quiz_user') || '{}')
      if (user.role === 'ADMIN') next('/admin')
      else if (user.role === 'LECTURER') next('/lecturer')
      else if (user.role === 'STUDENT') next('/student')
      else next('/login')
    }
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach((to, _from, next) => {
  const token = localStorage.getItem('sql_quiz_token')
  const user = JSON.parse(localStorage.getItem('sql_quiz_user') || '{}')

  if (!to.meta.public && !token) {
    next('/login')
  } else if (to.meta.role && to.meta.role !== user.role && user.role !== 'ADMIN') {
    // Lecturers can access admin-like stuff sometimes, but for now strict
    if (user.role === 'ADMIN') {
        next() // Admin can go anywhere
    } else {
        next('/dashboard')
    }
  } else {
    next()
  }
})

export default router
