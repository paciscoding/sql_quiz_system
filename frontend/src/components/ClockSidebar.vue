<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, computed } from 'vue'

const now = ref(new Date())
let timer: number | null = null

onMounted(() => {
  timer = window.setInterval(() => { now.value = new Date() }, 1000)
})
onBeforeUnmount(() => {
  if (timer !== null) window.clearInterval(timer)
})

const dateStr = computed(() =>
  now.value.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })
)
const timeStr = computed(() =>
  now.value.toLocaleTimeString(undefined, { hour12: false })
)
</script>

<template>
  <div class="sidebar-clock" role="region" aria-label="Server time">
    <div class="clock-row">
      <span class="clock-label">Date</span>
      <span class="clock-value">{{ dateStr }}</span>
    </div>
    <div class="clock-row">
      <span class="clock-label">Server Time</span>
      <span class="clock-value">{{ timeStr }}</span>
    </div>
  </div>
</template>
