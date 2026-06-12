const DT_FMT = new Intl.DateTimeFormat('en-US', {
  year: 'numeric', month: 'short', day: '2-digit',
  hour: '2-digit', minute: '2-digit', hour12: false,
})
const RT_FMT = new Intl.RelativeTimeFormat('en-US', { numeric: 'auto' })

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  return DT_FMT.format(d)
}

export function formatRelative(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (isNaN(d.getTime())) return ''
  const diffSec = Math.round((d.getTime() - now.getTime()) / 1000)
  const abs = Math.abs(diffSec)
  if (abs < 60)    return RT_FMT.format(diffSec, 'seconds')
  if (abs < 3600)  return RT_FMT.format(Math.round(diffSec / 60), 'minutes')
  if (abs < 86400) return RT_FMT.format(Math.round(diffSec / 3600), 'hours')
  return RT_FMT.format(Math.round(diffSec / 86400), 'days')
}
