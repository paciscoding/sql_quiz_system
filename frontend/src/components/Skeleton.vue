<script setup lang="ts">
withDefaults(defineProps<{
  variant?: 'line' | 'block' | 'table-row'
  lines?: number
  width?: string
  height?: string
  cols?: number
  rows?: number
}>(), {
  variant: 'line',
  lines: 3,
  width: '100%',
  height: undefined,
  cols: 4,
  rows: 3,
})

function lineWidth(i: number): string {
  return ['100%', '92%', '78%', '88%', '70%'][(i - 1) % 5]
}
</script>

<template>
  <div class="skeleton-wrap" aria-hidden="true">
    <template v-if="variant === 'line'">
      <div
        v-for="i in lines"
        :key="i"
        class="skeleton skeleton-line"
        :style="{ width: lineWidth(i), height: height ?? '14px' }"
      ></div>
    </template>
    <div
      v-else-if="variant === 'block'"
      class="skeleton skeleton-block"
      :style="{ width, height: height ?? '120px' }"
    ></div>
    <table v-else class="skeleton-table">
      <tbody>
        <tr v-for="r in rows" :key="r">
          <td v-for="c in cols" :key="c">
            <div class="skeleton skeleton-cell"></div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
