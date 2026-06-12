import { describe, it, expect } from 'vitest'
import { formatDate, formatRelative } from '../../utils/format_date'

describe('formatDate', () => {
  it('returns em dash for null', () => {
    expect(formatDate(null)).toBe('—')
  })

  it('returns em dash for undefined', () => {
    expect(formatDate(undefined)).toBe('—')
  })

  it('returns em dash for invalid date string', () => {
    expect(formatDate('not-a-date')).toBe('—')
  })

  it('returns formatted string for valid ISO date', () => {
    const result = formatDate('2024-01-15T10:30:00Z')
    expect(result).not.toBe('—')
    expect(result).toContain('2024')
  })
})

describe('formatRelative', () => {
  it('returns empty string for null', () => {
    expect(formatRelative(null)).toBe('')
  })

  it('returns empty string for undefined', () => {
    expect(formatRelative(undefined)).toBe('')
  })

  it('returns empty string for invalid date', () => {
    expect(formatRelative('not-a-date')).toBe('')
  })

  it('returns relative string for a date 5 minutes ago', () => {
    const now = new Date()
    const fiveMinutesAgo = new Date(now.getTime() - 5 * 60 * 1000).toISOString()
    const result = formatRelative(fiveMinutesAgo, now)
    expect(result).toContain('minute')
  })

  it('returns relative string for a date 2 hours ago', () => {
    const now = new Date()
    const twoHoursAgo = new Date(now.getTime() - 2 * 60 * 60 * 1000).toISOString()
    const result = formatRelative(twoHoursAgo, now)
    expect(result).toContain('hour')
  })
})
