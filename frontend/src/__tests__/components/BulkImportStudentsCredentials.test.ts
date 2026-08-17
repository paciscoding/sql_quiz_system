import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../utils/csv', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../utils/csv')>()
  return { ...actual, downloadCsv: vi.fn() }
})

// Unviewed passwords can only be destroyed after the admin confirms (a second
// import, or picking a different file) — same gate as BulkPasswordReset.vue's
// "Clear results". mockConfirm is controlled per-test so both the accept and
// the decline branches can be exercised.
const mockConfirm = vi.fn()
vi.mock('../../services/confirm_service', () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}))

// Mocked so the new-toast tests can assert on exact message text instead of
// reading rendered toast DOM (this component doesn't mount a toast host).
const mockNotify = vi.fn()
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: mockNotify }),
}))

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
async function selectFile(wrapper: ReturnType<typeof mount>, name = 'roster.csv') {
  const file = new File(['x'], name, { type: 'text/csv' })
  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  await input.trigger('change')
}

async function importSuccessfully(wrapper: ReturnType<typeof mount>) {
  await selectFile(wrapper)
  await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
  await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
  await flushPromises()
}

describe('BulkImportStudentsCredentials', () => {
  beforeEach(() => {
    localStorage.setItem('sql_quiz_token', 't')
    mockConfirm.mockReset().mockResolvedValue(true)
    mockNotify.mockClear()
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    localStorage.clear()
  })

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

  // -------------------------------------------------------------------------
  // Unviewed-password destruction guards (declining leaves results intact)
  // -------------------------------------------------------------------------

  it('prompts for confirmation before a second import overwrites displayed results; declining keeps them and fires no fetch', async () => {
    const fetchMock = vi.fn(() => jsonResponse(report))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledTimes(1)

    // Second attempt: admin declines the "discard?" prompt.
    mockConfirm.mockResolvedValueOnce(false)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    expect(mockConfirm).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledTimes(1) // no second network call
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)
  })

  it('lets a second import proceed once confirmed, replacing the displayed results', async () => {
    const secondReport = { ...report, accounts: [report.accounts[0]] }
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => jsonResponse(report))
      .mockImplementationOnce(() => jsonResponse(secondReport))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(1)
  })

  it('prompts for confirmation before changing the selected file wipes displayed results; declining keeps them', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)

    mockConfirm.mockResolvedValueOnce(false)
    await selectFile(wrapper, 'another-roster.csv')

    expect(mockConfirm).toHaveBeenCalledTimes(1)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)
  })

  it('lets a file change proceed once confirmed, clearing the displayed results', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)
    expect(wrapper.find('[data-testid="rotated-accounts-table"]').exists()).toBe(true)

    await selectFile(wrapper, 'another-roster.csv')

    expect(wrapper.find('[data-testid="rotated-accounts-table"]').exists()).toBe(false)
  })

  // -------------------------------------------------------------------------
  // Toast must reflect what actually happened, not assume emails are in flight
  // -------------------------------------------------------------------------

  it('does not claim passwords are being emailed when job_id is empty (no row had an email)', async () => {
    const noEmailReport = { ...report, job_id: '' }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(noEmailReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    expect(wrapper.emitted('job-started')).toBeUndefined()
    const emailingClaims = mockNotify.mock.calls.filter(([msg]) => String(msg).includes('being emailed'))
    expect(emailingClaims).toHaveLength(0)
    expect(mockNotify).toHaveBeenCalledWith(expect.stringContaining('deliver them by hand'), 'info')
  })

  it('does not claim passwords are being emailed when everything errored (no accounts, no job)', async () => {
    const allErrorsReport = { ...report, job_id: '', accounts: [], errors: [{ row: 1, student_number: 'N1', message: 'bad row' }] }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(allErrorsReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    const emailingClaims = mockNotify.mock.calls.filter(([msg]) => String(msg).includes('being emailed'))
    expect(emailingClaims).toHaveLength(0)
  })

  it('does claim passwords are being emailed when job_id is present', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    expect(mockNotify).toHaveBeenCalledWith(expect.stringContaining('being emailed'), 'success')
  })

  // -------------------------------------------------------------------------
  // beforeunload guard lifetime matches whether unrecoverable secrets are held
  // -------------------------------------------------------------------------

  it('registers the beforeunload guard once results with accounts exist, and removes it once cleared', async () => {
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')

    const emptyOnSecond = { ...report, job_id: '', accounts: [], errors: [{ row: 1, student_number: 'N1', message: 'bad row' }] }
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => jsonResponse(report))
      .mockImplementationOnce(() => jsonResponse(emptyOnSecond))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)

    expect(addSpy).not.toHaveBeenCalledWith('beforeunload', expect.any(Function))

    await importSuccessfully(wrapper)
    expect(addSpy).toHaveBeenCalledWith('beforeunload', expect.any(Function))
    expect(removeSpy).not.toHaveBeenCalledWith('beforeunload', expect.any(Function))

    // Second import (confirmed) resolves to zero accounts -> guard must come off.
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    expect(removeSpy).toHaveBeenCalledWith('beforeunload', expect.any(Function))
  })

  it('removes the beforeunload guard on unmount so it never leaks past this component', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))
    const removeSpy = vi.spyOn(window, 'removeEventListener')

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    wrapper.unmount()
    expect(removeSpy).toHaveBeenCalledWith('beforeunload', expect.any(Function))
  })

  // -------------------------------------------------------------------------
  // Regression: the browser swaps the native <input type="file">'s displayed
  // filename and .files BEFORE the change handler ever runs, and that cannot
  // be intercepted. Declining the discard prompt must not leave the input
  // showing a new file while the component still uploads the old one.
  // -------------------------------------------------------------------------

  it('after declining a file swap, the input keeps showing the file that will actually be uploaded', async () => {
    const fetchMock = vi.fn(() => jsonResponse(report))
    vi.stubGlobal('fetch', fetchMock)

    // This test deliberately does NOT use the shared selectFile()/importSuccessfully()
    // helpers above: those simulate a file pick via a non-writable Object.defineProperty
    // on `input.files`, which (unlike real browsers) permanently blocks any later native
    // assignment to that same property. A real <input type="file"> keeps `.files` a live,
    // re-assignable accessor (that's exactly what `input.files = dataTransfer.files`
    // relies on), so this test drives file selection through DataTransfer end-to-end to
    // stay faithful to that and to let the fix's own DataTransfer-based restore run for real.
    const wrapper = mount(BulkImportStudentsCredentials)
    const inputEl = wrapper.find('input[type="file"]').element as HTMLInputElement

    const fileA = new File(['x'], 'roster.csv', { type: 'text/csv' })
    const dtA = new DataTransfer()
    dtA.items.add(fileA)
    inputEl.files = dtA.files
    await wrapper.find('input[type="file"]').trigger('change')
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2) // results now held

    // Admin picks a different file — the browser has ALREADY updated the
    // input's displayed filename/.files to file-b.csv by the time `change`
    // fires — then declines the discard prompt.
    mockConfirm.mockResolvedValueOnce(false)
    const fileB = new File(['y'], 'file-b.csv', { type: 'text/csv' })
    const dtB = new DataTransfer()
    dtB.items.add(fileB)
    inputEl.files = dtB.files
    await wrapper.find('input[type="file"]').trigger('change')

    expect(mockConfirm).toHaveBeenCalledTimes(1)
    // Results must still be intact (decline = no destruction).
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)

    // The input's displayed file must agree with what a submit would upload —
    // it must NOT still be showing file-b.csv while roster.csv gets uploaded.
    const displayedName = (wrapper.find('input[type="file"]').element as HTMLInputElement).files?.[0]?.name
    expect(displayedName).not.toBe('file-b.csv')

    // A subsequent submit must upload exactly the file the input indicates —
    // proven against the actual FormData sent to fetch, not just internal state.
    fetchMock.mockClear()
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    const uploaded = (init.body as FormData).get('file') as File
    expect(uploaded.name).toBe(displayedName)
    expect(uploaded.name).not.toBe('file-b.csv')
    expect(uploaded.name).toBe('roster.csv')
  })
})
