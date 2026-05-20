<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import NotificationToast from './components/NotificationToast.vue'

const router = useRouter()
const route = useRoute()

const isLoggedIn = ref(false)
const user = ref<any>(null)

const checkAuth = () => {
  const token = localStorage.getItem('sql_quiz_token')
  const userData = localStorage.getItem('sql_quiz_user')
  if (token && userData) {
    isLoggedIn.value = true
    user.value = JSON.parse(userData)
  } else {
    isLoggedIn.value = false
    user.value = null
  }
}

const logout = () => {
  localStorage.removeItem('sql_quiz_token')
  localStorage.removeItem('sql_quiz_user')
  checkAuth()
  router.push('/login')
}

// Watch for route changes to re-check auth status
watch(() => route.path, () => {
  checkAuth()
})

onMounted(() => {
  checkAuth()
})
</script>

<template>
  <div class="app-container">
    <NotificationToast />
    <nav class="top-nav">
      <div class="logo" @click="router.push('/')" style="cursor: pointer;">
        <span class="logo-mark">{ }</span>
        SQL Quiz System
      </div>
      <div class="nav-links" v-if="isLoggedIn && user">
        <span class="user-info">Logged in as: <strong>{{ user.username }}</strong></span>
        
        <router-link to="/student" v-if="user.role === 'STUDENT'" custom v-slot="{ navigate, isActive }">
          <button :class="{ active: isActive }" @click="navigate">My Exam</button>
        </router-link>

        <router-link to="/practice" v-if="user.role === 'STUDENT'" custom v-slot="{ navigate, isActive }">
          <button :class="{ active: isActive }" @click="navigate">Practice</button>
        </router-link>

        <router-link to="/student/results" v-if="user.role === 'STUDENT'" custom v-slot="{ navigate, isActive }">
          <button :class="{ active: isActive }" @click="navigate">Results</button>
        </router-link>
        
        <router-link to="/lecturer" v-if="['ADMIN', 'LECTURER'].includes(user.role)" custom v-slot="{ navigate, isActive }">
          <button :class="{ active: isActive }" @click="navigate">Questions</button>
        </router-link>

        <router-link to="/admin" v-if="['ADMIN', 'LECTURER'].includes(user.role)" custom v-slot="{ navigate, isActive }">
          <button :class="{ active: isActive }" @click="navigate">Accounts</button>
        </router-link>

        <router-link to="/lecturer/practice" v-if="['ADMIN', 'LECTURER'].includes(user.role)" custom v-slot="{ navigate, isActive }">
          <button :class="{ active: isActive }" @click="navigate">Practice Authoring</button>
        </router-link>

        <button @click="logout" class="logout-btn">Logout</button>
      </div>
    </nav>

    <main class="content">
      <router-view />
    </main>
  </div>
</template>

<style>
.app-container { min-height: 100vh; background: var(--color-bg); }
.top-nav {
  display: flex; 
  justify-content: space-between; 
  align-items: center;
  padding: 0.8rem 2rem; 
  background: var(--color-surface); 
  color: var(--color-text);
  border-bottom: 2px solid var(--color-border);
}
.nav-links { display: flex; align-items: center; gap: 0.5rem; }
.nav-links button {
  padding: 0.5rem 1rem; 
  background: transparent; 
  border: 1px solid transparent;
  color: var(--color-text); 
  cursor: pointer; 
  border-radius: 0; 
  font-weight: 700;
  font-size: 0.85rem;
}
.nav-links button.active { 
  background-color: var(--color-primary); 
  color: var(--color-surface);
  border-color: var(--color-border);
}
.nav-links button:hover:not(.active) { 
  background-color: var(--color-surface-muted);
}
.logout-btn { 
  background: var(--color-danger) !important; 
  color: var(--color-surface) !important;
  border: 1px solid var(--color-danger) !important;
  font-weight: 800 !important;
}
.logout-btn:hover {
  opacity: 0.9;
}
.user-info { 
  font-size: 0.85rem; 
  margin-right: 1rem; 
  color: var(--color-text-muted); 
}
.content { max-width: 1200px; margin: 3rem auto; padding: 0 1rem; }
.logo { 
  font-weight: 800; 
  font-size: 1.25rem; 
  letter-spacing: -0.01em; 
  color: var(--color-text);
}
.logo-mark { 
  border: 2px solid var(--color-border); 
  padding: 2px 6px; 
  font-family: var(--font-mono); 
  font-weight: 900; 
  margin-right: 8px; 
}
</style>
