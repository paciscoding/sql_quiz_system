<script setup lang="ts">
import { ref, reactive, watch, onMounted, onUnmounted } from 'vue'
import type { IntroImageMetadata } from '../services/schemaImages'
import { categoryImageBlobUrl, examImageBlobUrl } from '../services/schemaImages'
import { loadState, saveState, type WindowState } from '../utils/schemaWindowState'

const props = defineProps<{
  scope: 'category' | 'exam'
  ownerId: string
  images: IntroImageMetadata[]
}>()

// ─── State ──────────────────────────────────────────────────────────────────

function defaultState(): WindowState {
  return {
    mode: 'normal',
    // y=80 keeps the window clear of the 60px sticky CMS header on first render
    pos: { x: 32, y: 80 },
    size: { w: 640, h: 480 },
    currentIndex: 0,
  }
}

// Top inset: keep the title bar from sliding behind the sticky header during drag.
const TOP_INSET = 64

const state = reactive<WindowState>(
  loadState(props.scope, props.ownerId) ?? defaultState()
)

// Persist on every change (debounced inside saveState)
watch(
  () => ({ ...state, pos: { ...state.pos }, size: { ...state.size } }),
  (val) => saveState(props.scope, props.ownerId, val),
  { deep: true }
)

// ─── Image blob URL ──────────────────────────────────────────────────────────

const objectUrl = ref<string | null>(null)

async function loadImage(index: number) {
  // Revoke previous blob URL to avoid memory leaks
  if (objectUrl.value) {
    URL.revokeObjectURL(objectUrl.value)
    objectUrl.value = null
  }
  if (props.images.length === 0) return
  const img = props.images[index]
  if (!img) return
  try {
    const url =
      props.scope === 'category'
        ? await categoryImageBlobUrl(props.ownerId, img.id)
        : await examImageBlobUrl(props.ownerId, img.id)
    objectUrl.value = url
  } catch {
    objectUrl.value = null
  }
}

watch(
  () => state.currentIndex,
  (i) => loadImage(i),
  { immediate: false }
)

onMounted(() => {
  // Clamp index in case images array is shorter than saved state
  if (state.currentIndex >= props.images.length) {
    state.currentIndex = 0
  }
  loadImage(state.currentIndex)
})

onUnmounted(() => {
  if (objectUrl.value) {
    URL.revokeObjectURL(objectUrl.value)
    objectUrl.value = null
  }
})

// ─── Navigation ──────────────────────────────────────────────────────────────

function prev() {
  if (state.currentIndex > 0) state.currentIndex--
}

function next() {
  if (state.currentIndex < props.images.length - 1) state.currentIndex++
}

// ─── Drag ────────────────────────────────────────────────────────────────────

const windowRef = ref<HTMLElement | null>(null)
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
  const maxY = window.innerHeight - 40 // at least the title bar remains visible
  state.pos.x = Math.max(0, Math.min(dragOriginX + dx, maxX))
  state.pos.y = Math.max(TOP_INSET, Math.min(dragOriginY + dy, maxY))
}

function onDocMouseup() {
  dragging = false
  document.removeEventListener('mousemove', onDocMousemove)
  document.removeEventListener('mouseup', onDocMouseup)
}

// ─── Resize ──────────────────────────────────────────────────────────────────

const MIN_W = 360
const MIN_H = 240
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
  // Push back into view if pos is now off-screen
  const maxX = window.innerWidth - state.size.w - 8
  const maxY = window.innerHeight - state.size.h - 8
  state.pos.x = Math.max(0, Math.min(state.pos.x, maxX))
  state.pos.y = Math.max(TOP_INSET, Math.min(state.pos.y, maxY))
}

// ─── Fullscreen (zoom/pan) ────────────────────────────────────────────────────
// Zoom/pan state is deliberately NOT part of `state` — it does not need to
// persist across sessions, and always resets when fullscreen is (re-)entered.

const zoomLevel = ref(1)
const panX = ref(0)
const panY = ref(0)
const fsImgWrapRef = ref<HTMLElement | null>(null)

