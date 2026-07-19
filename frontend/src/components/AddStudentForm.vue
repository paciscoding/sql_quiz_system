<script setup lang="ts">
import { ref, watch } from 'vue'
import { useSingleFlight } from '../composables/useSingleFlight'
import { useFormValidation, required } from '../composables/useFormValidation'
import FieldError from './FieldError.vue'
import { useNotifications } from '../services/notification_service'
import { handleApiError } from '../services/error_handler'
import { addStudent } from '../services/auth'
import Spinner from './Spinner.vue'

const emit = defineEmits<{
  (e: 'created'): void
}>()

const { notify } = useNotifications()

// --- Add student (roster fields) — default password = student number ---
const newStudent = ref({
  student_number: '',
  student_id_ext: '',
  sec_lec: '',
  sec_lab: '',
  first_name: '',
  last_name: '',
  email: '',
})
const studentReveal = ref<{ student_number: string; username: string; email: string; password: string } | null>(null)

// Form validation for add student
const newStudentForm = useFormValidation({
  student_number: { rules: [required('Student number is required')], initial: '' },
  student_id_ext: { rules: [required('Student ID is required')], initial: '' },
})
watch(() => newStudent.value.student_number, (v) => { newStudentForm.values.student_number = v })
watch(() => newStudent.value.student_id_ext, (v) => { newStudentForm.values.student_id_ext = v })

async function copyToClipboard(text: string) {
  try {
    if (navigator?.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      notify('Password copied to clipboard.', 'success')
    } else {
      notify('Clipboard not available in this browser.', 'error')
    }
  } catch {
    notify('Failed to copy password.', 'error')
  }
}

const { inFlight: addStudentLoading, run: addStudentAction } = useSingleFlight(async () => {
  if (!newStudentForm.validateAll()) return
  try {
    const { email, ...rosterFields } = newStudent.value
    const trimmedEmail = email.trim()
    const res = await addStudent({
      ...rosterFields,
      ...(trimmedEmail ? { email: trimmedEmail } : {}),
    })
    // Only a newly-created student has a generated password to reveal; an update
    // preserves the existing password, so there is nothing to show.
    if (res.created && res.generated_password) {
      studentReveal.value = {
        student_number: newStudent.value.student_number,
        username: newStudent.value.student_id_ext,
        email: trimmedEmail,
        password: res.generated_password,
      }
    } else {
      studentReveal.value = null
    }
    notify(`Student ${newStudent.value.student_number} ${res.created ? 'created' : 'updated'}.`, 'success')
    newStudent.value = {
      student_number: '',
      student_id_ext: '',
      sec_lec: '',
      sec_lab: '',
      first_name: '',
      last_name: '',
      email: '',
    }
    newStudentForm.reset()
    emit('created')
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to add student' })
  }
})
</script>

<template>
  <div>
    <form data-testid="add-student-form" @submit.prevent="addStudentAction" novalidate>
      <div class="field">
        <label>Student Number</label>
        <input class="input" data-testid="student-number" v-model="newStudent.student_number" required
          :aria-invalid="newStudentForm.touched.student_number && !!newStudentForm.errors.student_number"
          aria-describedby="err-student-number"
          @blur="newStudentForm.markTouched('student_number')"
        />
        <FieldError :message="newStudentForm.touched.student_number ? newStudentForm.errors.student_number : ''" id="err-student-number" />
      </div>
      <div class="field">
        <label>Student ID <span class="muted">(login username)</span></label>
        <input class="input" data-testid="student-id-ext" v-model="newStudent.student_id_ext" required
          :aria-invalid="newStudentForm.touched.student_id_ext && !!newStudentForm.errors.student_id_ext"
          aria-describedby="err-student-id-ext"
          @blur="newStudentForm.markTouched('student_id_ext')"
        />
        <FieldError :message="newStudentForm.touched.student_id_ext ? newStudentForm.errors.student_id_ext : ''" id="err-student-id-ext" />
      </div>
      <div class="field field-row">
        <div>
          <label>Lecture Section</label>
          <input class="input" data-testid="student-sec-lec" v-model="newStudent.sec_lec" />
        </div>
        <div>
          <label>Lab Section</label>
          <input class="input" data-testid="student-sec-lab" v-model="newStudent.sec_lab" />
        </div>
      </div>
      <div class="field field-row">
        <div>
          <label>First Name</label>
          <input class="input" data-testid="student-first-name" v-model="newStudent.first_name" />
        </div>
        <div>
          <label>Last Name</label>
          <input class="input" data-testid="student-last-name" v-model="newStudent.last_name" />
        </div>
      </div>
      <div class="field">
        <label for="student-email-input">Email <span class="muted">(optional)</span></label>
        <input id="student-email-input" class="input" type="email" data-testid="student-email-input"
          v-model="newStudent.email" autocomplete="email" placeholder="name@example.com" />
      </div>

      <button type="submit" class="btn btn-primary" style="width: 100%;" :disabled="addStudentLoading">
        <Spinner v-if="addStudentLoading" size="sm" inline :invert="true" />{{ addStudentLoading ? 'Adding' : 'Add Student' }}
      </button>
    </form>

    <div v-if="studentReveal" data-testid="student-password-reveal" class="reveal-panel">
      <p class="reveal-title">Login username: <strong>{{ studentReveal.username }}</strong></p>
      <p class="reveal-title">Generated password (shown once):</p>
      <code class="reveal-secret">{{ studentReveal.password }}</code>
      <div class="reveal-actions">
        <button type="button" class="btn btn-sm" @click="copyToClipboard(studentReveal.password)">Copy</button>
        <button type="button" class="btn btn-sm" @click="studentReveal = null">Dismiss</button>
      </div>
      <p class="reveal-notice">
        <template v-if="studentReveal.email">✉ Emailed to {{ studentReveal.email }}.</template>
        <template v-else>No email on file — copy and hand this password to the student.</template>
      </p>
    </div>
  </div>
</template>

<style scoped>
.field-row {
  display: flex;
  gap: var(--space-3);
}
.field-row > div { flex: 1; }

.reveal-panel {
  margin-top: var(--space-4);
  padding: var(--space-3);
  border: 1px solid var(--color-primary);
  background: var(--color-primary-bg);
  border-radius: var(--radius);
}
.reveal-title {
  margin: 0 0 var(--space-2) 0;
  font-size: 0.9rem;
}
.reveal-secret {
  display: inline-block;
  font-family: var(--font-mono, monospace);
  font-size: 1.05rem;
  font-weight: 700;
  padding: 4px 10px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  user-select: all;
}
.reveal-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  margin-top: var(--space-2);
}
.reveal-notice {
  margin: var(--space-2) 0 0 0;
  font-size: 0.85rem;
  color: var(--color-text-muted);
}
</style>
