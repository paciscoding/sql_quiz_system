<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuth } from '../services/auth'

const route = useRoute()
const router = useRouter()
const { user, isLoggedIn } = useAuth()

const homeTarget = computed(() => {
  const u = user.value
  if (!isLoggedIn.value || !u) return '/login'
  if (u.role === 'STUDENT') return '/student'
  return '/lecturer'
})

const showGoBack = computed(() => window.history.length > 1)
</script>

<template>
  <div class="notfound-shell">
    <div class="card notfound-card">
      <div class="error-mark">404</div>
      <h2>Page not found</h2>
      <p class="error-desc">
        {{ 'The path "' + route.fullPath + '" does not exist.' }}
      </p>
      <router-link :to="homeTarget" class="btn btn-primary return-link">
        Back to home
      </router-link>
      <button v-if="showGoBack" class="btn go-back-btn" @click="router.back()">
        Go back
      </button>
    </div>
  </div>
</template>

<style scoped>
.notfound-shell {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: calc(100vh - 80px);
  padding: var(--space-4);
  background: var(--color-surface);
}

.notfound-card {
  width: 100%;
  max-width: 440px;
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  padding: var(--space-6);
  text-align: center;
}

.error-mark {
  border: 2px solid var(--color-border);
  display: inline-block;
  padding: 4px 12px;
  font-family: var(--font-mono);
  font-weight: 900;
  font-size: 2rem;
  margin-bottom: var(--space-4);
}

.notfound-card h2 {
  color: var(--color-text);
  font-weight: 800;
  font-size: 1.6rem;
  margin-bottom: var(--space-3);
}

.error-desc {
  color: var(--color-text-muted);
  font-size: 0.9rem;
  margin-bottom: var(--space-4);
}

.return-link {
  display: inline-block;
  text-decoration: none;
  margin-top: var(--space-2);
}

.go-back-btn {
  display: block;
  margin: var(--space-3) auto 0;
  background: transparent;
  color: var(--color-text-muted);
  border: 1px solid var(--color-border);
  cursor: pointer;
}
</style>