// Mode to return to on exit — captured when entering fullscreen (not persisted).
let previousFsMode: WindowState['mode'] = 'normal'

function resetView() {
  zoomLevel.value = 1
  panX.value = 0
  panY.value = 0
}

function enterFullscreen() {
  if (state.mode === 'fullscreen') return
  previousFsMode = state.mode
  state.mode = 'fullscreen'
  resetView()
}

function exitFullscreen() {
  state.mode = previousFsMode
}

const ZOOM_MIN = 0.25
const ZOOM_MAX = 8

function onFsWheel(e: WheelEvent) {
  e.preventDefault()
  const wrap = fsImgWrapRef.value
  if (!wrap) return
  const rect = wrap.getBoundingClientRect()
  // Coordinates relative to the wrap's centre — matches the image's default
  // transform-origin (50% 50%), which is also where zoom=1/pan=0 centres it.
  const cx = e.clientX - rect.left - rect.width / 2
  const cy = e.clientY - rect.top - rect.height / 2
  const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1
  const newZoom = Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, zoomLevel.value * factor))
  // Keep the point under the cursor fixed while zooming
  panX.value = cx - ((cx - panX.value) / zoomLevel.value) * newZoom
  panY.value = cy - ((cy - panY.value) / zoomLevel.value) * newZoom
  zoomLevel.value = newZoom
}

let panning = false
let panStartX = 0
let panStartY = 0
let panOriginX = 0
let panOriginY = 0

function onFsMousedown(e: MouseEvent) {
  // Middle button always pans; left button pans too (cheap to allow).
  if (e.button !== 0 && e.button !== 1) return
  e.preventDefault()
  panning = true
  panStartX = e.clientX
  panStartY = e.clientY
  panOriginX = panX.value
  panOriginY = panY.value
  document.addEventListener('mousemove', onFsPanMove)
  document.addEventListener('mouseup', onFsPanUp)
}

function onFsPanMove(e: MouseEvent) {
  if (!panning) return
  panX.value = panOriginX + (e.clientX - panStartX)
  panY.value = panOriginY + (e.clientY - panStartY)
}

function onFsPanUp() {
  panning = false
  document.removeEventListener('mousemove', onFsPanMove)
  document.removeEventListener('mouseup', onFsPanUp)
}

function onFsAuxclick(e: MouseEvent) {
  // Suppress the browser's middle-click autoscroll icon / paste-on-click.
  if (e.button === 1) e.preventDefault()
}

function onFsDblclick() {
  resetView()
}

function onFsKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.preventDefault()
    exitFullscreen()
  }
}

// Document-level Escape listener is only live while in fullscreen, since the
// fullscreen overlay isn't guaranteed to hold DOM focus (unlike onKeydown
// below, which relies on the normal-mode window's own tabindex focus).
watch(
  () => state.mode,
  (mode, prevMode) => {
    if (mode === 'fullscreen') {
      document.addEventListener('keydown', onFsKeydown)
    } else if (prevMode === 'fullscreen') {
      document.removeEventListener('keydown', onFsKeydown)
    }
  }
)

// ─── Keyboard ────────────────────────────────────────────────────────────────

function onKeydown(e: KeyboardEvent) {
  switch (e.key) {
    case 'ArrowLeft':
      // Carousel: advance to previous image
      e.preventDefault()
      prev()
      break
    case 'ArrowRight':
      // Carousel: advance to next image
      e.preventDefault()
      next()
      break
    case 'ArrowUp':
      // Window: nudge up (vertical movement reserved for window position)
      e.preventDefault()
      state.pos.y = Math.max(0, state.pos.y - 16)
      break
    case 'ArrowDown':
      // Window: nudge down
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
  document.removeEventListener('mousemove', onFsPanMove)
  document.removeEventListener('mouseup', onFsPanUp)
  document.removeEventListener('keydown', onFsKeydown)
})
</script>

