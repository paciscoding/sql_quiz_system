<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import Icon from './Icon.vue'

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
    <span v-if="hasOverflow && !scrolled" class="scroll-hint">Scroll <Icon name="arrow-right" /></span>
    <table v-if="rows.length > 0" class="data-table">
      <thead>
        <tr><th v-for="c in columns" :key="c">{{ c }}</th></tr>
      </thead>
      <tbody>
        <tr v-for="(row, i) in rows" :key="i">
          <td v-for="c in columns" :key="c" :title="row[c] ?? ''">{{ row[c] ?? '' }}</td>
        </tr>
      </tbody>
    </table>
    <p v-else class="muted">No rows.</p>
  </div>
</template>

<style scoped>
.responsive-table-wrap {
  position: relative;
  /* Let the wrapper actually shrink to whatever width its flex/grid parent
     gives it (instead of being pushed wide by the table's intrinsic width),
     so overflow-x:auto (from the global .responsive-table-wrap rule) is the
     thing that scrolls — not the surrounding card or the page body. The
     wrapper's own border/background (also global) always match its own box,
     which now tracks the visible/scrollable area rather than the content. */
  min-width: 0;
  max-width: 100%;
}
/* .data-table itself renders inside this component, so this scoped rule
   overrides the global `width: 100%` without touching style.css: the table
   fills the wrapper when content is narrow (min-width: 100%) but is free to
   grow past it when content is wide (width: auto), which is what makes the
   wrapper's horizontal scrollbar engage instead of the table clipping. */
.data-table {
  width: auto;
  min-width: 100%;
}
th,
td {
  white-space: nowrap;
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
