export interface IntroImageMetadata {
  id: number
  owner_id: number
  sort_order: number
  mime_type: 'image/png' | 'image/jpeg'
  byte_size: number
  created_at: string
  updated_at: string
}

export interface ReorderEntry {
  id: number
  sort_order: number
}

// Internal helper — mirrors the apiFetch in practice.ts but is scoped to this
// module so we don't need to export a private function from another module.
async function schemaFetch(path: string, options: RequestInit = {}): Promise<any> {
  const token = localStorage.getItem('sql_quiz_token')
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string> ?? {}),
    Authorization: `Bearer ${token}`
  }
  const res = await fetch(path, { ...options, headers })
  if (!res.ok) {
    let body: any = {}
    try { body = await res.json() } catch { /* ignore */ }
    throw new Error(body.error || `HTTP ${res.status}`)
  }
  // 204 No Content — return void
  if (res.status === 204) return undefined
  return res.json()
}

// ─── Category intro-image endpoints ─────────────────────────────────────────

export async function listCategoryIntroImages(catId: number): Promise<IntroImageMetadata[]> {
  const data = await schemaFetch(`/api/v1/practice/categories/${catId}/intro-images`)
  return data.images as IntroImageMetadata[]
}

export async function deleteCategoryIntroImage(catId: number, imgId: number): Promise<void> {
  await schemaFetch(`/api/v1/lecturer/practice/categories/${catId}/intro-images/${imgId}`, {
    method: 'DELETE'
  })
}

export async function reorderCategoryIntroImages(catId: number, order: ReorderEntry[]): Promise<void> {
  await schemaFetch(`/api/v1/lecturer/practice/categories/${catId}/intro-images/order`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order })
  })
}

export function uploadCategoryIntroImage(
  catId: number,
  file: File,
  onProgress?: (pct: number) => void
): Promise<IntroImageMetadata> {
  return new Promise((resolve, reject) => {
    const token = localStorage.getItem('sql_quiz_token')
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/v1/lecturer/practice/categories/${catId}/intro-images`)
    xhr.setRequestHeader('Authorization', `Bearer ${token}`)

    if (onProgress) {
      xhr.upload.addEventListener('progress', (e) => {
        if (e.lengthComputable) {
          onProgress(Math.round((e.loaded / e.total) * 100))
        }
      })
    }

    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText) as IntroImageMetadata)
        } catch {
          reject(new Error('Invalid response from server'))
        }
      } else if (xhr.status === 413) {
        reject(new Error('too large'))
      } else if (xhr.status === 415) {
        reject(new Error('unsupported format'))
      } else {
        let msg = 'Upload failed'
        try {
          const body = JSON.parse(xhr.responseText)
          if (body.error) msg = body.error
        } catch { /* ignore */ }
        reject(new Error(msg))
      }
    })

    xhr.addEventListener('error', () => reject(new Error('Network error during upload')))
    xhr.addEventListener('abort', () => reject(new Error('Upload aborted')))

    const form = new FormData()
    form.append('image', file)
    xhr.send(form)
  })
}

// ─── Exam intro-image endpoints ──────────────────────────────────────────────

export async function listExamIntroImages(examId: number): Promise<IntroImageMetadata[]> {
  const data = await schemaFetch(`/api/v1/quiz/exams/${examId}/intro-images`)
  return data.images as IntroImageMetadata[]
}

export async function deleteExamIntroImage(examId: number, imgId: number): Promise<void> {
  await schemaFetch(`/api/v1/lecturer/exams/${examId}/intro-images/${imgId}`, {
    method: 'DELETE'
  })
}

export async function reorderExamIntroImages(examId: number, order: ReorderEntry[]): Promise<void> {
  await schemaFetch(`/api/v1/lecturer/exams/${examId}/intro-images/order`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order })
  })
}

export function uploadExamIntroImage(
  examId: number,
  file: File,
  onProgress?: (pct: number) => void
): Promise<IntroImageMetadata> {
  return new Promise((resolve, reject) => {
    const token = localStorage.getItem('sql_quiz_token')
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/api/v1/lecturer/exams/${examId}/intro-images`)
    xhr.setRequestHeader('Authorization', `Bearer ${token}`)

    if (onProgress) {
      xhr.upload.addEventListener('progress', (e) => {
        if (e.lengthComputable) {
          onProgress(Math.round((e.loaded / e.total) * 100))
        }
      })
    }

    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText) as IntroImageMetadata)
        } catch {
          reject(new Error('Invalid response from server'))
        }
      } else if (xhr.status === 413) {
        reject(new Error('too large'))
      } else if (xhr.status === 415) {
        reject(new Error('unsupported format'))
      } else {
        let msg = 'Upload failed'
        try {
          const body = JSON.parse(xhr.responseText)
          if (body.error) msg = body.error
        } catch { /* ignore */ }
        reject(new Error(msg))
      }
    })

    xhr.addEventListener('error', () => reject(new Error('Network error during upload')))
    xhr.addEventListener('abort', () => reject(new Error('Upload aborted')))

    const form = new FormData()
    form.append('image', file)
    xhr.send(form)
  })
}

// ─── Blob URL helpers ────────────────────────────────────────────────────────
// NOTE: The <img> tag cannot send an Authorization header, so we fetch the raw
// bytes then hand an object URL to the img element. The caller is responsible
// for calling URL.revokeObjectURL when the URL is no longer needed.

export async function categoryImageBlobUrl(catId: number, imgId: number): Promise<string> {
  const token = localStorage.getItem('sql_quiz_token')
  const res = await fetch(`/api/v1/practice/categories/${catId}/intro-images/${imgId}`, {
    headers: { Authorization: `Bearer ${token}` }
  })
  if (!res.ok) throw new Error(`Failed to fetch image: HTTP ${res.status}`)
  const blob = await res.blob()
  return URL.createObjectURL(blob)
}

export async function examImageBlobUrl(examId: number, imgId: number): Promise<string> {
  const token = localStorage.getItem('sql_quiz_token')
  const res = await fetch(`/api/v1/quiz/exams/${examId}/intro-images/${imgId}`, {
    headers: { Authorization: `Bearer ${token}` }
  })
  if (!res.ok) throw new Error(`Failed to fetch image: HTTP ${res.status}`)
  const blob = await res.blob()
  return URL.createObjectURL(blob)
}
