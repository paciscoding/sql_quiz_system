/**
 * Tests for TODO-080 (ARCH-034 (1) CollapsibleCard.vue shared disclosure primitive).
 * happy-dom only reports display:none via getComputedStyle when the element is
 * attached to document.body, so every test that asserts on isVisible() mounts
 * with attachTo: document.body and unmounts afterwards (house convention, see
 * result_table_overflow.test.ts).
 */
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import CollapsibleCard from '../../components/CollapsibleCard.vue'

describe('CollapsibleCard', () => {
  it('hides the body by default', () => {
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General' },
      slots: { default: '<p class="body-content">hello</p>' },
      attachTo: document.body,
    })

    const body = wrapper.find('.collapsible-card__body')
    expect(body.exists()).toBe(true)
    expect(body.isVisible()).toBe(false)

    wrapper.unmount()
  })

  it('reveals the body and flips aria-expanded from false to true on click', async () => {
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General' },
      slots: { default: '<p class="body-content">hello</p>' },
      attachTo: document.body,
    })

    const button = wrapper.find('button')
    expect(button.attributes('aria-expanded')).toBe('false')

    await button.trigger('click')

    expect(button.attributes('aria-expanded')).toBe('true')
    expect(wrapper.find('.collapsible-card__body').isVisible()).toBe(true)

    wrapper.unmount()
  })

  it("aria-controls references the body element's real id", () => {
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General' },
      slots: { default: '<p class="body-content">hello</p>' },
    })

    const button = wrapper.find('button')
    const controlsId = button.attributes('aria-controls')
    expect(controlsId).toBeTruthy()

    const body = wrapper.find('.collapsible-card__body')
    expect(body.attributes('id')).toBe(controlsId)

    wrapper.unmount()
  })

  it('keeps header-actions content clickable while the card is collapsed', async () => {
    let clicked = false
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General' },
      slots: {
        'header-actions': '<button class="delete-btn">Delete</button>',
        default: '<p class="body-content">hello</p>',
      },
      attachTo: document.body,
    })

    // Body still hidden (collapsed).
    expect(wrapper.find('.collapsible-card__body').isVisible()).toBe(false)

    const deleteBtn = wrapper.find('.delete-btn')
    expect(deleteBtn.exists()).toBe(true)
    expect(deleteBtn.isVisible()).toBe(true)

    deleteBtn.element.addEventListener('click', () => { clicked = true })
    await deleteBtn.trigger('click')
    expect(clicked).toBe(true)

    wrapper.unmount()
  })

  it('renders all three slots (header-meta, header-actions, default)', () => {
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General', defaultOpen: true },
      slots: {
        'header-meta': '<span class="meta-slot">3 questions</span>',
        'header-actions': '<button class="actions-slot">Rename</button>',
        default: '<p class="default-slot">body content</p>',
      },
    })

    expect(wrapper.find('.meta-slot').exists()).toBe(true)
    expect(wrapper.find('.meta-slot').text()).toBe('3 questions')
    expect(wrapper.find('.actions-slot').exists()).toBe(true)
    expect(wrapper.find('.default-slot').exists()).toBe(true)
    expect(wrapper.find('.default-slot').text()).toBe('body content')

    wrapper.unmount()
  })

  it('honours defaultOpen to render the body expanded on first paint', () => {
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General', defaultOpen: true },
      slots: { default: '<p class="body-content">hello</p>' },
      attachTo: document.body,
    })

    expect(wrapper.find('button').attributes('aria-expanded')).toBe('true')
    expect(wrapper.find('.collapsible-card__body').isVisible()).toBe(true)

    wrapper.unmount()
  })

  it('renders the title and optional subtitle', () => {
    const wrapper = mount(CollapsibleCard, {
      props: { title: 'General', subtitle: 'general_schema' },
    })

    expect(wrapper.find('.collapsible-card__title').text()).toBe('General')
    expect(wrapper.find('.collapsible-card__subtitle').text()).toBe('general_schema')

    wrapper.unmount()
  })

  it('gives each instance a distinct body id and wires aria-controls to its own body, not a shared one', () => {
    // Regression test for TODO-078: uidCounter previously lived inside
    // <script setup>, so it was reset to 0 for every instance and every card
    // rendered the same id. With N cards in a v-for, every button's
    // aria-controls resolved to the FIRST card's body. Asserting id
    // uniqueness alone would not catch that -- a component that hard-wired
    // aria-controls to a constant string would still pass a uniqueness-only
    // check on the ids of the *body* elements. We must assert that each
    // button's aria-controls equals *its own* card's body id specifically.
    const wrapperA = mount(CollapsibleCard, {
      props: { title: 'General' },
      slots: { default: '<p class="body-content">a</p>' },
    })
    const wrapperB = mount(CollapsibleCard, {
      props: { title: 'Advanced' },
      slots: { default: '<p class="body-content">b</p>' },
    })

    const bodyIdA = wrapperA.find('.collapsible-card__body').attributes('id')
    const bodyIdB = wrapperB.find('.collapsible-card__body').attributes('id')

    expect(bodyIdA).toBeTruthy()
    expect(bodyIdB).toBeTruthy()
    expect(bodyIdA).not.toBe(bodyIdB)

    const controlsA = wrapperA.find('button').attributes('aria-controls')
    const controlsB = wrapperB.find('button').attributes('aria-controls')

    expect(controlsA).toBe(bodyIdA)
    expect(controlsB).toBe(bodyIdB)
    expect(controlsA).not.toBe(controlsB)

    wrapperA.unmount()
    wrapperB.unmount()
  })
})
