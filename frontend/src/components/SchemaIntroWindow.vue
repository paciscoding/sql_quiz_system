<script setup lang="ts">
import { reactive, watch, onUnmounted } from 'vue'
import type { TableIntrospection } from '../services/practice'
import SchemaIntroduction from './SchemaIntroduction.vue'
import { loadState, saveState, type WindowState } from '../utils/schemaWindowState'

const props = defineProps<{
  scope: 'category' | 'exam'
  ownerId: string
  introduction: string
  tables: TableIntrospection[]
  imported?: boolean
  categoryName?: string
  mode?: 'student' | 'lecturer-preview'
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

// ─── State ──────────────────────────────────────────────────────────────────

function defaultState(): WindowState {
  return {
    mode: 'normal',
    // Offset from the schema-diagram window (x=32) so both are visible side-by-side.
    pos: { x: 700, y: 80 },
    size: { w: 460, h: 420 },
    currentIndex: 0,
  }
}

const TOP_INSET = 64

const state = reactive<WindowState>(
  loadState(props.scope, props.ownerId, 'intro') ?? defaultState()
)

watch(
  () => ({ ...state, pos: { ...state.pos }, size: { ...state.size } }),
  (val) => saveState(props.scope, props.ownerId, val, 'intro'),
  { deep: true }
)

// ─── Drag ────────────────────────────────────────────────────────────────────

let dragging = false
let dragStartX = 0
let dragStartY = 0
let dragOriginX = 0
let dragOriginY = 0

function onTitleMousedown(e: MouseEvent) {
  if (state.mode === 'minimized') return
  dragging = true
  dragStartX = e.clientX
  dragStartY = e.clientY
  dragOriginX = state.pos.x
  dragOriginY = state.pos.y
  document.addEventListener('mousemove', onDocMousemove)
  document.addEventListener('mouseup', onDocMouseup)
}

function onDocMousemove(e: MouseEvent) {
  if (!dragging) return
  const dx = e.clientX - dragStartX
  const dy = e.clientY - dragStartY
  const maxX = window.innerWidth - state.size.w - 8
  const maxY = window.innerHeight - 40
  state.pos.x = Math.max(0, Math.min(dragOriginX + dx, maxX))
  state.pos.y = Math.max(TOP_INSET, Math.min(dragOriginY + dy, maxY))
}

function onDocMouseup() {
  dragging = false
  document.removeEventListener('mousemove', onDocMousemove)
  document.removeEventListener('mouseup', onDocMouseup)
}

// ─── Resize ──────────────────────────────────────────────────────────────────

const MIN_W = 320
const MIN_H = 200
let resizing = false
let resizeStartX = 0
let resizeStartY = 0
let resizeOriginW = 0
let resizeOriginH = 0

function onResizeMousedown(e: MouseEvent) {
  e.preventDefault()
  resizing = true
  resizeStartX = e.clientX
  resizeStartY = e.clientY
  resizeOriginW = state.size.w
  resizeOriginH = state.size.h
  document.addEventListener('mousemove', onResizeMousemove)
  document.addEventListener('mouseup', onResizeMouseup)
}

function onResizeMousemove(e: MouseEvent) {
  if (!resizing) return
  const dx = e.clientX - resizeStartX
  const dy = e.clientY - resizeStartY
  const maxW = window.innerWidth - state.pos.x - 8
  const maxH = window.innerHeight - state.pos.y - 8
  state.size.w = Math.max(MIN_W, Math.min(resizeOriginW + dx, maxW))
  state.size.h = Math.max(MIN_H, Math.min(resizeOriginH + dy, maxH))
}

function onResizeMouseup() {
  resizing = false
  document.removeEventListener('mousemove', onResizeMousemove)
  document.removeEventListener('mouseup', onResizeMouseup)
}

// ─── Mode controls ───────────────────────────────────────────────────────────

function toggleCollapse() {
  state.mode = state.mode === 'collapsed' ? 'normal' : 'collapsed'
}

function minimize() {
  state.mode = 'minimized'
}

function restore() {
  state.mode = 'normal'
  const maxX = window.innerWidth - state.size.w - 8
  const maxY = window.innerHeight - state.size.h - 8
  state.pos.x = Math.max(0, Math.min(state.pos.x, maxX))
  state.pos.y = Math.max(TOP_INSET, Math.min(state.pos.y, maxY))
}

// ─── Keyboard ────────────────────────────────────────────────────────────────

function onKeydown(e: KeyboardEvent) {
  switch (e.key) {
    case 'ArrowUp':
      e.preventDefault()
      state.pos.y = Math.max(0, state.pos.y - 16)
      break
    case 'ArrowDown':
      e.preventDefault()
      state.pos.y = Math.min(window.innerHeight - state.size.h - 8, state.pos.y + 16)
      break
    case 'Escape':
      e.preventDefault()
      minimize()
      break
  }
}

onUnmounted(() => {
  document.removeEventListener('mousemove', onDocMousemove)
  document.removeEventListener('mouseup', onDocMouseup)
  document.removeEventListener('mousemove', onResizeMousemove)
  document.removeEventListener('mouseup', onResizeMouseup)
})
</script>

<template>
  <!-- Minimized chip -->
  <div
    v-if="state.mode === 'minimized'"
    class="siw-chip siw-chip--intro"
    role="button"
    tabindex="0"
    aria-label="Restore Category Introduction window"
    @click="restore"
    @keydown.enter="restore"
    @keydown.space.prevent="restore"
  >
    Category Introduction
  </div>

  <!-- Main floating window -->
  <div
    v-else
    class="siw-window siw-window--intro"
    :class="{ 'siw-window--collapsed': state.mode === 'collapsed' }"
    :style="{
      left: state.pos.x + 'px',
      top: state.pos.y + 'px',
      width: state.size.w + 'px',
      height: state.mode === 'collapsed' ? 'auto' : state.size.h + 'px',
    }"
    role="dialog"
    aria-roledescription="window"
    aria-label="Category Introduction"
    tabindex="0"
    @keydown="onKeydown"
  >
    <!-- Title bar -->
    <div class="siw-titlebar" @mousedown="onTitleMousedown">
      <span class="siw-title">
        Category Introduction
        <span class="siw-counter" v-if="categoryName">— {{ categoryName }}</span>
      </span>
      <div class="siw-controls">
        <button
          class="siw-ctrl-btn"
          :title="state.mode === 'collapsed' ? 'Expand' : 'Collapse'"
          @click.stop="toggleCollapse"
          @mousedown.stop
        >{{ state.mode === 'collapsed' ? '▾' : '▴' }}</button>
        <button
          class="siw-ctrl-btn"
          title="Minimize"
          @click.stop="minimize"
          @mousedown.stop
        >─</button>
        <button
          class="siw-ctrl-btn"
          title="Close"
          @click.stop="emit('close')"
          @mousedown.stop
        >✕</button>
      </div>
    </div>

    <!-- Body — hidden when collapsed -->
    <div v-if="state.mode !== 'collapsed'" class="siw-body siw-body--intro">
      <SchemaIntroduction
        :introduction="introduction"
        :tables="tables"
        :imported="imported"
        :mode="mode ?? 'student'"
      />
    </div>

    <!-- Resize grip -->
    <div
      v-if="state.mode !== 'collapsed'"
      class="siw-resize-grip"
      title="Resize"
      @mousedown.stop="onResizeMousedown"
    ></div>
  </div>
