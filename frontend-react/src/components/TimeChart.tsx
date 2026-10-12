import { useCallback, useMemo, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { scaleLinear, scaleTime } from 'd3-scale'
import { area, line } from 'd3-shape'
import { bisector } from 'd3-array'
import type { Interval } from '../shared/api/client'
import { bucketLabel } from '../shared/lib/format'
import { intervalMs } from '../shared/state/filters'
import { HOUR } from '../shared/lib/time'
import { useWidth } from '../lib/useWidth'
import type { ChartPoint, ChartSeries } from '../shared/lib/chartSeries'

const H = 224
const M = { top: 24, right: 16, bottom: 28, left: 48 }

/** Insert a break wherever two neighbours are further apart than `gap`, so missing data is a gap, not a slope. */
function withGaps(points: ChartPoint[], gap: number): ChartPoint[] {
  const out: ChartPoint[] = []
  points.forEach((p, i) => {
    const prev = points[i - 1]
    if (prev && p.t - prev.t > gap) out.push({ t: prev.t + 1, v: null, lo: null, hi: null, n: 0 })
    out.push(p)
  })
  return out
}

const nearest = (points: ChartPoint[], t: number, tol: number): ChartPoint | undefined => {
  const i = bisector((p: ChartPoint) => p.t).center(points, t)
  const p = points[i]
  return p && Math.abs(p.t - t) <= tol ? p : undefined
}

function xTickLabel(t: Date, spanMs: number): string {
  if (spanMs <= 2 * 24 * HOUR) return t.toLocaleTimeString(undefined, { hour: 'numeric' })
  if (spanMs <= 120 * 24 * HOUR) return t.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  return t.toLocaleDateString(undefined, { month: 'short', year: '2-digit' })
}

export function TimeChart({ title, unit, series, interval, format, showBand, yMax }: {
  title: string
  unit: string
  series: ChartSeries[]
  interval: Interval
  format: (n: number) => string
  showBand: boolean
  yMax?: number
}) {
  const [box, width] = useWidth<HTMLDivElement>()
  const [active, setActive] = useState<number | null>(null)
  const [focusVisible, setFocusVisible] = useState(false)
  const step = intervalMs(interval)
  const gap = interval === 'raw' ? 12 * HOUR : step * 1.6
  const tol = interval === 'raw' ? 3 * HOUR : step / 2

  const xs = useMemo(() => [...new Set(series.flatMap((s) => s.points.map((p) => p.t)))].sort((a, b) => a - b), [series])

  const { x, y, spanMs, yTicks, yFmt } = useMemo(() => {
    const t0 = xs[0] ?? 0
    const t1 = xs[xs.length - 1] ?? 1
    const pad = t0 === t1 ? step / 2 : 0
    const innerW = Math.max(10, width - M.left - M.right)
    const x = scaleTime().domain([new Date(t0 - pad), new Date(t1 + pad)]).range([0, innerW])
    // The scale follows what is drawn: the band's extremes only when the band is shown.
    let top = yMax ?? 0
    for (const s of series) for (const p of s.points) top = Math.max(top, (showBand ? p.hi : null) ?? p.v ?? 0)
    const y = scaleLinear().domain([0, top > 0 ? top * 1.06 : 1]).nice(4).range([H - M.top - M.bottom, 0])
    return { x, y, spanMs: t1 - t0, yTicks: y.ticks(4), yFmt: y.tickFormat(4, ',f') }
  }, [xs, series, width, step, yMax, showBand])

  const paths = useMemo(() => {
    const ln = line<ChartPoint>().defined((p) => p.v != null).x((p) => x(new Date(p.t))).y((p) => y(p.v as number))
    const ar = area<ChartPoint>().defined((p) => p.lo != null && p.hi != null)
      .x((p) => x(new Date(p.t))).y0((p) => y(p.lo as number)).y1((p) => y(p.hi as number))
    return series.map((s) => {
      const g = withGaps(s.points, gap)
      return { id: s.id, line: ln(g) ?? '', band: ar(g) ?? '' }
    })
  }, [series, x, y, gap])

  const setFromPointer = useCallback((e: PointerEvent<SVGRectElement>) => {
    if (!xs.length) return
    const rect = e.currentTarget.getBoundingClientRect()
    const t = x.invert(e.clientX - rect.left).getTime()
    setActive(bisector((v: number) => v).center(xs, t))
  }, [xs, x])

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!xs.length) return
    const last = xs.length - 1
    const move = (i: number) => { e.preventDefault(); setActive(Math.min(last, Math.max(0, i))) }
    const cur = active ?? last
    if (e.key === 'ArrowLeft') move(cur - 1)
    else if (e.key === 'ArrowRight') move(cur + 1)
    else if (e.key === 'Home') move(0)
    else if (e.key === 'End') move(last)
    else if (e.key === 'Escape') setActive(null)
  }

  const activeT = active != null ? xs[active] : undefined
  const readout = useMemo(() => {
    if (activeT == null) return []
    return series.flatMap((s) => {
      const p = nearest(s.points, activeT, tol)
      return p && p.v != null ? [{ s, p }] : []
    })
  }, [activeT, series, tol])

  const innerW = Math.max(10, width - M.left - M.right)
  const cx = activeT != null ? x(new Date(activeT)) : 0
  const flip = cx > innerW * 0.58
  const showPoints = series.every((s) => s.points.length <= 14) // dense lines need no per-point dots

  return (
    <div ref={box} className="chart" data-testid={`chart-${title.toLowerCase().replace(/\W+/g, '-')}`}>
      {width > 0 && (
        <div
          className="chart-frame"
          tabIndex={0}
          role="group"
          aria-label={`${title} chart. Use the left and right arrow keys to read values.`}
          onKeyDown={onKey}
          onFocus={(e) => { setFocusVisible(e.currentTarget.matches(':focus-visible')); setActive((a) => a ?? xs.length - 1) }}
          onBlur={() => { setFocusVisible(false); setActive(null) }}
        >
          <svg width={width} height={H} role="presentation">
            <g transform={`translate(${M.left},${M.top})`}>
              {yTicks.map((t) => (
                <g key={t} transform={`translate(0,${y(t)})`}>
                  <line className="grid" x1={0} x2={innerW} />
                  <text className="tick" x={-10} dy="0.32em" textAnchor="end">{yFmt(t)}</text>
                </g>
              ))}
              <line className="axis" x1={0} x2={innerW} y1={H - M.top - M.bottom} y2={H - M.top - M.bottom} />
              {x.ticks(Math.max(3, Math.floor(innerW / 95))).map((t) => (
                <text key={+t} className="tick" x={x(t)} y={H - M.top - M.bottom + 18} textAnchor="middle">
                  {xTickLabel(t, spanMs)}
                </text>
              ))}
              <text className="unit" x={-M.left + 4} y={-12}>{unit}</text>

              {showBand && paths.map((p, i) => p.band && (
                <path key={`b-${p.id}`} d={p.band} fill={series[i]?.color} fillOpacity={0.1} />
              ))}
              {paths.map((p, i) => (
                <path key={p.id} d={p.line} fill="none" stroke={series[i]?.color} strokeWidth={2}
                  strokeLinejoin="round" strokeLinecap="round" />
              ))}
              {showPoints && series.map((s) => s.points.map((p) => p.v != null && (
                <circle key={`${s.id}-${p.t}`} className="dot" cx={x(new Date(p.t))} cy={y(p.v)} r={4} fill={s.color} />
              )))}
              {series.map((s) => {
                const last = [...s.points].reverse().find((p) => p.v != null)
                return last && last.v != null ? (
                  <circle key={`end-${s.id}`} className="end-dot" cx={x(new Date(last.t))} cy={y(last.v)} r={5} fill={s.color} />
                ) : null
              })}

              {activeT != null && (
                <g pointerEvents="none">
                  <line className="crosshair" x1={cx} x2={cx} y1={0} y2={H - M.top - M.bottom} />
                  {readout.map(({ s, p }) => (
                    <circle key={s.id} className="end-dot" cx={cx} cy={y(p.v as number)} r={5} fill={s.color} />
                  ))}
                </g>
              )}

              {/* The hit area is the whole plot, far larger than any mark. */}
              <rect x={0} y={0} width={innerW} height={H - M.top - M.bottom} fill="transparent"
                onPointerMove={setFromPointer} onPointerDown={setFromPointer}
                onPointerLeave={() => !focusVisible && setActive(null)} />
            </g>
          </svg>

          {activeT != null && (
            <div className={`tooltip${flip ? ' flip' : ''}`} style={{ left: M.left + cx, top: M.top }} data-testid="tooltip">
              <div className="tooltip-time">{bucketLabel(activeT, interval)}</div>
              {readout.length === 0 && <div className="tooltip-none">No test in this period</div>}
              {readout.map(({ s, p }) => (
                <div key={s.id} className="tooltip-row">
                  <span className="key" style={{ background: s.color }} />
                  <span className="tooltip-value">{format(p.v as number)} <span className="tooltip-unit">{unit}</span></span>
                  <span className="tooltip-label">{s.label}</span>
                  {showBand && p.lo != null && p.hi != null && p.lo !== p.hi && (
                    <span className="tooltip-range">{format(p.lo)}–{format(p.hi)} range · {p.n} {p.n === 1 ? 'test' : 'tests'}</span>
                  )}
                </div>
              ))}
            </div>
          )}
          <p className="sr-only" aria-live="polite">
            {activeT != null && focusVisible
              ? `${bucketLabel(activeT, interval)}: ${readout.map(({ s, p }) => `${s.label} ${format(p.v as number)} ${unit}`).join('; ') || 'no test'}`
              : ''}
          </p>
        </div>
      )}
    </div>
  )
}
