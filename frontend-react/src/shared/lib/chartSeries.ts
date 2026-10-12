import type { Point, Timeseries } from '../api/client'
import { colorVar, keyId, MAX_SERIES, slotFor, type SeriesKey } from './colors'
import { providerName } from './format'

export interface ChartPoint {
  t: number
  v: number | null
  lo: number | null
  hi: number | null
  n: number
}

export interface ChartSeries {
  id: string
  label: string
  color: string
  points: ChartPoint[]
}

export type MetricName = 'downloadMbps' | 'uploadMbps' | 'latencyMs' | 'packetLossPct'

/** Turn API series into plottable series for one metric. Colors follow the entity, never the row number. */
export function toChartSeries(
  ts: Timeseries | undefined,
  metric: MetricName,
  centre: 'p50' | 'avg',
  slots: Map<string, number>,
): { series: ChartSeries[]; hidden: number } {
  if (!ts) return { series: [], hidden: 0 }
  const providers = new Set(ts.series.map((s) => s.key.provider))
  const devices = new Set(ts.series.map((s) => s.key.device))
  let hidden = 0
  const out: ChartSeries[] = []
  for (const s of ts.series) {
    const key: SeriesKey = s.key
    const slot = slotFor(key, slots)
    if (slot === null && slots.size > MAX_SERIES) { hidden++; continue }
    const parts: string[] = []
    if (devices.size > 1 || key.device) parts.push(key.device ?? '')
    if (key.connectionType) parts.push(key.connectionType === 'wired' ? 'wired' : 'wireless')
    if (providers.size > 1 && key.provider) parts.push(providerName(key.provider))
    out.push({
      id: keyId(key),
      label: parts.filter(Boolean).join(' · ') || 'All tests',
      color: colorVar(slot),
      points: s.points.map((p: Point) => ({
        t: Date.parse(p.bucket),
        v: p[metric][centre],
        lo: p[metric].min,
        hi: p[metric].max,
        n: p.count,
      })),
    })
  }
  return { series: out, hidden }
}

