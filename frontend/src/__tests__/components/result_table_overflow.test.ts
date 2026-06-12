/**
 * Tests for TODO-075
 * Mount ResultTable, simulate ResizeObserver trigger, assert hasOverflow / scroll-hint.
 */
import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import ResultTable from '../../components/ResultTable.vue'

const ROWS = [{ name: 'Alice', age: 30 }, { name: 'Bob', age: 25 }]
const COLUMNS = ['name', 'age']

// We need a ResizeObserver mock because happy-dom may not implement it
let observerCallback: ResizeObserverCallback | null = null

class MockResizeObserver {
  constructor(cb: ResizeObserverCallback) {
    observerCallback = cb
  }
  observe(_el: Element) {}
  unobserve(_el: Element) {}
  disconnect() { observerCallback = null }
}

beforeEach(() => {
  vi.stubGlobal('ResizeObserver', MockResizeObserver)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ResultTable overflow detection (TODO-075)', () => {
  it('renders table rows when rows prop is non-empty', () => {
    const wrapper = mount(ResultTable, {
      props: { rows: ROWS, columns: COLUMNS },
      attachTo: document.body,
    })

    expect(wrapper.find('table').exists()).toBe(true)
    const tds = wrapper.findAll('td')
    expect(tds.length).toBeGreaterThan(0)

    wrapper.unmount()
  })

  it('renders "No rows." when rows is empty', () => {
    const wrapper = mount(ResultTable, {
      props: { rows: [], columns: [] },
    })

    expect(wrapper.find('p.muted').text()).toBe('No rows.')
    expect(wrapper.find('table').exists()).toBe(false)

    wrapper.unmount()
  })

  it('shows scroll-hint span when scrollWidth > clientWidth (overflow detected)', async () => {
    const wrapper = mount(ResultTable, {
      props: { rows: ROWS, columns: COLUMNS },
      attachTo: document.body,
    })

    // Access the wrapper element and override its dimensions to simulate overflow
    const wrapEl = wrapper.find('.responsive-table-wrap').element as HTMLElement
    Object.defineProperty(wrapEl, 'scrollWidth', { configurable: true, get: () => 800 })
    Object.defineProperty(wrapEl, 'clientWidth', { configurable: true, get: () => 300 })

    // Trigger the ResizeObserver callback
    if (observerCallback) {
      observerCallback([] as any, {} as any)
    }
    await flushPromises()

    // scroll-hint should be visible
    expect(wrapper.find('.scroll-hint').exists()).toBe(true)
    expect(wrapper.find('.scroll-hint').text()).toContain('Scroll')

    wrapper.unmount()
  })

  it('hides scroll-hint when no overflow (scrollWidth <= clientWidth)', async () => {
    const wrapper = mount(ResultTable, {
      props: { rows: ROWS, columns: COLUMNS },
      attachTo: document.body,
    })

    const wrapEl = wrapper.find('.responsive-table-wrap').element as HTMLElement
    Object.defineProperty(wrapEl, 'scrollWidth', { configurable: true, get: () => 200 })
    Object.defineProperty(wrapEl, 'clientWidth', { configurable: true, get: () => 500 })

    if (observerCallback) {
      observerCallback([] as any, {} as any)
    }
    await flushPromises()

    expect(wrapper.find('.scroll-hint').exists()).toBe(false)

    wrapper.unmount()
  })

  it('renders all column headers', () => {
    const wrapper = mount(ResultTable, {
      props: { rows: ROWS, columns: COLUMNS },
    })

    const headers = wrapper.findAll('th')
    expect(headers.map(h => h.text())).toEqual(['name', 'age'])

    wrapper.unmount()
  })

  it('applies maxHeight as CSS variable when provided', () => {
    const wrapper = mount(ResultTable, {
      props: { rows: ROWS, columns: COLUMNS, maxHeight: '500px' },
    })

    const wrapEl = wrapper.find('.responsive-table-wrap')
    const style = wrapEl.attributes('style') || ''
    expect(style).toContain('500px')

    wrapper.unmount()
  })
})
