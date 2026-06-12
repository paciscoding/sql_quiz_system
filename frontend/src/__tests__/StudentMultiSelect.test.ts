import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { describe, expect, it } from 'vitest'
import StudentMultiSelect from '../components/StudentMultiSelect.vue'

type Student = {
  id: number
  username: string
}

const mkStudents = (n: number): Student[] =>
  Array.from({ length: n }, (_, i) => ({
    id: i + 1,
    username: `student${i + 1}`,
  }))

function mountStudentMultiSelect(props: {
  modelValue: number[]
  students: Student[]
  loading?: boolean
  placeholder?: string
  maxHeight?: string
}) {
  return mount(StudentMultiSelect, { props })
}

function findButton(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text() === text)

  if (!button) {
    throw new Error(`Button not found: ${text}`)
  }

  return button
}

describe('StudentMultiSelect', () => {
  describe('basic rendering', () => {
    it('renders one row per students prop', () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
        ],
        modelValue: [],
      })

      expect(wrapper.findAll('[role="option"]')).toHaveLength(2)
    })

    it('renders empty state No students found when students is empty', () => {
      const wrapper = mountStudentMultiSelect({
        students: [],
        modelValue: [],
      })

      expect(wrapper.text()).toContain('No students found.')
    })

    it('shows loading state when loading prop is true', () => {
      const wrapper = mountStudentMultiSelect({
        loading: true,
        students: [],
        modelValue: [],
      })

      expect(wrapper.text()).toContain('Loading students...')
    })
  })

  describe('selection behavior', () => {
    it('checking a row emits update:modelValue with the new ID added', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [{ id: 1, username: 'alice' }],
        modelValue: [],
      })

      await wrapper.find('input[type="checkbox"]').trigger('change')

      expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual([1])
    })

    it('unchecking a row emits update:modelValue with the ID removed', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [{ id: 1, username: 'alice' }],
        modelValue: [1],
      })

      await wrapper.find('input[type="checkbox"]').trigger('change')

      expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual([])
    })
  })

  describe('Select All / Select None', () => {
    it('Select All adds all currently-filtered IDs to selection (union with existing)', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
          { id: 3, username: 'charlie' },
        ],
        modelValue: [3],
      })

      await findButton(wrapper, '[Select All]').trigger('click')

      expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual(
        expect.arrayContaining([1, 2, 3]),
      )
    })

    it('Select None removes only the currently-filtered IDs from selection', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
          { id: 3, username: 'charlie' },
        ],
        modelValue: [1, 2, 3],
      })

      await findButton(wrapper, '[Select None]').trigger('click')

      expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual([])
    })
  })

  describe('search filtering', () => {
    it('search input filters the visible rows (case-insensitive)', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'BOB' },
        ],
        modelValue: [],
      })

      await wrapper.find('input[aria-label="Search students"]').setValue('alice')
      await nextTick()

      const rows = wrapper.findAll('[role="option"]')
      expect(rows).toHaveLength(1)
      expect(rows[0].text()).toContain('alice')
    })

    it('search shows No students match when filter finds nothing', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [{ id: 1, username: 'alice' }],
        modelValue: [],
      })

      await wrapper.find('input[aria-label="Search students"]').setValue('xyz')
      await nextTick()

      expect(wrapper.text()).toContain('No students match.')
    })

    it('Select All with active filter only adds filtered IDs (union)', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
        ],
        modelValue: [],
      })

      await wrapper.find('input[aria-label="Search students"]').setValue('alice')
      await nextTick()
      await findButton(wrapper, '[Select All]').trigger('click')

      expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual([1])
    })

    it('Select None with active filter only removes filtered IDs (preserves others)', async () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
        ],
        modelValue: [1, 2],
      })

      await wrapper.find('input[aria-label="Search students"]').setValue('alice')
      await nextTick()
      await findButton(wrapper, '[Select None]').trigger('click')

      expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual([2])
    })
  })

  describe('accessibility', () => {
    it('container has role=group', () => {
      const wrapper = mountStudentMultiSelect({
        students: [],
        modelValue: [],
      })

      expect(wrapper.attributes('role')).toBe('group')
    })

    it('listbox has role=listbox', () => {
      const wrapper = mountStudentMultiSelect({
        students: [{ id: 1, username: 'alice' }],
        modelValue: [],
      })

      expect(wrapper.find('[role="listbox"]').exists()).toBe(true)
    })

    it('rows have role=option and aria-selected reflecting modelValue', () => {
      const wrapper = mountStudentMultiSelect({
        students: [
          { id: 1, username: 'alice' },
          { id: 2, username: 'bob' },
        ],
        modelValue: [1],
      })

      const rows = wrapper.findAll('[role="option"]')
      expect(rows[0].attributes('aria-selected')).toBe('true')
      expect(rows[1].attributes('aria-selected')).toBe('false')
    })

    it('arrow key navigation moves focus through rendered rows', async () => {
      const wrapper = mountStudentMultiSelect({
        students: mkStudents(3),
        modelValue: [],
      })

      const searchInput = wrapper.find('input[aria-label="Search students"]')

      await searchInput.trigger('keydown.down')
      await nextTick()
      expect(wrapper.findAll('[role="option"]')[0].attributes('tabindex')).toBe('0')

      await wrapper.find('[role="listbox"]').trigger('keydown.down')
      await nextTick()
      expect(wrapper.findAll('[role="option"]')[1].attributes('tabindex')).toBe('0')

      await wrapper.find('[role="listbox"]').trigger('keydown.up')
      await nextTick()
      expect(wrapper.findAll('[role="option"]')[0].attributes('tabindex')).toBe('0')
    })
  })
})
