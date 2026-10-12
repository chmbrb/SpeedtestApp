<script lang="ts">
  import { scaleLinear } from 'd3-scale'
  import { line } from 'd3-shape'
  import type { Result } from '../shared/api/client'
  import { mbps } from '../shared/lib/format'

  let { results, accent }: { results: Result[]; accent: string } = $props()

  let width = $state(0)
  const H = 88
  const pad = { l: 4, r: 10, t: 10, b: 8 }

  const data = $derived([...results].reverse()) // oldest -> newest
  const shape = $derived.by(() => {
    if (!data.length || width === 0) return { path: '', last: null as null | [number, number], lo: 0, hi: 0 }
    const vals = data.map((d) => d.downloadMbps)
    const hi = Math.max(...vals)
    const lo = Math.min(...vals)
    const x = scaleLinear().domain([0, Math.max(1, data.length - 1)]).range([pad.l, width - pad.r])
    const y = scaleLinear().domain([0, hi || 1]).range([H - pad.b, pad.t])
    const path = line<number>().x((_, i) => x(i)).y((v) => y(v))(vals) ?? ''
    const lastIdx = vals.length - 1
    return { path, last: [x(lastIdx), y(vals[lastIdx] as number)] as [number, number], lo, hi }
  })
</script>

{#if data.length < 2}
  <div class="spark" bind:clientWidth={width}></div>
{:else}
  <figure class="spark" bind:clientWidth={width}>
    <figcaption>Last {data.length} tests</figcaption>
    <svg {width} height={H} role="img"
      aria-label={`Download speed over the last ${data.length} tests, from ${mbps(shape.lo)} to ${mbps(shape.hi)} megabits per second. The latest is ${mbps(data[data.length - 1]?.downloadMbps)}.`}>
      <path d={shape.path} fill="none" stroke="var(--muted)" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
      {#if shape.last}<circle class="end-dot" cx={shape.last[0]} cy={shape.last[1]} r="5" fill={accent} />{/if}
    </svg>
    <p class="spark-range">{mbps(shape.lo)}–{mbps(shape.hi)} Mbps</p>
  </figure>
{/if}
