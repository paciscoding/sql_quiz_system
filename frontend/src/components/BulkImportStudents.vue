<script setup lang="ts">
import { ref } from 'vue'
import { useNotifications } from '../services/notification_service'

interface ImportError {
  row: number
  student_number: string
  message: string
}

interface ImportReport {
  total: number
  created: number
  updated: number
  skipped: number
  errors: ImportError[]
}

const { notify } = useNotifications()

const selectedFile = ref<File | null>(null)
const loading = ref(false)
const topLevelError = ref('')
const result = ref<ImportReport | null>(null)

const handleFileChange = (event: Event) => {
  const input = event.target as HTMLInputElement
  selectedFile.value = input.files?.[0] ?? null
  topLevelError.value = ''
  result.value = null
}

const submitImport = async () => {
  if (!selectedFile.value || loading.value) return

  loading.value = true
  topLevelError.value = ''
  result.value = null

  const token = localStorage.getItem('sql_quiz_token')
  const formData = new FormData()
  formData.append('file', selectedFile.value)

  try {
    const response = await fetch('/api/v1/lecturer/students/import', {
      method: 'POST',
      headers: {
        'Authorization': `Bearer ${token}`
      },
      body: formData
    })

    const data = await response.json().catch(() => ({} as any))

    if (!response.ok) {
      if (response.status === 400) {
        topLevelError.value = data.error || 'File format rejected'
        return
      }
      throw new Error(data.error || 'Import failed')
    }

    result.value = data
    notify('Import complete', 'success')
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="bulk-import card">
    <h2>Import Class Roster</h2>
    <p class="desc">Upload a .csv or .xlsx file with columns: Student Number, First Name, Last Name, SECLEC, SECLAB, Student_id.</p>

    <div class="form-row">
      <input
        type="file"
        accept=".csv,.xlsx"
        :disabled="loading"
        @change="handleFileChange"
        class="file-input"
      />
      <button
        class="primary-btn"
        :disabled="loading || !selectedFile"
        @click="submitImport"
      >
        {{ loading ? 'Importing...' : 'Import' }}
      </button>
    </div>

    <div v-if="loading" class="loading-msg">Uploading and processing...</div>

    <div v-if="topLevelError" class="error-box">
      {{ topLevelError }}
    </div>

    <div v-if="result !== null" class="results-section">
      <div class="summary-grid">
        <div class="summary-cell">
          <div class="summary-value">{{ result.total }}</div>
          <div class="summary-label">Total</div>
        </div>
        <div class="summary-cell">
          <div class="summary-value summary-created">{{ result.created }}</div>
          <div class="summary-label">Created</div>
        </div>
        <div class="summary-cell">
          <div class="summary-value summary-updated">{{ result.updated }}</div>
          <div class="summary-label">Updated</div>
        </div>
        <div class="summary-cell">
          <div class="summary-value summary-skipped">{{ result.skipped }}</div>
          <div class="summary-label">Skipped</div>
        </div>
      </div>

      <div v-if="result.errors.length > 0" class="errors-section">
        <h3>Row Errors ({{ result.errors.length }})</h3>
        <div class="errors-table-wrap">
          <table class="errors-table">
            <thead>
              <tr>
                <th>Row</th>
                <th>Student Number</th>
                <th>Reason</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in result.errors" :key="e.row">
                <td>{{ e.row }}</td>
                <td>{{ e.student_number || '—' }}</td>
                <td>{{ e.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-else class="no-errors-msg">
        No row errors.
      </div>
    </div>
  </div>
</template>

<style scoped>
.bulk-import {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.card {
  background: var(--color-surface);
  padding: var(--space-6);
  border: 2px solid var(--color-border);
  border-radius: 0;
}

h2 {
  color: var(--color-text);
  margin-top: 0;
  margin-bottom: 0;
  font-weight: 800;
}

h3 {
  margin-top: 0;
  margin-bottom: var(--space-2);
  font-size: 1rem;
  font-weight: 800;
}

.desc {
  color: var(--color-text-muted);
  margin: 0;
  font-size: 0.9rem;
}

.form-row {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  flex-wrap: wrap;
}

.file-input {
  flex: 1;
  min-width: 200px;
  padding: var(--space-2);
  border: 2px solid var(--color-border);
  border-radius: 0;
  background: var(--color-surface);
  color: var(--color-text);
  font-family: var(--font-mono);
  font-size: 0.9rem;
}

.primary-btn {
  padding: var(--space-2) var(--space-4);
  background-color: var(--color-primary);
  color: var(--color-surface);
  border: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  white-space: nowrap;
}

.primary-btn:hover:not(:disabled) {
  background-color: #333333;
}

.primary-btn:active {
  transform: translateY(1px);
}

.primary-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.loading-msg {
  color: var(--color-text-muted);
  font-weight: 700;
  font-size: 0.9rem;
}

.error-box {
  background-color: var(--color-danger-bg);
  color: var(--color-danger);
  padding: var(--space-4);
  border: 2px solid var(--color-danger);
  font-weight: 700;
}

.results-section {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-4);
}

.summary-cell {
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  padding: var(--space-4);
  text-align: center;
}

.summary-value {
  font-size: 1.8rem;
  font-weight: 800;
  color: var(--color-text);
}

.summary-label {
  font-size: 0.75rem;
  color: var(--color-text-muted);
  margin-top: 0.2rem;
  font-weight: 700;
}

.errors-section {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.errors-table-wrap {
  overflow-x: auto;
  border: 2px solid var(--color-border);
}

.errors-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
  background: var(--color-surface);
  color: var(--color-text);
}

.errors-table th,
.errors-table td {
  border: 1px solid var(--color-border);
  padding: 0.5rem;
  text-align: left;
}

.errors-table th {
  background: var(--color-surface-muted);
  color: var(--color-text);
  font-weight: 700;
}

.no-errors-msg {
  color: var(--color-success);
  background: var(--color-success-bg);
  padding: var(--space-2);
  border: 2px solid var(--color-success);
  font-weight: 700;
  text-align: center;
}
</style>
