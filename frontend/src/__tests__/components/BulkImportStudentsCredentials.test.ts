import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../utils/csv', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../utils/csv')>()
  return { ...actual, downloadCsv: vi.fn() }
})

import BulkImportStudentsCredentials from '../../components/BulkImportStudentsCredentials.vue'
import { downloadCsv } from '../../utils/csv'

const report = {
  total: 2,
  created: 1,
  updated: 1,
  skipped: 0,
  errors: [],
  job_id: 'job-abc',
  accounts: [
    { row: 1, student_number: 'N1', student_id_ext: 's1', email: 'a@example.com', password: 'pw1', outcome: 'created', email_status: 'queued' },
    { row: 2, student_number: 'N2', student_id_ext: 's2', email: '', password: 'pw2', outcome: 'updated', email_status: 'no_email' },
  ],
}

function jsonResponse(data: unknown, ok = true, status = 200) {
  return Promise.resolve({ ok, status, json: () => Promise.resolve(data) } as Response)
}

/** Attach a fake file so the component's validation passes without a real input. */
async function selectFile(wrapper: ReturnType<typeof mount>) {
  const file = new File(['x'], 'roster.csv', { type: 'text/csv' })
  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  await input.trigger('change')
}

describe('BulkImportStudentsCredentials', () => {
  beforeEach(() => { localStorage.setItem('sql_quiz_token', 't') })
  afterEach(() => { vi.restoreAllMocks(); localStorage.clear() })

  it('refuses to submit until ROTATE is typed exactly', async () => {
    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)

    const button = wrapper.find('[data-testid="rotate-import-submit"]')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('rotate')
    expect((button.element as HTMLButtonElement).disabled).toBe(true)

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('posts the file plus the confirm field and emits the job id', async () => {
    const fetchMock = vi.fn(() => jsonResponse(report))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/students/import-credentials')
    expect(init.method).toBe('POST')
    const body = init.body as FormData
    expect(body.get('confirm')).toBe('ROTATE')
    expect(body.get('file')).toBeInstanceOf(File)

    expect(wrapper.emitted('job-started')).toEqual([['job-abc']])
  })

  it('renders outcome and email-status for every account row', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    const rows = wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('created')
    expect(rows[0].text()).toContain('queued')
    expect(rows[1].text()).toContain('updated')
    expect(rows[1].text()).toContain('no email')
  })

  it('copy-all-as-CSV emits one line per account', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await selectFile(wrapper)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    await wrapper.find('[data-testid="rotate-copy-csv"]').trigger('click')
    expect(downloadCsv).toHaveBeenCalledTimes(1)
    const [, content] = (downloadCsv as unknown as ReturnType<typeof vi.fn>).mock.calls[0]
    const lines = (content as string).split('\r\n')
    expect(lines).toHaveLength(3) // header + 2 rows
    expect(lines[0]).toBe('student_id_ext,email,password')
    expect(lines[1]).toContain('pw1')
  })
})
