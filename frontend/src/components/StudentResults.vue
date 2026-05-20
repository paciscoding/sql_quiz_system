<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import ResultTable from './ResultTable.vue'
import { useNotifications } from '../services/notification_service'

const { notify } = useNotifications()

interface ExamResult {
  exam_id: number
  exam_title: string
  is_active: boolean
  question_count: number
  answered_count: number
  correct_count: number
  total_score: number
  max_score: number
}

interface PracticeSummary {
  question_id: number
  category: string
  requirement: string
  points: number
  is_solved: boolean
  attempt_count: number
}

interface QuestionDetail {
  question_id: number
  requirement: string
  question_type: string
  points: number
  submission?: {
    sql_text: string
    is_correct: boolean
    submitted_at: string
    validation_details: any
  }
}

interface ExamDetails {
  exam_id: number
  exam_title: string
  questions: QuestionDetail[]
}

const exams = ref<ExamResult[]>([])
const practiceSummary = ref<PracticeSummary[]>([])
const loadingExams = ref(false)
const loadingPractice = ref(false)
const activeTab = ref<'exams' | 'practice'>('exams')

const view = ref<'list' | 'details'>('list')
const selectedExam = ref<ExamDetails | null>(null)
const loadingDetails = ref(false)

const fetchExams = async () => {
  loadingExams.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/quiz/results', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      exams.value = await response.json()
    }
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loadingExams.value = false
  }
}

const fetchPracticeSummary = async () => {
  loadingPractice.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/practice/summary', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      practiceSummary.value = await response.json()
    }
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loadingPractice.value = false
  }
}

