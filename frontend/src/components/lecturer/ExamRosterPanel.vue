<script setup lang="ts">
import { ref, watch, onMounted } from 'vue'
import BulkImportStudents from '../BulkImportStudents.vue'
import StudentMultiSelect from '../StudentMultiSelect.vue'
import Spinner from '../Spinner.vue'
import EmptyState from '../EmptyState.vue'
import { useSingleFlight } from '../../composables/useSingleFlight'
import { useConfirm } from '../../services/confirm_service'
import { useNotifications } from '../../services/notification_service'
import { handleApiError } from '../../services/error_handler'
import type { Examination, Student, RosterStudent } from './types'

const props = defineProps<{
  exam: Examination
}>()

const emit = defineEmits<{
  (e: 'changed'): void
}>()

const { notify } = useNotifications()
const { confirm } = useConfirm()

const studentsList = ref<Student[]>([])
const selectedStudentIds = ref<number[]>([])
const rosterStudents = ref<RosterStudent[]>([])

const fetchStudents = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/users/students', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      studentsList.value = (await response.json()) || []
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load students' })
  }
}

const fetchRoster = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/students`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      const data = await resp.json()
      rosterStudents.value = data.students || []
    }
  } catch (err) {
    // silently ignore
  }
}

watch(() => props.exam.id, () => {
  rosterStudents.value = []
  fetchRoster()
}, { immediate: true })

onMounted(() => {
  fetchStudents()
})

const { inFlight: assignStudentsLoading, run: assignStudents } = useSingleFlight(async () => {
  if (selectedStudentIds.value.length === 0) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/students`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ student_ids: selectedStudentIds.value }),
    })
    if (response.ok) {
      notify('Students enrolled successfully', 'success')
      selectedStudentIds.value = []
      fetchRoster()
      emit('changed')
    } else {
      const errData = await response.json()
      handleApiError(new Error(errData.error || 'Failed to enroll students'), { fallback: 'Failed to enroll students' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to enroll students' })
  }
})

async function removeStudentFromRoster(studentId: number, studentName: string) {
  const ok = await confirm({
    title: 'Remove from roster?',
    body: studentName,
    confirmLabel: 'Remove',
    danger: true
  })
  if (!ok) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch(`/api/v1/lecturer/exams/${props.exam.id}/students/${studentId}`, {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      notify('Student removed from roster', 'success')
      fetchRoster()
      emit('changed')
    } else {
      const e = await resp.json()
      handleApiError(new Error(e.error || 'Failed to remove student'), { fallback: 'Failed to remove student' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to remove student' })
  }
}
</script>

<template>
  <div class="exam-roster-panel">
    <h2>Enroll Students in Exam</h2>

    <div class="input-group">
      <label>Select Students:</label>
      <StudentMultiSelect v-model="selectedStudentIds" :students="studentsList" />
    </div>

    <button
      @click="assignStudents"
      :disabled="assignStudentsLoading || selectedStudentIds.length === 0"
      class="primary-btn"
      style="width: auto; padding: 0.5rem 1rem;"
    >
      <Spinner v-if="assignStudentsLoading" size="sm" inline :invert="true" />{{ assignStudentsLoading ? 'Enrolling' : `Enroll ${selectedStudentIds.length} Students` }}
    </button>

    <details class="import-roster-panel">
      <summary>Import Roster</summary>
      <BulkImportStudents />
    </details>

    <!-- Roster table -->
    <div style="margin-top: var(--space-5);">
      <h3>Enrolled: {{ rosterStudents.length }} student{{ rosterStudents.length !== 1 ? 's' : '' }}</h3>
      <EmptyState
        v-if="rosterStudents.length === 0"
        :compact="true"
        title="No students enrolled"
        body="Use the form above to enroll students."
      />
      <table v-else class="roster-table">
        <thead>
          <tr>
            <th>Student #</th>
            <th>Name</th>
            <th>Sec Lec / Lab</th>
            <th>Enrolled At</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in rosterStudents" :key="s.student_id">
            <td>{{ s.student_number || s.username }}</td>
            <td>{{ s.first_name }} {{ s.last_name }}</td>
            <td>{{ s.sec_lec }} / {{ s.sec_lab }}</td>
            <td>{{ new Date(s.assigned_at).toLocaleDateString() }}</td>
            <td>
              <button
                class="btn-sm fail-btn"
                :disabled="props.exam.effective_state === 'active'"
                :title="props.exam.effective_state === 'active' ? 'Cannot remove during active exam' : 'Remove from roster'"
                @click="removeStudentFromRoster(s.student_id, s.first_name + ' ' + s.last_name)"
              >
                Remove
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.exam-roster-panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

h2 {
  color: var(--color-text);
  font-weight: 700;
  margin: 0 0 var(--space-3) 0;
  font-size: 1.1rem;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}

h3 {
  margin: 0 0 var(--space-2) 0;
  font-size: 1rem;
  font-weight: 700;
  color: var(--color-text);
}

.input-group {
  display: flex;
  flex-direction: column;
}

label {
  font-weight: 700;
  margin-bottom: 0.4rem;
  font-size: 0.95rem;
  color: var(--color-text);
}

.primary-btn {
  padding: var(--space-3) var(--space-4);
  background-color: var(--color-primary);
  color: var(--color-surface);
  border: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  font-size: 1rem;
  transition: none;
}

.primary-btn:hover {
  background-color: #333333;
}

.primary-btn:active {
  transform: translateY(1px);
}

.primary-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.import-roster-panel {
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
  padding: var(--space-4);
  background: var(--color-surface-muted);
}

.import-roster-panel summary {
  cursor: pointer;
  font-weight: 800;
  color: var(--color-text);
  font-size: 1.1rem;
}

.roster-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.86rem;
  background: var(--color-surface);
  color: var(--color-text);
}

.roster-table th,
.roster-table td {
  border-bottom: 1px solid var(--color-border-soft);
  padding: 6px 10px;
  text-align: left;
}

.roster-table th {
  background: var(--color-surface-sunk);
  color: var(--color-text);
  font-weight: 700;
  font-size: 0.74rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.roster-table tr:nth-child(even) td {
  background: var(--color-surface-muted);
}

.btn-sm {
  padding: 4px 12px;
  font-size: 0.8rem;
  font-weight: 700;
  cursor: pointer;
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
}

.btn-sm:hover {
  background: var(--color-surface-muted);
}

.btn-sm:active {
  transform: translateY(1px);
}

.fail-btn {
  background-color: var(--color-danger-bg);
  color: var(--color-danger);
  border: 2px solid var(--color-danger);
  padding: 0.5rem var(--space-4);
  cursor: pointer;
  font-weight: 700;
  transition: none;
}

.fail-btn:active {
  transform: translateY(1px);
}

.fail-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.badge {
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-size: 0.75rem;
  font-weight: 700;
  display: inline-block;
}
</style>
