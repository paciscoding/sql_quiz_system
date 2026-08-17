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

// Every submit now fires a GET .../preflight call BEFORE the actual upload
// POST (the exam-active safeguard) -- see checkExamActiveAndConfirm in the
// component. Most existing tests below don't care about that call at all: a
// bare `vi.fn(() => jsonResponse(report))` answers it too, and since `report`
// carries no exam_active field, `data.exam_active !== true` is true and the
// component proceeds without an extra confirm. Tests that DO care which call
// is which (finding the POST call, or supplying different bodies to
// preflight vs import per invocation) use this URL-aware router instead of a
// bare/sequential mock, so the preflight call can never be mistaken for the
// import call or desync a sequential mockImplementationOnce chain.
function routedFetch(opts: { preflight?: () => Promise<Response>; importResponse?: () => Promise<Response> }) {
  return vi.fn((url: string) => {
    if (typeof url === 'string' && url.includes('/preflight')) {
      return (opts.preflight ?? (() => jsonResponse({ exam_active: false })))()
    }
    return (opts.importResponse ?? (() => jsonResponse(report)))()
  })
}

/** Isolates the actual upload POST call(s) out of a fetch mock's call log. */
function postCalls(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'POST')
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

    // A preflight GET fires first (the exam-active safeguard), then the
    // actual upload POST -- pick the POST call out explicitly rather than
    // assuming index 0, since the preflight call now occupies that slot.
    const [url, init] = postCalls(fetchMock)[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/students/import-credentials')
    expect(init.method).toBe('POST')
    const body = init.body as FormData
    expect(body.get('confirm')).toBe('ROTATE')
    expect(body.get('file')).toBeInstanceOf(File)

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/students/import-credentials/preflight',
      expect.objectContaining({ headers: expect.objectContaining({ Authorization: 'Bearer t' }) }),
    )

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
    // Each submit fires a preflight GET before the upload POST (the
    // exam-active safeguard), so a completed import is 2 calls, not 1.
    expect(fetchMock).toHaveBeenCalledTimes(2)

    // Second attempt: admin declines the "discard?" prompt. That gate runs
    // BEFORE the preflight check, so declining must produce zero additional
    // fetch calls of any kind (not even a preflight GET).
    mockConfirm.mockResolvedValueOnce(false)
    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    expect(mockConfirm).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledTimes(2) // no additional network call at all
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)
  })

  it('lets a second import proceed once confirmed, replacing the displayed results', async () => {
    const secondReport = { ...report, accounts: [report.accounts[0]] }
    // Sequential per-import responses only apply to the upload POST calls;
    // the preflight GET is answered separately by routedFetch so it can
    // never consume a slot meant for an import response.
    let importCallCount = 0
    const fetchMock = routedFetch({
      importResponse: () => {
        importCallCount += 1
        return jsonResponse(importCallCount === 1 ? report : secondReport)
      },
    })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises()

    expect(postCalls(fetchMock)).toHaveLength(2)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(1)
  })

  // The spinner/"Importing" label must reflect only the actual network round
  // trip, not the time spent waiting on the discard-confirmation dialog. The
  // guard used to wrap await confirm(...) too, so the button showed
  // "Importing" with a spinner the entire time the dialog sat open — before
  // any request had even been sent.
  it('does not show the spinner while the discard-confirmation dialog is awaiting a decision', async () => {
    const fetchMock = vi.fn(() => jsonResponse(report))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper) // results held -> the next submit must prompt to discard

    let resolveConfirm!: (v: boolean) => void
    mockConfirm.mockImplementationOnce(() => new Promise<boolean>((resolve) => { resolveConfirm = resolve }))

    await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
    wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
    await flushPromises() // submitImport runs up to (and blocks on) awaiting confirm()

    const button = wrapper.find('[data-testid="rotate-import-submit"]')
    expect(button.text()).not.toContain('Importing')
    // The discard-results confirm gate runs BEFORE the preflight check, and
    // it is still awaiting a decision, so nothing past the earlier setup
    // import's 2 calls (preflight + upload) has fired yet.
    expect(fetchMock).toHaveBeenCalledTimes(2)

    resolveConfirm(true)
    await flushPromises()

    // Once the discard confirm resolves, the guarded import proceeds through
    // its own preflight check (exam_active is absent/false on `report`, so no
    // second confirm) straight to the upload: 2 more calls.
    expect(fetchMock).toHaveBeenCalledTimes(4)
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

  // -------------------------------------------------------------------------
  // CRITICAL 1: a nil/absent errors or accounts array must never throw during
  // render. Go marshals a nil slice as JSON `null`, and a completely clean
  // import (or an all-skipped one) used to leave one of these two fields
  // null. Before the fix, `result.errors.length`/`result.accounts.length`
  // were read unguarded in the template, so null threw a TypeError mid-patch:
  // the whole results subtree failed to mount and the admin saw a success
  // toast with NO table, even though every student in the file had already
  // been rotated and signed out.
  // -------------------------------------------------------------------------

  it('renders the accounts table when the server sends errors: null (an all-clean import)', async () => {
    const cleanReport = { ...report, errors: null }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(cleanReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    // Must not throw during the whole submit -> render cycle.
    await expect(importSuccessfully(wrapper)).resolves.not.toThrow()

    expect(wrapper.find('[data-testid="rotated-accounts-table"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')).toHaveLength(2)
    // The row-errors section must render as "no errors", not crash.
    expect(wrapper.text()).toContain('No row errors.')
  })

  it('does not throw when the server sends accounts: null (an all-skipped import)', async () => {
    const allSkippedReport = {
      ...report,
      job_id: '',
      accounts: null,
      errors: [{ row: 1, student_number: 'N1', message: 'Student number is required' }],
    }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(allSkippedReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await expect(importSuccessfully(wrapper)).resolves.not.toThrow()

    // No accounts table (there are no accounts), but the row-errors table
    // must render, and hasHeldResults-driven code (submitImport itself, and
    // the beforeunload watch below) must not have thrown either.
    expect(wrapper.find('[data-testid="rotated-accounts-table"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Row Errors (1)')
  })

  it('renders "not sent — email is not configured" for not_configured rows without throwing', async () => {
    const notConfiguredReport = {
      ...report,
      job_id: '',
      accounts: [
        { row: 1, student_number: 'N1', student_id_ext: 's1', email: 'a@example.com', password: 'pw1', outcome: 'created', email_status: 'not_configured' },
      ],
    }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(notConfiguredReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    const rows = wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('not sent')
    expect(rows[0].text()).toContain('not configured')
    // Must NOT read as if it were queued/sent.
    expect(rows[0].text()).not.toContain('queued')
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
    // Per-import sequential responses apply only to the upload POST; the
    // preflight GET is routed separately so it can't consume a slot meant
    // for one of the two import responses below.
    let importCallCount = 0
    const fetchMock = routedFetch({
      importResponse: () => {
        importCallCount += 1
        return jsonResponse(importCallCount === 1 ? report : emptyOnSecond)
      },
    })
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

    // Held results from the first import still trigger the discard-confirm
    // gate (resolved true by the default mock), and the submit's own
    // preflight GET adds a second call -- 2 calls total, not 1.
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const [, init] = postCalls(fetchMock)[0] as unknown as [string, RequestInit]
    const uploaded = (init.body as FormData).get('file') as File
    expect(uploaded.name).toBe(displayedName)
    expect(uploaded.name).not.toBe('file-b.csv')
    expect(uploaded.name).toBe('roster.csv')
  })

  // -------------------------------------------------------------------------
  // Owner-approved safeguard: rotating passwords evicts every affected
  // student's session immediately, so submitting while an exam is active
  // must warn and demand a SECOND explicit confirmation naming the
  // consequence plainly -- but never hard-block the admin. Each of these
  // scenarios uses a fresh mount with no held results, so the ONLY confirm()
  // call in play is the exam-active one (the pre-existing "discard held
  // results?" gate never fires).
  // -------------------------------------------------------------------------

  // -------------------------------------------------------------------------
  // MINOR 2: exam_active on the IMPORT response itself (not the preflight
  // check) covers the window between preflight and the import actually
  // running -- an exam that goes active in that gap is exactly the case
  // preflight cannot catch. The admin must be able to see it happened.
  // -------------------------------------------------------------------------

  it('renders an exam-active warning in the results when the import response carries exam_active: true', async () => {
    const examActiveReport = { ...report, exam_active: true }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(examActiveReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    const warning = wrapper.find('[data-testid="exam-active-warning"]')
    expect(warning.exists()).toBe(true)
    expect(warning.text()).toMatch(/exam/i)
    expect(warning.text()).toMatch(/signed out/i)
  })

  it('renders no exam-active warning when exam_active is false', async () => {
    const examInactiveReport = { ...report, exam_active: false }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(examInactiveReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    expect(wrapper.find('[data-testid="exam-active-warning"]').exists()).toBe(false)
  })

  it('renders no exam-active warning when exam_active is absent from the response', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(report)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    expect(wrapper.find('[data-testid="exam-active-warning"]').exists()).toBe(false)
  })

  // -------------------------------------------------------------------------
  // MINOR 3: an unrecognised email_status must not render as "no email" --
  // that positively (and possibly falsely) asserts the row has no address on
  // file, the same class of dishonest-label bug as the "200/200 emailed with
  // SMTP unconfigured" Critical this feature already had to fix.
  // -------------------------------------------------------------------------

  it('does not render an unrecognised email_status as "no email"', async () => {
    const unknownStatusReport = {
      ...report,
      accounts: [
        { row: 1, student_number: 'N1', student_id_ext: 's1', email: 'a@example.com', password: 'pw1', outcome: 'created', email_status: 'some_future_status' },
      ],
    }
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(unknownStatusReport)))

    const wrapper = mount(BulkImportStudentsCredentials)
    await importSuccessfully(wrapper)

    const rows = wrapper.findAll('[data-testid="rotated-accounts-table"] tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).not.toContain('no email')
    expect(rows[0].text()).toContain('some_future_status')
  })

  describe('exam-active preflight safeguard', () => {
    it('with exam_active: true, submitting shows a second confirm; declining issues no upload fetch', async () => {
      const fetchMock = routedFetch({ preflight: () => jsonResponse({ exam_active: true }) })
      vi.stubGlobal('fetch', fetchMock)
      mockConfirm.mockResolvedValue(false) // decline the exam-active confirm

      const wrapper = mount(BulkImportStudentsCredentials)
      await selectFile(wrapper)
      await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
      await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
      await flushPromises()

      expect(mockConfirm).toHaveBeenCalledTimes(1)
      const confirmArgs = mockConfirm.mock.calls[0][0]
      expect(confirmArgs.danger).toBe(true)
      const confirmText = `${confirmArgs.title} ${confirmArgs.body ?? ''}`
      expect(confirmText).toMatch(/exam/i)
      expect(confirmText).toMatch(/sign\w*\s+.*\bout\b/i)

      // Declining must leave state untouched: no upload POST at all, and no
      // job-started event -- only the preflight GET happened.
      expect(postCalls(fetchMock)).toHaveLength(0)
      expect(wrapper.emitted('job-started')).toBeUndefined()
      expect(mockNotify).not.toHaveBeenCalled()
    })

    it('with exam_active: true, accepting the second confirm proceeds with the upload', async () => {
      const fetchMock = routedFetch({ preflight: () => jsonResponse({ exam_active: true }) })
      vi.stubGlobal('fetch', fetchMock)
      mockConfirm.mockResolvedValue(true)

      const wrapper = mount(BulkImportStudentsCredentials)
      await selectFile(wrapper)
      await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
      await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
      await flushPromises()

      expect(mockConfirm).toHaveBeenCalledTimes(1)
      expect(postCalls(fetchMock)).toHaveLength(1)
      expect(wrapper.emitted('job-started')).toEqual([['job-abc']])
    })

    it('with exam_active: false, no second confirm prompt appears', async () => {
      const fetchMock = routedFetch({ preflight: () => jsonResponse({ exam_active: false }) })
      vi.stubGlobal('fetch', fetchMock)

      const wrapper = mount(BulkImportStudentsCredentials)
      await selectFile(wrapper)
      await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
      await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
      await flushPromises()

      expect(mockConfirm).not.toHaveBeenCalled()
      expect(postCalls(fetchMock)).toHaveLength(1)
      expect(wrapper.emitted('job-started')).toEqual([['job-abc']])
    })

    // Failing closed: an unreachable/erroring preflight must neither silently
    // behave as if no exam were active (skip the confirm) nor hard-block the
    // admin outright (refuse to proceed at all) -- it demands the same
    // explicit confirmation the exam_active:true path does.
    it('when the preflight request fails, the same confirmation is still required, and declining issues no upload fetch', async () => {
      const fetchMock = vi.fn((url: string) => {
        if (typeof url === 'string' && url.includes('/preflight')) {
          return Promise.reject(new Error('network error'))
        }
        return jsonResponse(report)
      })
      vi.stubGlobal('fetch', fetchMock)
      mockConfirm.mockResolvedValue(false)

      const wrapper = mount(BulkImportStudentsCredentials)
      await selectFile(wrapper)
      await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
      await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
      await flushPromises()

      expect(mockConfirm).toHaveBeenCalledTimes(1)
      expect(mockConfirm.mock.calls[0][0].danger).toBe(true)
      expect(postCalls(fetchMock)).toHaveLength(0)
      expect(wrapper.emitted('job-started')).toBeUndefined()
    })

    it('when the preflight request fails, accepting the confirmation still proceeds with the upload', async () => {
      const fetchMock = vi.fn((url: string) => {
        if (typeof url === 'string' && url.includes('/preflight')) {
          return Promise.reject(new Error('network error'))
        }
        return jsonResponse(report)
      })
      vi.stubGlobal('fetch', fetchMock)
      mockConfirm.mockResolvedValue(true)

      const wrapper = mount(BulkImportStudentsCredentials)
      await selectFile(wrapper)
      await wrapper.find('[data-testid="rotate-confirm-input"]').setValue('ROTATE')
      await wrapper.find('[data-testid="rotate-import-submit"]').trigger('click')
      await flushPromises()

      expect(mockConfirm).toHaveBeenCalledTimes(1)
      expect(postCalls(fetchMock)).toHaveLength(1)
      expect(wrapper.emitted('job-started')).toEqual([['job-abc']])
    })
  })
})
