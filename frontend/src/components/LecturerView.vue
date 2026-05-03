<script setup lang="ts">
import { ref, onMounted } from 'vue'

interface Examination {
  id: number
  title: string
  is_active: boolean
}

interface Student {
  id: number
  username: string
}

interface ValidationResult {
  status: string
  score: number
  feedback: string
  is_correct: boolean
}

const question = ref({
  id: 1,
  exam_id: null as number | null,
  target_table: '',
  correct_sql: '',
  type: 'QUERY',
  points: 10,
  strict_mode: {
    ordering: false,
    attributes: false,
    record_order: false
  }
})

onMounted(() => {
  fetchExams()
})

const exams = ref<Examination[]>([])
const newExamTitle = ref('')
const selectedExamId = ref<number | null>(null)
const students = ref<Student[]>([])
const selectedStudentIds = ref<number[]>([])

const loading = ref(false)
const message = ref('')
const error = ref('')

const fetchExams = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/exams', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      exams.value = await response.json()
    } else {
      const errData = await response.json()
      error.value = errData.error || 'Failed to fetch exams'
    }
  } catch (err: any) {
    error.value = err.message
  }
}

const createExam = async () => {
  if (!newExamTitle.value) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/exams', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ title: newExamTitle.value }),
    })
    if (response.ok) {
      newExamTitle.value = ''
      fetchExams()
      message.value = 'Exam created successfully'
    } else {
      const errData = await response.json()
      error.value = errData.error || 'Failed to create exam'
    }
  } catch (err: any) {
    error.value = err.message
  }
}

const toggleExamActive = async (id: number, active: boolean) => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${id}/activate`, {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ active }),
    })
    if (response.ok) {
      fetchExams()
      message.value = `Exam ${active ? 'activated' : 'deactivated'}`
    } else {
      const errData = await response.json()
      error.value = errData.error || 'Failed to update exam status'
    }
  } catch (err: any) {
    error.value = err.message
  }
}

const assignStudents = async () => {
  if (!selectedExamId.value || selectedStudentIds.value.length === 0) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${selectedExamId.value}/students`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ student_ids: selectedStudentIds.value }),
    })
    if (response.ok) {
      message.value = 'Students assigned successfully'
      selectedStudentIds.value = []
    } else {
      const errData = await response.json()
      error.value = errData.error || 'Failed to assign students'
    }
  } catch (err: any) {
    error.value = err.message
  }
}

const saveQuestion = async () => {
  loading.value = true
  message.value = ''
  error.value = ''

  const token = localStorage.getItem('sql_quiz_token')

  try {
    const response = await fetch('/api/v1/lecturer/question', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify(question.value),
    })

    if (!response.ok) {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to save question')
    }

    const data = await response.json()
    message.value = data.message
  } catch (err: any) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

const importSQL = ref('')
const importLoading = ref(false)
const importMessage = ref('')
const importError = ref('')

const handleImportSchema = async () => {
  if (!importSQL.value) return
  importLoading.value = true
  importMessage.value = ''
  importError.value = ''

  const token = localStorage.getItem('sql_quiz_token')

  try {
    const response = await fetch('/api/v1/lecturer/import-schema', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({ sql: importSQL.value }),
    })

    if (!response.ok) {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to import schema')
    }

    const data = await response.json()
    importMessage.value = data.message
    importSQL.value = ''
  } catch (err: any) {
    importError.value = err.message
  } finally {
    importLoading.value = false
  }
}
</script>

