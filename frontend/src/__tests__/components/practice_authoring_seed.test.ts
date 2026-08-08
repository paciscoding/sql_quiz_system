/**
 * Tests for the four-way Question Type select and the lecturer-authored seed
 * field on PracticeAuthoring.vue (Task 9,
 * docs/superpowers/plans/2026-08-08-lecturer-seed-rows.md, ARCH-043/044/051).
 *
 * The wire format keeps question_type as 'QUERY' | 'DML' (the database CHECK
 * constraint is deliberately unchanged); the four-way authoring dropdown is
 * question_type + dml_kind, converted by the single `questionKind` computed.
 * Neither is exposed on the component instance (plain <script setup>, no
 * defineExpose), so every assertion here observes the DOM effects of that
 * computed rather than reaching into internal state directly.
 *
 * Mocking conventions copied from PracticeAuthoring.layout.test.ts: SqlEditor
 * is a bare textarea (CodeMirror needs a real browser), notification_service
 * is a spy, IntroImagesEditor is stubbed so its fetch-on-mount never runs.
 */

import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PracticeCategory, PracticeQuestion } from '../../services/practice'

vi.mock('../../components/SqlEditor.vue', () => ({
  default: {
    name: 'SqlEditor',
    props: ['modelValue', 'placeholder', 'height'],
    emits: ['update:modelValue'],
    template:
      '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
}))

vi.mock('../../components/IntroImagesEditor.vue', () => ({
  default: {
    name: 'IntroImagesEditor',
    props: ['scope', 'ownerId'],
    template: '<div class="stub-intro-images" :data-scope="scope" :data-owner-id="ownerId" />',
  },
}))

const mockNotify = vi.fn()
vi.mock('../../services/notification_service', () => ({
  useNotifications: () => ({ notify: mockNotify, notifications: [], remove: vi.fn() }),
}))

// editQuestion gates on confirmDiscardIfDirty, which is a real risk here:
// loadCategories sets form.value.category_id asynchronously after the initial
// dirty-check snapshot is taken, which can otherwise mark the fresh form
// "dirty" before the test ever touches it and leave the confirm dialog
// unresolved forever. Always confirming keeps these tests about the
// questionKind computed, not about the discard-guard.
vi.mock('../../services/confirm_service', () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(true) }),
}))

vi.mock('../../services/practice', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/practice')>()
  return {
    ...actual,
    lecturerListQuestions: vi.fn(),
    listCategories: vi.fn(),
  }
})

// Imported after the mocks are hoisted.
import PracticeAuthoring from '../../components/PracticeAuthoring.vue'
import { lecturerListQuestions, listCategories } from '../../services/practice'

function makeCategory(overrides: Partial<PracticeCategory> = {}): PracticeCategory {
  return {
    id: 'cat-1',
    name: 'Joins',
    schema_name: 'practice_cat_1',
    preview_schema_name: '',
    has_preview_schema: false,
    introduction: '',
    question_count: 2,
    ...overrides,
  } as PracticeCategory
}

async function mountPage() {
  const wrapper = mount(PracticeAuthoring, {
    global: { stubs: { BackLink: true, RouterLink: true } },
  })
  await flushPromises()
  return wrapper
}

// None of these fields carry a data-testid, so find them by their field
// label. findField deliberately returns `undefined` (not a not-found
// DOMWrapper) when the label is absent, so callers can do a plain boolean
// existence check with `!!findField(...)` instead of the misleading
// `wrapper.find(...)?.exists()` (which is `undefined`, not `false`, when the
// optional chain short-circuits).
function findField(wrapper: VueWrapper<any>, label: string) {
  return wrapper.findAll('.field').find(f => f.find('label').exists() && f.find('label').text() === label)
}

function typeSelect(wrapper: VueWrapper<any>) {
  const field = findField(wrapper, 'Question Type')
  if (!field) throw new Error('Question Type field not found')
  return field.find('select')
}

function targetTableFieldExists(wrapper: VueWrapper<any>): boolean {
  return !!findField(wrapper, 'Target Table (INSERT/UPDATE/DELETE only)')
}

function seedFieldExists(wrapper: VueWrapper<any>): boolean {
  return !!findField(wrapper, 'Seed rows (sandbox only)')
}

function seedTextarea(wrapper: VueWrapper<any>) {
  const field = findField(wrapper, 'Seed rows (sandbox only)')
  if (!field) throw new Error('Seed rows field not found')
  return field.find('textarea')
}