<template>
  <!-- Minimized chip — rendered as a fixed-position pill at the bottom of viewport -->
  <div
    v-if="state.mode === 'minimized'"
    class="siw-chip"
    role="button"
    tabindex="0"
    aria-label="Restore Schema Diagram window"
    @click="restore"
    @keydown.enter="restore"
    @keydown.space.prevent="restore"
  >
    Schema Diagram
  </div>

  <!-- Fullscreen viewing mode — fills the viewport above everything else -->
  <div
    v-else-if="state.mode === 'fullscreen'"
    class="siw-fullscreen"
    role="dialog"
    aria-roledescription="dialog"
    :aria-label="`Schema Diagram fullscreen (${state.currentIndex + 1} of ${images.length})`"
    tabindex="0"
  >
    <div class="siw-fs-topbar">
      <span class="siw-title">
        Schema Diagram
        <span class="siw-counter" v-if="images.length > 1">
          ({{ state.currentIndex + 1 }}/{{ images.length }})
        </span>
      </span>
      <div class="siw-controls">
        <button
          class="siw-ctrl-btn"
          title="Reset zoom (or double-click the image)"
          @click="resetView"
        >Reset</button>
        <button
          class="siw-ctrl-btn"
          title="Exit fullscreen (Esc)"
          @click="exitFullscreen"
        >✕ Close</button>
      </div>
    </div>

    <div
      ref="fsImgWrapRef"
      class="siw-fs-img-wrap"
      @wheel="onFsWheel"
      @mousedown="onFsMousedown"
      @auxclick="onFsAuxclick"
      @dblclick="onFsDblclick"
    >
      <img
        v-if="objectUrl"
        :src="objectUrl"
        alt="Schema diagram"
        class="siw-fs-img"
        draggable="false"
        :style="{ transform: `translate(${panX}px, ${panY}px) scale(${zoomLevel})` }"
      />
      <div v-else class="siw-img-placeholder">Loading image…</div>
    </div>

    <!-- Navigation row (only shown when multiple images) -->
    <div v-if="images.length > 1" class="siw-nav siw-fs-nav">
      <button
        class="btn btn-secondary siw-nav-btn"
        :disabled="state.currentIndex === 0"
        @click="prev"
      >&#8249; Prev</button>
      <span class="siw-nav-counter">{{ state.currentIndex + 1 }} / {{ images.length }}</span>
      <button
        class="btn btn-secondary siw-nav-btn"
        :disabled="state.currentIndex === images.length - 1"
        @click="next"
      >Next &#8250;</button>
    </div>
  </div>

  <!-- Main floating window (hidden when minimized/fullscreen) -->
  <div
    v-else
    ref="windowRef"
    class="siw-window"
    :class="{
      'siw-window--collapsed': state.mode === 'collapsed',
    }"
    :style="{
      left: state.pos.x + 'px',
      top: state.pos.y + 'px',
      width: state.size.w + 'px',
      height: state.mode === 'collapsed' ? 'auto' : state.size.h + 'px',
    }"
    role="dialog"
    aria-roledescription="window"
    :aria-label="`Schema Diagram (${state.currentIndex + 1} of ${images.length})`"
    tabindex="0"
    @keydown="onKeydown"
  >
    <!-- Title bar -->
    <div
      class="siw-titlebar"
      @mousedown="onTitleMousedown"
    >
      <span class="siw-title">
        Schema Diagram
        <span class="siw-counter" v-if="images.length > 1">
          ({{ state.currentIndex + 1 }}/{{ images.length }})
        </span>
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
          title="Fullscreen"
          @click.stop="enterFullscreen"
          @mousedown.stop
        >⛶</button>
        <button
          class="siw-ctrl-btn"
          title="Minimize"
          @click.stop="minimize"
          @mousedown.stop
        >─</button>
      </div>
    </div>

    <!-- Body — hidden when collapsed -->
    <div v-if="state.mode !== 'collapsed'" class="siw-body">
      <div class="siw-img-wrap">
        <img
          v-if="objectUrl"
          :src="objectUrl"
          alt="Schema diagram"
          class="siw-img"
          draggable="false"
        />
        <div v-else class="siw-img-placeholder">Loading image…</div>
      </div>

      <!-- Navigation row (only shown when multiple images) -->
      <div v-if="images.length > 1" class="siw-nav">
        <button
          class="btn btn-secondary siw-nav-btn"
          :disabled="state.currentIndex === 0"
          @click="prev"
        >&#8249; Prev</button>
        <span class="siw-nav-counter">{{ state.currentIndex + 1 }} / {{ images.length }}</span>
        <button
          class="btn btn-secondary siw-nav-btn"
          :disabled="state.currentIndex === images.length - 1"
          @click="next"
        >Next &#8250;</button>
      </div>
    </div>

    <!-- Resize grip (bottom-right corner) -->
    <div
      v-if="state.mode !== 'collapsed'"
      class="siw-resize-grip"
      title="Resize"
      @mousedown.stop="onResizeMousedown"
    ></div>
  </div>