</template>

<style scoped>
/* Chrome mirrors SchemaImageWindow so both floating windows read as one system. */
.siw-chip {
  position: fixed;
  bottom: 16px;
  left: 50%;
  transform: translateX(-50%);
  z-index: var(--z-schema-chip);
  background: var(--color-primary);
  color: #fff;
  padding: 6px 20px;
  border-radius: 999px;
  font-size: 0.85rem;
  font-weight: 600;
  cursor: pointer;
  box-shadow: var(--shadow-2);
  user-select: none;
  white-space: nowrap;
  border: none;
  transition: background 0.12s ease;
}
.siw-chip:hover { background: var(--color-primary-strong); }
/* Nudge the intro chip left so it doesn't stack directly on the diagram chip. */
.siw-chip--intro { left: calc(50% - 180px); }

.siw-window {
  position: fixed;
  z-index: var(--z-schema-window);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-2);
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-width: 320px;
  min-height: 48px;
  outline: none;
  will-change: left, top, width, height;
}
.siw-window:focus {
  box-shadow: 0 0 0 2px var(--color-primary), var(--shadow-2);
}

.siw-titlebar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  background: var(--color-surface-sunk);
  border-bottom: 1px solid var(--color-border);
  cursor: grab;
  user-select: none;
  flex-shrink: 0;
  min-height: 40px;
}
.siw-titlebar:active { cursor: grabbing; }

.siw-title {
  font-size: 0.85rem;
  font-weight: 700;
  color: var(--color-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.siw-counter {
  font-weight: 400;
  color: var(--color-text-muted);
  margin-left: 4px;
}

.siw-controls {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
  margin-left: 8px;
}
.siw-ctrl-btn {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  padding: 2px 8px;
  font-size: 0.82rem;
  cursor: pointer;
  color: var(--color-text);
  line-height: 1.4;
  transition: background 0.1s ease;
}
.siw-ctrl-btn:hover { background: var(--color-surface-muted); }

.siw-body--intro {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding: var(--space-4);
  background: var(--color-surface);
}

.siw-resize-grip {
  position: absolute;
  right: 0;
  bottom: 0;
  width: 20px;
  height: 20px;
  cursor: nwse-resize;
  background:
    linear-gradient(135deg,
      transparent 0 45%,
      var(--color-text-muted) 45% 55%,
      transparent 55% 65%,
      var(--color-text-muted) 65% 75%,
      transparent 75% 85%,
      var(--color-text-muted) 85% 95%,
      transparent 95% 100%);
  opacity: 0.55;
  transition: opacity 0.12s ease;
  z-index: 1;
}
.siw-resize-grip:hover { opacity: 1; }

/* Mobile — render inline, drop drag/resize affordances. */
@media (max-width: 767px) {
  .siw-window {
    position: static !important;
    width: 100% !important;
    height: auto !important;
    border-radius: var(--radius);
    min-width: 0;
  }
  .siw-titlebar { cursor: default; }
  .siw-resize-grip { display: none; }
  .siw-chip--intro {
    left: 16px;
    right: 16px;
    transform: none;
    text-align: center;
  }
}
</style>
