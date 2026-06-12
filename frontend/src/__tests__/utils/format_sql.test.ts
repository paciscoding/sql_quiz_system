import { describe, it, expect } from 'vitest'
import { formatSql } from '../../utils/format_sql'

describe('formatSql', () => {
  it('formats a simple SELECT', () => {
    const result = formatSql('select id from users where id=1')
    expect(result).toContain('SELECT')
  })

  it('returns empty string unchanged', () => {
    expect(formatSql('')).toBe('')
  })

  it('returns whitespace-only string unchanged', () => {
    expect(formatSql('   ')).toBe('   ')
  })

  it('does not throw on unusual input', () => {
    expect(() => formatSql('NOT VALID SQL !!!')).not.toThrow()
  })
})
