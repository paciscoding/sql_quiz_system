<script setup lang="ts">
import { ref, onUnmounted } from 'vue'
import { useNotifications } from '../services/notification_service'
import {
  listCategoryIntroImages,
  listExamIntroImages,
  deleteCategoryIntroImage,
  deleteExamIntroImage,
  reorderCategoryIntroImages,
  reorderExamIntroImages,
  uploadCategoryIntroImage,
  uploadExamIntroImage,
  categoryImageBlobUrl,
  examImageBlobUrl,
  type IntroImageMetadata,
} from '../services/schemaImages'

const props = defineProps<{
  scope: 'category' | 'exam'
  ownerId: number
}>()

const { notify } = useNotifications()

// ─── Image list ──────────────────────────────────────────────────────────────

const images = ref<IntroImageMetadata[]>([])
const loading = ref(false)
const localError = ref<string | null>(null)

// Thumbnail blob URLs keyed by image id — revoked on unmount
const thumbUrls = ref<Record<number, string>>({})

async function loadThumbs(imgs: IntroImageMetadata[]) {
  // Revoke URLs no longer needed
  const newIds = new Set(imgs.map((i) => i.id))
  for (const id of Object.keys(thumbUrls.value).map(Number)) {
    if (!newIds.has(id)) {
      URL.revokeObjectURL(thumbUrls.value[id])
      delete thumbUrls.value[id]
    }
  }
  // Load new ones
  await Promise.all(
    imgs.map(async (img) => {
      if (thumbUrls.value[img.id]) return
      try {
        thumbUrls.value[img.id] =
          props.scope === 'category'
            ? await categoryImageBlobUrl(props.ownerId, img.id)
            : await examImageBlobUrl(props.ownerId, img.id)
      } catch {
        // Non-fatal: thumbnail fails to load
      }
    })
  )
}

async function fetchImages() {
  loading.value = true
  localError.value = null
  try {
    images.value =
      props.scope === 'category'
        ? await listCategoryIntroImages(props.ownerId)
        : await listExamIntroImages(props.ownerId)
    await loadThumbs(images.value)
  } catch (err: any) {
    localError.value = err?.message ?? 'Failed to load images.'
  } finally {
    loading.value = false
  }
}

fetchImages()

onUnmounted(() => {
  for (const url of Object.values(thumbUrls.value)) {
    URL.revokeObjectURL(url)
  }
  thumbUrls.value = {}
})

// ─── Upload ──────────────────────────────────────────────────────────────────

interface UploadEntry {
  file: File
  pct: number
  error: string | null
}

const uploads = ref<UploadEntry[]>([])

const MAX_FILE_SIZE = 5 * 1024 * 1024 // 5 MB

async function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  if (!input.files || input.files.length === 0) return
  const files = Array.from(input.files)
  // Reset input so the same file can be re-selected after a failure
  input.value = ''

  // Client-side size check
  const oversized = files.filter((f) => f.size > MAX_FILE_SIZE)
  if (oversized.length > 0) {
    notify(`File(s) exceed 5 MB: ${oversized.map((f) => f.name).join(', ')}`, 'error')
    return
  }

  // Sequential upload
  for (const file of files) {
    const entry: UploadEntry = { file, pct: 0, error: null }
    uploads.value.push(entry)
    try {
      await (props.scope === 'category'
        ? uploadCategoryIntroImage(props.ownerId, file, (pct) => (entry.pct = pct))
        : uploadExamIntroImage(props.ownerId, file, (pct) => (entry.pct = pct)))
      entry.pct = 100
      notify(`Uploaded ${file.name}`, 'success')
    } catch (err: any) {
      const msg = mapUploadError(err?.message ?? '')
      entry.error = msg
      notify(msg, 'error')
    } finally {
      // Remove this entry after a brief delay so the user can see the result
      setTimeout(() => {
        const idx = uploads.value.indexOf(entry)
        if (idx !== -1) uploads.value.splice(idx, 1)
      }, 2000)
    }
  }

  await fetchImages()
}

