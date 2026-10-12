import { useMemo } from 'react'
import { scaleLinear } from 'd3-scale'
import { line } from 'd3-shape'
import type { Result, Summary } from '../api/client'
import { ago, count, label, mbps, ms, pct, providerName } from '../lib/format'
import { judgeSpeed, delta, deltaPoints, type Delta, type Tone } from '../lib/stats'
import { useWidth } from '../lib/useWidth'
import { AlertIcon, ArrowDownIcon, ArrowUpIcon, InfoIcon, MinusIcon, OkIcon } from './ui'

const TONE_ICON: Record<Tone, typeof OkIcon> = { good: OkIcon, warning: AlertIcon, critical: AlertIcon, neutral: InfoIcon }

export function Hero({ latest, recent, typical, now, accent, loading, error }: {
  latest: Result | undefined
  recent: Result[]
  typical: number | null | undefined
  now: number
  accent: string
  loading: boolean
  error?: string | null
}) {
  if (!latest) {
    return (
      <section className="hero" aria-labelledby="hero-label">
        <div className="hero-main">
          <p id="hero-label" className="hero-label">Latest download</p>
          {error ? <p role="alert"><strong>Couldn’t load results.</strong> {error}</p>
            : loading ? <p className="muted">Loading…</p>
            : <p className="hero-empty">No tests yet for these filters.</p>}
        </div>
      </section>
    )
  }
  const verdict = judgeSpeed(latest.downloadMbps, typical, mbps)
  const Icon = TONE_ICON[verdict.tone]
  return (
    <section className="hero" aria-labelledby="hero-label">
      <div className="hero-main">
        <p id="hero-label" className="hero-label">
          Latest download <span className="hero-when">· {ago(Date.parse(latest.testedAt), now)}</span>
        </p>
        <p className="hero-figure" data-testid="hero-figure">
          <span className="hero-number">{mbps(latest.downloadMbps)}</span>
          <span className="hero-unit">Mbps</span>
        </p>
        <p className={`verdict tone-${verdict.tone}`} data-testid="verdict">
          <Icon className="verdict-icon" />
          <span><strong>{verdict.title}.</strong> {verdict.detail}</span>
        </p>
        <p className="hero-meta">
          {latest.device}, {label(latest.connectionType)} · {providerName(latest.provider)}
          {latest.server.name ? ` · ${latest.server.name}${latest.server.location ? `, ${latest.server.location}` : ''}` : ''}
        </p>
      </div>
      <Sparkline results={recent} accent={accent} />
    </section>
  )
}

function Sparkline({ results, accent }: { results: Result[]; accent: string }) {
  const [box, width] = useWidth<HTMLDivElement>()
  const data = useMemo(() => [...results].reverse(), [results]) // oldest -> newest
  const H = 88
  const pad = { l: 4, r: 10, t: 10, b: 8 }
  const { path, last, lo, hi } = useMemo(() => {
    if (!data.length || width === 0) return { path: '', last: null as null | [number, number], lo: 0, hi: 0 }
    const vals = data.map((d) => d.downloadMbps)
    const hi = Math.max(...vals)
    const lo = Math.min(...vals)
    const x = scaleLinear().domain([0, Math.max(1, data.length - 1)]).range([pad.l, width - pad.r])
    const y = scaleLinear().domain([0, hi || 1]).range([H - pad.b, pad.t])
    const path = line<number>().x((_, i) => x(i)).y((v) => y(v))(vals) ?? ''
    const lastIdx = vals.length - 1
    return { path, last: [x(lastIdx), y(vals[lastIdx] as number)] as [number, number], lo, hi }
  }, [data, width])

  if (data.length < 2) return <div className="spark" ref={box} />
  return (
    <figure className="spark" ref={box}>
      <figcaption>Last {data.length} tests</figcaption>
      <svg width={width} height={H} role="img"
        aria-label={`Download speed over the last ${data.length} tests, from ${mbps(lo)} to ${mbps(hi)} megabits per second. The latest is ${mbps(data[data.length - 1]?.downloadMbps)}.`}>
        <path d={path} fill="none" stroke="var(--muted)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
        {last && <circle className="end-dot" cx={last[0]} cy={last[1]} r={5} fill={accent} />}
      </svg>
      <p className="spark-range">{mbps(lo)}–{mbps(hi)} Mbps</p>
    </figure>
  )
}

type Tile = {
  id: string
  label: string
  value: string
  unit?: string
  d: Delta | null
}

export function KpiStrip({ current, previous, previousLabel }: {
  current: Summary | undefined
  previous: Summary | undefined
  previousLabel: string | null
}) {
  const tiles: Tile[] = current ? [
    { id: 'download', label: 'Typical download', value: mbps(current.downloadMbps.p50), unit: 'Mbps', d: delta(current.downloadMbps.p50, previous?.downloadMbps.p50, true) },
    { id: 'upload', label: 'Typical upload', value: mbps(current.uploadMbps.p50), unit: 'Mbps', d: delta(current.uploadMbps.p50, previous?.uploadMbps.p50, true) },
    { id: 'latency', label: 'Typical latency', value: ms(current.latencyMs.p50), unit: 'ms', d: delta(current.latencyMs.p50, previous?.latencyMs.p50, false) },
    { id: 'loss', label: 'Average packet loss', value: pct(current.packetLossPct.avg), unit: '%', d: deltaPoints(current.packetLossPct.avg, previous?.packetLossPct.avg, false) },
    { id: 'tests', label: 'Tests', value: count(current.count), d: delta(current.count, previous?.count, true) && { ...delta(current.count, previous?.count, true), good: null } },
  ] : []
  return (
    <ul className="kpis" aria-label="Summary for the selected period">
      {tiles.map((t) => (
        <li key={t.id} className="kpi" data-testid={`kpi-${t.id}`}>
          <p className="kpi-label">{t.label}</p>
          <p className="kpi-value">{t.value}{t.unit && <span className="kpi-unit"> {t.unit}</span>}</p>
          <DeltaLine d={t.d} previousLabel={previousLabel} />
        </li>
      ))}
    </ul>
  )
}

function DeltaLine({ d, previousLabel }: { d: Delta | null; previousLabel: string | null }) {
  if (!previousLabel) return <p className="kpi-delta none">No earlier period to compare</p>
  if (!d || d.direction === 'none') return <p className="kpi-delta none">No earlier data to compare</p>
  const tone = d.good === null ? 'neutral' : d.good ? 'better' : 'worse'
  const Icon = d.direction === 'up' ? ArrowUpIcon : d.direction === 'down' ? ArrowDownIcon : MinusIcon
  const size = d.unit === 'pts' ? `${Math.abs(d.pct as number).toFixed(2)} points` : `${Math.abs(d.pct as number)}%`
  const text = d.direction === 'flat' ? `Same as ${previousLabel}` : `${size} vs ${previousLabel}`
  const spoken = d.direction === 'flat' ? text
    : `${d.direction === 'up' ? 'Up' : 'Down'} ${text}${d.good === null ? '' : d.good ? ', better' : ', worse'}`
  return (
    <p className={`kpi-delta ${tone}`}>
      <span aria-hidden="true" className="kpi-delta-visual"><Icon size={14} /> {text}</span>
      <span className="sr-only">{spoken}</span>
    </p>
  )
}
