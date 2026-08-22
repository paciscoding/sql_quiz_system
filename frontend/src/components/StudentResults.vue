<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import ResultTable from './ResultTable.vue'
import EmptyState from './EmptyState.vue'
import Skeleton from './Skeleton.vue'
import { handleApiError, toApiError } from '../services/error_handler'
import { formatDate, formatRelative } from '../utils/format_date'

interface ExamResult {
  exam_id: string
  exam_title: string
  is_active: boolean
  question_count: number
  answered_count: number
  correct_count: number
  total_score: number
  max_score: number
}

interface PracticeSummary {
  question_id: string
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
    score: number
    submitted_at: string
    validation_details: any
  }
}

interface ExamDetails {
  exam_id: string
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
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam results' })
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
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load practice summary' })
  } finally {
    loadingPractice.value = false
  }
}

interface StudentExamResultRow {
  question_id: number
  requirement: string
  type: string
  points: number
  sql_text: string
  is_correct: boolean
  score: number
  submitted_at: string
  validation_details: any
}

const fetchExamDetails = async (examId: string) => {
  loadingDetails.value = true
  view.value = 'details'
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/quiz/exams/${examId}/results`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      const rows: StudentExamResultRow[] = (await response.json()) || []
      const examMeta = exams.value.find(e => e.exam_id === examId)
      selectedExam.value = {
        exam_id: examId,
        exam_title: examMeta?.exam_title ?? '',
        questions: rows.map((r): QuestionDetail => ({
          question_id: r.question_id,
          requirement: r.requirement,
          question_type: r.type,
          points: r.points,
          submission: r.submitted_at
            ? {
                sql_text: r.sql_text,
                is_correct: r.is_correct,
                score: r.score,
                submitted_at: r.submitted_at,
                validation_details: r.validation_details
              }
            : undefined
        }))
      }
    } else {
      throw await toApiError(response, 'Failed to load exam details')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load exam details' })
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

const getColumns = (rows: any[]) => {
  if (!rows || rows.length === 0) return []
  return Object.keys(rows[0])
}

// Translate a (scored, max) pair into the CMS-spec score class:
//  - 0          → score-zero    (translucent red)
//  - (0, max)   → score-partial (translucent amber)
//  - == max     → score-full    (translucent green)
//  - undefined  → score-pending (grey)
function scoreClass(scored: number | null | undefined, max: number | null | undefined): string {
  if (scored == null || max == null || max <= 0) return 'score-pending'
  if (scored <= 0) return 'score-zero'
  if (scored >= max) return 'score-full'
  return 'score-partial'
}
</script>

<template>
  <div class="results-container">
    <div v-if="view === 'list'">
      <div class="page-header">
        <h1>Submissions &amp; Results</h1>
        <p class="page-subtitle">All your graded examination and practice submissions.</p>
      </div>

      <section class="overall-summary well">
        <h2 style="margin-bottom: var(--space-3);">Performance Overview</h2>
        <div class="summary-grid">
          <div class="summary-item">
            <span class="muted small">Exam Score</span>
            <p class="summary-val tabular">{{ totalExamScore.toFixed(2) }}</p>
          </div>
          <div class="summary-item">
            <span class="muted small">Practice Points</span>
            <p class="summary-val tabular">{{ totalPracticePoints.toFixed(2) }}</p>
          </div>
          <div class="summary-item highlight">
            <span class="muted small">Grand Total</span>
            <p class="summary-val tabular">{{ grandTotal.toFixed(2) }}</p>
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
        <Skeleton v-if="loadingExams" variant="table-row" :rows="3" :cols="4" />

        <EmptyState v-else-if="!loadingExams && exams.length === 0" title="No exam attempts yet" body="Once you submit an exam answer, results appear here." />

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
                <!-- max_score comes from /quiz/results ExamResultSummary.MaxScore, populated by GetStudentResultsSummary query -->
                <span class="score-pill" :class="scoreClass(exam.total_score, exam.max_score)">
                  {{ exam.total_score }} / {{ exam.max_score }}
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Practice Summary View -->
      <div v-else-if="activeTab === 'practice'">
        <Skeleton v-if="loadingPractice" variant="table-row" :rows="3" :cols="4" />
        <EmptyState v-else-if="!loadingPractice && practiceSummary.length === 0" title="No practice attempts yet" body="Head to the Practice page to start." />
        <div v-else class="card table-card sticky-table-head">
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
      <div class="page-header" style="display:flex; align-items:flex-end; justify-content:space-between; gap:var(--space-3);">
        <div>
          <h1>{{ selectedExam.exam_title }}</h1>
          <p class="page-subtitle">Per-question grading breakdown.</p>
        </div>
        <button class="btn back-btn" @click="goBack">&larr; Back to list</button>
      </div>

      <div v-if="loadingDetails" class="loading-state">
        <p>Loading question details...</p>
      </div>

      <div v-else class="details-content">
        <div class="card table-card sticky-table-head">
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
                  <td class="tabular">{{ q.points }}</td>
                  <td
                    class="score-cell"
                    :class="q.submission
                      ? scoreClass(q.submission.score, q.points)
                      : 'score-pending'"
                  >
                    <template v-if="q.submission">
                      {{ q.submission.score }} / {{ q.points }}
                    </template>
                    <template v-else>&mdash;</template>
                  </td>
                  <td class="sql-cell">
                    <code v-if="q.submission">{{ q.submission.sql_text }}</code>
                    <span v-else class="muted">&mdash;</span>
                  </td>
                  <td><template v-if="q.submission"><span :title="new Date(q.submission.submitted_at).toISOString()">{{ formatDate(q.submission.submitted_at) }} <span class="muted small">({{ formatRelative(q.submission.submitted_at) }})</span></span></template><template v-else>&mdash;</template></td>
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
.results-container { /* sits inside .cms-main, which provides the panel */ }

.overall-summary {
  margin-bottom: var(--space-5);
}

.overall-summary h2 {
  font-size: 1.05rem;
  margin-bottom: var(--space-3);
  font-weight: 700;
  color: var(--color-text);
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: var(--space-4);
}

.summary-item {
  padding: var(--space-4);
  border: 1px solid var(--color-border-soft);
  background: var(--color-surface);
  border-radius: var(--radius);
}

.summary-item.highlight {
  background: var(--color-primary-bg);
  border-color: var(--color-primary);
}

.summary-val {
  margin: 4px 0 0 0;
  font-size: 1.55rem;
  font-weight: 700;
  font-family: var(--font-mono);
  color: var(--color-text);
  line-height: 1;
}

.tab-bar {
  display: inline-flex;
  gap: 0;
  background: var(--color-surface-muted);
  padding: 3px;
  margin-bottom: var(--space-5);
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius);
}

.tab-bar button {
  padding: 6px 16px;
  background: transparent;
  border: 1px solid transparent;
  font-weight: 600;
  cursor: pointer;
  font-size: 0.86rem;
  color: var(--color-text-muted);
  border-radius: var(--radius-sm);
}
.tab-bar button:hover { color: var(--color-text); }
.tab-bar button.active {
  background: var(--color-surface);
  color: var(--color-primary);
  border-color: var(--color-border);
  box-shadow: var(--shadow-1);
}

.exam-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: var(--space-5);
}

.exam-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  padding: var(--space-4);
  box-shadow: var(--shadow-1);
  transition: transform .12s ease, box-shadow .12s ease, border-color .12s ease;
}

.exam-card.clickable:hover {
  cursor: pointer;
  border-color: var(--color-primary);
  box-shadow: var(--shadow-2);
  transform: translateY(-1px);
}

.exam-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: var(--space-2);
  border-bottom: 1px solid var(--color-border-soft);
  padding-bottom: var(--space-2);
}

.exam-card-header h3 {
  margin: 0;
  font-size: 0.98rem;
  font-weight: 700;
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
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
}

.requirement-cell {
  max-width: 400px;
  white-space: pre-wrap;
  font-size: 0.85rem;
  color: var(--color-text);
}

.sql-cell code {
  display: block;
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  background: var(--color-surface-sunk);
  padding: 2px 8px;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius-sm);
  font-size: 0.78rem;
  font-family: var(--font-mono);
  color: var(--color-text);
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
  border: 1px solid var(--color-border);
  padding: 6px 12px;
  font-weight: 600;
  font-size: 0.84rem;
  background: var(--color-surface);
  color: var(--color-text);
  cursor: pointer;
  border-radius: var(--radius);
  flex-shrink: 0;
}
.back-btn:hover { background: var(--color-surface-muted); }

@media (max-width: 768px) {
  .view-header {
    flex-direction: column;
    align-items: flex-start;
    gap: var(--space-4);
  }
}
</style>
