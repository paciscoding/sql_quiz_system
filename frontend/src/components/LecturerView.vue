<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import BulkImportStudents from './BulkImportStudents.vue'
import SqlEditor from './SqlEditor.vue'
import { useNotifications } from '../services/notification_service'

interface Examination {
  id: number
  title: string
  is_active: boolean
}

interface Student {
  id: number
  username: string
}

interface CaptureStatus {
  status: 'captured' | 'failed' | 'pending' | ''
  error: string
  capturedAt: string | null
}

const { notify } = useNotifications()

const blankQuestion = () => ({
  id: null as number | null,
  exam_id: null as number | null,
  target_table: '',
  correct_sql: '',
  requirement: '',
  type: 'QUERY',
  points: 10,
  strict_mode: {
    ordering: false,
    attributes: false
  }
})

const question = ref(blankQuestion())
const lastCapture = ref<CaptureStatus>({ status: '', error: '', capturedAt: null })
const examQuestions = ref<any[]>([])

const fetchExamQuestions = async () => {
  if (question.value.exam_id == null) {
    examQuestions.value = []
    return
  }
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/exams/${question.value.exam_id}/questions`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      examQuestions.value = await response.json()
    }
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

watch(() => question.value.exam_id, (newId) => {
  if (newId !== null) {
    fetchExamQuestions()
  } else {
    examQuestions.value = []
  }
})

const deleteQuestion = async (id: number) => {
  if (!confirm('Are you sure you want to delete this question?')) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/lecturer/questions/${id}`, {
      method: 'DELETE',
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      notify('Question deleted', 'success')
      fetchExamQuestions()
    } else {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to delete')
    }
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

const editQuestion = (q: any) => {
  question.value = {
    id: q.id,
    exam_id: q.exam_id,
    target_table: q.target_table || '',
    correct_sql: q.correct_sql || '',
    requirement: q.requirement || '',
    type: q.question_type || 'QUERY',
    points: q.points || 0,
    strict_mode: {
      ordering: q.strict_mode?.ordering || false,
      attributes: q.strict_mode?.attributes || false
    }
  }
  lastCapture.value = {
    status: q.expected_result_status || '',
    error: q.capture_error || '',
    capturedAt: q.expected_result_captured_at || null
  }
  previewResult.value = { status: '', rows: [], columns: [], error: '' }
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

interface PreviewResult {
  status: 'ok' | 'error' | ''
  rows: Record<string, any>[]
  columns: string[]
  error: string
}
const previewResult = ref<PreviewResult>({ status: '', rows: [], columns: [], error: '' })
const previewLoading = ref(false)

const previewCorrectSQL = async () => {
  if (!question.value.correct_sql) {
    notify('Enter the Correct SQL before previewing', 'error')
    return
  }
  if (question.value.type === 'DML' && !question.value.target_table) {
    notify('Target table is required to preview a DML question', 'error')
    return
  }
  previewLoading.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/preview', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        type: question.value.type,
        target_table: question.value.target_table,
        correct_sql: question.value.correct_sql
      })
    })
    const data = await response.json().catch(() => ({} as any))
    if (!response.ok) {
      throw new Error(data.error || 'Preview failed')
    }
    if (data.status === 'error') {
      previewResult.value = { status: 'error', rows: [], columns: [], error: data.error || 'SQL error' }
    } else {
      previewResult.value = {
        status: 'ok',
        rows: data.rows || [],
        columns: data.columns || [],
        error: ''
      }
    }
  } catch (err: any) {
    previewResult.value = { status: 'error', rows: [], columns: [], error: err.message }
    notify(err.message, 'error')
  } finally {
    previewLoading.value = false
  }
}

onMounted(() => {
  fetchExams()
  fetchStudents()
})

const exams = ref<Examination[]>([])
const newExamTitle = ref('')
const selectedExamId = ref<number | null>(null)

const activeExam = computed(() => exams.value.find(e => e.id === selectedExamId.value))

const studentsList = ref<Student[]>([])
const selectedStudentIds = ref<number[]>([])

const loading = ref(false)
const error = ref('')

