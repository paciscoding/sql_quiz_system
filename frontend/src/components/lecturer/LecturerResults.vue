<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import type { Examination, RosterStudent } from './types'
import { handleApiError, toApiError } from '../../services/error_handler'
import Skeleton from '../Skeleton.vue'
import EmptyState from '../EmptyState.vue'
import Spinner from '../Spinner.vue'
import { toCsv, downloadCsv } from '../../utils/csv'

interface ConsolidatedScore {
  student_id: string
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

interface DrillSubmission {
  student_id: string
  question_id: number
  is_correct: boolean
  score: number
}

// Mirrors backend models.LecturerPracticeResultRow (ARCH-006 frozen DTO).
// Typed-id fields (student_id, question_id, category_id) are opaque string
// tokens on the wire — never parsed as numbers.
interface LecturerPracticeResultRow {
  student_id: string
  student_number: string
  username: string
  first_name: string
  last_name: string
  sec_lec: string
  sec_lab: string
  question_id: string
  category_id: string
  category_name: string
  question_title: string
  requirement: string
  points: number
  is_solved: boolean
  attempt_count: number
  earned_points: number
}

// Independent exams list for the drill-down <select>
const exams = ref<Examination[]>([])

const fetchExams = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/exams', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      exams.value = (await response.json()) || []
    } else {
      throw await toApiError(response, 'Failed to fetch exams')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to fetch exams' })
  }
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
      handleApiError(new Error('Failed to fetch scores'), { fallback: 'Failed to fetch scores' })
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to fetch scores' })
  } finally {
    loadingScores.value = false
  }
  // Also fetch per-exam max scores for the denominators
  fetchExamMaxScores()
}

const examMaxScores = ref<Record<string, number>>({})

const fetchExamMaxScores = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const resp = await fetch('/api/v1/lecturer/exams/max-scores', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (resp.ok) {
      const data = await resp.json()
      examMaxScores.value = data.max_scores || {}
    }
  } catch (err) {
    // Non-fatal: just display '?' for max
  }
}

// Exam and practice scores are exported as two SEPARATE CSVs, built client-side
// from consolidatedScores (already fetched over an authenticated fetch). This
// avoids the previous window.open('?token=') download, which could not send the
// Authorization header and 401'd ("Authorization header required").
const IDENTITY_HEADER = ['StudentNumber', 'Username', 'FirstName', 'LastName', 'SecLec', 'SecLab']

const identityCells = (s: ConsolidatedScore) =>
  [s.student_number, s.username, s.first_name, s.last_name, s.sec_lec, s.sec_lab]

const exportExamCSV = () => {
  const titles = allExamTitles.value
  const header = [...IDENTITY_HEADER, ...titles, 'ExamTotal']
  const rows = consolidatedScores.value.map(s => {
    const examCells = titles.map(t => (s.exam_scores[t] ?? 0).toFixed(2))
    const examTotal = titles.reduce((acc, t) => acc + (s.exam_scores[t] ?? 0), 0)
    return [...identityCells(s), ...examCells, examTotal.toFixed(2)]
  })
  downloadCsv('exam_scores.csv', toCsv(header, rows))
}

const exportPracticeCSV = () => {
  const header = [...IDENTITY_HEADER, 'PracticeTotal']
  const rows = consolidatedScores.value.map(s =>
    [...identityCells(s), s.practice_total.toFixed(2)]
  )
  downloadCsv('practice_scores.csv', toCsv(header, rows))
}

const allExamTitles = computed(() => {
  const titles = new Set<string>()
  consolidatedScores.value.forEach(s => {
    Object.keys(s.exam_scores).forEach(t => titles.add(t))
  })
  return Array.from(titles).sort()
})

// --- Practice Breakdown (ARCH-006 / ARCH-007) ---
const practiceResults = ref<LecturerPracticeResultRow[]>([])
const loadingPracticeResults = ref(false)

