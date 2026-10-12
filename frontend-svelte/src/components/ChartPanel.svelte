<script lang="ts">
  import type { Interval } from '../shared/api/client'
  import type { ChartSeries } from '../shared/lib/chartSeries'
  import { MAX_SERIES } from '../shared/lib/colors'
  import { bucketLabel } from '../shared/lib/format'
  import Icon from './Icon.svelte'
  import TimeChart from './TimeChart.svelte'

  let { title, subtitle, unit, series, hidden, interval, format, showBand, loading, error = null, onRetry, yMax = undefined, emptyHint }: {
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
  } = $props()

  let view = $state<'chart' | 'table'>('chart')
  const hasData = $derived(series.some((s) => s.points.length > 0))
  const band = $derived(showBand && series.length <= 2 && interval !== 'raw')

  const rows = $derived.by(() => {
    const times = [...new Set(series.flatMap((s) => s.points.map((p) => p.t)))].sort((a, b) => b - a)
    const lookup = series.map((s) => new Map(s.points.map((p) => [p.t, p.v])))
    return times.slice(0, 300).map((t) => ({ t, cells: lookup.map((m) => m.get(t) ?? null) }))
  })
</script>

<section class="panel{loading && hasData ? ' is-refreshing' : ''}" aria-busy={loading}>
  <header class="panel-head">
    <div>
      <h3>{title}</h3>
      <p class="panel-sub">{subtitle}</p>
    </div>
    {#if hasData}
      <div class="view-toggle" role="group" aria-label="{title} view">
        <button type="button" aria-pressed={view === 'chart'} onclick={() => (view = 'chart')}>Chart</button>
        <button type="button" aria-pressed={view === 'table'} onclick={() => (view = 'table')}>Table</button>
      </div>
    {/if}
  </header>

  {#if error && !hasData}
    <div class="state" role="alert">
      <p><strong>Couldn’t load {title.toLowerCase()}.</strong> {error}</p>
      {#if onRetry}<button type="button" class="btn" onclick={onRetry}>Try again</button>{/if}
    </div>
  {:else if !hasData}
    <div class="state">{#if loading}<p class="muted">Loading…</p>{:else}<p>{emptyHint}</p>{/if}</div>
  {:else}
    {#if series.length > 1}
      <ul class="legend" aria-label="Series">
        {#each series as s (s.id)}
          {@const last = [...s.points].reverse().find((p) => p.v != null)}
          <li>
            <span class="key" style:background={s.color}></span>
            <span class="legend-label">{s.label}</span>
            {#if last?.v != null}<span class="legend-value">{format(last.v)} {unit}</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}
    {#if view === 'chart'}
      <TimeChart {title} {unit} {series} {interval} {format} showBand={band} {yMax} />
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-wrap" tabindex="0" role="region" aria-label="{title} values">
        <table class="data">
          <caption class="sr-only">{title} ({unit}), newest first</caption>
          <thead>
            <tr>
              <th scope="col">Period</th>
              {#each series as s (s.id)}<th scope="col" class="num">{s.label}</th>{/each}
            </tr>
          </thead>
          <tbody>
            {#each rows as r (r.t)}
              <tr>
                <th scope="row">{bucketLabel(r.t, interval)}</th>
                {#each r.cells as c, i (i)}<td class="num">{c == null ? '—' : format(c)}</td>{/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
    {#if hidden > 0}
      <p class="note"><Icon name="info" /> Showing the first {MAX_SERIES} series; filter by device to see the other {hidden}.</p>
    {/if}
  {/if}
</section>