<template>
  <div class="lecturer-view">
    <!-- Exam Management Section -->
    <div class="lecturer-form card">
      <h2>Examination Events</h2>
      <div class="form-grid">
        <div class="input-group">
          <label>New Exam Title:</label>
          <input type="text" v-model="newExamTitle" placeholder="Midterm 2026" />
        </div>
        <button @click="createExam" class="secondary-btn" style="margin-top: 1.8rem;">Create Exam</button>
      </div>

      <div class="exam-list" v-if="exams.length > 0">
        <h3>Existing Exams</h3>
        <div v-for="e in exams" :key="e.id" class="exam-item">
          <span>{{ e.title }} (ID: {{ e.id }})</span>
          <button @click="toggleExamActive(e.id, !e.is_active)" :class="e.is_active ? 'fail-btn' : 'success-btn'">
            {{ e.is_active ? 'Deactivate' : 'Activate' }}
          </button>
        </div>
      </div>

      <div class="assign-section" style="margin-top: 2rem; padding-top: 1rem; border-top: 1px solid #eee;">
        <h3>Assign Students to Exam</h3>
        <div class="form-grid">
          <div class="input-group">
            <label>Select Exam ID:</label>
            <input type="number" v-model.number="selectedExamId" />
          </div>
          <div class="input-group">
            <label>Student IDs (comma separated):</label>
            <input type="text" @input="selectedStudentIds = ($event.target as HTMLInputElement).value.split(',').map(s => parseInt(s.trim())).filter(n => !isNaN(n))" />
          </div>
        </div>
        <button @click="assignStudents" class="primary-btn" style="width: auto; padding: 0.5rem 1rem;">Assign Students</button>
      </div>
    </div>

    <!-- New Import Section -->
    <div class="lecturer-form card import-card">
      <h2>Initialize Exam Database</h2>
      <p class="desc">Paste the full SQL schema script (e.g. `company.sql`) to set up the base tables and data.</p>
      <textarea 
        v-model="importSQL" 
        placeholder="CREATE TABLE department (...); INSERT INTO department ..."
        class="import-textarea"
      ></textarea>
      <button @click="handleImportSchema" :disabled="importLoading || !importSQL" class="secondary-btn">
        {{ importLoading ? 'Importing...' : 'Import Schema & Data' }}
      </button>
      <div v-if="importMessage" class="success-box">{{ importMessage }}</div>
      <div v-if="importError" class="error-box">{{ importError }}</div>
    </div>

    <div class="lecturer-form card">
      <h2>Manage Questions</h2>
      
      <div class="form-grid">
        <div class="input-group">
          <label>Question ID:</label>
          <input type="number" v-model.number="question.id" min="1" />
        </div>

        <div class="input-group">
          <label>Exam ID:</label>
          <input type="number" v-model.number="question.exam_id" placeholder="Optional" />
        </div>

        <div class="input-group">
          <label>Type:</label>
          <select v-model="question.type">
            <option value="QUERY">Query (SELECT)</option>
            <option value="DML">DML (Insert/Update/Delete)</option>
          </select>
        </div>

        <div class="input-group">
          <label>Target Table:</label>
          <input type="text" v-model="question.target_table" placeholder="e.g. users" />
        </div>

        <div class="input-group">
          <label>Points:</label>
          <input type="number" v-model.number="question.points" />
        </div>
      </div>

      <div class="input-group full-width">
        <label>Correct SQL:</label>
        <textarea v-model="question.correct_sql" placeholder="SELECT * FROM ..."></textarea>
      </div>

      <div class="strict-mode-section" v-if="question.type === 'QUERY'">
        <h3>Strict Mode Options</h3>
        <div class="checkbox-group">
          <label><input type="checkbox" v-model="question.strict_mode.ordering" /> Command Ordering</label>
          <label><input type="checkbox" v-model="question.strict_mode.attributes" /> Exact Attributes</label>
          <label><input type="checkbox" v-model="question.strict_mode.record_order" /> Record Order</label>
        </div>
      </div>

      <button @click="saveQuestion" :disabled="loading" class="primary-btn">
        {{ loading ? 'Saving...' : 'Save Question' }}
      </button>

      <div v-if="message" class="success-box">{{ message }}</div>
      <div v-if="error" class="error-box">{{ error }}</div>
    </div>
  </div>
</template>

<style scoped>
.lecturer-view {
  display: flex;
  flex-direction: column;
  gap: 2rem;
}

.card {
  background: #fff;
  padding: 2rem;
  border-radius: 8px;
  box-shadow: 0 2px 4px rgba(0,0,0,0.05);
}

.desc {
  color: #666;
  margin-bottom: 1rem;
}

.import-textarea {
  width: 100%;
  min-height: 200px;
  margin-bottom: 1rem;
  font-family: monospace;
  padding: 0.8rem;
  border: 1px solid #ddd;
  border-radius: 4px;
}

.secondary-btn {
  padding: 1rem;
  background-color: #4a5568;
  color: white;
  border: none;
  border-radius: 4px;
  font-weight: bold;
  cursor: pointer;
  margin-bottom: 1rem;
}

.success-btn {
  background-color: #41b883;
  color: white;
  border: none;
  padding: 0.3rem 0.8rem;
  border-radius: 4px;
  cursor: pointer;
}

.fail-btn {
  background-color: #e53e3e;
  color: white;
  border: none;
  padding: 0.3rem 0.8rem;
  border-radius: 4px;
  cursor: pointer;
}

.exam-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.5rem;
  border-bottom: 1px solid #eee;
}

.exam-list {
  margin-top: 1rem;
}

.secondary-btn:hover {
  background-color: #2d3748;
}

.lecturer-form {
  background: #fff;
  padding: 1rem;
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
}

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1rem;
}

.input-group {
  display: flex;
  flex-direction: column;
}

.full-width {
  grid-column: span 2;
}

label {
  font-weight: bold;
  margin-bottom: 0.4rem;
  font-size: 0.9rem;
}

input, select, textarea {
  padding: 0.6rem;
  border: 1px solid #ddd;
  border-radius: 4px;
}

textarea {
  min-height: 120px;
  font-family: monospace;
}

.strict-mode-section {
  border-top: 1px solid #eee;
  padding-top: 1rem;
}

.checkbox-group {
  display: flex;
  gap: 1.5rem;
}

.checkbox-group label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-weight: normal;
  cursor: pointer;
}

.primary-btn {
  padding: 1rem;
  background-color: #3182ce;
  color: white;
  border: none;
  border-radius: 4px;
  font-weight: bold;
  cursor: pointer;
}

.primary-btn:hover {
  background-color: #2b6cb0;
}

.success-box {
  background-color: #f0fff4;
  color: #38a169;
  padding: 1rem;
  border-radius: 4px;
}

.error-box {
  background-color: #fff5f5;
  color: #e53e3e;
  padding: 1rem;
  border-radius: 4px;
}

h2 {
  color: black;
}
</style>
