<script setup lang="ts">
// Lecturer-facing composer/list for exam announcements.
//
// IMPORTANT: announcements only reach students while the exam is active.
// The server only returns announcements with sent_at >= examinations.activated_at
// to the student-facing endpoint, so anything sent before the *current*
// activation is effectively invisible to students (this is how "announcements
// are cleared on re-activation" is enforced — no rows are ever deleted here).
import { ref, computed, watch } from 'vue'
import type { Examination } from './types'
import Icon from '../Icon.vue'
import Spinner from '../Spinner.vue'
import EmptyState from '../EmptyState.vue'
import { useConfirm } from '../../services/confirm_service'
import { useNotifications } from '../../services/notification_service'
import { handleApiError } from '../../services/error_handler'

const props = defineProps<{ exam: Examination }>()

const emit = defineEmits<{
  changed: []
}>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

interface ExamAnnouncement {
  // id is a bare SERIAL on app.exam_announcements (like :imageId on the
  // intro-image routes), so it arrives as a JSON number — not an opaque
  // public id. exam_id IS an opaque public id string.
  id: number
  exam_id: string
  lecturer_name: string
  message: string
  sent_at: string
}

const announcements = ref<ExamAnnouncement[]>([])
const loading = ref(false)
const posting = ref(false)
const deletingId = ref<string | null>(null)
const draft = ref('')

const MAX_LEN = 2000
const trimmedDraft = computed(() => draft.value.trim())
const remaining = computed(() => MAX_LEN - draft.value.length)
const canSubmit = computed(() => trimmedDraft.value.length > 0 && draft.value.length <= MAX_LEN && !posting.value)

function authHeaders(extra: Record<string, string> = {}): Record<string, string> {
  const token = localStorage.getItem('sql_quiz_token')
  return {
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...extra
  }
}

const loadAnnouncements = async () => {
  loading.value = true
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/announcements`, {
      headers: authHeaders()
    })
    if (!response.ok) {
      throw new Error(`Failed to load announcements (${response.status})`)
    }
    announcements.value = await response.json()
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load announcements' })
  } finally {
    loading.value = false
  }
}

watch(() => props.exam.id, loadAnnouncements, { immediate: true })

async function postAnnouncement() {
  if (!canSubmit.value) return
  posting.value = true
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/announcements`, {
      method: 'POST',
      headers: authHeaders({ 'Content-Type': 'application/json' }),
      body: JSON.stringify({ message: trimmedDraft.value })
    })
    if (!response.ok) {
      let msg = `Failed to post announcement (${response.status})`
      try {
        const body = await response.json()
        if (body && typeof body.error === 'string') msg = body.error
      } catch {
        // not JSON
      }
      throw new Error(msg)
    }
    const created: ExamAnnouncement = await response.json()
    announcements.value = [created, ...announcements.value]
    draft.value = ''
    notify('Announcement sent', 'success')
    emit('changed')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to post announcement' })
  } finally {
    posting.value = false
  }
}

async function deleteAnnouncement(announcement: ExamAnnouncement) {
  const ok = await confirm({
    title: 'Delete this announcement?',
    body: 'Students who already saw it may still remember it, but it will be removed from the list. This cannot be undone.',
    confirmLabel: 'Delete',
    danger: true
  })
  if (!ok) return

  deletingId.value = announcement.id
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/announcements/${announcement.id}`, {
      method: 'DELETE',
      headers: authHeaders()
    })
    if (!response.ok) {
      throw new Error(`Failed to delete announcement (${response.status})`)
    }
    announcements.value = announcements.value.filter(a => a.id !== announcement.id)
    notify('Announcement deleted', 'success')
    emit('changed')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to delete announcement' })
  } finally {
    deletingId.value = null
  }
}

function formatSentAt(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}
</script>

<template>
  <div class="announcements-panel">
    <div class="composer">
      <label for="announcement-message">New announcement</label>
      <textarea
        id="announcement-message"
        v-model="draft"
        :maxlength="MAX_LEN"
        rows="3"
        placeholder="Message shown to students while the exam is active..."
        :disabled="posting"
      />
      <div class="composer-footer">
        <span class="char-count" :class="{ 'char-count-warn': remaining < 100 }">{{ remaining }} characters left</span>
        <button class="btn-primary" :disabled="!canSubmit" @click="postAnnouncement">
          <Spinner v-if="posting" size="sm" inline :invert="true" />
          Send
        </button>
      </div>
      <p class="hint">
        Only visible to students while this exam is active. Announcements sent before the exam's
        current activation are not delivered.
      </p>
    </div>

    <div class="announcement-list">
      <div v-if="loading" class="loading-state">
        <Spinner size="sm" inline />
        <span>Loading announcements...</span>
      </div>
      <EmptyState v-else-if="announcements.length === 0" title="No announcements yet" />
      <div v-else class="announcement" v-for="a in announcements" :key="a.id">
        <div class="announcement-header">
          <span class="announcement-author">{{ a.lecturer_name }}</span>
          <span class="announcement-time">{{ formatSentAt(a.sent_at) }}</span>
          <button
            class="btn-icon"
            title="Delete announcement"
            :disabled="deletingId === a.id"
            @click="deleteAnnouncement(a)"
          >
            <Spinner v-if="deletingId === a.id" size="sm" inline />
            <Icon v-else name="x" />
          </button>
        </div>
        <p class="announcement-message">{{ a.message }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.announcements-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.composer {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-4);
  background: var(--color-surface);
}

.composer label {
  font-weight: 700;
  font-size: 0.86rem;
  color: var(--color-text);
}

.composer textarea {
  resize: vertical;
  min-height: 4.5rem;
  font: inherit;
  padding: var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  color: var(--color-text);
}

.composer-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
}

.char-count {
  font-size: 0.78rem;
  color: var(--color-text-muted);
}

.char-count-warn {
  color: var(--color-danger, #d9534f);
}

.hint {
  margin: 0;
  font-size: 0.78rem;
  color: var(--color-text-muted);
}

.btn-primary {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 16px;
  font-size: 0.86rem;
  font-weight: 700;
  cursor: pointer;
  border: 2px solid var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-primary, #fff);
  border-radius: var(--radius-sm);
}

.btn-primary:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.announcement-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.loading-state {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  color: var(--color-text-muted);
  font-size: 0.86rem;
}

.announcement {
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-3);
  background: var(--color-surface);
}

.announcement-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
}

.announcement-author {
  font-weight: 700;
  font-size: 0.82rem;
  color: var(--color-text);
}

.announcement-time {
  font-size: 0.78rem;
  color: var(--color-text-muted);
  flex: 1;
}

.announcement-message {
  margin: 0;
  white-space: pre-wrap;
  color: var(--color-text);
  font-size: 0.9rem;
}

.btn-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  border: none;
  cursor: pointer;
  color: var(--color-text-muted);
  padding: 2px;
}

.btn-icon:hover {
  color: var(--color-danger, #d9534f);
}

.btn-icon:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
