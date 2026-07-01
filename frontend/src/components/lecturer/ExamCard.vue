<script setup lang="ts">
import { ref } from 'vue'
import type { Examination } from './types'
import { useConfirm } from '../../services/confirm_service'
import { useNotifications } from '../../services/notification_service'
import { handleApiError } from '../../services/error_handler'
import { UI_COPY } from '../../constants/ui_copy'
import Icon from '../Icon.vue'

const props = defineProps<{ exam: Examination }>()

const emit = defineEmits<{
  open: []
  changed: []
  deleted: []
}>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

// ---------------------------------------------------------------------------
// Rename
// ---------------------------------------------------------------------------
const renamingMode = ref(false)
const renameDraft = ref('')
const renameSaving = ref(false)

function startRename() {
  renamingMode.value = true
  renameDraft.value = props.exam.title
}

function cancelRename() {
  renamingMode.value = false
  renameDraft.value = ''
}

async function saveRename() {
  const title = renameDraft.value.trim()
  if (!title) {
    notify('Exam title cannot be empty', 'error')
    return
  }
  renameSaving.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ title })
    })
    if (resp.ok) {
      notify('Exam renamed', 'success')
      renamingMode.value = false
      renameDraft.value = ''
      emit('changed')
    } else {
      const errData = await resp.json().catch(() => ({} as Record<string, string>))
      handleApiError(new Error(errData['error'] || 'Failed to rename exam'), { fallback: 'Failed to rename exam' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to rename exam' })
  } finally {
    renameSaving.value = false
  }
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------
const deleteLoading = ref(false)

async function deleteExam() {
  const ok = await confirm({
    title: 'Delete exam?',
    body: `This permanently removes "${props.exam.title}" and its questions, roster, and submissions. This cannot be undone.`,
    confirmLabel: 'Delete',
    danger: true
  })
  if (!ok) return
  const token = localStorage.getItem('sql_quiz_token')
  deleteLoading.value = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}`, {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      notify('Exam deleted', 'success')
      emit('deleted')
    } else {
      const errData = await resp.json().catch(() => ({} as Record<string, string>))
      handleApiError(new Error(errData['error'] || 'Failed to delete exam'), { fallback: 'Failed to delete exam' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to delete exam' })
  } finally {
    deleteLoading.value = false
  }
}

// ---------------------------------------------------------------------------
// Activation / deactivation
// Collapsed from per-exam keyed maps to plain local refs (one card = one exam).
// ---------------------------------------------------------------------------
const activationMode = ref<'instant' | 'scheduled'>('instant')
const scheduledStart = ref('')
const scheduledEnd = ref('')
const deactivateEndAt = ref('')
const actionLoading = ref(false)

function toISOString(localStr: string): string {
  if (!localStr) return ''
  return new Date(localStr).toISOString()
}

function effectiveStateBadgeClass(state: string): string {
  switch (state) {
    case 'active':    return 'success-btn'
    case 'scheduled': return 'badge-info'
    case 'ended':     return 'fail-btn'
    default:          return ''
  }
}

async function activateExam() {
  const token = localStorage.getItem('sql_quiz_token')
  let body: Record<string, unknown> = { active: true, mode: 'instant' }
  if (activationMode.value === 'scheduled') {
    if (!scheduledStart.value) { notify('Please set a start time', 'error'); return }
    body = {
      active: true,
      mode: 'scheduled',
      scheduled_start_at: toISOString(scheduledStart.value),
      ...(scheduledEnd.value ? { scheduled_end_at: toISOString(scheduledEnd.value) } : {})
    }
  }
  actionLoading.value = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/activate`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify(body)
    })
    if (resp.ok) {
      notify('Exam activated', 'success')
      emit('changed')
    } else {
      const e = await resp.json().catch(() => ({} as Record<string, string>))
      handleApiError(new Error(e['error'] || 'Failed to activate'), { fallback: 'Failed to activate exam' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to activate exam' })
  } finally {
    actionLoading.value = false
  }
}

async function deactivateExamInstant() {
  const ok = await confirm({
    title: 'Deactivate exam?',
    body: 'This will drop the exam sandbox immediately. Students cannot submit after this.',
    confirmLabel: 'Deactivate',
    danger: true
  })
  if (!ok) return
  const token = localStorage.getItem('sql_quiz_token')
  actionLoading.value = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/deactivate`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ mode: 'instant' })
    })
    if (resp.ok) {
      notify('Exam deactivated', 'success')
      emit('changed')
    } else {
      const e = await resp.json().catch(() => ({} as Record<string, string>))
      handleApiError(new Error(e['error'] || 'Failed to deactivate'), { fallback: 'Failed to deactivate exam' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to deactivate exam' })
  } finally {
    actionLoading.value = false
  }
}

async function deactivateExamScheduled() {
  if (!deactivateEndAt.value) { notify('Please set an end time', 'error'); return }
  const token = localStorage.getItem('sql_quiz_token')
  actionLoading.value = true
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/deactivate`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` },
      body: JSON.stringify({ mode: 'scheduled', scheduled_end_at: toISOString(deactivateEndAt.value) })
    })
    if (resp.ok) {
      notify('Exam end time set', 'success')
      emit('changed')
    } else {
      const e = await resp.json().catch(() => ({} as Record<string, string>))
      handleApiError(new Error(e['error'] || 'Failed to set end time'), { fallback: 'Failed to set end time' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to set end time' })
  } finally {
    actionLoading.value = false
  }
}
</script>

<template>
  <div class="exam-item exam-item-extended">
    <!-- Exam header: title (inline rename) + state badge + action buttons -->
    <div class="exam-item-header">
      <template v-if="renamingMode">
        <input
          v-model="renameDraft"
          class="exam-rename-input"
          @keydown.enter="saveRename"
          @keydown.escape="cancelRename"
          style="flex: 1; min-width: 0; margin-right: 0.5rem;"
        />
        <button class="btn-sm success-btn" :disabled="renameSaving" @click="saveRename" style="margin-right: 0.25rem;">
          {{ renameSaving ? 'Saving...' : UI_COPY.SAVE }}
        </button>
        <button class="btn-sm" @click="cancelRename">{{ UI_COPY.CANCEL }}</button>
      </template>
      <template v-else>
        <span style="flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">
          {{ exam.title }} (ID: {{ exam.id }})
        </span>
        <span
          class="badge"
          :class="effectiveStateBadgeClass(exam.effective_state)"
          style="font-size: 0.75rem; padding: 2px 8px; flex-shrink: 0; margin-left: 0.5rem;"
        >
          {{ exam.effective_state || (exam.is_active ? 'active' : 'inactive') }}
        </span>
        <div style="display: flex; gap: 0.25rem; margin-left: 0.5rem; flex-shrink: 0;">
          <button class="btn-sm" @click="emit('open')">Open</button>
          <button class="btn-sm" @click="startRename">{{ UI_COPY.RENAME_EXAM }}</button>
          <button
            class="btn-sm fail-btn"
            :disabled="exam.effective_state === 'active' || deleteLoading"
            :title="exam.effective_state === 'active' ? 'Deactivate before deleting' : UI_COPY.DELETE_EXAM"
            @click="deleteExam"
          >
            {{ UI_COPY.DELETE_EXAM }}
          </button>
        </div>
      </template>
    </div>

    <!-- Readiness summary line -->
    <div
      class="exam-item-readiness muted"
      style="font-size: 0.82rem; margin-top: 4px; display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap;"
    >
      <span>{{ exam.question_count ?? 0 }} questions · {{ exam.total_points ?? 0 }} pts ·</span>
      <span v-if="(exam.uncaptured_count ?? 0) > 0" class="badge badge-danger" style="font-size: 0.72rem; padding: 2px 6px;">
        <Icon name="x" /> {{ exam.uncaptured_count }} uncaptured
      </span>
      <span v-else class="badge badge-success" style="font-size: 0.72rem; padding: 2px 6px;">
        <Icon name="check" /> all captured
      </span>
    </div>

    <div class="exam-item-meta muted" v-if="exam.scheduled_start_at">
      Starts: {{ new Date(exam.scheduled_start_at).toLocaleString() }}
    </div>
    <div class="exam-item-meta muted" v-if="exam.scheduled_end_at">
      Ends: {{ new Date(exam.scheduled_end_at).toLocaleString() }}
    </div>

    <!-- Activation controls for inactive/scheduled exams -->
    <div
      v-if="exam.effective_state === 'inactive' || exam.effective_state === 'scheduled' || !exam.is_active"
      class="exam-activation-controls"
    >
      <label style="display: flex; align-items: center; gap: 0.5rem; cursor: pointer;">
        <input
          type="radio"
          :name="'activation-mode-' + exam.id"
          value="instant"
          v-model="activationMode"
        />
        Activate now
      </label>
      <label style="display: flex; align-items: center; gap: 0.5rem; cursor: pointer;">
        <input
          type="radio"
          :name="'activation-mode-' + exam.id"
          value="scheduled"
          v-model="activationMode"
        />
        Schedule activation
      </label>
      <template v-if="activationMode === 'scheduled'">
        <div style="display: flex; gap: 1rem; flex-wrap: wrap; margin-top: 0.5rem;">
          <div class="input-group" style="flex: 1; min-width: 200px;">
            <label>Start time:</label>
            <input type="datetime-local" v-model="scheduledStart" />
          </div>
          <div class="input-group" style="flex: 1; min-width: 200px;">
            <label>End time (optional):</label>
            <input type="datetime-local" v-model="scheduledEnd" />
          </div>
        </div>
      </template>
      <button
        class="success-btn"
        :disabled="actionLoading || (exam.uncaptured_count ?? 0) > 0"
        :title="(exam.uncaptured_count ?? 0) > 0 ? 'All questions must be captured before activating' : ''"
        @click="activateExam"
        style="margin-top: 0.5rem;"
      >
        Activate
      </button>
    </div>

    <!-- Deactivation controls for active exams -->
    <div v-else-if="exam.is_active" class="exam-activation-controls">
      <div style="display: flex; gap: 0.5rem; flex-wrap: wrap;">
        <button class="fail-btn" :disabled="actionLoading" @click="deactivateExamInstant">
          Deactivate Now
        </button>
        <div style="display: flex; align-items: center; gap: 0.5rem;">
          <input
            type="datetime-local"
            v-model="deactivateEndAt"
            style="border: 1px solid var(--color-border); padding: 0.3rem;"
          />
          <button class="secondary-btn" :disabled="actionLoading" @click="deactivateExamScheduled">
            Set End Time
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Exam card container */
.exam-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 14px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  margin-bottom: var(--space-2);
  background: var(--color-surface);
}
.exam-item:hover { border-color: var(--color-primary); background: var(--color-primary-bg); }

.exam-item-extended {
  display: block;
}

.exam-item-header {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  flex-wrap: wrap;
}

.exam-item-meta {
  margin-top: 2px;
  font-size: 0.82rem;
}

.exam-rename-input {
  padding: 0.3rem 0.5rem;
  border: 2px solid var(--color-primary);
  background: var(--color-surface);
  color: var(--color-text);
  font-weight: 600;
  font-family: var(--font-mono);
  font-size: 0.95rem;
}

.exam-activation-controls {
  margin-top: 0.5rem;
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}
</style>