// Update question.exam_id and fetch questions whenever the selected exam changes.
watch(selectedExamId, (id) => {
  if (id !== null) {
    question.value.exam_id = id
    fetchExamQuestions()
  } else {
    question.value.exam_id = null
    examQuestions.value = []
  }
})

const fetchStudents = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/users/students', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      studentsList.value = (await response.json()) || []
    }
  } catch (err: any) {
    error.value = err.message
  }
}

const fetchExams = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/exams', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      exams.value = (await response.json()) || []
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
      notify('Exam created successfully', 'success')
    } else {
      const errData = await response.json()
      notify(errData.error || 'Failed to create exam', 'error')
    }
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

const toggleExamActive = async (id: number, active: boolean) => {
  const token = localStorage.getItem('sql_quiz_token')
  // Phase 3 has its own dedicated endpoint that drops the per-exam DML sandbox
  // exactly once. Phase 1 activation goes through /activate with {active:true}.
  const url = active
    ? `/api/v1/lecturer/exams/${id}/activate`
    : `/api/v1/lecturer/exams/${id}/deactivate`
  const init: RequestInit = {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`
    }
  }
  if (active) {
    init.body = JSON.stringify({ active: true })
  }
  try {
    const response = await fetch(url, init)
    if (response.ok) {
      fetchExams()
      notify(`Exam ${active ? 'activated' : 'deactivated'}`, 'success')
    } else {
      const errData = await response.json()
      notify(errData.error || 'Failed to update exam status', 'error')
    }
  } catch (err: any) {
    notify(err.message, 'error')
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
      notify('Students assigned successfully', 'success')
      selectedStudentIds.value = []
    } else {
      const errData = await response.json()
      notify(errData.error || 'Failed to assign students', 'error')
    }
  } catch (err: any) {
    notify(err.message, 'error')
  }
}

const saveQuestion = async () => {
  if (question.value.id == null || question.value.id <= 0) {
    notify('Please enter a valid Question ID', 'error')
    return
  }
  if (question.value.exam_id == null) {
    notify('Please pick an Exam ID before saving a question', 'error')
    return
  }
  loading.value = true
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

    const data = await response.json().catch(() => ({} as any))
    if (!response.ok) {
      throw new Error(data.error || 'Failed to save question')
    }

    // Surface Phase 0 capture outcome so the lecturer knows whether the question
    // is ready to ship without having to inspect the DB.
    lastCapture.value = {
      status: data.expected_result_status || '',
      error: data.capture_error || '',
      capturedAt: data.expected_result_captured_at || null
    }

    if (lastCapture.value.status === 'failed') {
      notify(`Question saved, but capture failed: ${lastCapture.value.error}`, 'error')
    } else if (lastCapture.value.status === 'captured') {
      notify('Question saved and Correct Result captured', 'success')
    } else {
      notify(data.message || 'Question saved', 'success')
    }

    fetchExamQuestions()

    // Reset for the next question to avoid silent ON CONFLICT overwrites.
    const lastExamId = question.value.exam_id
    question.value = blankQuestion()
    question.value.exam_id = lastExamId
    previewResult.value = { status: '', rows: [], columns: [], error: '' }
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

const importSQL = ref('')
const importLoading = ref(false)

const handleImportSchema = async () => {
  if (!importSQL.value) return
  importLoading.value = true

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
    notify(data.message, 'success')
    importSQL.value = ''
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    importLoading.value = false
  }
}

interface ConsolidatedScore {
  student_id: number
  username: string
  student_number: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  exam_scores: Record<string, number>
  practice_total: number
  grand_total: number
}

const consolidatedScores = ref<ConsolidatedScore[]>([])
const loadingScores = ref(false)

const fetchScores = async () => {
  loadingScores.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/scores', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      consolidatedScores.value = await response.json()
    } else {
      notify('Failed to fetch scores', 'error')
    }
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loadingScores.value = false
  }
}

const exportCSV = () => {
  const token = localStorage.getItem('sql_quiz_token')
  window.open(`/api/v1/lecturer/export-scores?token=${token}`, '_blank')
}

const allExamTitles = computed(() => {
  const titles = new Set<string>()
  consolidatedScores.value.forEach(s => {
    Object.keys(s.exam_scores).forEach(t => titles.add(t))
  })
  return Array.from(titles).sort()
})

const activeTab = ref<'schema'|'questions'|'exams'|'assignments'|'results'>(
  (localStorage.getItem('lecturerTab') as any) || 'questions'
)
watch(activeTab, (v) => {
  localStorage.setItem('lecturerTab', v)
  if (v === 'results') {
    fetchScores()
  }
})
</script>

<template>
  <div>
    <div class="tab-bar">
      <button :class="{ 'tab-active': activeTab === 'schema' }" @click="activeTab = 'schema'">Schema</button>
      <button :class="{ 'tab-active': activeTab === 'questions' }" @click="activeTab = 'questions'">Questions</button>
      <button :class="{ 'tab-active': activeTab === 'exams' }" @click="activeTab = 'exams'">Exams</button>
      <button :class="{ 'tab-active': activeTab === 'assignments' }" @click="activeTab = 'assignments'">Assignments</button>
      <button :class="{ 'tab-active': activeTab === 'results' }" @click="activeTab = 'results'">Results</button>
    </div>

    <div v-show="activeTab === 'schema'" class="lecturer-form card">
      <h2>Initialize Exam Database</h2>
      <p class="desc">Paste the full SQL schema script (e.g. company.sql) to set up the base tables and data.</p>
      <SqlEditor v-model="importSQL" height="300px" style="margin-bottom: 1rem;" />
      <button @click="handleImportSchema" :disabled="importLoading || !importSQL" class="secondary-btn">
        {{ importLoading ? 'Importing...' : 'Import Schema & Data' }}
      </button>
    </div>

    <div v-show="activeTab === 'questions'" class="lecturer-form card">
      <h2>Manage Questions</h2>
      
      <div class="context-selector">
        <div class="input-group">
          <label>Working Exam:</label>
          <select v-model.number="selectedExamId">
            <option :value="null">-- Select an Exam to Author Questions --</option>
            <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }} (ID: {{ e.id }})</option>
          </select>
        </div>
        
        <div v-if="activeExam" class="exam-context-status">
          <div class="status-info">
            <span class="status-label">Status:</span>
            <span class="badge" :class="activeExam.is_active ? 'captured' : 'failed'">
              {{ activeExam.is_active ? 'Active' : 'Inactive' }}
            </span>
          </div>
          <button 
            @click="toggleExamActive(activeExam.id, !activeExam.is_active)" 
            :class="activeExam.is_active ? 'fail-btn' : 'success-btn'"
            class="toggle-btn"
          >
            {{ activeExam.is_active ? 'Deactivate Exam' : 'Activate Exam' }}
          </button>
        </div>
      </div>

      <div v-if="!selectedExamId" class="empty-state">
        <p>Please select an exam from the dropdown above to start authoring questions.</p>
      </div>

      <template v-else>
        <div class="form-grid">
          <div class="input-group">
            <label>Question ID:</label>
            <input type="number" v-model.number="question.id" min="1" />
          </div>

          <div class="input-group">
            <label>Exam ID:</label>
            <input type="number" v-model.number="question.exam_id" disabled />
          </div>

          <div class="input-group">
            <label>Type:</label>
            <select v-model="question.type">
              <option value="QUERY">Query (SELECT)</option>
              <option value="DML">DML (Insert/Update/Delete)</option>
            </select>
          </div>

          <div class="input-group" v-if="question.type === 'DML'">
            <label>Target Table:</label>
            <input type="text" v-model="question.target_table" placeholder="e.g. employee" />
          </div>

          <div class="input-group">
            <label>Points:</label>
            <input type="number" v-model.number="question.points" />
          </div>
        </div>

        <div class="input-group full-width">
          <label>Question Requirement:</label>
          <textarea
            v-model="question.requirement"
            rows="3"
            placeholder="Describe what the student should do"
          ></textarea>
        </div>

        <div class="input-group full-width">
          <label>Correct SQL:</label>
          <SqlEditor v-model="question.correct_sql" height="200px" />
          <button
            @click="previewCorrectSQL"
            :disabled="previewLoading || !question.correct_sql"
            class="secondary-btn"
            style="margin-top: 0.5rem; width: auto; padding: 0.5rem 1rem;"
          >
            {{ previewLoading ? 'Running...' : 'Preview Correct SQL Result' }}
          </button>
        </div>

        <div v-if="previewResult.status" class="preview-block" :class="previewResult.status">
          <strong v-if="previewResult.status === 'ok'">Preview ({{ previewResult.rows.length }} row{{ previewResult.rows.length === 1 ? '' : 's' }})</strong>
          <strong v-else>Preview failed</strong>
          <div v-if="previewResult.status === 'error'" class="preview-error">{{ previewResult.error }}</div>
          <div v-else-if="previewResult.rows.length === 0" class="preview-empty">(no rows returned)</div>
          <div v-else class="preview-table-wrap">
            <table class="preview-table">
              <thead>
                <tr><th v-for="c in previewResult.columns" :key="c">{{ c }}</th></tr>
              </thead>
              <tbody>
                <tr v-for="(row, i) in previewResult.rows" :key="i">
                  <td v-for="c in previewResult.columns" :key="c">{{ row[c] }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div class="strict-mode-section">
          <h3>Strict Mode Options</h3>
          <div class="checkbox-group">
            <label><input type="checkbox" v-model="question.strict_mode.ordering" /> Row Ordering <span class="hint">(rows must match the teacher's order; off means set-equality)</span></label>
            <label><input type="checkbox" v-model="question.strict_mode.attributes" /> Exact Attributes <span class="hint">(column names must match)</span></label>
          </div>
        </div>

        <button @click="saveQuestion" :disabled="loading" class="primary-btn">
          {{ loading ? 'Saving...' : 'Save Question' }}
        </button>

        <div v-if="lastCapture.status" class="capture-status" :class="lastCapture.status">
          <strong v-if="lastCapture.status === 'captured'">Correct Result captured</strong>
          <strong v-else-if="lastCapture.status === 'failed'">Capture failed</strong>
          <strong v-else>Status: {{ lastCapture.status }}</strong>
          <div v-if="lastCapture.error" class="capture-error">{{ lastCapture.error }}</div>
          <div v-if="lastCapture.capturedAt" class="capture-time">Captured at: {{ lastCapture.capturedAt }}</div>
        </div>

        <div v-if="examQuestions.length > 0" class="questions-list-section" style="margin-top: 2rem; border-top: 2px solid var(--color-border); padding-top: 1rem;">
          <h3 style="margin-bottom: 1rem;">Questions in Exam {{ question.exam_id }}</h3>
          <div class="preview-table-wrap">
            <table class="preview-table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Type</th>
                  <th>Points</th>
                  <th>Requirement</th>
                  <th>Capture</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="q in examQuestions" :key="q.id">
                  <td>{{ q.id }}</td>
                  <td>{{ q.question_type }}</td>
                  <td>{{ q.points }}</td>
                  <td style="max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">
                    {{ q.requirement }}
                  </td>
                  <td>
                    <span class="badge" :class="q.expected_result_status">
                      {{ q.expected_result_status }}
                    </span>
                  </td>
                  <td>
                    <div style="display: flex; gap: 4px;">
                      <button class="btn-sm" @click="editQuestion(q)">Edit</button>
                      <button class="btn-sm btn-danger" @click="deleteQuestion(q.id)">Delete</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>

    <div v-show="activeTab === 'exams'" class="lecturer-form card">
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
    </div>

    <div v-show="activeTab === 'assignments'" class="lecturer-form card">
      <h2>Assign Students to Exam</h2>
      <div class="form-grid">
        <div class="input-group">
          <label>Select Exam:</label>
          <select v-model.number="selectedExamId">
            <option :value="null">-- Select Exam --</option>
            <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }} (ID: {{ e.id }})</option>
          </select>
        </div>
        <div class="input-group full-width">
          <label>Select Students ({{ selectedStudentIds.length }} selected):</label>
          <div class="multi-select-box">
            <div v-for="s in studentsList" :key="s.id" class="student-item">
              <input type="checkbox" :id="'std-' + s.id" :value="s.id" v-model="selectedStudentIds" />
              <label :for="'std-' + s.id">{{ s.username }}</label>
            </div>
            <div v-if="studentsList.length === 0" class="empty-msg">No students found.</div>
          </div>
        </div>
      </div>
      <button @click="assignStudents" :disabled="!selectedExamId || selectedStudentIds.length === 0" class="primary-btn" style="width: auto; padding: 0.5rem 1rem;">
        Assign {{ selectedStudentIds.length }} Students
      </button>

      <details class="import-roster-panel">
        <summary>Import Roster</summary>
        <BulkImportStudents />
      </details>
    </div>

    <div v-show="activeTab === 'results'" class="lecturer-form card">
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: var(--space-4);">
        <h2>Consolidated Student Performance</h2>
        <button @click="exportCSV" class="secondary-btn" style="margin: 0;">Export Scores (CSV)</button>
      </div>

      <div v-if="loadingScores" class="loading-state">Loading scores...</div>
      <div v-else class="preview-table-wrap">
        <table class="preview-table">
          <thead>
            <tr>
              <th>Std #</th>
              <th>Username</th>
              <th>Name</th>
              <th>Sec (Lec/Lab)</th>
              <th v-for="title in allExamTitles" :key="title">{{ title }}</th>
              <th>Practice</th>
              <th>Total</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in consolidatedScores" :key="s.student_id">
              <td>{{ s.student_number || '—' }}</td>
              <td>{{ s.username }}</td>
              <td>{{ s.first_name }} {{ s.last_name }}</td>
              <td>{{ s.sec_lec }} / {{ s.sec_lab }}</td>
              <td v-for="title in allExamTitles" :key="title">
                {{ s.exam_scores[title] !== undefined ? s.exam_scores[title].toFixed(2) : '0.00' }}
              </td>
              <td style="font-weight: 800;">{{ s.practice_total.toFixed(2) }}</td>
              <td style="font-weight: 900; color: var(--color-primary);">{{ s.grand_total.toFixed(2) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.card {
  background: var(--color-surface);
  padding: var(--space-6);
  border: 2px solid var(--color-border);
  border-radius: 0;
}

.context-selector {
  display: flex;
  justify-content: space-between;
  align-items: flex-end;
  gap: var(--space-4);
  padding-bottom: var(--space-4);
  border-bottom: 2px solid var(--color-border);
  margin-bottom: var(--space-4);
}

.context-selector .input-group {
  flex: 1;
}

.exam-context-status {
  display: flex;
  align-items: center;
  gap: var(--space-4);
}

.status-info {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.status-label {
  font-size: 0.75rem;
  font-weight: 700;
  margin-bottom: 4px;
}

.toggle-btn {
  white-space: nowrap;
}

.empty-state {
  padding: var(--space-10) 0;
  text-align: center;
  background: var(--color-surface);
  border: 2px dashed var(--color-border);
  font-weight: 700;
  font-size: 1.1rem;
}

.desc {
  color: var(--color-text-muted);
  font-weight: 700;
  margin-bottom: var(--space-4);
  font-size: 0.9rem;
}

.multi-select-box {
  max-height: 250px;
  overflow-y: auto;
  border: 2px solid var(--color-border);
  padding: var(--space-3);
  background: var(--color-surface);
}

.student-item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 0.5rem 0;
  border-bottom: 1px solid var(--color-border);
}

.student-item:last-child { border-bottom: none; }

.student-item label {
  margin-bottom: 0;
  cursor: pointer;
  font-weight: 700;
}

.secondary-btn {
  padding: var(--space-2) var(--space-4);
  background-color: var(--color-surface);
  color: var(--color-text);
  border: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  margin-bottom: var(--space-4);
  transition: none;
}

.secondary-btn:hover {
  background-color: var(--color-surface-muted);
}

.secondary-btn:active {
  transform: translateY(1px);
}

.success-btn {
  background-color: var(--color-success-bg);
  color: var(--color-success);
  border: 2px solid var(--color-success);
  padding: 0.5rem var(--space-4);
  cursor: pointer;
  font-weight: 700;
  transition: none;
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

.success-btn:active, .fail-btn:active {
  transform: translateY(1px);
}

.exam-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--space-3);
  border: 2px solid var(--color-border);
  margin-bottom: var(--space-3);
  background: var(--color-surface);
}

.exam-list {
  margin-top: var(--space-6);
}

.import-roster-panel {
  border: 2px solid var(--color-border);
  padding: var(--space-5);
  margin-top: var(--space-6);
  background: var(--color-surface);
}

.import-roster-panel summary {
  cursor: pointer;
  font-weight: 800;
  color: var(--color-text);
  font-size: 1.1rem;
}

.lecturer-form {
  background: var(--color-surface);
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-5);
}

.input-group {
  display: flex;
  flex-direction: column;
}

.full-width {
  grid-column: span 2;
}

label {
  font-weight: 700;
  margin-bottom: 0.4rem;
  font-size: 0.95rem;
  color: var(--color-text);
}

input, select, textarea {
  padding: 0.6rem;
  border: 2px solid var(--color-border);
  border-radius: 0;
  font-weight: 600;
  font-family: var(--font-mono);
  background: var(--color-surface);
  color: var(--color-text);
}

textarea {
  min-height: 120px;
}

.strict-mode-section {
  border-top: 2px solid var(--color-border);
  padding-top: var(--space-5);
}

.checkbox-group {
  display: flex;
  gap: var(--space-6);
}

.checkbox-group label {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-weight: 700;
  cursor: pointer;
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

.capture-status {
  margin-top: var(--space-4);
  padding: 1rem var(--space-5);
  border: 2px solid var(--color-border);
  font-weight: 700;
}
.capture-status.captured { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.capture-status.failed { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }

.preview-block {
  margin-top: var(--space-4);
  padding: 1rem var(--space-5);
  border: 2px solid var(--color-border);
}
.preview-block.ok { background: var(--color-surface); color: var(--color-text); }
.preview-block.error { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
.preview-error { margin-top: 0.5rem; font-family: var(--font-mono); font-size: 0.85rem; white-space: pre-wrap; font-weight: 700; }
.preview-table-wrap { margin-top: 1rem; overflow-x: auto; border: 2px solid var(--color-border); }
.preview-table { width: 100%; border-collapse: collapse; font-size: 0.9rem; background: var(--color-surface); color: var(--color-text); }
.preview-table th, .preview-table td { border: 1px solid var(--color-border); padding: 0.5rem 0.8rem; text-align: left; }
.preview-table th { background: var(--color-surface-muted); color: var(--color-text); font-weight: 700; }

.badge {
  padding: 4px 10px;
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-size: 0.75rem;
  font-weight: 700;
  display: inline-block;
}
.badge.captured { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.badge.failed { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
.badge.pending { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }

.btn-sm {
  padding: 4px 12px;
  font-size: 0.8rem;
  font-weight: 700;
  cursor: pointer;
  border: 2px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
}
.btn-sm:hover { background: var(--color-surface-muted); }
.btn-sm:active { transform: translateY(1px); }

.btn-danger {
  background: var(--color-danger);
  color: var(--color-surface);
  border-color: var(--color-danger);
}

h2 {
  color: var(--color-text);
  font-weight: 800;
  margin: 0;
  letter-spacing: -0.01em;
  font-size: 1.5rem;
}

.tab-bar {
  display: flex;
  gap: 0;
  background: var(--color-border);
  padding: 0;
  margin-bottom: var(--space-6);
  border: 2px solid var(--color-border);
}
.tab-bar button {
  background: var(--color-surface);
  border: none;
  border-right: 2px solid var(--color-border);
  padding: var(--space-3) var(--space-4);
  cursor: pointer;
  color: var(--color-text);
  font-size: 0.95rem;
  font-weight: 700;
  flex: 1;
  transition: none;
}
.tab-bar button:last-child { border-right: none; }
.tab-bar button:hover { background: var(--color-surface-muted); }
.tab-bar button.tab-active {
  background: var(--color-primary);
  color: var(--color-surface);
}
</style>
