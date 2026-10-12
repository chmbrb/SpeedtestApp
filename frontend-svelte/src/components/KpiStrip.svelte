<script lang="ts">
  import type { Summary } from '../shared/api/client'
  import { count, mbps, ms, pct } from '../shared/lib/format'
  import { delta, deltaPoints, type Delta } from '../shared/lib/stats'
  import Icon from './Icon.svelte'

  let { current, previous, previousLabel }: {
    current: Summary | undefined
    previous: Summary | undefined
    previousLabel: string | null
  } = $props()

  type Tile = { id: string; label: string; value: string; unit?: string; d: Delta | null }

  const tiles: Tile[] = $derived.by(() => {
    if (!current) return []
    const tests = delta(current.count, previous?.count, true)
    return [
      { id: 'download', label: 'Typical download', value: mbps(current.downloadMbps.p50), unit: 'Mbps', d: delta(current.downloadMbps.p50, previous?.downloadMbps.p50, true) },
      { id: 'upload', label: 'Typical upload', value: mbps(current.uploadMbps.p50), unit: 'Mbps', d: delta(current.uploadMbps.p50, previous?.uploadMbps.p50, true) },
      { id: 'latency', label: 'Typical latency', value: ms(current.latencyMs.p50), unit: 'ms', d: delta(current.latencyMs.p50, previous?.latencyMs.p50, false) },
      { id: 'loss', label: 'Average packet loss', value: pct(current.packetLossPct.avg), unit: '%', d: deltaPoints(current.packetLossPct.avg, previous?.packetLossPct.avg, false) },
      { id: 'tests', label: 'Tests', value: count(current.count), d: tests && { ...tests, good: null } },
    ]
  })

  function describe(d: Delta) {
    const tone = d.good === null ? 'neutral' : d.good ? 'better' : 'worse'
    const icon = d.direction === 'up' ? 'arrowUp' : d.direction === 'down' ? 'arrowDown' : 'minus'
    const size = d.unit === 'pts' ? `${Math.abs(d.pct as number).toFixed(2)} points` : `${Math.abs(d.pct as number)}%`
    const text = d.direction === 'flat' ? `Same as ${previousLabel}` : `${size} vs ${previousLabel}`
    const spoken = d.direction === 'flat' ? text : `${d.direction === 'up' ? 'Up' : 'Down'} ${text}${d.good === null ? '' : d.good ? ', better' : ', worse'}`
    return { tone, icon: icon as 'arrowUp' | 'arrowDown' | 'minus', text, spoken }
  }
</script>

<ul class="kpis" aria-label="Summary for the selected period">
  {#each tiles as t (t.id)}
    <li class="kpi" data-testid="kpi-{t.id}">
      <p class="kpi-label">{t.label}</p>
      <p class="kpi-value">{t.value}{#if t.unit}<span class="kpi-unit"> {t.unit}</span>{/if}</p>
      {#if !previousLabel}
        <p class="kpi-delta none">No earlier period to compare</p>
      {:else if !t.d || t.d.direction === 'none'}
        <p class="kpi-delta none">No earlier data to compare</p>
      {:else}
        {@const d = describe(t.d)}
        <p class="kpi-delta {d.tone}">
          <span aria-hidden="true" class="kpi-delta-visual"><Icon name={d.icon} size={14} /> {d.text}</span>
          <span class="sr-only">{d.spoken}</span>
        </p>
      {/if}
    </li>
  {/each}
</ul>
