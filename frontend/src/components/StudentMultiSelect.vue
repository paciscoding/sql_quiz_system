<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'

interface Student {
  id: string
  username: string
}

const props = withDefaults(
  defineProps<{
    modelValue: string[]
    students: Student[]
    loading?: boolean
    placeholder?: string
    maxHeight?: string
  }>(),
  {
    loading: false,
    placeholder: 'Search students...',
    maxHeight: '250px',
  },
)

const emit = defineEmits<{
  (event: 'update:modelValue', value: string[]): void
}>()

const rootEl = ref<HTMLElement | null>(null)
const searchQuery = ref('')
const focusedIndex = ref(-1)

const filteredList = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()

  if (!query) {
    return props.students
  }

  return props.students.filter((student) =>
    student.username.toLowerCase().includes(query),
  )
})

watch(filteredList, (list) => {
  if (list.length === 0) {
    focusedIndex.value = -1
    return
  }

  if (focusedIndex.value >= list.length) {
    focusedIndex.value = list.length - 1
  }

  if (focusedIndex.value < -1) {
    focusedIndex.value = -1
  }
})

function toggleStudent(id: string) {
  const nextValue = [...props.modelValue]
  const selectedIndex = nextValue.indexOf(id)

  if (selectedIndex >= 0) {
    nextValue.splice(selectedIndex, 1)
  } else {
    nextValue.push(id)
  }

  emit('update:modelValue', nextValue)
}

function selectAll() {
  const selectedIds = new Set(props.modelValue)

  filteredList.value.forEach((student) => {
    selectedIds.add(student.id)
  })

  emit('update:modelValue', Array.from(selectedIds))
}

function selectNone() {
  const filteredIds = new Set(filteredList.value.map((student) => student.id))
  const nextValue = props.modelValue.filter((id) => !filteredIds.has(id))

  emit('update:modelValue', nextValue)
}

function moveFocus(delta: number) {
  if (filteredList.value.length === 0) {
    focusedIndex.value = -1
    return
  }

  const currentIndex = focusedIndex.value
  const nextIndex = Math.max(
    0,
    Math.min(currentIndex + delta, filteredList.value.length - 1),
  )

  focusedIndex.value = nextIndex

  nextTick(() => {
    const item = rootEl.value?.querySelector<HTMLElement>(
      `[data-index="${nextIndex}"]`,
    )
    item?.focus()
  })
}
</script>

<template>
  <div
    ref="rootEl"
    role="group"
    aria-label="Student selector"
    class="msel-root"
  >
    <div v-if="loading" class="msel-loading">Loading students...</div>

    <template v-else>
      <div class="msel-toolbar">
        <input
          v-model="searchQuery"
          type="text"
          :placeholder="placeholder"
          aria-label="Search students"
          class="msel-search input"
          @keydown.down.prevent="moveFocus(1)"
          @keydown.up.prevent="moveFocus(-1)"
        />

        <div class="msel-toolbar-actions">
          <button type="button" class="msel-action-btn" @click="selectAll">
            [Select All]
          </button>
          <button type="button" class="msel-action-btn" @click="selectNone">
            [Select None]
          </button>
        </div>
      </div>

      <div class="msel-counter">
        {{ modelValue.length }} of {{ students.length }} selected
      </div>

      <div
        role="listbox"
        aria-multiselectable="true"
        class="msel-list"
        :style="{ maxHeight: maxHeight, overflowY: 'auto' }"
        @keydown.down.prevent="moveFocus(1)"
        @keydown.up.prevent="moveFocus(-1)"
      >
        <div
          v-if="filteredList.length === 0 && students.length > 0"
          class="msel-empty"
        >
          No students match.
        </div>

        <div v-else-if="students.length === 0" class="msel-empty">
          No students found.
        </div>

        <div
          v-for="(s, idx) in filteredList"
          :key="s.id"
          class="student-item msel-row"
          role="option"
          :aria-selected="modelValue.includes(s.id)"
          :tabindex="focusedIndex === idx ? 0 : -1"
          :data-index="idx"
          @keydown.space.prevent="toggleStudent(s.id)"
          @keydown.enter.prevent="toggleStudent(s.id)"
          @focus="focusedIndex = idx"
        >
          <input
            type="checkbox"
            :id="'msel-' + s.id"
            :checked="modelValue.includes(s.id)"
            tabindex="-1"
            @change="toggleStudent(s.id)"
          />
          <label :for="'msel-' + s.id" @click.prevent="toggleStudent(s.id)">
            {{ s.username }}
          </label>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.msel-root {
  border: 2px solid var(--color-border);
  background: var(--color-surface);
}

.msel-toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  flex-wrap: wrap;
}

.msel-search {
  flex: 1;
  min-width: 120px;
}

.msel-toolbar-actions {
  display: flex;
  gap: var(--space-1);
}

.msel-action-btn {
  background: transparent;
  border: 1px solid var(--color-border);
  color: var(--color-text);
  font-family: var(--font-mono);
  font-size: 0.78rem;
  font-weight: 700;
  cursor: pointer;
  padding: 2px 8px;
  border-radius: 0;
}

.msel-action-btn:hover {
  background: var(--color-surface-muted);
}

.msel-counter {
  padding: var(--space-1) var(--space-3);
  font-size: 0.78rem;
  font-weight: 700;
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--color-border);
}

.msel-list {
  overflow-y: auto;
}

.msel-loading,
.msel-empty {
  padding: var(--space-3);
  font-family: var(--font-mono);
  color: var(--color-text-muted);
}

.student-item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  cursor: pointer;
  border-bottom: 1px solid var(--color-border);
}

.student-item:last-child {
  border-bottom: none;
}

.student-item:hover,
.student-item:focus {
  background: var(--color-surface-muted);
  outline: 2px solid var(--color-border);
  outline-offset: -2px;
}

.student-item label {
  cursor: pointer;
  font-family: var(--font-mono);
  font-size: 0.9rem;
  font-weight: 600;
  user-select: none;
}
</style>
