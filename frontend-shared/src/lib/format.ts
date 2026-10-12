import { DAY, HOUR, MINUTE } from './time'
import type { Interval } from '../api/client'

const nf = (digits: number) =>
  new Intl.NumberFormat('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits })

/** Speeds: 928 / 92.8 / 9.28 — three significant figures is plenty for a speed. */
export function mbps(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '—'
  const a = Math.abs(v)
  return nf(a >= 100 ? 0 : a >= 10 ? 1 : 2).format(v)
}

export function ms(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '—'
  return nf(Math.abs(v) >= 100 ? 0 : 1).format(v)
}

export function pct(v: number | null | undefined): string {
  if (v == null || !isFinite(v)) return '—'
  return nf(v === 0 ? 0 : Math.abs(v) >= 10 ? 1 : 2).format(v)
}

export function count(v: number): string {
  return new Intl.NumberFormat('en-US').format(v)
}

/** "3 hours ago" style; coarse on purpose. */
export function ago(t: number, now: number): string {
  const d = Math.max(0, now - t)
  if (d < MINUTE) return 'just now'
  if (d < HOUR) return plural(Math.floor(d / MINUTE), 'minute') + ' ago'
  if (d < DAY) return plural(Math.floor(d / HOUR), 'hour') + ' ago'
  if (d < 60 * DAY) return plural(Math.floor(d / DAY), 'day') + ' ago'
  return new Date(t).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

const plural = (n: number, unit: string) => `${n} ${unit}${n === 1 ? '' : 's'}`

export function dateTime(t: number | string | Date): string {
  return new Date(t).toLocaleString(undefined, {
    month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit',
  })
}

/** Label for one chart bucket, matched to its size. */
export function bucketLabel(t: number, interval: Interval): string {
  const d = new Date(t)
  switch (interval) {
    case 'raw':
    case 'hour':
      return dateTime(d)
    case 'day':
      return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' })
    case 'week':
      return 'Week of ' + d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
    case 'month':
      return d.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })
    case 'quarter':
      return `Q${Math.floor(d.getMonth() / 3) + 1} ${d.getFullYear()}`
    case 'year':
      return String(d.getFullYear())
  }
}

export function label(connection: string, provider?: string): string {
  const c = connection === 'wired' ? 'Wired' : connection === 'wireless' ? 'Wireless' : connection
  return provider ? `${c} · ${providerName(provider)}` : c
}

export function providerName(p: string): string {
  return p === 'ookla' ? 'Ookla' : p === 'ndt7' ? 'NDT7' : p
}
