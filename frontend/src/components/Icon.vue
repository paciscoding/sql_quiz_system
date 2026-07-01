<script setup lang="ts">
import { computed } from 'vue'

/**
 * Local, dependency-free icon set. Inner SVG markup is taken from Lucide
 * (MIT-licensed): 24×24 viewBox, stroke-based, currentColor, stroke-width 2.
 * Consistent stroke/size/colour satisfies the vector-only, no-emoji-icons,
 * and consistent-icon-sizing UI rules without pulling in a dependency.
 */
const props = withDefaults(defineProps<{
  name: string
  size?: number | string
  title?: string
}>(), { size: 16 })

const ICONS: Record<string, string> = {
  'arrow-left':   '<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>',
  'arrow-right':  '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>',
  'arrow-up':     '<path d="m5 12 7-7 7 7"/><path d="M12 19V5"/>',
  'arrow-down':   '<path d="M12 5v14"/><path d="m19 12-7 7-7-7"/>',
  'chevron-up':   '<path d="m18 15-6-6-6 6"/>',
  'chevron-down': '<path d="m6 9 6 6 6-6"/>',
  'check':        '<path d="M20 6 9 17l-5-5"/>',
  'x':            '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
  'circle':       '<circle cx="12" cy="12" r="10"/>',
  'circle-dot':   '<circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="1" fill="currentColor"/>',
}

const inner = computed(() => ICONS[props.name] ?? '')
</script>

<template>
  <svg
    v-if="inner"
    class="app-icon"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    :aria-hidden="title ? undefined : 'true'"
    :role="title ? 'img' : undefined"
    :aria-label="title || undefined"
    v-html="inner"
  />
</template>

<style scoped>
.app-icon {
  display: inline-block;
  vertical-align: -0.15em;
  flex: none;
}
</style>