describe('PracticeAuthoring — four-way type select and seed field (ARCH-043/044/051)', () => {
  beforeEach(() => {
    vi.mocked(lecturerListQuestions).mockReset().mockResolvedValue([] as PracticeQuestion[])
    vi.mocked(listCategories).mockReset().mockResolvedValue([makeCategory()])
    mockNotify.mockClear()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => [] } as Response)))
    localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.clear()
  })

  it('offers exactly QUERY, INSERT, UPDATE, DELETE as selectable options', async () => {
    const wrapper = await mountPage()
    const select = typeSelect(wrapper)
    const options = select.findAll('option')
    const selectable = options.filter(o => o.attributes('disabled') === undefined).map(o => o.element.value)
    expect(selectable).toEqual(['QUERY', 'INSERT', 'UPDATE', 'DELETE'])
  })

  it('choosing DELETE maps to question_type DML + dml_kind DELETE (target table field and seed field both appear)', async () => {
    const wrapper = await mountPage()
    await typeSelect(wrapper).setValue('DELETE')

    expect(typeSelect(wrapper).element.value).toBe('DELETE')
    // Target Table only renders when form.question_type === 'DML'.
    expect(targetTableFieldExists(wrapper)).toBe(true)
    // Seed field only renders when form.dml_kind is UPDATE or DELETE.
    expect(seedFieldExists(wrapper)).toBe(true)
  })

  it('choosing QUERY maps back to question_type QUERY + dml_kind empty', async () => {
    const wrapper = await mountPage()
    await typeSelect(wrapper).setValue('DELETE')
    await typeSelect(wrapper).setValue('QUERY')

    expect(typeSelect(wrapper).element.value).toBe('QUERY')
    expect(targetTableFieldExists(wrapper)).toBe(false)
    expect(seedFieldExists(wrapper)).toBe(false)
  })

  it('the seed textarea is absent for QUERY and INSERT, present for UPDATE and DELETE', async () => {
    const wrapper = await mountPage()

    await typeSelect(wrapper).setValue('QUERY')
    expect(seedFieldExists(wrapper)).toBe(false)

    await typeSelect(wrapper).setValue('INSERT')
    expect(seedFieldExists(wrapper)).toBe(false)

    await typeSelect(wrapper).setValue('UPDATE')
    expect(seedFieldExists(wrapper)).toBe(true)

    await typeSelect(wrapper).setValue('DELETE')
    expect(seedFieldExists(wrapper)).toBe(true)
  })

  it('typing a seed then switching the kind to INSERT clears form.seed_sql', async () => {
    const wrapper = await mountPage()

    await typeSelect(wrapper).setValue('DELETE')
    await seedTextarea(wrapper).setValue("INSERT INTO employee (ssn) VALUES ('999999999')")
    expect(seedTextarea(wrapper).element.value).toBe("INSERT INTO employee (ssn) VALUES ('999999999')")

    await typeSelect(wrapper).setValue('INSERT')
    // The seed field is not rendered for INSERT — confirm it was actually
    // cleared, not merely hidden, by switching back to DELETE.
    expect(seedFieldExists(wrapper)).toBe(false)

    await typeSelect(wrapper).setValue('DELETE')
    expect(seedTextarea(wrapper).element.value).toBe('')
  })

  it('loading a question with question_type DML and no dml_kind falls back to the disabled DML placeholder', async () => {
    vi.mocked(lecturerListQuestions).mockResolvedValue([
      {
        id: 'q-1',
        created_by: 'lect-1',
        category_id: 'cat-1',
        category: 'Joins',
        category_name: 'Joins',
        title: 'Delete a row',
        target_table: 'employee',
        correct_sql: "DELETE FROM employee WHERE ssn = '1'",
        question_type: 'DML',
        requirement: 'Delete the row',
        points: 1,
        strict_ordering: false,
        strict_attrs: false,
        strict_column_order: false,
        expected_result_status: 'captured',
        created_at: '',
        updated_at: '',
        // dml_kind intentionally absent — a legacy DML question written
        // before the column existed.
      } as unknown as PracticeQuestion,
    ])
    const wrapper = await mountPage()

    const editButton = wrapper.findAll('button').find(b => b.text() === 'Edit')
    expect(editButton, 'Edit button for the loaded question').toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()

    expect(typeSelect(wrapper).element.value).toBe('DML')
    const dmlOption = typeSelect(wrapper).findAll('option').find(o => o.element.value === 'DML')
    expect(dmlOption?.attributes('disabled')).toBeDefined()
  })
})
