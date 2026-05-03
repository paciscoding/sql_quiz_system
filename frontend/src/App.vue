<script setup lang="ts">
import { ref, onMounted } from 'vue'
import StudentView from './components/StudentView.vue'
import LecturerView from './components/LecturerView.vue'
import LoginView from './components/LoginView.vue'
import AdminView from './components/AdminView.vue'

const isLoggedIn = ref(false)
const user = ref<any>(null)
const currentView = ref('student')

const checkAuth = () => {
  const token = localStorage.getItem('sql_quiz_token')
  const userData = localStorage.getItem('sql_quiz_user')
  if (token && userData) {
    isLoggedIn.value = true
    user.value = JSON.parse(userData)
    // Set default view based on role
    if (user.value.role === 'ADMIN') currentView.value = 'admin'
    else if (user.value.role === 'LECTURER') currentView.value = 'lecturer'
    else currentView.value = 'student'
  }
}

const handleLoginSuccess = () => {
  checkAuth()
}

const logout = () => {
  localStorage.removeItem('sql_quiz_token')
  localStorage.removeItem('sql_quiz_user')
  isLoggedIn.value = false
  user.value = null
}

onMounted(() => {
  checkAuth()
})
</script>

<template>
  <div class="app-container">
    <nav class="top-nav">
      <div class="logo">SQL Quiz System</div>
      <div class="nav-links" v-if="isLoggedIn">
        <span class="user-info">Logged in as: <strong>{{ user.username }}</strong> ({{ user.role }})</span>
        
        <button v-if="user.role === 'STUDENT'" :class="{ active: currentView === 'student' }" @click="currentView = 'student'">
          My Exam
        </button>
        
        <button v-if="['ADMIN', 'LECTURER'].includes(user.role)" :class="{ active: currentView === 'lecturer' }" @click="currentView = 'lecturer'">
          Questions
        </button>

        <button v-if="['ADMIN', 'LECTURER'].includes(user.role)" :class="{ active: currentView === 'admin' }" @click="currentView = 'admin'">
          Manage Accounts
        </button>

        <button @click="logout" class="logout-btn">Logout</button>
      </div>
    </nav>

    <main class="content">
      <LoginView v-if="!isLoggedIn" @login-success="handleLoginSuccess" />
      <template v-else>
        <StudentView v-if="currentView === 'student'" />
        <LecturerView v-if="currentView === 'lecturer'" />
        <AdminView v-if="currentView === 'admin'" />
      </template>
    </main>
  </div>
</template>

<style>
body { margin: 0; padding: 0; background-color: #f5f7fa; color: #2d3748; font-family: sans-serif; }
.app-container { min-height: 100vh; }
.top-nav {
  display: flex; justify-content: space-between; align-items: center;
  padding: 0.5rem 2rem; background-color: #2d3748; color: white;
}
.nav-links { display: flex; align-items: center; gap: 1rem; }
.nav-links button {
  padding: 0.5rem 1rem; background: transparent; border: none;
  color: #a0aec0; cursor: pointer; border-radius: 4px; font-weight: 500;
}
.nav-links button.active, .nav-links button:hover { color: white; background-color: #4a5568; }
.logout-btn { color: #feb2b2 !important; }
.user-info { font-size: 0.9rem; margin-right: 1rem; color: #cbd5e0; }
.content { max-width: 900px; margin: 2rem auto; padding: 0 1rem; }
</style>
