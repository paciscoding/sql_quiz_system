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
let resizeObserver: ResizeObserver | null = null

watch(() => route.fullPath, () => { navOpen.value = false })

onMounted(() => {
  resizeObserver = new ResizeObserver(() => {
    if (document.body.clientWidth >= 1024) navOpen.value = false
  })
  resizeObserver.observe(document.body)

  const mq = window.matchMedia('(max-width: 1023.98px)')
  const mqHandler = (e: MediaQueryListEvent) => { isMobile.value = e.matches }
  mq.addEventListener('change', mqHandler)
  mqCleanup = () => mq.removeEventListener('change', mqHandler)
})

onBeforeUnmount(() => {
  mqCleanup?.()
  resizeObserver?.disconnect()
  resizeObserver = null
})

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
    <a href="#content" class="skip-link">Skip to main content</a>
    <NotificationToast />
    <ConfirmDialogHost />

    <!-- Persistent top header -->
    <header class="cms-header">
      <div
        class="cms-brand"
        @click="router.push('/')"
        @keydown.enter="router.push('/')"
        @keydown.space.prevent="router.push('/')"
        role="link"
        tabindex="0"
      >
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

      <nav
        v-if="showSidebar"
        ref="navRef"
        id="main-nav-links"
        class="cms-nav"
        :class="{ 'is-open': navOpen }"
        aria-label="Primary"
      >
        <div class="nav-group">
          <div class="nav-group-label">General</div>

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
        </div>

        <div class="nav-group" v-if="['ADMIN','LECTURER'].includes(user.role)">
          <div class="nav-group-label">Authoring</div>
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
          <router-link to="/lecturer/results" custom v-slot="{ navigate, isActive }">
            <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
              <span>Results</span>
            </button>
          </router-link>
          <router-link to="/lecturer/submission-log" custom v-slot="{ navigate, isActive }">
            <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
              <span>Submission Log</span>
            </button>
          </router-link>
        </div>

        <div class="nav-group" v-if="user.role === 'ADMIN'">
          <div class="nav-group-label">Administration</div>
          <router-link to="/admin" custom v-slot="{ navigate, isActive }">
            <button class="nav-item" :class="{ 'is-active': isActive }" @click="navigate">
              <span>Accounts</span>
            </button>
          </router-link>
        </div>
      </nav>

      <div v-if="isLoggedIn" class="cms-header-right">
        <ClockSidebar class="header-clock" />
        <select v-model="locale" class="locale-select" aria-label="Language" disabled title="Additional languages coming soon">
          <option v-for="l in locales" :key="l.code" :value="l.code">{{ l.label }}</option>
        </select>
        <span class="header-greeting">
          Logged in as <strong>{{ user.username }}</strong>
        </span>
        <button class="btn btn-sm logout-btn" @click="logout">{{ UI_COPY.LOGOUT }}</button>
      </div>
    </header>

    <div class="cms-body">
      <main id="content" class="cms-main" :class="{ 'is-bare': isLoginRoute }" role="main">
        <router-view />
      </main>
    </div>
  </div>
</template>

<style scoped>
/* Keyboard skip-link: hidden until focused */
.skip-link {
  position: absolute;
  left: 8px;
  top: -48px;
  z-index: 100;
  padding: 8px 14px;
  background: var(--color-primary);
  color: #fff;
  border-radius: var(--radius);
  transition: top .15s ease;
}
.skip-link:focus { top: 8px; }

/* Mobile dropdown behavior for the nav (anchored under the header, not a
   full-screen drawer — see .cms-nav rules in style.css for the panel itself) */
@media (max-width: 1023.98px) {
  .nav-hamburger { display: inline-flex; }
}
@media (min-width: 1024px) {
  .nav-hamburger { display: none; }
}
</style>
