/**
 * Tests for TODO-049..064 — schemaImages service
 *
 * Key regression tests:
 *   - reorderCategoryIntroImages uses path /order (not /reorder)
 *   - reorderExamIntroImages uses path /order (not /reorder)
 *   - All requests carry a Bearer token from localStorage.
 */
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import {
  listCategoryIntroImages,
  deleteCategoryIntroImage,
  reorderCategoryIntroImages,
  listExamIntroImages,
  deleteExamIntroImage,
  reorderExamIntroImages,
} from '../../services/schemaImages'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeFetchMock(status: number, body: unknown) {
  return vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  })
}

const TEST_TOKEN = 'test-bearer-token'

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('sql_quiz_token', TEST_TOKEN)
})

afterEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
})

// ---------------------------------------------------------------------------
// Category endpoints
// ---------------------------------------------------------------------------

describe('listCategoryIntroImages', () => {
  it('calls GET /api/v1/practice/categories/:id/intro-images with Bearer header', async () => {
    const images = [{ id: 1, owner_id: 2, sort_order: 0, mime_type: 'image/png', byte_size: 100, created_at: '', updated_at: '' }]
    globalThis.fetch = makeFetchMock(200, { images })

    const result = await listCategoryIntroImages(7)

    expect(globalThis.fetch).toHaveBeenCalledOnce()
    const [url, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/v1/practice/categories/7/intro-images')
    expect((opts?.headers as Record<string, string>)?.Authorization).toBe(`Bearer ${TEST_TOKEN}`)
    expect(result).toEqual(images)
  })

  it('throws when the server returns a non-ok status', async () => {
    globalThis.fetch = makeFetchMock(404, { error: 'not found' })
    await expect(listCategoryIntroImages(99)).rejects.toThrow('not found')
  })
})

describe('deleteCategoryIntroImage', () => {
  it('calls DELETE /api/v1/lecturer/practice/categories/:catId/intro-images/:imgId', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    await deleteCategoryIntroImage(3, 10)

    const [url, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/v1/lecturer/practice/categories/3/intro-images/10')
    expect(opts?.method).toBe('DELETE')
  })
})

// ── Regression: /order path (not /reorder) ────────────────────────────────

describe('reorderCategoryIntroImages — REGRESSION: /order path', () => {
  it('calls PUT /api/v1/lecturer/practice/categories/:id/intro-images/order (not /reorder)', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    const order = [{ id: 1, sort_order: 0 }, { id: 2, sort_order: 1 }]
    await reorderCategoryIntroImages(7, order)

    const [url, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/v1/lecturer/practice/categories/7/intro-images/order')
    expect(url).not.toContain('/reorder')
    expect(opts?.method).toBe('PUT')
  })

  it('sends the order array in the request body', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    const order = [{ id: 5, sort_order: 0 }, { id: 3, sort_order: 1 }]
    await reorderCategoryIntroImages(7, order)

    const [, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    const body = JSON.parse(opts?.body as string)
    expect(body.order).toEqual(order)
  })

  it('sends Authorization Bearer header', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    await reorderCategoryIntroImages(7, [])

    const [, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect((opts?.headers as Record<string, string>)?.Authorization).toBe(`Bearer ${TEST_TOKEN}`)
  })
})

// ---------------------------------------------------------------------------
// Exam endpoints
// ---------------------------------------------------------------------------

describe('listExamIntroImages', () => {
  it('calls GET /api/v1/quiz/exams/:id/intro-images with Bearer header', async () => {
    const images = [{ id: 10, owner_id: 1, sort_order: 0, mime_type: 'image/jpeg', byte_size: 200, created_at: '', updated_at: '' }]
    globalThis.fetch = makeFetchMock(200, { images })

    const result = await listExamIntroImages(4)

    const [url, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/v1/quiz/exams/4/intro-images')
    expect((opts?.headers as Record<string, string>)?.Authorization).toBe(`Bearer ${TEST_TOKEN}`)
    expect(result).toEqual(images)
  })
})

describe('deleteExamIntroImage', () => {
  it('calls DELETE /api/v1/lecturer/exams/:examId/intro-images/:imgId', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    await deleteExamIntroImage(5, 20)

    const [url, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/v1/lecturer/exams/5/intro-images/20')
    expect(opts?.method).toBe('DELETE')
  })
})

// ── Regression: /order path (not /reorder) ────────────────────────────────

describe('reorderExamIntroImages — REGRESSION: /order path', () => {
  it('calls PUT /api/v1/lecturer/exams/:id/intro-images/order (not /reorder)', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    const order = [{ id: 1, sort_order: 0 }, { id: 2, sort_order: 1 }]
    await reorderExamIntroImages(9, order)

    const [url, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect(url).toBe('/api/v1/lecturer/exams/9/intro-images/order')
    expect(url).not.toContain('/reorder')
    expect(opts?.method).toBe('PUT')
  })

  it('sends the order array in the request body', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    const order = [{ id: 7, sort_order: 0 }, { id: 4, sort_order: 1 }]
    await reorderExamIntroImages(9, order)

    const [, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    const body = JSON.parse(opts?.body as string)
    expect(body.order).toEqual(order)
  })

  it('sends Authorization Bearer header', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, status: 204, json: async () => undefined })

    await reorderExamIntroImages(9, [])

    const [, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    expect((opts?.headers as Record<string, string>)?.Authorization).toBe(`Bearer ${TEST_TOKEN}`)
  })
})

// ---------------------------------------------------------------------------
// Token forwarding edge-cases
// ---------------------------------------------------------------------------

describe('token forwarding', () => {
  it('omits the Authorization header when localStorage has no token set', async () => {
    localStorage.removeItem('sql_quiz_token')
    globalThis.fetch = makeFetchMock(200, { images: [] })

    await listCategoryIntroImages(1)

    const [, opts] = (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls[0]
    // No token — Authorization header must be absent (never "Bearer null")
    expect((opts?.headers as Record<string, string>)?.Authorization).toBeUndefined()
  })
})
