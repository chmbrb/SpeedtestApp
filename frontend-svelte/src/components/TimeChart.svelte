<script lang="ts">
  import { bisector } from 'd3-array'
  import { scaleLinear, scaleTime } from 'd3-scale'
  import { area, line } from 'd3-shape'
  import type { Interval } from '../shared/api/client'
  import type { ChartPoint, ChartSeries } from '../shared/lib/chartSeries'
  import { bucketLabel } from '../shared/lib/format'
  import { HOUR } from '../shared/lib/time'
  import { intervalMs } from '../shared/state/filters'

  let { title, unit, series, interval, format, showBand, yMax = undefined }: {
    title: string
    unit: string
    series: ChartSeries[]
    interval: Interval
    format: (n: number) => string
    showBand: boolean
    yMax?: number
  } = $props()

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

  let width = $state(0)
  let active = $state<number | null>(null)
  let focusVisible = $state(false)

  const step = $derived(intervalMs(interval))
  const gap = $derived(interval === 'raw' ? 12 * HOUR : step * 1.6)
  const tol = $derived(interval === 'raw' ? 3 * HOUR : step / 2)
  const innerW = $derived(Math.max(10, width - M.left - M.right))
  const innerH = H - M.top - M.bottom

  const xs = $derived([...new Set(series.flatMap((s) => s.points.map((p) => p.t)))].sort((a, b) => a - b))

  const scales = $derived.by(() => {
    const t0 = xs[0] ?? 0
    const t1 = xs[xs.length - 1] ?? 1
    const pad = t0 === t1 ? step / 2 : 0
    const x = scaleTime().domain([new Date(t0 - pad), new Date(t1 + pad)]).range([0, innerW])
    // The scale follows what is drawn: the band's extremes only when the band is shown.
    let top = yMax ?? 0
    for (const s of series) for (const p of s.points) top = Math.max(top, (showBand ? p.hi : null) ?? p.v ?? 0)
    const y = scaleLinear().domain([0, top > 0 ? top * 1.06 : 1]).nice(4).range([innerH, 0])
    return { x, y, spanMs: t1 - t0, yTicks: y.ticks(4), yFmt: y.tickFormat(4, ',f') }
  })

  const paths = $derived.by(() => {
    const { x, y } = scales
    const ln = line<ChartPoint>().defined((p) => p.v != null).x((p) => x(new Date(p.t))).y((p) => y(p.v as number))
    const ar = area<ChartPoint>().defined((p) => p.lo != null && p.hi != null)
      .x((p) => x(new Date(p.t))).y0((p) => y(p.lo as number)).y1((p) => y(p.hi as number))
    return series.map((s) => {
      const g = withGaps(s.points, gap)
      return { id: s.id, line: ln(g) ?? '', band: ar(g) ?? '' }
    })
  })

  function setFromPointer(e: PointerEvent) {
    if (!xs.length) return
    const rect = (e.currentTarget as SVGRectElement).getBoundingClientRect()
    const t = scales.x.invert(e.clientX - rect.left).getTime()
    active = bisector((v: number) => v).center(xs, t)
  }

  function onKey(e: KeyboardEvent) {
    if (!xs.length) return
    const last = xs.length - 1
    const move = (i: number) => { e.preventDefault(); active = Math.min(last, Math.max(0, i)) }
    const cur = active ?? last
    if (e.key === 'ArrowLeft') move(cur - 1)
    else if (e.key === 'ArrowRight') move(cur + 1)
    else if (e.key === 'Home') move(0)
    else if (e.key === 'End') move(last)
    else if (e.key === 'Escape') active = null
  }

  const activeT = $derived(active != null ? xs[active] : undefined)
  const readout = $derived.by(() => {
    const t = activeT
    if (t == null) return []
    return series.flatMap((s) => {
      const p = nearest(s.points, t, tol)
      return p && p.v != null ? [{ s, p }] : []
    })
  })
  const cx = $derived(activeT != null ? scales.x(new Date(activeT)) : 0)
  const flip = $derived(cx > innerW * 0.58)
  const showPoints = $derived(series.every((s) => s.points.length <= 14)) // dense lines need no per-point dots
  const lastPoints = $derived(series.map((s) => ({ s, last: [...s.points].reverse().find((p) => p.v != null) })))
