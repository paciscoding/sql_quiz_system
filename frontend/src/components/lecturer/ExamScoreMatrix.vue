<script setup lang="ts">
import { computed } from 'vue'
import type {
  ExamMatrixQuestion,
  ExamMatrixStudent,
  ExamMatrixCell,
  ExamSubmissionRow
} from '../../utils/exam_matrix'
import {
  buildExamMatrix,
  buildExamRowAxis,
  examCellClass,
  examCellTooltip,
  examColumnLabel,
  examColumnTooltip
} from '../../utils/exam_matrix'
import { scoreClass } from '../../utils/score_class'
import Skeleton from '../Skeleton.vue'
import EmptyState from '../EmptyState.vue'

const props = defineProps<{
  students: ExamMatrixStudent[]
  questions: ExamMatrixQuestion[]
  submissions: ExamSubmissionRow[]
  loading?: boolean
}>()

const matrix = computed(() => buildExamMatrix(props.submissions))
const rows = computed(() => buildExamRowAxis(props.students, props.submissions))

const maxTotal = computed(() =>
  props.questions.reduce((sum, q) => sum + (q.points || 0), 0)
)

const rowCellsMap = computed(() => {
  const map = new Map<string, Array<ExamMatrixCell | undefined>>()
  const m = matrix.value
  for (const student of rows.value) {
    const studentMatrix = m.get(student.student_id)
    const cells = props.questions.map(q => studentMatrix?.get(q.id))
    map.set(student.student_id, cells)
  }
  return map
})

function rowTotal(studentId: string): number {
  const studentMatrix = matrix.value.get(studentId)
  if (!studentMatrix) return 0
  let total = 0
  for (const q of props.questions) {
    const cell = studentMatrix.get(q.id)
    if (cell) {
      total += cell.score
    }
  }
  return total
}

</script>

<template>
  <Skeleton v-if="loading" variant="table-row" :rows="8" :cols="10" />

  <EmptyState
    v-else-if="questions.length === 0"
    title="No questions in this exam"
    body="Add questions to this examination and, once students submit, their per-question scores will appear here."
  />

  <EmptyState
    v-else-if="rows.length === 0"
    title="No students enrolled"
    body="Enroll students from the Exam Roster tab to see their per-question scores here."
  />

  <div v-else>
    <div
      class="responsive-table-wrap sticky-table-head esm-wrap"
      :style="{ '--sticky-max-h': '70vh' }"
    >
      <table class="data-table tabular esm-table">
        <thead>
          <tr>
            <th class="esm-id" scope="col">Student</th>
            <th
              v-for="(q, i) in questions"
              :key="q.id"
              class="esm-q"
              scope="col"
              :title="examColumnTooltip(q, i)"
            >
              <div class="esm-q-label">{{ examColumnLabel(q, i) }}</div>
              <span v-if="q.requirement && q.requirement.trim() !== ''" class="esm-q-req">{{ q.requirement }}</span>
              <div class="esm-q-pts">/{{ q.points.toFixed(1) }}</div>
            </th>
            <th scope="col">Total</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in rows" :key="s.student_id">
            <td class="esm-id" :title="`${s.student_number || '—'} · ${s.username} · Sec ${s.sec_lec}/${s.sec_lab}`">
              <div class="esm-student-num">{{ s.student_number || s.username }}</div>
              <div v-if="s.first_name || s.last_name" class="esm-student-name">
                {{ s.first_name }} {{ s.last_name }}
              </div>
            </td>
            <td
              v-for="(cell, qIndex) in (rowCellsMap.get(s.student_id) || [])"
              :key="questions[qIndex].id"
              :class="['score-cell', 'tabular', examCellClass(cell, questions[qIndex].points)]"
              :title="examCellTooltip(cell, questions[qIndex].points)"
            >
              <template v-if="cell === undefined">—</template>
              <template v-else>{{ cell.score.toFixed(1) }} / {{ questions[qIndex].points.toFixed(1) }}</template>
            </td>
            <td
              :class="[
                'score-cell',
                'tabular',
                'esm-total',
                scoreClass(rowTotal(s.student_id), maxTotal)
              ]"
            >
              {{ rowTotal(s.student_id).toFixed(1) }} / {{ maxTotal.toFixed(1) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p class="esm-caption">
      Shows each student's best attempt per question — the same best-per-question rule the consolidated exam total uses. Exam grading allows partial credit, so a cell can be amber. A dash (—) means never attempted.
    </p>
  </div>
</template>

<style scoped>
/* Deliberately over-specified. The global .data-table tr:nth-child(even) td is
   (0,2,2) and outranks .score-cell.score-zero at (0,2,0), so a naive score cell
   renders correctly on odd rows and grey on even ones. These selectors are (0,4,3). */
.esm-table tbody tr td.score-cell.score-zero { background: var(--score-zero); }
.esm-table tbody tr td.score-cell.score-partial { background: var(--score-partial); }
.esm-table tbody tr td.score-cell.score-full { background: var(--score-full); }
.esm-table tbody tr td.score-cell.score-pending { background: var(--color-surface); color: var(--score-pending-fg); }
.esm-table tbody tr:hover td.score-cell.score-zero { background: var(--score-zero-hover); }
.esm-table tbody tr:hover td.score-cell.score-partial { background: var(--score-partial-hover); }
.esm-table tbody tr:hover td.score-cell.score-full { background: var(--score-full-hover); }

.esm-wrap {
  margin-bottom: var(--space-2);
}

.esm-id {
  position: sticky;
  left: 0;
  z-index: 2;
  background: var(--color-surface);
  box-shadow: inset -1px 0 0 var(--color-border);
  min-width: 160px;
  max-width: 220px;
}
thead th.esm-id {
  z-index: 3;
  background: var(--color-surface-sunk);
}
.esm-table tbody tr:nth-child(even) td.esm-id {
  background: var(--color-surface-muted);
}

.esm-q {
  min-width: 78px;
  text-align: center;
  padding: 4px 6px;
}
.esm-q-label {
  font-weight: 700;
}
.esm-q-req {
  text-overflow: ellipsis;
  overflow: hidden;
  white-space: nowrap;
  display: block;
  max-width: 100%;
  font-weight: 400;
  font-size: 0.75rem;
  text-transform: none;
}
.esm-q-pts {
  color: var(--color-text-muted);
  font-size: 0.75rem;
  font-weight: 400;
}
.esm-total {
  font-weight: 700;
}
.esm-student-num {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.esm-student-name {
  font-size: 0.78rem;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.esm-caption {
  margin-top: var(--space-3);
  font-size: 0.82rem;
  color: var(--color-text-muted);
}
</style>