function mapUploadError(raw: string): string {
  if (raw === 'too large') return 'Aggregate intro-image size exceeds 25 MB.'
  if (raw === 'unsupported format') return 'Only PNG and JPEG are accepted.'
  return raw || 'Upload failed.'
}

// ─── Reorder ─────────────────────────────────────────────────────────────────

async function moveUp(index: number) {
  if (index === 0) return
  const swapped = [...images.value]
  ;[swapped[index - 1], swapped[index]] = [swapped[index], swapped[index - 1]]
  const order = swapped.map((img, i) => ({ id: img.id, sort_order: i }))
  try {
    if (props.scope === 'category') {
      await reorderCategoryIntroImages(props.ownerId, order)
    } else {
      await reorderExamIntroImages(props.ownerId, order)
    }
    await fetchImages()
  } catch (err: any) {
    notify(err?.message ?? 'Reorder failed.', 'error')
  }
}

async function moveDown(index: number) {
  if (index === images.value.length - 1) return
  const swapped = [...images.value]
  ;[swapped[index], swapped[index + 1]] = [swapped[index + 1], swapped[index]]
  const order = swapped.map((img, i) => ({ id: img.id, sort_order: i }))
  try {
    if (props.scope === 'category') {
      await reorderCategoryIntroImages(props.ownerId, order)
    } else {
      await reorderExamIntroImages(props.ownerId, order)
    }
    await fetchImages()
  } catch (err: any) {
    notify(err?.message ?? 'Reorder failed.', 'error')
  }
}

// ─── Delete ──────────────────────────────────────────────────────────────────

async function deleteImage(img: IntroImageMetadata) {
  if (!confirm(`Delete image #${img.id}?`)) return
  try {
    if (props.scope === 'category') {
      await deleteCategoryIntroImage(props.ownerId, img.id)
    } else {
      await deleteExamIntroImage(props.ownerId, img.id)
    }
    notify('Image deleted.', 'success')
    await fetchImages()
  } catch (err: any) {
    notify(err?.message ?? 'Delete failed.', 'error')
  }
}

function formatKb(bytes: number): string {
  return (bytes / 1024).toFixed(1) + ' KB'
}
</script>

<template>
  <div class="iie-root">
    <div class="iie-header">
      <h4 class="iie-heading">Intro Images</h4>
      <label class="btn btn-secondary btn-sm iie-upload-btn">
        Add Images
        <input
          type="file"
          accept="image/png,image/jpeg"
          multiple
          class="iie-file-input"
          @change="onFileChange"
        />
      </label>
    </div>

    <!-- Local error banner -->
    <div v-if="localError" class="iie-error-banner">{{ localError }}</div>

    <!-- Upload progress rows -->
    <div v-if="uploads.length > 0" class="iie-uploads">
      <div v-for="(u, i) in uploads" :key="i" class="iie-upload-row">
        <span class="iie-upload-name">{{ u.file.name }}</span>
        <template v-if="u.error">
          <span class="iie-upload-error">{{ u.error }}</span>
        </template>
        <template v-else>
          <div class="iie-progress-bar">
            <div class="iie-progress-fill" :style="{ width: u.pct + '%' }"></div>
          </div>
          <span class="iie-upload-pct">{{ u.pct }}%</span>
        </template>
      </div>
    </div>

    <!-- Image list table -->
    <div v-if="loading" class="iie-loading">Loading images…</div>
    <template v-else-if="images.length > 0">
      <div class="iie-table-wrap">
        <table class="iie-table">
          <thead>
            <tr>
              <th class="iie-th-num">#</th>
              <th class="iie-th-thumb">Preview</th>
              <th class="iie-th-size">Size</th>
              <th class="iie-th-actions">Actions</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(img, idx) in images" :key="img.id">
              <td class="iie-td-num">{{ idx + 1 }}</td>
              <td class="iie-td-thumb">
                <img
                  v-if="thumbUrls[img.id]"
                  :src="thumbUrls[img.id]"
                  :alt="'Image ' + (idx + 1)"
                  class="iie-thumb"
                />
                <span v-else class="iie-thumb-placeholder">…</span>
              </td>
              <td class="iie-td-size">{{ formatKb(img.byte_size) }}</td>
              <td class="iie-td-actions">
                <button
                  class="btn btn-secondary btn-sm"
                  :disabled="idx === 0"
                  title="Move up"
                  @click="moveUp(idx)"
                >&#8593;</button>
                <button
                  class="btn btn-secondary btn-sm"
                  :disabled="idx === images.length - 1"
                  title="Move down"
                  @click="moveDown(idx)"
                >&#8595;</button>
                <button
                  class="btn btn-sm iie-delete-btn"
                  title="Delete"
                  @click="deleteImage(img)"
                >Delete</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <div v-else class="iie-empty">
      No intro images yet. Add PNG or JPEG files using the button above.
    </div>
  </div>
