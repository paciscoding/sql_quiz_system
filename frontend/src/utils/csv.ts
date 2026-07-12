// Client-side CSV building + download. Used by lecturer score exports so the
// browser generates the file from data already fetched over an authenticated
// fetch() — avoiding token-in-URL downloads (window.open cannot send the
// Authorization header, which previously 401'd the /export-scores endpoint).

/** Escape one CSV cell per RFC 4180: quote if it contains ", comma, or newline. */
export function escapeCsvCell(value: string | number): string {
  const s = String(value ?? '')
  if (/[",\n\r]/.test(s)) {
    return `"${s.replace(/"/g, '""')}"`
  }
  return s
}

/** Build a CSV string from a header row + data rows. */
export function toCsv(header: string[], rows: Array<Array<string | number>>): string {
  const lines = [header, ...rows].map(row => row.map(escapeCsvCell).join(','))
  return lines.join('\r\n')
}

/** Trigger a browser download of `content` as `filename`. */
export function downloadCsv(filename: string, content: string): void {
  // Prepend a UTF-8 BOM so Excel opens non-ASCII names correctly.
  const blob = new Blob(['﻿' + content], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
