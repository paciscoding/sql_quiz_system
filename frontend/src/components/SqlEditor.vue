<script setup lang="ts">
import { ref, onMounted, watch, onBeforeUnmount } from 'vue'
import { EditorState } from '@codemirror/state'
import { EditorView, keymap, lineNumbers, highlightActiveLineGutter } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { sql } from '@codemirror/lang-sql'

const props = defineProps<{
  modelValue: string
  placeholder?: string
  height?: string
}>()

const emit = defineEmits(['update:modelValue'])

const editorContainer = ref<HTMLElement | null>(null)
let view: EditorView | null = null

const initEditor = () => {
  if (!editorContainer.value) return

  const state = EditorState.create({
    doc: props.modelValue,
    extensions: [
      lineNumbers(),
      highlightActiveLineGutter(),
      history(),
      sql(),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      EditorView.updateListener.of((update) => {
        if (update.docChanged) {
          emit('update:modelValue', update.state.doc.toString())
        }
      }),
      EditorView.theme({
        "&": { 
          height: props.height || "300px",
          backgroundColor: "var(--color-surface)",
          color: "var(--color-text)",
          fontFamily: "var(--font-mono)"
        },
        ".cm-scroller": { overflow: "auto" },
        ".cm-gutters": {
          backgroundColor: "var(--color-surface-muted)",
          color: "var(--color-text-muted)",
          borderRight: "1px solid var(--color-border)"
        },
        ".cm-activeLineGutter": {
          backgroundColor: "var(--color-surface-muted)"
        }
      }),
      EditorView.baseTheme({
        "&.cm-focused": { outline: "none" }
      })
    ]
  })

  view = new EditorView({
    state,
    parent: editorContainer.value
  })
}

onMounted(() => {
  initEditor()
})

onBeforeUnmount(() => {
  if (view) {
    view.destroy()
  }
})

// Sync external changes to modelValue back into the editor
watch(() => props.modelValue, (newVal) => {
  if (view && newVal !== view.state.doc.toString()) {
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: newVal }
    })
  }
})
</script>

<template>
  <div ref="editorContainer" class="sql-editor-container"></div>
</template>

<style scoped>
.sql-editor-container {
  border: 2px solid var(--color-border);
  border-radius: 0;
  overflow: hidden;
  background: var(--color-surface);
}
</style>
