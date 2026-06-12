/**
 * Tests for TODO-049..064 — SchemaIntroduction.vue mode prop
 *
 * Verifies that:
 *   - mode='lecturer-preview' (default) shows the introspection table list.
 *   - mode='student' hides the introspection table list.
 *   - Description text renders as plain text in both modes (no v-html).
 */
import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import SchemaIntroduction from '../../components/SchemaIntroduction.vue'
import type { TableIntrospection } from '../../services/practice'

const TABLES: TableIntrospection[] = [
  {
    name: 'employee',
    columns: [
      {
        name: 'id',
        data_type: 'integer',
        nullable: false,
        is_primary_key: true,
        is_foreign_key: false,
      },
      {
        name: 'name',
        data_type: 'text',
        nullable: true,
        is_primary_key: false,
        is_foreign_key: false,
      },
    ],
  },
  {
    name: 'department',
    columns: [
      {
        name: 'dept_id',
        data_type: 'integer',
        nullable: false,
        is_primary_key: true,
        is_foreign_key: false,
      },
    ],
  },
]

describe('SchemaIntroduction.vue — mode prop', () => {
  describe('mode="lecturer-preview" (default)', () => {
    it('renders the introspection tables when tables is non-empty', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: 'Test description',
          tables: TABLES,
          imported: true,
        },
      })

      // Both table blocks should be rendered
      const tableBlocks = wrapper.findAll('.schema-intro__table-block')
      expect(tableBlocks).toHaveLength(2)
      expect(tableBlocks[0].text()).toContain('employee')
      expect(tableBlocks[1].text()).toContain('department')
    })

    it('renders table column headers (Name, Type, Nullable, Keys)', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: '',
          tables: TABLES,
          imported: true,
          mode: 'lecturer-preview',
        },
      })

      const headers = wrapper.findAll('.schema-intro__col-table th').map((th) => th.text())
      expect(headers).toContain('Name')
      expect(headers).toContain('Type')
      expect(headers).toContain('Nullable')
      expect(headers).toContain('Keys')
    })

    it('shows EmptyState when tables array is empty and imported=true', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: '',
          tables: [],
          imported: true,
          mode: 'lecturer-preview',
        },
      })

      // Empty state element should be present
      expect(wrapper.find('.empty-state').exists()).toBe(true)
      expect(wrapper.find('.schema-intro__table-block').exists()).toBe(false)
    })
  })

  describe('mode="student"', () => {
    it('does NOT render the introspection table list', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: 'Some description',
          tables: TABLES,
          imported: true,
          mode: 'student',
        },
      })

      expect(wrapper.find('.schema-intro__table-block').exists()).toBe(false)
    })

    it('does not render EmptyState either in student mode', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: '',
          tables: [],
          imported: false,
          mode: 'student',
        },
      })

      expect(wrapper.find('.empty-state').exists()).toBe(false)
      expect(wrapper.find('.schema-intro__table-block').exists()).toBe(false)
    })
  })

  describe('description text rendering', () => {
    it('renders description in lecturer-preview mode', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: 'Hello description text',
          tables: [],
          imported: true,
          mode: 'lecturer-preview',
        },
      })

      expect(wrapper.find('.schema-intro__description-text').text()).toBe('Hello description text')
    })

    it('renders description in student mode', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: 'Student sees this too',
          tables: TABLES,
          imported: true,
          mode: 'student',
        },
      })

      expect(wrapper.find('.schema-intro__description-text').text()).toBe('Student sees this too')
    })

    it('does not render the description block when introduction is empty', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: '',
          tables: TABLES,
          imported: true,
        },
      })

      expect(wrapper.find('.schema-intro__description').exists()).toBe(false)
    })

    // XSS / v-html guard: HTML-looking input must render as literal text
    it('renders HTML-looking input as literal text — no v-html injection (XSS guard)', () => {
      const xssInput = '<script>alert(1)</script>'
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: xssInput,
          tables: [],
          imported: true,
          mode: 'lecturer-preview',
        },
      })

      const descEl = wrapper.find('.schema-intro__description-text')
      expect(descEl.exists()).toBe(true)

      // text() returns the visible text content — if v-html were used the script
      // tag would not appear as literal text here.
      expect(descEl.text()).toBe(xssInput)

      // No actual <script> element should have been injected into the DOM
      expect(descEl.find('script').exists()).toBe(false)
    })
  })

  describe('title prop', () => {
    it('renders the title when provided', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: '',
          tables: [],
          imported: true,
          title: 'My Schema',
        },
      })

      const titleEl = wrapper.find('.schema-intro__title')
      expect(titleEl.exists()).toBe(true)
      expect(titleEl.text()).toBe('My Schema')
    })

    it('omits the title element when not provided', () => {
      const wrapper = mount(SchemaIntroduction, {
        props: {
          introduction: '',
          tables: [],
          imported: true,
        },
      })

      expect(wrapper.find('.schema-intro__title').exists()).toBe(false)
    })
  })
})
