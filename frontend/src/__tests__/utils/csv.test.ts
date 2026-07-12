import { describe, it, expect } from 'vitest'
import { escapeCsvCell, toCsv } from '../../utils/csv'

describe('escapeCsvCell', () => {
  it('leaves plain values untouched', () => {
    expect(escapeCsvCell('alice')).toBe('alice')
    expect(escapeCsvCell(42)).toBe('42')
  })

  it('quotes and doubles embedded quotes', () => {
    expect(escapeCsvCell('a"b')).toBe('"a""b"')
  })

  it('quotes values with commas or newlines', () => {
    expect(escapeCsvCell('a,b')).toBe('"a,b"')
    expect(escapeCsvCell('a\nb')).toBe('"a\nb"')
  })

  it('renders null/undefined as empty string', () => {
    expect(escapeCsvCell(undefined as unknown as string)).toBe('')
    expect(escapeCsvCell(null as unknown as string)).toBe('')
  })
})

describe('toCsv', () => {
  it('joins header + rows with CRLF and escapes cells', () => {
    const csv = toCsv(['Name', 'Score'], [['al,ice', 10], ['bob', 20]])
    expect(csv).toBe('Name,Score\r\n"al,ice",10\r\nbob,20')
  })
})