</template>

<style scoped>
.iie-root {
  margin-top: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
  padding: var(--space-3);
}

.iie-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  margin-bottom: var(--space-3);
}

.iie-heading {
  margin: 0;
  font-size: 0.88rem;
  font-weight: 700;
  color: var(--color-text);
}

.iie-upload-btn {
  position: relative;
  overflow: hidden;
  cursor: pointer;
}

.iie-file-input {
  position: absolute;
  inset: 0;
  opacity: 0;
  cursor: pointer;
  font-size: 0;
}

.iie-error-banner {
  padding: 8px 12px;
  background: var(--color-danger-bg);
  border: 1px solid #e9bcbc;
  color: var(--color-danger);
  border-radius: var(--radius);
  font-size: 0.85rem;
  margin-bottom: var(--space-2);
}

/* Upload progress */
.iie-uploads {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  margin-bottom: var(--space-3);
}

.iie-upload-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: 0.82rem;
  padding: 4px 0;
}

.iie-upload-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text);
}

.iie-upload-error {
  color: var(--color-danger);
  font-size: 0.8rem;
}

.iie-progress-bar {
  width: 80px;
  height: 6px;
  background: var(--color-surface-sunk);
  border-radius: 999px;
  overflow: hidden;
}

.iie-progress-fill {
  height: 100%;
  background: var(--color-primary);
  transition: width 0.1s linear;
}

.iie-upload-pct {
  min-width: 32px;
  text-align: right;
  color: var(--color-text-muted);
  font-size: 0.78rem;
}

/* Image list table */
.iie-table-wrap {
  overflow-x: auto;
}

.iie-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.83rem;
}

.iie-table th,
.iie-table td {
  padding: 6px 10px;
  border-bottom: 1px solid var(--color-border-soft);
  vertical-align: middle;
  text-align: left;
}

.iie-table tr:last-child td {
  border-bottom: none;
}

.iie-table th {
  background: var(--color-surface-muted);
  font-size: 0.72rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-text-muted);
}

.iie-th-num  { width: 36px; }
.iie-th-thumb { width: 80px; }
.iie-th-size  { width: 80px; }

.iie-td-num {
  color: var(--color-text-muted);
  font-size: 0.78rem;
}

.iie-thumb {
  width: 64px;
  height: 48px;
  object-fit: cover;
  border: 1px solid var(--color-border-soft);
  border-radius: var(--radius-sm);
  display: block;
}

.iie-thumb-placeholder {
  display: inline-block;
  width: 64px;
  height: 48px;
  background: var(--color-surface-sunk);
  border-radius: var(--radius-sm);
}

.iie-td-actions {
  display: flex;
  gap: var(--space-1);
  flex-wrap: wrap;
  align-items: center;
}

.iie-delete-btn {
  border-color: var(--color-danger);
  color: var(--color-danger);
}

.iie-loading,
.iie-empty {
  padding: var(--space-4);
  text-align: center;
  color: var(--color-text-muted);
  font-size: 0.85rem;
  background: var(--color-surface-muted);
  border: 1px dashed var(--color-border);
  border-radius: var(--radius);
}
</style>
