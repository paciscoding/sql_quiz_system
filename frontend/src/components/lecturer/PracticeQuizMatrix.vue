<script setup lang="ts">
import { computed } from 'vue'
import type {
  MatrixQuestion,
  MatrixStudent,
  MatrixCell,
  PracticeResultRow
} from '../../utils/practice_matrix'
import {
  buildMatrix,
  buildRowAxis,
  cellClass,
  cellTooltip,
  scoreClass,
  columnLabel,
  columnTooltip
} from '../../utils/practice_matrix'
import Skeleton from '../Skeleton.vue'
import EmptyState from '../EmptyState.vue'

const props = defineProps<{
  students: MatrixStudent[]
  questions: MatrixQuestion[]
  results: PracticeResultRow[]
  loading?: boolean
  degraded?: boolean
}>()

const matrix = computed(() => buildMatrix(props.results))
const rows = computed(() => buildRowAxis(props.students, props.results))

const maxTotal = computed(() =>
  props.questions.reduce((sum, q) => sum + (q.points || 0), 0)
)

const rowCellsMap = computed(() => {
  const map = new Map<string, Array<MatrixCell | undefined>>()
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
      total += cell.earned
    }
  }
  return total
}

</script>

<template>
  <Skeleton v-if="loading" variant="table-row" :rows="8" :cols="10" />

  <EmptyState
    v-else-if="questions.length === 0"
    title="No practice questions yet"
    body="Once you publish practice questions and students attempt them, their best scores will appear here."
  />

  <div v-else>
    <div v-if="degraded" class="alert-warning pqm-alert" role="alert">
      Question list unavailable — showing only questions with at least one attempt.
    </div>

    <div
      class="responsive-table-wrap sticky-table-head pqm-wrap"
      :style="{ '--sticky-max-h': '70vh' }"
    >
      <table class="data-table tabular pqm-table">
        <thead>
          <tr>
            <th class="pqm-id" scope="col">Student</th>
            <th
              v-for="(q, i) in questions"
              :key="q.id"
              class="pqm-q"
              scope="col"
              :title="columnTooltip(q, i)"
            >
              <div class="pqm-q-label">{{ columnLabel(q, i) }}</div>
              <span v-if="q.title && q.title.trim() !== ''" class="pqm-q-title">{{ q.title }}</span>
              <div class="pqm-q-pts">/{{ q.points.toFixed(1) }}</div>
            </th>
            <th scope="col">Total</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in rows" :key="s.student_id">
            <td class="pqm-id" :title="`${s.student_number || '—'} · ${s.username} · Sec ${s.sec_lec}/${s.sec_lab}`">
              <div class="pqm-student-num">{{ s.student_number || s.username }}</div>
              <div v-if="s.first_name || s.last_name" class="pqm-student-name">
                {{ s.first_name }} {{ s.last_name }}
              </div>
            </td>
            <td
              v-for="(cell, qIndex) in (rowCellsMap.get(s.student_id) || [])"
              :key="questions[qIndex].id"
              :class="['score-cell', 'tabular', cellClass(cell, questions[qIndex].points)]"
              :title="cellTooltip(cell)"
            >
              <template v-if="cell === undefined">—</template>
              <template v-else>{{ cell.earned.toFixed(1) }} / {{ questions[qIndex].points.toFixed(1) }}</template>
            </td>
            <td
              :class="[
                'score-cell',
                'tabular',
                'pqm-total',
                scoreClass(rowTotal(s.student_id), maxTotal)
              ]"
            >
              {{ rowTotal(s.student_id).toFixed(1) }} / {{ maxTotal.toFixed(1) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p class="pqm-caption">
      Shows best score per question. Practice grading is all-or-nothing (full points once solved, 0 otherwise). A dash (—) means never attempted.
    </p>
  </div>
</template>

<style scoped>
.pqm-table tbody tr td.score-cell.score-zero { background: var(--score-zero); }
.pqm-table tbody tr td.score-cell.score-partial { background: var(--score-partial); }
.pqm-table tbody tr td.score-cell.score-full { background: var(--score-full); }
.pqm-table tbody tr td.score-cell.score-pending { background: var(--color-surface); color: var(--score-pending-fg); }
.pqm-table tbody tr:hover td.score-cell.score-zero { background: var(--score-zero-hover); }
.pqm-table tbody tr:hover td.score-cell.score-partial { background: var(--score-partial-hover); }
.pqm-table tbody tr:hover td.score-cell.score-full { background: var(--score-full-hover); }

.pqm-alert {
  margin-bottom: var(--space-3);
}

.pqm-wrap {
  margin-bottom: var(--space-2);
}

.pqm-id {
  position: sticky;
  left: 0;
  z-index: 2;
  background: var(--color-surface);
  box-shadow: inset -1px 0 0 var(--color-border);
  min-width: 160px;
  max-width: 220px;
}
thead th.pqm-id {
  z-index: 3;
  background: var(--color-surface-sunk);
}
.pqm-table tbody tr:nth-child(even) td.pqm-id {
  background: var(--color-surface-muted);
}

.pqm-q {
  min-width: 78px;
  text-align: center;
  padding: 4px 6px;
}
.pqm-q-label {
  font-weight: 700;
}
.pqm-q-title {
  text-overflow: ellipsis;
  overflow: hidden;
  white-space: nowrap;
  display: block;
  max-width: 100%;
  font-weight: 400;
  font-size: 0.75rem;
  text-transform: none;
}
.pqm-q-pts {
  color: var(--color-text-muted);
  font-size: 0.75rem;
  font-weight: 400;
}
.pqm-total {
  font-weight: 700;
}
.pqm-student-num {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pqm-student-name {
  font-size: 0.78rem;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pqm-caption {
  margin-top: var(--space-3);
  font-size: 0.82rem;
  color: var(--color-text-muted);
}
</style>