</template>

<style scoped>
/* ── Minimized chip ─────────────────────────────────────────────────────── */
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
.siw-chip:hover {
  background: var(--color-primary-strong);
}

/* ── Floating window ─────────────────────────────────────────────────────── */
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
  min-width: 360px;
  min-height: 48px; /* title bar height when collapsed */
  outline: none;
  will-change: left, top, width, height;
}

.siw-window:focus {
  box-shadow: 0 0 0 2px var(--color-primary), var(--shadow-2);
}

/* ── Title bar ───────────────────────────────────────────────────────────── */
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

/* ── Body / image area ───────────────────────────────────────────────────── */
.siw-body {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  min-height: 0; /* allow shrinking below content; required for nested flex+overflow */
}

.siw-img-wrap {
  flex: 1;
  min-height: 0; /* same — lets max-height:100% on .siw-img resolve correctly */
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 8px;
  background: var(--color-surface-muted);
}

.siw-img {
  max-width: 100%;
  max-height: 100%;
  width: auto;
  height: auto;
  object-fit: contain;
  display: block;
}

.siw-img-placeholder {
  padding: var(--space-5);
  color: var(--color-text-muted);
  font-size: 0.88rem;
}

/* ── Navigation ─────────────────────────────────────────────────────────── */
.siw-nav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  padding: 6px 12px;
  border-top: 1px solid var(--color-border-soft);
  background: var(--color-surface);
  flex-shrink: 0;
}

.siw-nav-btn {
  font-size: 0.82rem;
  padding: 3px 10px;
}

.siw-nav-counter {
  font-size: 0.82rem;
  color: var(--color-text-muted);
  font-weight: 600;
}

/* ── Resize grip ─────────────────────────────────────────────────────────── */
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

/* ── Fullscreen mode ──────────────────────────────────────────────────────── */
.siw-fullscreen {
  position: fixed;
  inset: 0;
  /* Above the sticky header (z-index 200) and above the normal window/chip. */
  z-index: 900;
  background: var(--color-surface);
  display: flex;
  flex-direction: column;
  outline: none;
}

.siw-fs-topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
  background: var(--color-surface-sunk);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  min-height: 40px;
}

.siw-fs-img-wrap {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-surface-muted);
  cursor: grab;
  touch-action: none;
}
.siw-fs-img-wrap:active { cursor: grabbing; }

.siw-fs-img {
  max-width: 100%;
  max-height: 100%;
  width: auto;
  height: auto;
  object-fit: contain;
  display: block;
  user-select: none;
  will-change: transform;
}

.siw-fs-nav {
  flex-shrink: 0;
}

/* ── Mobile fallback — render inline, hide drag/resize affordances ────────── */
@media (max-width: 767px) {
  .siw-window {
    position: static !important;
    width: 100% !important;
    height: auto !important;
    border-radius: var(--radius);
    min-width: 0;
  }
  .siw-titlebar {
    cursor: default;
  }
  .siw-resize-grip {
    display: none;
  }
  .siw-chip {
    left: 16px;
    right: 16px;
    transform: none;
    text-align: center;
  }
}
</style>
