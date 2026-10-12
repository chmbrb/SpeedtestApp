import type { ConnectionType, Interval, Provider } from '../api/client'
import { DAY, HOUR } from '../lib/time'

export const RANGES = [
  { key: '24h', label: 'Last 24 hours', ms: DAY },
  { key: '7d', label: 'Last 7 days', ms: 7 * DAY },
  { key: '30d', label: 'Last 30 days', ms: 30 * DAY },
  { key: '90d', label: 'Last 90 days', ms: 90 * DAY },
  { key: '1y', label: 'Last year', ms: 365 * DAY },
  { key: 'all', label: 'All time', ms: 0 },
] as const
export type RangeKey = (typeof RANGES)[number]['key']

export const INTERVALS: { key: 'auto' | Interval; label: string }[] = [
  { key: 'auto', label: 'Automatic' },
  { key: 'raw', label: 'Every test' },
  { key: 'hour', label: 'Hourly' },
  { key: 'day', label: 'Daily' },
  { key: 'week', label: 'Weekly' },
  { key: 'month', label: 'Monthly' },
]

export interface Filters {
  range: RangeKey
  devices: string[]
  connection: 'all' | ConnectionType
  providers: Provider[]
  interval: 'auto' | Interval
}

// Ookla and NDT7 are not comparable (parallel vs single connection), so the default shows one provider.
export const defaultFilters: Filters = {
  range: '30d',
  devices: [],
  connection: 'all',
  providers: ['ookla'],
  interval: 'auto',
}

const list = (v: string | null) => (v ? v.split(',').map((s) => s.trim()).filter(Boolean) : [])

/** Read filters from a URL query string. Anything invalid falls back to the default for that field. */
export function parseFilters(search: string): Filters {
  const q = new URLSearchParams(search)
  const range = q.get('range')
  const connection = q.get('connection')
  const interval = q.get('interval')
  const providers = list(q.get('provider')).filter((p): p is Provider => p === 'ookla' || p === 'ndt7')
  return {
    range: RANGES.some((r) => r.key === range) ? (range as RangeKey) : defaultFilters.range,
    devices: list(q.get('device')).slice(0, 20),
    connection: connection === 'wired' || connection === 'wireless' ? connection : 'all',
    providers: providers.length ? [...new Set(providers)].sort() : defaultFilters.providers,
    interval: INTERVALS.some((i) => i.key === interval) ? (interval as Filters['interval']) : 'auto',
  }
}

/** Serialize filters, omitting defaults so the common view has a clean URL. */
export function serializeFilters(f: Filters): string {
  const q = new URLSearchParams()
  if (f.range !== defaultFilters.range) q.set('range', f.range)
  if (f.devices.length) q.set('device', f.devices.join(','))
  if (f.connection !== 'all') q.set('connection', f.connection)
  if (f.providers.join() !== defaultFilters.providers.join()) q.set('provider', f.providers.join(','))
  if (f.interval !== 'auto') q.set('interval', f.interval)
  const s = q.toString()
  return s ? `?${s}` : ''
}

export interface Window {
  from?: string
  to?: string
  /** Length of the window in ms, when it is bounded. */
  ms?: number
}

/** The window for a range, ending now (left open so the newest result is always included). */
export function rangeWindow(range: RangeKey, now: number): Window {
  const r = RANGES.find((x) => x.key === range)
  if (!r || r.ms === 0) return {}
  return { from: new Date(now - r.ms).toISOString(), ms: r.ms }
}

/** The equal-length window immediately before the current one, for "vs previous period" deltas. */
export function previousWindow(range: RangeKey, now: number): Window | null {
  const r = RANGES.find((x) => x.key === range)
  if (!r || r.ms === 0) return null
  return { from: new Date(now - 2 * r.ms).toISOString(), to: new Date(now - r.ms).toISOString(), ms: r.ms }
}

/** Pick a bucket size that gives a readable number of points for the span being shown. */
export function resolveInterval(choice: Filters['interval'], spanMs: number | undefined): Interval {
  if (choice !== 'auto') return choice
  if (spanMs === undefined) return 'week'
  if (spanMs <= 3 * DAY) return 'hour'
  if (spanMs <= 120 * DAY) return 'day'
  if (spanMs <= 800 * DAY) return 'week'
  return 'month'
}

/** Approximate length of one bucket, used to break lines across gaps and to size hover tolerance. */
export function intervalMs(interval: Interval): number {
  switch (interval) {
    case 'raw': return 6 * HOUR
    case 'hour': return HOUR
    case 'day': return DAY
    case 'week': return 7 * DAY
    case 'month': return 31 * DAY
    case 'quarter': return 92 * DAY
    case 'year': return 366 * DAY
  }
}
