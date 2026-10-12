import { useMemo, useState } from 'react'
import type { Interval, Point, Timeseries } from '../api/client'
import { TimeChart, type ChartSeries } from './TimeChart'
import { bucketLabel } from '../lib/format'
import { colorVar, keyId, MAX_SERIES, slotFor, type SeriesKey } from '../lib/colors'
import { providerName } from '../lib/format'
import { InfoIcon } from './ui'

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

export function ChartPanel({ title, subtitle, unit, series, hidden, interval, format, showBand, loading, error, onRetry, yMax, emptyHint }: {
  title: string
  subtitle: string
  unit: string
  series: ChartSeries[]
  hidden: number
  interval: Interval
  format: (n: number) => string
  showBand: boolean
  loading: boolean
  error?: string | null
  onRetry?: () => void
  yMax?: number
  emptyHint: string
}) {
  const [view, setView] = useState<'chart' | 'table'>('chart')
  const hasData = series.some((s) => s.points.length > 0)
  const band = showBand && series.length <= 2 && interval !== 'raw'

  return (
    <section className={`panel${loading && hasData ? ' is-refreshing' : ''}`} aria-busy={loading}>
      <header className="panel-head">
        <div>
          <h3>{title}</h3>
          <p className="panel-sub">{subtitle}</p>
        </div>
        {hasData && (
          <div className="view-toggle" role="group" aria-label={`${title} view`}>
            <button type="button" aria-pressed={view === 'chart'} onClick={() => setView('chart')}>Chart</button>
            <button type="button" aria-pressed={view === 'table'} onClick={() => setView('table')}>Table</button>
          </div>
        )}
      </header>

      {error && !hasData ? (
        <div className="state" role="alert">
          <p><strong>Couldn’t load {title.toLowerCase()}.</strong> {error}</p>
          {onRetry && <button type="button" className="btn" onClick={onRetry}>Try again</button>}
        </div>
      ) : !hasData ? (
        <div className="state">{loading ? <p className="muted">Loading…</p> : <p>{emptyHint}</p>}</div>
      ) : (
        <>
          {series.length > 1 && (
            <ul className="legend" aria-label="Series">
              {series.map((s) => {
                const last = [...s.points].reverse().find((p) => p.v != null)
                return (
                  <li key={s.id}>
                    <span className="key" style={{ background: s.color }} />
                    <span className="legend-label">{s.label}</span>
                    {last?.v != null && <span className="legend-value">{format(last.v)} {unit}</span>}
                  </li>
                )
              })}
            </ul>
          )}
          {view === 'chart' ? (
            <TimeChart title={title} unit={unit} series={series} interval={interval} format={format} showBand={band} yMax={yMax} />
          ) : (
            <DataTable title={title} unit={unit} series={series} interval={interval} format={format} />
          )}
          {hidden > 0 && (
            <p className="note"><InfoIcon /> Showing the first {MAX_SERIES} series; filter by device to see the other {hidden}.</p>
          )}
        </>
      )}
    </section>
  )
}

function DataTable({ title, unit, series, interval, format }: {
  title: string; unit: string; series: ChartSeries[]; interval: Interval; format: (n: number) => string
}) {
  const rows = useMemo(() => {
    const times = [...new Set(series.flatMap((s) => s.points.map((p) => p.t)))].sort((a, b) => b - a)
    const lookup = series.map((s) => new Map(s.points.map((p) => [p.t, p.v])))
    return times.slice(0, 300).map((t) => ({ t, cells: lookup.map((m) => m.get(t) ?? null) }))
  }, [series])
  return (
    <div className="table-wrap" tabIndex={0} role="region" aria-label={`${title} values`}>
      <table className="data">
        <caption className="sr-only">{title} ({unit}), newest first</caption>
        <thead>
          <tr>
            <th scope="col">Period</th>
            {series.map((s) => <th scope="col" key={s.id} className="num">{s.label}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.t}>
              <th scope="row">{bucketLabel(r.t, interval)}</th>
              {r.cells.map((c, i) => <td key={i} className="num">{c == null ? '—' : format(c)}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