const fetchPracticeResults = async () => {
  loadingPracticeResults.value = true
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/lecturer/practice/results', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) {
      practiceResults.value = await response.json()
    } else {
      throw await toApiError(response, 'Failed to fetch practice breakdown')
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to fetch practice breakdown' })
  } finally {
    loadingPracticeResults.value = false
  }
}

// Sorted by student (number, then username), then category, matching the
// backend's deterministic ORDER BY (ARCH-006).
const sortedPracticeResults = computed(() => {
  return [...practiceResults.value].sort((a, b) => {
    return (a.student_number || '').localeCompare(b.student_number || '')
      || a.username.localeCompare(b.username)
      || a.category_name.localeCompare(b.category_name)
      || a.question_title.localeCompare(b.question_title)
  })
})

const PRACTICE_BREAKDOWN_HEADER = [
  'StudentNumber', 'Username', 'FirstName', 'LastName', 'SecLec', 'SecLab',
  'Category', 'Question', 'Points', 'Solved', 'Attempts', 'Earned'
]

const practiceQuestionLabel = (r: LecturerPracticeResultRow) => r.question_title || r.requirement

const exportPracticeBreakdownCSV = () => {
  const rows = sortedPracticeResults.value.map(r => [
    r.student_number, r.username, r.first_name, r.last_name, r.sec_lec, r.sec_lab,
    r.category_name, practiceQuestionLabel(r), r.points.toFixed(2),
    r.is_solved ? 'Yes' : 'No', r.attempt_count, r.earned_points.toFixed(2)
  ])
  downloadCsv('practice_breakdown.csv', toCsv(PRACTICE_BREAKDOWN_HEADER, rows))
}

// --- Results drill-down ---
const resultsExamId = ref<string | null>(null)
const drillLoading = ref(false)
const drillQuestions = ref<any[]>([])
const drillSubmissions = ref<DrillSubmission[]>([])
const drillRoster = ref<RosterStudent[]>([])

const drillQuestionStats = computed(() => {
  return drillQuestions.value.map(q => {
    const subs = drillSubmissions.value.filter(s => s.question_id === q.id)
    const attempts = subs.length
    const correctStudents = new Set(subs.filter(s => s.is_correct).map(s => s.student_id)).size
    // Average of each distinct student's BEST score (not all attempts)
    const bestByStudent = new Map<string, number>()
    subs.forEach(s => {
      const prev = bestByStudent.get(s.student_id) ?? 0
      bestByStudent.set(s.student_id, Math.max(prev, s.score || 0))
    })
    const distinctStudents = bestByStudent.size
    const avgScore = distinctStudents > 0
      ? Array.from(bestByStudent.values()).reduce((acc, v) => acc + v, 0) / distinctStudents
      : 0
    return { id: q.id, requirement: q.requirement, attempts, correctStudents, avgScore }
  })
})

const drillNotSubmitted = computed(() => {
  const submittedIds = new Set(drillSubmissions.value.map(s => s.student_id))
  return drillRoster.value.filter(s => !submittedIds.has(s.student_id))
})

async function fetchDrillData(examId: string) {
  drillLoading.value = true
  drillQuestions.value = []
  drillSubmissions.value = []
  drillRoster.value = []
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const [qResp, sResp, rResp] = await Promise.all([
      fetch(`/api/v1/lecturer/exams/${examId}/questions`, { headers: { 'Authorization': `Bearer ${token}` } }),
      fetch(`/api/v1/lecturer/exams/${examId}/submissions`, { headers: { 'Authorization': `Bearer ${token}` } }),
      fetch(`/api/v1/lecturer/exams/${examId}/students`, { headers: { 'Authorization': `Bearer ${token}` } })
    ])
    if (qResp.ok) {
      drillQuestions.value = await qResp.json()
    }
    if (sResp.ok) {
      drillSubmissions.value = await sResp.json()
    }
    if (rResp.ok) {
      const rData = await rResp.json()
      drillRoster.value = rData.students || []
    }
  } catch (err) {
    handleApiError(err, { fallback: 'Failed to load drill-down data' })
  } finally {
    drillLoading.value = false
  }
}

