import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Icon from '../../components/Icon.vue'

describe('Icon', () => {
  it('renders an svg with the correct path data for a known name', () => {
    const wrapper = mount(Icon, { props: { name: 'arrow-left' } })
    const svg = wrapper.find('svg')
    expect(svg.exists()).toBe(true)
    // Lucide arrow-left path fragment
    expect(svg.html()).toContain('M19 12H5')
  })

  it('renders nothing for an unknown name', () => {
    const wrapper = mount(Icon, { props: { name: 'not-a-real-icon' } })
    expect(wrapper.find('svg').exists()).toBe(false)
  })

  it('applies the size prop to width and height', () => {
    const wrapper = mount(Icon, { props: { name: 'check', size: 24 } })
    const svg = wrapper.find('svg')
    expect(svg.attributes('width')).toBe('24')
    expect(svg.attributes('height')).toBe('24')
  })

  it('is decorative (aria-hidden) by default', () => {
    const wrapper = mount(Icon, { props: { name: 'x' } })
    expect(wrapper.find('svg').attributes('aria-hidden')).toBe('true')
  })

  it('becomes a labelled image when a title is provided', () => {
    const wrapper = mount(Icon, { props: { name: 'x', title: 'Close' } })
    const svg = wrapper.find('svg')
    expect(svg.attributes('role')).toBe('img')
    expect(svg.attributes('aria-label')).toBe('Close')
    expect(svg.attributes('aria-hidden')).toBeUndefined()
  })
})
