import { format } from 'sql-formatter'

export function formatSql(text: string): string {
  if (!text || !text.trim()) return text
  try {
    return format(text, { language: 'postgresql', keywordCase: 'upper', tabWidth: 2 })
  } catch {
    return text
  }
}
