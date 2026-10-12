<script lang="ts">
  import { scaleLinear } from 'd3-scale'
  import { line } from 'd3-shape'
  import { mbps } from '../shared/lib/format'
  import type { LiveState, Sample } from '../shared/state/liveTest'

  /** Speed against time while the test runs; one line per phase, with a hairline where upload begins. */
  let { samples, phase }: { samples: Sample[]; phase: LiveState['phase'] } = $props()

  const H = 150
  const M = { top: 10, right: 12, bottom: 22, left: 40 }
  let width = $state(0)
  const innerW = $derived(Math.max(10, width - M.left - M.right))
  const innerH = H - M.top - M.bottom

  const g = $derived.by(() => {
    const maxT = Math.max(...samples.map((p) => p.t), 1000)
    const minT = Math.min(maxT - 1000, samples[0]?.t ?? 0) // start at the first reading, not at the ping period before it
    const maxV = Math.max(10, ...samples.map((p) => p.mbps))
    const x = scaleLinear().domain([Math.max(0, minT), maxT]).range([0, innerW])
    const y = scaleLinear().domain([0, maxV * 1.1]).nice(3).range([innerH, 0])
    const ln = line<Sample>().x((p) => x(p.t)).y((p) => y(p.mbps))
    const down = samples.filter((p) => p.phase === 'download')
    const up = samples.filter((p) => p.phase === 'upload')
    const firstUp = up[0]
    return {
      x, y,
      paths: { download: ln(down) ?? '', upload: ln(up) ?? '' },
      boundary: firstUp ? x(firstUp.t) : null,
      ticks: y.ticks(3),
      last: samples[samples.length - 1],
      hasDown: down.length > 0,
    }
  })
</script>

<div bind:clientWidth={width} class="live-chart">
  {#if width > 0}
    <svg {width} height={H} role="img"
      aria-label={samples.length ? `Speed so far. Latest reading ${mbps(g.last?.mbps)} megabits per second.` : 'Waiting for the first reading'}>
      <g transform="translate({M.left},{M.top})">
        {#each g.ticks as t (t)}
          <g transform="translate(0,{g.y(t)})">
            <line class="grid" x1={0} x2={innerW} />
            <text class="tick" x={-8} dy="0.32em" text-anchor="end">{t}</text>
          </g>
        {/each}
        <line class="axis" x1={0} x2={innerW} y1={innerH} y2={innerH} />
        {#if g.boundary != null}<line class="crosshair" x1={g.boundary} x2={g.boundary} y1={0} y2={innerH} />{/if}
        {#if g.hasDown}<text class="tick" x={4} y={12}>Download</text>{/if}
        {#if g.boundary != null}<text class="tick" x={g.boundary + 6} y={12}>Upload</text>{/if}
        <path d={g.paths.download} fill="none" stroke="var(--ink)" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
        <path d={g.paths.upload} fill="none" stroke="var(--ink)" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
        {#if g.last && phase}<circle class="end-dot" cx={g.x(g.last.t)} cy={g.y(g.last.mbps)} r="5" fill="var(--series-1)" />{/if}
      </g>
    </svg>
  {/if}
</div>
