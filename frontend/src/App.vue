<script setup lang="ts">
import type { ComputedRef } from 'vue'
import { ref, watch, onMounted, onBeforeUnmount, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import NotificationToast from './components/NotificationToast.vue'
import ConfirmDialogHost from './components/ConfirmDialogHost.vue'
import ClockSidebar from './components/ClockSidebar.vue'
import { useAuth, type AuthUser } from './services/auth'
import { UI_COPY } from './constants/ui_copy'
import { useKeyboardShortcut } from './composables/useKeyboardShortcut'
import { useFocusTrap } from './composables/useFocusTrap'

type AuthNavState = {
  user: ComputedRef<AuthUser>
  isLoggedIn: ComputedRef<boolean>
  logout: () => void
}

const router = useRouter()
const route = useRoute()
const { user, isLoggedIn, logout: doLogout } = useAuth() as AuthNavState

const logout = () => {
  doLogout()
  router.push('/login')
}

const navOpen = ref(false)
const navRef = ref<HTMLElement | null>(null)

const isMobile = ref(window.matchMedia('(max-width: 1023.98px)').matches)
let mqCleanup: (() => void) | null = null

watch(() => route.fullPath, () => { navOpen.value = false })

onMounted(() => {
  const resizeObserver = new ResizeObserver(() => {
    if (document.body.clientWidth >= 1024) navOpen.value = false
  })
  resizeObserver.observe(document.body)

  const mq = window.matchMedia('(max-width: 1023.98px)')
  const mqHandler = (e: MediaQueryListEvent) => { isMobile.value = e.matches }
  mq.addEventListener('change', mqHandler)
  mqCleanup = () => mq.removeEventListener('change', mqHandler)
})

onBeforeUnmount(() => { mqCleanup?.() })

useKeyboardShortcut({
  combo: 'Escape',
  handler: () => { navOpen.value = false },
})

const focusTrapActive = computed(() => navOpen.value && isMobile.value)
useFocusTrap(navRef, focusTrapActive)

const isLoginRoute = computed(() => route.path === '/login' || route.name === 'login')

// Sidebar visibility: hide entirely on the login page or when no user is signed in
const showSidebar = computed(() => isLoggedIn.value && !isLoginRoute.value)

// Locale dropdown (cosmetic — UI is currently single-locale).
const locale = ref('en-US')
const locales = [
  { code: 'en-US', label: 'English (US)' },
  { code: 'th-TH', label: 'ภาษาไทย' },
]
</script>

<template>
  <div class="cms-shell">
    <NotificationToast />
    <ConfirmDialogHost />

    <!-- Persistent top header -->
    <header class="cms-header">
      <div class="cms-brand" @click="router.push('/')" role="link" tabindex="0">
        <span class="brand-mark">SQ</span>
        <span>SQL Quiz</span>
        <span class="brand-sub">Grader Portal</span>
      </div>

      <button
        v-if="showSidebar"
        class="nav-hamburger"
        @click="navOpen = !navOpen"
        :aria-expanded="navOpen"
        aria-label="Toggle navigation"
        aria-controls="main-nav-links"
      >
        <span class="hbar" /><span class="hbar" /><span class="hbar" />
      </button>

      <div v-if="isLoggedIn" class="cms-header-right">
        <select v-model="locale" class="locale-select" aria-label="Language">
          <option v-for="l in locales" :key="l.code" :value="l.code">{{ l.label }}</option>
        </select>
        <span class="header-greeting">
          Logged in as <strong>{{ user.username }}</strong>
        </span>
        <button class="btn btn-sm logout-btn" @click="logout">{{ UI_COPY.LOGOUT }}</button>
      </div>
    </header>

    <!-- Two-column body -->
    <div class="cms-body">
      <aside
        v-if="showSidebar"
        ref="navRef"
        id="main-nav-links"
        class="cms-sidebar"
        :class="{ 'is-open': navOpen }"
      >
        <ClockSidebar />

        <nav class="sidebar-nav" aria-label="Primary">
          <div class="nav-section">General</div>

          <router-link to="/student" v-if="user.role === 'STUDENT'" custom v-slot="{ navigate, isActive }">
            <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
              <span>My Examination</span>
            </button>
          </router-link>
          <router-link to="/practice" v-if="user.role === 'STUDENT'" custom v-slot="{ navigate, isActive }">
            <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
              <span>Practice</span>
            </button>
          </router-link>
          <router-link to="/student/results" v-if="user.role === 'STUDENT'" custom v-slot="{ navigate, isActive }">
            <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
              <span>Submissions</span>
            </button>
          </router-link>

          <template v-if="['ADMIN','LECTURER'].includes(user.role)">
            <div class="nav-section">Authoring</div>
            <router-link to="/lecturer" custom v-slot="{ navigate, isActive }">
              <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
                <span>Examinations</span>
              </button>
            </router-link>
            <router-link to="/lecturer/practice" custom v-slot="{ navigate, isActive }">
              <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
                <span>Practice Authoring</span>
              </button>
            </router-link>
          </template>

          <template v-if="user.role === 'ADMIN'">
            <div class="nav-section">Administration</div>
            <router-link to="/admin" custom v-slot="{ navigate, isActive }">
              <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
                <span>Accounts</span>
              </button>
            </router-link>
          </template>
        </nav>
      </aside>

      <main id="content" class="cms-main" :class="{ 'is-bare': isLoginRoute }" role="main">
        <router-view />
      </main>
    </div>
  </div>
</template>

<style scoped>
/* Mobile drawer behavior for the sidebar */
@media (max-width: 1023.98px) {
  .cms-sidebar {
    display: none;
  }
  .cms-sidebar.is-open {
    display: flex;
    position: fixed;
    top: var(--layout-header-h);
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 40;
    background: var(--color-bg);
    padding: var(--space-4);
    overflow-y: auto;
  }
  .nav-hamburger { display: inline-flex; }
}
@media (min-width: 1024px) {
  .nav-hamburger { display: none; }
}
</style>