</script>

<div bind:clientWidth={width} class="chart" data-testid="chart-{title.toLowerCase().replace(/\W+/g, '-')}">
  {#if width > 0}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
    <div class="chart-frame" tabindex="0" role="group" aria-label="{title} chart. Use the left and right arrow keys to read values."
      onkeydown={onKey}
      onfocus={(e) => { focusVisible = e.currentTarget.matches(':focus-visible'); active = active ?? xs.length - 1 }}
      onblur={() => { focusVisible = false; active = null }}>
      <svg {width} height={H} role="presentation">
        <g transform="translate({M.left},{M.top})">
          {#each scales.yTicks as t (t)}
            <g transform="translate(0,{scales.y(t)})">
              <line class="grid" x1={0} x2={innerW} />
              <text class="tick" x={-10} dy="0.32em" text-anchor="end">{scales.yFmt(t)}</text>
            </g>
          {/each}
          <line class="axis" x1={0} x2={innerW} y1={innerH} y2={innerH} />
          {#each scales.x.ticks(Math.max(3, Math.floor(innerW / 95))) as t (+t)}
            <text class="tick" x={scales.x(t)} y={innerH + 18} text-anchor="middle">{xTickLabel(t, scales.spanMs)}</text>
          {/each}
          <text class="unit" x={-M.left + 4} y={-12}>{unit}</text>

          {#if showBand}
            {#each paths as p, i (`b-${p.id}`)}
              {#if p.band}<path d={p.band} fill={series[i]?.color} fill-opacity="0.1" />{/if}
            {/each}
          {/if}
          {#each paths as p, i (p.id)}
            <path d={p.line} fill="none" stroke={series[i]?.color} stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
          {/each}
          {#if showPoints}
            {#each series as s (s.id)}
              {#each s.points as p (p.t)}
                {#if p.v != null}<circle class="dot" cx={scales.x(new Date(p.t))} cy={scales.y(p.v)} r="4" fill={s.color} />{/if}
              {/each}
            {/each}
          {/if}
          {#each lastPoints as { s, last } (`end-${s.id}`)}
            {#if last && last.v != null}
              <circle class="end-dot" cx={scales.x(new Date(last.t))} cy={scales.y(last.v)} r="5" fill={s.color} />
            {/if}
          {/each}

          {#if activeT != null}
            <g pointer-events="none">
              <line class="crosshair" x1={cx} x2={cx} y1={0} y2={innerH} />
              {#each readout as { s, p } (s.id)}
                <circle class="end-dot" cx={cx} cy={scales.y(p.v as number)} r="5" fill={s.color} />
              {/each}
            </g>
          {/if}

          <!-- The hit area is the whole plot, far larger than any mark. -->
          <rect x={0} y={0} width={innerW} height={innerH} fill="transparent" role="presentation"
            onpointermove={setFromPointer} onpointerdown={setFromPointer}
            onpointerleave={() => { if (!focusVisible) active = null }} />
        </g>
      </svg>

      {#if activeT != null}
        <div class="tooltip{flip ? ' flip' : ''}" style="left: {M.left + cx}px; top: {M.top}px" data-testid="tooltip">
          <div class="tooltip-time">{bucketLabel(activeT, interval)}</div>
          {#if readout.length === 0}<div class="tooltip-none">No test in this period</div>{/if}
          {#each readout as { s, p } (s.id)}
            <div class="tooltip-row">
              <span class="key" style:background={s.color}></span>
              <span class="tooltip-value">{format(p.v as number)} <span class="tooltip-unit">{unit}</span></span>
              <span class="tooltip-label">{s.label}</span>
              {#if showBand && p.lo != null && p.hi != null && p.lo !== p.hi}
                <span class="tooltip-range">{format(p.lo)}–{format(p.hi)} range · {p.n} {p.n === 1 ? 'test' : 'tests'}</span>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
      <p class="sr-only" aria-live="polite">
        {activeT != null && focusVisible
          ? `${bucketLabel(activeT, interval)}: ${readout.map(({ s, p }) => `${s.label} ${format(p.v as number)} ${unit}`).join('; ') || 'no test'}`
          : ''}
      </p>
    </div>
  {/if}
</div>
