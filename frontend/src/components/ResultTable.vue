<script setup lang="ts">
defineProps<{
  rows: Record<string, any>[]
  columns: string[]
  maxHeight?: string
}>()
</script>

<template>
  <div :style="{ maxHeight: maxHeight ?? '300px', overflowX: 'auto', overflowY: 'auto' }">
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
