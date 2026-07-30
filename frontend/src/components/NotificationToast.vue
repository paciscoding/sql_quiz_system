<script setup lang="ts">
import { useNotifications } from '../services/notification_service'

const { notifications, remove } = useNotifications()
</script>

<template>
  <div class="notification-container" aria-live="polite" role="status">
    <TransitionGroup name="toast">
      <div
        v-for="n in notifications"
        :key="n.id"
        class="toast"
        :class="n.type"
        :role="n.type === 'error' ? 'alert' : undefined"
        @click="remove(n.id)"
      >
        <div class="toast-content">{{ n.message }}</div>
        <button class="close-btn" aria-label="Dismiss notification" @click.stop="remove(n.id)">&times;</button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.notification-container {
  position: fixed;
  top: 20px;
  right: 20px;
  z-index: 9999;
  display: flex;
  flex-direction: column;
  gap: 12px;
  pointer-events: none;
}

.toast {
  min-width: 280px;
  max-width: 450px;
  padding: var(--space-3) var(--space-4);
  border: 2px solid var(--color-border);
  display: flex;
  justify-content: space-between;
  align-items: center;
  cursor: pointer;
  pointer-events: auto;
  color: var(--color-text);
  background: var(--color-surface);
  transition: all 0.2s ease;
}

.toast.success { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.toast.error { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
.toast.info { background: var(--color-primary); color: var(--color-surface); }

.toast-content {
  font-weight: 700;
  font-size: 0.95rem;
}

.close-btn {
  background: transparent;
  border: none;
  color: inherit;
  font-size: 1.2rem;
  margin-left: 1rem;
  cursor: pointer;
  width: 20px;
  height: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 400;
}

.toast-enter-active, .toast-leave-active {
  transition: all 0.3s ease;
}
.toast-enter-from {
  opacity: 0;
  transform: translateY(-10px);
}
.toast-leave-to {
  opacity: 0;
  transform: translateX(20px);
}
</style>
