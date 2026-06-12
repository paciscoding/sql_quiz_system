<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'

defineProps<{
  rows: Record<string, any>[]
  columns: string[]
  maxHeight?: string
}>()

const wrapperRef = ref<HTMLElement | null>(null)
const hasOverflow = ref(false)
const scrolled = ref(false)

let observer: ResizeObserver | null = null

function handleScroll() {
  if (wrapperRef.value) {
    scrolled.value = wrapperRef.value.scrollLeft > 8
  }
}

function checkOverflow() {
  if (wrapperRef.value) {
    hasOverflow.value = wrapperRef.value.scrollWidth > wrapperRef.value.clientWidth
  }
}

onMounted(() => {
  if (wrapperRef.value) {
    observer = new ResizeObserver(() => {
      checkOverflow()
    })
    observer.observe(wrapperRef.value)
    wrapperRef.value.addEventListener('scroll', handleScroll)
    checkOverflow()
  }
})

onBeforeUnmount(() => {
  if (observer) {
    observer.disconnect()
    observer = null
  }
  if (wrapperRef.value) {
    wrapperRef.value.removeEventListener('scroll', handleScroll)
  }
})
</script>

<template>
  <div ref="wrapperRef" class="responsive-table-wrap sticky-table-head" :style="{ '--sticky-max-h': maxHeight ?? '300px' }">
    <span v-if="hasOverflow && !scrolled" class="scroll-hint">Scroll →</span>
    <table v-if="rows.length > 0" class="data-table">
      <thead>
        <tr><th v-for="c in columns" :key="c">{{ c }}</th></tr>
      </thead>
      <tbody>
        <tr v-for="(row, i) in rows" :key="i">
          <td v-for="c in columns" :key="c">{{ row[c] ?? '' }}</td>
        </tr>
      </tbody>
    </table>
    <p v-else class="muted">No rows.</p>
  </div>
</template>

<style scoped>
.responsive-table-wrap {
  position: relative;
}
th {
  white-space: nowrap;
}
</style>
