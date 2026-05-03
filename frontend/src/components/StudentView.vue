<script setup lang="ts">
import { ref, onMounted } from 'vue'

interface ValidationResult {
  status: string
  score: number
  feedback: string
  is_correct: boolean
}

interface Examination {
  id: number
  title: string
}

interface Question {
  id: number
  points: number
  type: string
}

const user = JSON.parse(localStorage.getItem('sql_quiz_user') || '{}')
const exams = ref<Examination[]>([])
const selectedExamId = ref<number | null>(null)
const questions = ref<Question[]>([])
const selectedQuestionId = ref<number | null>(null)

const sqlText = ref('')
const result = ref<ValidationResult | null>(null)
const loading = ref(false)
const error = ref('')

onMounted(async () => {
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch('/api/v1/quiz/exams', {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) exams.value = await response.json()
  } catch (err) {}
})

const fetchQuestions = async () => {
  if (!selectedExamId.value) return
  const token = localStorage.getItem('sql_quiz_token')
  try {
    const response = await fetch(`/api/v1/quiz/exams/${selectedExamId.value}/questions`, {
      headers: { 'Authorization': `Bearer ${token}` }
    })
    if (response.ok) questions.value = await response.json()
  } catch (err) {}
}

const submitAnswer = async () => {
  const token = localStorage.getItem('sql_quiz_token')
  if (!sqlText.value) {
    error.value = 'Please provide a SQL query.'
    return
  }
  if (!selectedQuestionId.value) {
    error.value = 'Please select a question.'
    return
  }

  loading.value = true
  error.value = ''
  result.value = null

  try {
    const response = await fetch('/api/v1/quiz/submit', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`
      },
      body: JSON.stringify({
        question_number: selectedQuestionId.value,
        sql_text: sqlText.value,
      }),
    })

    if (!response.ok) {
      const errData = await response.json()
      throw new Error(errData.error || 'Failed to submit answer')
    }

    result.value = await response.json()
  } catch (err: any) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <section class="setup-section">
    <div class="input-group">
      <label>Examination:</label>
      <select v-model="selectedExamId" @change="fetchQuestions">
        <option :value="null">Select an Examination</option>
        <option v-for="e in exams" :key="e.id" :value="e.id">{{ e.title }}</option>
      </select>
    </div>

    <div class="input-group" v-if="selectedExamId">
      <label>Question:</label>
      <select v-model="selectedQuestionId">
        <option :value="null">Select a Question</option>
        <option v-for="q in questions" :key="q.id" :value="q.id">
          Q{{ q.id }} ({{ q.type }} - {{ q.points }} pts)
        </option>
      </select>
    </div>
  </section>

  <section class="editor-section">
    <label for="sql-editor">Your SQL Query:</label>
    <textarea
      id="sql-editor"
      v-model="sqlText"
      placeholder="-- Write your SQL here..."
      spellcheck="false"
    ></textarea>
    <button @click="submitAnswer" :disabled="loading" class="primary-btn">
      {{ loading ? 'Evaluating...' : 'Submit Answer' }}
    </button>
  </section>

  <section v-if="error" class="error-box">
    {{ error }}
  </section>

  <section v-if="result" class="result-box" :class="{ 'success': result.is_correct, 'fail': !result.is_correct }">
    <h3>Result: {{ result.is_correct ? 'Correct' : 'Incorrect' }}</h3>
    <p><strong>Score:</strong> {{ result.score }}</p>
    <p><strong>Feedback:</strong> {{ result.feedback }}</p>
  </section>
</template>

<style scoped>
.setup-section {
  display: flex;
  gap: 1rem;
  background: #f9f9f9;
  padding: 1rem;
  border-radius: 8px;
  margin-bottom: 1.5rem;
}

.input-group {
  display: flex;
  flex-direction: column;
  flex: 1;
}

label {
  font-weight: bold;
  margin-bottom: 0.5rem;
  color: #555;
}

input, textarea {
  padding: 0.8rem;
  border: 1px solid #ccc;
  border-radius: 4px;
  font-size: 1rem;
}

textarea {
  min-height: 200px;
  font-family: 'Courier New', Courier, monospace;
  resize: vertical;
  width: 100%;
}

.primary-btn {
  margin-top: 1rem;
  padding: 1rem;
  background-color: #41b883;
  color: white;
  border: none;
  border-radius: 4px;
  font-size: 1.1rem;
  font-weight: bold;
  cursor: pointer;
  transition: background-color 0.2s;
  width: 100%;
}

.primary-btn:hover {
  background-color: #3aa373;
}

.primary-btn:disabled {
  background-color: #ccc;
  cursor: not-allowed;
}

.result-box {
  padding: 1.5rem;
  border-radius: 8px;
  margin-top: 1rem;
}

.result-box.success {
  background-color: #e6fffa;
  border: 1px solid #b2f5ea;
  color: #2c7a7b;
}

.result-box.fail {
  background-color: #fff5f5;
  border: 1px solid #feb2b2;
  color: #c53030;
}

.error-box {
  background-color: #fff5f5;
  color: #c53030;
  padding: 1rem;
  border-radius: 4px;
  border-left: 4px solid #c53030;
  margin-top: 1rem;
}
</style>
