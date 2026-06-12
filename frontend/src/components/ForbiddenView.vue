<script setup lang="ts">
import { computed } from 'vue'
import { useAuth } from '../services/auth'

const { user, isLoggedIn } = useAuth()

const homeTarget = computed(() => {
  const u = user.value
  if (!isLoggedIn.value || !u) return '/login'
  if (u.role === 'STUDENT') return '/student'
  return '/lecturer'
})
</script>

<template>
  <div class="forbidden-shell">
    <div class="card forbidden-card">
      <div class="error-mark">403</div>
      <h2>Forbidden</h2>
      <p class="error-desc">You do not have permission to access this page.</p>
      <router-link :to="homeTarget" class="btn btn-primary return-link">
        Return to home
      </router-link>
    </div>
  </div>
</template>

<style scoped>
.forbidden-shell {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: calc(100vh - 80px);
  padding: var(--space-4);
  background: var(--color-surface);
}

.forbidden-card {
  width: 100%;
  max-width: 400px;
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

.forbidden-card h2 {
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
</style>