watch(resultsExamId, (id) => {
  if (id !== null) {
    fetchDrillData(id)
  } else {
    drillQuestions.value = []
    drillSubmissions.value = []
    drillRoster.value = []
  }
})

onMounted(() => {
  fetchExams()
  fetchScores()
  fetchPracticeResults()
})
</script>

<template>
  <div class="lecturer-form card">
    <div class="results-header">
      <h2>Consolidated Student Performance</h2>
      <div class="export-actions">
        <button @click="exportExamCSV" class="secondary-btn" style="margin: 0;" :disabled="consolidatedScores.length === 0">Export Exam Scores (CSV)</button>
        <button @click="exportPracticeCSV" class="secondary-btn" style="margin: 0;" :disabled="consolidatedScores.length === 0">Export Practice Scores (CSV)</button>
        <button @click="exportPracticeBreakdownCSV" class="secondary-btn" style="margin: 0;" :disabled="practiceResults.length === 0">Export Practice Breakdown (CSV)</button>
      </div>
    </div>

    <!-- Exam filter for drill-down -->
    <div class="input-group" style="max-width: 400px;">
      <label>View:</label>
      <select v-model="resultsExamId">
        <option :value="null">All exams (consolidated)</option>
        <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }} (ID: {{ e.id }})</option>
      </select>
    </div>

    <!-- Consolidated view (when no specific exam selected) -->
    <template v-if="resultsExamId === null">
      <Skeleton v-if="loadingScores" variant="table-row" :rows="5" :cols="6" />
      <EmptyState v-else-if="!loadingScores && consolidatedScores.length === 0" title="No scores yet" body="Once students submit answers, their scores appear here." />
      <div v-else class="preview-table-wrap sticky-table-head">
        <table class="preview-table">
          <thead>
            <tr>
              <th>Std #</th>
              <th>Username</th>
              <th>Name</th>
              <th>Sec (Lec/Lab)</th>
              <th v-for="title in allExamTitles" :key="title">
                {{ title }}<br /><span class="muted" style="font-size: 0.75rem; font-weight: 400;">(max: {{ examMaxScores[title] !== undefined ? examMaxScores[title].toFixed(0) : '?' }})</span>
              </th>
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
                <span>{{ (s.exam_scores[title] ?? 0).toFixed(2) }} / {{ examMaxScores[title] !== undefined ? examMaxScores[title].toFixed(0) : '?' }}</span>
                <br />
                <span class="muted" style="font-size: 0.8rem;">
                  {{ examMaxScores[title] && examMaxScores[title] > 0 ? Math.round(((s.exam_scores[title] ?? 0) / examMaxScores[title]) * 100) + '%' : '—' }}
                </span>
              </td>
              <td style="font-weight: 800;">{{ s.practice_total.toFixed(2) }}</td>
              <td style="font-weight: 900; color: var(--color-primary);">{{ s.grand_total.toFixed(2) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- Per-exam drill-down view -->
    <template v-else>
      <Spinner v-if="drillLoading" inline />
      <template v-else>
        <!-- Per-question difficulty table -->
        <div style="margin-top: var(--space-4);">
          <h3 style="margin-bottom: var(--space-2);">Question Difficulty</h3>
          <EmptyState v-if="drillQuestions.length === 0" :compact="true" title="No questions" body="This exam has no questions yet." />
          <div v-else class="preview-table-wrap sticky-table-head">
            <table class="preview-table">
              <thead>
                <tr>
                  <th>Q#</th>
                  <th>Requirement</th>
                  <th>Attempts</th>
                  <th>Students Correct</th>
                  <th>Avg Score</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="qs in drillQuestionStats" :key="qs.id">
                  <td>{{ qs.id }}</td>
                  <td style="max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;">{{ qs.requirement }}</td>
                  <td>{{ qs.attempts }}</td>
                  <td>{{ qs.correctStudents }}</td>
                  <td>{{ qs.avgScore.toFixed(2) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <!-- Not-yet-submitted students -->
        <div style="margin-top: var(--space-4);">
          <h3 style="margin-bottom: var(--space-2);">Not Yet Submitted</h3>
          <EmptyState v-if="drillNotSubmitted.length === 0 && drillRoster.length > 0" :compact="true" title="All students have submitted" body="Every enrolled student has at least one submission." />
          <EmptyState v-else-if="drillRoster.length === 0" :compact="true" title="No enrolled students" body="Enroll students from the Exam Roster tab." />
          <div v-else-if="drillNotSubmitted.length > 0" class="preview-table-wrap sticky-table-head">
            <table class="preview-table">
              <thead>
                <tr>
                  <th>Student #</th>
                  <th>Name</th>
                  <th>Sec (Lec/Lab)</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="s in drillNotSubmitted" :key="s.student_id">
                  <td>{{ s.student_number || s.username }}</td>
                  <td>{{ s.first_name }} {{ s.last_name }}</td>
                  <td>{{ s.sec_lec }} / {{ s.sec_lab }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </template>

    <!-- Practice Breakdown: per-student/per-question practice results (ARCH-007) -->
    <div style="margin-top: var(--space-4);">
      <h3 style="margin-bottom: var(--space-2);">Practice Breakdown</h3>
      <Skeleton v-if="loadingPracticeResults" variant="table-row" :rows="5" :cols="6" />
      <EmptyState v-else-if="!loadingPracticeResults && practiceResults.length === 0" title="No practice attempts yet" body="Once students attempt practice questions, their per-question breakdown appears here." />
      <div v-else class="preview-table-wrap sticky-table-head">
        <table class="preview-table">
          <thead>
            <tr>
              <th>Std #</th>
              <th>Username</th>
              <th>Name</th>
              <th>Sec (Lec/Lab)</th>
              <th>Category</th>
              <th>Question</th>
              <th>Points</th>
              <th>Solved</th>
              <th>Attempts</th>
              <th>Earned</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in sortedPracticeResults" :key="`${r.student_id}-${r.question_id}`">
              <td>{{ r.student_number || '—' }}</td>
              <td>{{ r.username }}</td>
              <td>{{ r.first_name }} {{ r.last_name }}</td>
              <td>{{ r.sec_lec }} / {{ r.sec_lab }}</td>
              <td>{{ r.category_name }}</td>
              <td>{{ practiceQuestionLabel(r) }}</td>
              <td>{{ r.points.toFixed(2) }}</td>
              <td>{{ r.is_solved ? 'Yes' : 'No' }}</td>
              <td>{{ r.attempt_count }}</td>
              <td style="font-weight: 800;">{{ r.earned_points.toFixed(2) }}</td>
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
  padding: var(--space-5);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow-1);
  margin-bottom: var(--space-4);
}

.lecturer-form {
  background: var(--color-surface);
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.results-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--space-4);
}

.export-actions {
  display: flex;
  gap: var(--space-3);
  flex-wrap: wrap;
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

select {
  padding: 0.6rem;
  border: 2px solid var(--color-border);
  border-radius: 0;
  font-weight: 600;
  font-family: var(--font-mono);
  background: var(--color-surface);
  color: var(--color-text);
}

h2 {
  color: var(--color-text);
  font-weight: 700;
  margin: 0 0 var(--space-3) 0;
  font-size: 1.1rem;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border-soft);
}

.preview-table-wrap { margin-top: 10px; overflow-x: auto; border: 1px solid var(--color-border); border-radius: var(--radius); background: var(--color-surface); }
.preview-table { width: 100%; border-collapse: collapse; font-size: 0.86rem; background: var(--color-surface); color: var(--color-text); }
.preview-table th, .preview-table td { border-bottom: 1px solid var(--color-border-soft); padding: 6px 10px; text-align: left; }
.preview-table th { background: var(--color-surface-sunk); color: var(--color-text); font-weight: 700; font-size: 0.74rem; text-transform: uppercase; letter-spacing: 0.04em; }
.preview-table tr:nth-child(even) td { background: var(--color-surface-muted); }

@media (max-width: 767.98px) {
  .results-header {
    flex-direction: column;
    align-items: stretch;
  }
}
</style>
