<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import type { Examination } from './types'
import { handleApiError, toApiError } from '../../services/error_handler'
import ExamListPanel from './ExamListPanel.vue'
import ExamWorkspace from './ExamWorkspace.vue'

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

const selectedExamId = ref<number | null>(null)

onMounted(fetchExams)

const currentExam = computed(() => exams.value.find(e => e.id === selectedExamId.value))
</script>

<template>
  <div>
    <div class="page-header">
      <h1>Examination Authoring</h1>
      <p class="page-subtitle">Manage schemas, author questions, schedule exams, and review consolidated student results.</p>
    </div>

    <ExamListPanel
      v-if="selectedExamId === null"
      :exams="exams"
      @open="selectedExamId = $event"
      @changed="fetchExams()"
    />
    <ExamWorkspace
      v-else
      :exam="currentExam!"
      @back="selectedExamId = null"
      @changed="fetchExams()"
    />
  </div>
</template>

<style scoped>
.page-header {
  margin-bottom: var(--space-5);
}
</style>