const fetchExamDetails = async (examId: number) => {
  loadingDetails.value = true
  view.value = 'details'
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/quiz/results/${examId}`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      selectedExam.value = await response.json()
    }
  } catch (err: any) {
    notify(err.message, 'error')
  } finally {
    loadingDetails.value = false
  }
}

const goBack = () => {
  view.value = 'list'
  selectedExam.value = null
}

const totalExamScore = computed(() => exams.value.reduce((acc, e) => acc + e.total_score, 0))
const totalPracticePoints = computed(() => practiceSummary.value.reduce((acc, p) => acc + (p.is_solved ? p.points : 0), 0))
const grandTotal = computed(() => totalExamScore.value + totalPracticePoints.value)

onMounted(() => {
  fetchExams()
  fetchPracticeSummary()
})

const formatDate = (dateStr: string) => {
  return new Date(dateStr).toLocaleString()
}

const getColumns = (rows: any[]) => {
  if (!rows || rows.length === 0) return []
  return Object.keys(rows[0])
}
</script>

<template>
  <div class="results-container">
    <div v-if="view === 'list'">
      <header class="view-header">
        <h1>Your Results</h1>
      </header>

      <section class="overall-summary">
        <h2>Performance Overview</h2>
        <div class="summary-grid">
          <div class="summary-item">
            <span class="muted small">Exam Score</span>
            <p class="summary-val">{{ totalExamScore.toFixed(2) }}</p>
          </div>
          <div class="summary-item">
            <span class="muted small">Practice Points</span>
            <p class="summary-val">{{ totalPracticePoints.toFixed(2) }}</p>
          </div>
          <div class="summary-item highlight">
            <span class="muted small">Grand Total</span>
            <p class="summary-val">{{ grandTotal.toFixed(2) }}</p>
          </div>
        </div>
      </section>

      <!-- Tabs -->
      <div class="tab-bar">
        <button 
          :class="{ active: activeTab === 'exams' }" 
          @click="activeTab = 'exams'"
        >
          Exams
        </button>
        <button 
          :class="{ active: activeTab === 'practice' }" 
          @click="activeTab = 'practice'"
        >
          Practice
        </button>
      </div>

      <!-- Exam List View -->
      <div v-if="activeTab === 'exams'">
        <div v-if="loadingExams" class="loading-state">
          <p>Loading results...</p>
        </div>

        <div v-else-if="exams.length === 0" class="empty-state card">
          <p class="muted">No exams enrolled yet.</p>
        </div>

        <div v-else class="exam-grid">
          <div 
            v-for="exam in exams" 
            :key="exam.exam_id" 
            class="card exam-card clickable"
            @click="fetchExamDetails(exam.exam_id)"
          >
            <div class="exam-card-header">
              <h3>{{ exam.exam_title }}</h3>
              <span 
                class="badge" 
                :class="exam.is_active ? 'badge-success' : 'badge-warning'"
              >
                {{ exam.is_active ? 'Active' : 'Closed' }}
              </span>
            </div>
            
            <div class="exam-stats">
              <div class="stat">
                <span class="muted small">Questions</span>
                <p>{{ exam.answered_count }} / {{ exam.question_count }}</p>
              </div>
              <div class="stat">
                <span class="muted small">Correct</span>
                <p>{{ exam.correct_count }} / {{ exam.question_count }}</p>
              </div>
              <div class="stat">
                <span class="muted small">Score</span>
                <p>{{ exam.total_score }} / {{ exam.max_score }} pts</p>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Practice Summary View -->
      <div v-else-if="activeTab === 'practice'">
        <div v-if="loadingPractice" class="loading-state">
          <p>Loading practice summary...</p>
        </div>
        <div v-else-if="practiceSummary.length === 0" class="empty-state card">
          <p class="muted">No practice questions found.</p>
        </div>
        <div v-else class="card table-card">
          <table class="data-table">
            <thead>
              <tr>
                <th>Category</th>
                <th>Requirement</th>
                <th>Points</th>
                <th>Solved</th>
                <th>Attempts</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in practiceSummary" :key="p.question_id">
                <td style="font-weight: 800; font-size: 0.75rem;">{{ p.category }}</td>
                <td class="requirement-cell">{{ p.requirement }}</td>
                <td>{{ p.points }}</td>
                <td>
                  <span class="badge" :class="p.is_solved ? 'badge-success' : 'badge-danger'">
                    {{ p.is_solved ? 'Yes' : 'No' }}
                  </span>
                </td>
                <td>{{ p.attempt_count }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- Drill-down Details View -->
    <div v-else-if="view === 'details' && selectedExam">
      <header class="view-header">
        <button class="btn back-btn" @click="goBack">
          &larr; Back to List
        </button>
        <h1>{{ selectedExam.exam_title }}</h1>
      </header>

      <div v-if="loadingDetails" class="loading-state">
        <p>Loading question details...</p>
      </div>

      <div v-else class="details-content">
        <div class="card table-card">
          <table class="data-table questions-table">
            <thead>
              <tr>
                <th>#</th>
                <th>Requirement</th>
                <th>Type</th>
                <th>Points</th>
                <th>Correct</th>
                <th>Your SQL</th>
                <th>Submitted At</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="q in selectedExam.questions" :key="q.question_id">
                <tr class="question-row">
                  <td>{{ q.question_id }}</td>
                  <td class="requirement-cell">{{ q.requirement }}</td>
                  <td>
                    <span class="badge" :class="q.question_type === 'QUERY' ? 'badge-info' : 'badge-warning'">
                      {{ q.question_type }}
                    </span>
                  </td>
                  <td>{{ q.points }}</td>
                  <td>
                    <template v-if="q.submission">
                      <span 
                        class="badge" 
                        :class="q.submission.is_correct ? 'badge-success' : 'badge-danger'"
                      >
                        {{ q.submission.is_correct ? 'Yes' : 'No' }}
                      </span>
                    </template>
                    <template v-else>&mdash;</template>
                  </td>
                  <td class="sql-cell">
                    <code v-if="q.submission">{{ q.submission.sql_text }}</code>
                    <span v-else class="muted">&mdash;</span>
                  </td>
                  <td>{{ q.submission ? formatDate(q.submission.submitted_at) : '&mdash;' }}</td>
                </tr>
                <!-- Validation Details Row -->
                <tr v-if="q.submission && q.submission.validation_details" class="details-row">
                  <td colspan="7">
                    <div class="validation-details">
                      <template v-if="Array.isArray(q.submission.validation_details)">
                        <h4 class="small muted">Validation Output</h4>
                        <ResultTable 
                          :rows="q.submission.validation_details" 
                          :columns="getColumns(q.submission.validation_details)" 
                        />
                      </template>
                      <template v-else>
                        <div v-if="q.submission.validation_details.actual_rows" class="detail-section">
                          <h4 class="small muted">Your Output</h4>
                          <ResultTable 
                            :rows="q.submission.validation_details.actual_rows" 
                            :columns="getColumns(q.submission.validation_details.actual_rows)" 
                          />
                        </div>
                        <div v-if="q.submission.validation_details.expected_rows" class="detail-section">
                          <h4 class="small muted">Expected Output</h4>
                          <ResultTable 
                            :rows="q.submission.validation_details.expected_rows" 
                            :columns="getColumns(q.submission.validation_details.expected_rows)" 
                          />
                        </div>
                        <div v-if="q.submission.validation_details.error" class="error-detail">
                          <h4 class="small muted">Error</h4>
                          <p style="color: var(--color-danger);">{{ q.submission.validation_details.error }}</p>
                        </div>
                      </template>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.results-container {
  max-width: 1200px;
  margin: var(--space-6) auto;
  padding: 0 var(--space-4);
}

.view-header {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-bottom: var(--space-6);
}

.view-header h1 {
  margin: 0;
  font-size: 1.75rem;
  letter-spacing: -0.01em;
  font-weight: 800;
  color: var(--color-text);
}

.overall-summary {
  margin-bottom: var(--space-6);
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  padding: var(--space-6);
}

.overall-summary h2 {
  font-size: 1.25rem;
  margin-bottom: var(--space-4);
  font-weight: 800;
  color: var(--color-text);
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-4);
}

.summary-item {
  padding: var(--space-4);
  border: 2px solid var(--color-border);
  background: var(--color-surface);
}

.summary-item.highlight {
  background: var(--color-surface-muted);
}

.summary-val {
  margin: 4px 0 0 0;
  font-size: 1.75rem;
  font-weight: 800;
  color: var(--color-text);
  line-height: 1;
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
  flex: 1;
  padding: var(--space-3);
  background: var(--color-surface);
  border: none;
  border-right: 2px solid var(--color-border);
  font-weight: 700;
  cursor: pointer;
  transition: none;
  font-size: 0.95rem;
  color: var(--color-text);
}
.tab-bar button:last-child { border-right: none; }

.tab-bar button.active {
  background: var(--color-primary);
  color: var(--color-surface);
}

.exam-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: var(--space-5);
}

.exam-card {
  transition: all 0.1s ease;
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  background: var(--color-surface);
  border: 2px solid var(--color-border);
  padding: var(--space-5);
}

.exam-card.clickable:hover {
  background-color: var(--color-surface-muted);
  cursor: pointer;
}

.exam-card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  border-bottom: 2px solid var(--color-border);
  padding-bottom: var(--space-2);
  margin-bottom: var(--space-1);
}

.exam-card-header h3 {
  margin: 0;
  font-size: 1.1rem;
  font-weight: 800;
  color: var(--color-text);
}

.exam-stats {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  gap: var(--space-4);
}

.stat p {
  margin: 2px 0 0 0;
  font-weight: 700;
  font-size: 1rem;
  color: var(--color-text);
}

.small {
  font-size: 0.75rem;
  font-weight: 700;
  color: var(--color-text-muted);
}

.table-card {
  padding: 0;
  overflow: auto;
  border-radius: 0;
  border: 2px solid var(--color-border);
}

.data-table {
  width: 100%;
  border-collapse: collapse;
}

.data-table th, .data-table td {
  border: 1px solid var(--color-border);
  padding: 0.6rem 0.8rem;
  text-align: left;
}

.data-table th {
  background: var(--color-surface-muted);
  color: var(--color-text);
  font-weight: 700;
}

.requirement-cell {
  max-width: 400px;
  white-space: pre-wrap;
  font-size: 0.85rem;
  font-weight: 600;
}

.sql-cell code {
  display: block;
  max-width: 250px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  background: var(--color-surface-muted);
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  font-size: 0.8rem;
  font-weight: 600;
  font-family: var(--font-mono);
}

.details-row td {
  padding: 0;
  background-color: var(--color-surface);
  border-bottom: 2px solid var(--color-border);
}

.validation-details {
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  border: none;
}

.detail-section h4 {
  margin: 0 0 var(--space-1) 0;
  color: var(--color-text);
  font-weight: 700;
  font-size: 0.9rem;
}

.back-btn {
  border: 2px solid var(--color-border);
  padding: var(--space-2) var(--space-4);
  font-weight: 700;
  background: var(--color-surface);
  color: var(--color-text);
  cursor: pointer;
}

.back-btn:active {
  transform: translateY(1px);
}

.badge {
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: 0;
  font-weight: 700;
  font-size: 0.7rem;
}
.badge-success { background: var(--color-success-bg); color: var(--color-success); border-color: var(--color-success); }
.badge-warning { background: var(--color-warning-bg); color: var(--color-warning); border-color: var(--color-warning); }
.badge-danger { background: var(--color-danger-bg); color: var(--color-danger); border-color: var(--color-danger); }
.badge-info { background: var(--color-primary); color: var(--color-surface); }

@media (max-width: 768px) {
  .view-header {
    flex-direction: column;
    align-items: flex-start;
    gap: var(--space-4);
  }
}
</style>
