<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useConfirm } from '../services/confirm_service'
import { useKeyboardShortcut } from '../composables/useKeyboardShortcut'
import { useFocusTrap } from '../composables/useFocusTrap'

const { state, _resolveCurrent } = useConfirm()
const confirmBtnRef = ref<HTMLButtonElement | null>(null)
const dialogRef = ref<HTMLElement | null>(null)
const open = computed(() => state.current !== null)
const active = computed(() => !!state.current)

function onCancel() { _resolveCurrent(false) }
function onConfirm() { _resolveCurrent(true) }

useKeyboardShortcut([
  { combo: 'Escape', whenTyping: true, handler: () => { if (!open.value) return; onCancel() } },
  { combo: 'Enter', whenTyping: true, handler: () => { if (!open.value) return; onConfirm() } },
])

useFocusTrap(dialogRef, active)

watch(open, async (v) => {
  if (v) {
    await nextTick()
    confirmBtnRef.value?.focus()
  }
})
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open && state.current"
      class="confirm-backdrop"
      @click.self="onCancel"
      role="presentation"
    >
      <div
        ref="dialogRef"
        class="confirm-dialog"
        role="alertdialog"
        aria-modal="true"
        :aria-labelledby="'confirm-title-' + state.current.id"
      >
        <h3 :id="'confirm-title-' + state.current.id" class="confirm-title">
          {{ state.current.title }}
        </h3>
        <p v-if="state.current.body" class="confirm-body">{{ state.current.body }}</p>
        <div class="confirm-actions">
          <button type="button" class="btn" @click="onCancel">
            {{ state.current.cancelLabel || 'Cancel' }}
          </button>
          <button
            type="button"
            class="btn"
            :class="state.current.danger ? 'btn-danger' : 'btn-primary'"
            ref="confirmBtnRef"
            @click="onConfirm"
          >
            {{ state.current.confirmLabel || 'Confirm' }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.confirm-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10000;
  padding: var(--space-4);
}
.confirm-dialog {
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  padding: var(--space-5);
  max-width: 480px;
  width: 100%;
}
.confirm-title {
  margin: 0 0 var(--space-3) 0;
  font-weight: 800;
  font-size: 1.1rem;
}
.confirm-body {
  margin: 0 0 var(--space-4) 0;
  color: var(--color-text);
  font-family: var(--font-mono);
  font-size: 0.9rem;
  white-space: pre-wrap;
}
.confirm-actions {
  display: flex;
  gap: var(--space-2);
  justify-content: flex-end;
}
.btn-danger {
  background: var(--color-danger);
  color: var(--color-surface);
  border-color: var(--color-danger);
}
</style>
