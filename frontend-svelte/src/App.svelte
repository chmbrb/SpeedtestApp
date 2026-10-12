<script lang="ts">
  import { ApiError } from './shared/api/client'
  import Alerts from './components/Alerts.svelte'
  import ChartPanel from './components/ChartPanel.svelte'
  import FilterBar from './components/FilterBar.svelte'
  import Header from './components/Header.svelte'
  import Hero from './components/Hero.svelte'
  import InstallBanner from './components/InstallBanner.svelte'
  import KpiStrip from './components/KpiStrip.svelte'
  import Notifications from './components/Notifications.svelte'
  import OfflineBanner from './components/OfflineBanner.svelte'
  import ResultsTable from './components/ResultsTable.svelte'
  import RunTest from './components/RunTest.svelte'
  import { clock } from './lib/clock.svelte'
  import * as data from './lib/data.svelte'
  import { filters } from './lib/filters.svelte'
  import { invalidate } from './lib/query.svelte'
  import { toChartSeries } from './shared/lib/chartSeries'
  import { buildColorSlots, colorVar, slotFor } from './shared/lib/colors'
  import { mbps, ms, pct, providerName } from './shared/lib/format'
  import { browserTimezone } from './shared/lib/time'
  import { RANGES, previousWindow, rangeWindow, resolveInterval } from './shared/state/filters'

  const message = (e: unknown) => (e == null ? null : e instanceof ApiError ? e.message : 'The server could not be reached.')

  let online = $state(navigator.onLine)
  $effect(() => {
    const on = () => (online = true)
    const off = () => (online = false)
    window.addEventListener('online', on)
    window.addEventListener('offline', off)
    return () => { window.removeEventListener('online', on); window.removeEventListener('offline', off) }
  })

  const tz = browserTimezone()
  const f = $derived(filters.value)
  const now = $derived(clock.now)

  const devices = data.devices()
  const notifications = data.notifications()
  const capabilities = data.capabilities()
  const activeTests = data.activeTests()
  const slots = $derived(buildColorSlots(devices.data ?? []))

  const win = $derived(rangeWindow(f.range, now))
  const prev = $derived(previousWindow(f.range, now))
  const summary = data.summary(() => f, () => win)
  const prevSummary = data.summary(() => f, () => prev ?? {}, () => prev !== null)

  // "All time" has no fixed span, so size the buckets from the oldest result once the summary is in.
  const spanMs = $derived(win.ms ?? (summary.data?.first ? now - Date.parse(summary.data.first) : undefined))
  const interval = $derived(resolveInterval(f.interval, spanMs))
  const series = data.timeseries(() => f, () => win, () => interval, tz, () => spanMs !== undefined || !!summary.error)

  const recent = data.recent(() => f, 100)
  const pages = data.resultsPages(() => f, () => win)
  const rows = $derived(pages.pages.flatMap((p) => p.items))

  const latest = $derived(recent.data?.[0])
  // The hero is about one device: judge it against that device's own typical speed, and sketch its own history.
  const sameEntity = $derived(latest ? { ...f, devices: [latest.device], connection: latest.connectionType, providers: [latest.provider] } : f)
  const entitySummary = data.summary(() => sameEntity, () => win, () => !!latest)
  const entityRecent = $derived(
    (recent.data ?? []).filter((r) => latest && r.device === latest.device && r.connectionType === latest.connectionType && r.provider === latest.provider).slice(0, 30),
  )
  const accent = $derived(colorVar(latest ? slotFor({ provider: latest.provider, device: latest.device, connectionType: latest.connectionType }, slots) : null))
  const rangeLabel = $derived(RANGES.find((r) => r.key === f.range)?.label.toLowerCase() ?? '')
  const previousLabel = $derived(prev ? `previous ${rangeLabel.replace('last ', '')}` : null)

  const steps: Record<string, string> = { raw: 'Every test', hour: 'Hourly median', day: 'Daily median', week: 'Weekly median', month: 'Monthly median', quarter: 'Quarterly median', year: 'Yearly median' }
  const sub = (shade: string) => `${steps[interval]}${interval === 'raw' ? '' : `, ${shade}`}`
  const dl = $derived(toChartSeries(series.data, 'downloadMbps', 'p50', slots))
  const ul = $derived(toChartSeries(series.data, 'uploadMbps', 'p50', slots))
  const lat = $derived(toChartSeries(series.data, 'latencyMs', 'p50', slots))
  const loss = $derived(toChartSeries(series.data, 'packetLossPct', 'avg', slots))
  const bothProviders = $derived(f.providers.length > 1)
  const common = $derived({
    interval, loading: series.fetching, error: message(series.error), onRetry: () => void series.refetch(),
    emptyHint: 'No tests in this period. Try a longer period or clear a filter.',
  })
</script>

<Header>
  {#snippet actions()}
    <RunTest capabilities={capabilities.data} active={activeTests.data ?? []} {now} />
  {/snippet}
</Header>
{#if !online}<OfflineBanner />{/if}
<InstallBanner />
<Alerts alerts={notifications.data?.active ?? []} />
<FilterBar filters={f} update={filters.update} reset={filters.reset} devices={devices.data ?? []} />

<main>
  <Hero {latest} recent={entityRecent} typical={entitySummary.data?.downloadMbps.p50} {now} {accent}
    loading={recent.loading} error={message(recent.error)} />
  <KpiStrip current={summary.data} previous={prevSummary.data} {previousLabel} />

  <h2>Trends</h2>
  <div class="charts">
    <ChartPanel title="Download" subtitle={sub('shaded from slowest to fastest')} unit="Mbps" format={mbps} showBand {...common} {...dl} />
    <ChartPanel title="Upload" subtitle={sub('shaded from slowest to fastest')} unit="Mbps" format={mbps} showBand {...common} {...ul} />
    <ChartPanel title="Latency" subtitle={sub('shaded from lowest to highest')} unit="ms" format={ms} showBand {...common} {...lat} />
    <ChartPanel title="Packet loss" subtitle={`${interval === 'raw' ? 'Every test' : steps[interval]?.replace('median', 'average')}`} unit="%" format={pct} showBand={false} yMax={1} {...common} {...loss} />
  </div>
  {#if bothProviders}
    <p class="note">Ookla and NDT7 measure differently (Ookla uses many connections, NDT7 one), so their speeds are not directly comparable.</p>
  {/if}

  <h2>Recent tests</h2>
  <ResultsTable results={rows} {slots} hasMore={pages.hasNext} loadingMore={pages.fetchingNext} onMore={() => void pages.fetchNext()}
    loading={pages.loading} error={message(pages.error)} onRetry={() => invalidate('results', 'summary', 'timeseries', 'recent', 'devices', 'notifications')}
    onReset={filters.reset} />

  <Notifications status={notifications.data} loading={notifications.loading} failed={!!notifications.error} />
</main>

<footer class="foot">
  <p>
    {f.providers.map(providerName).join(' and ')} results · times shown in {tz} · refreshes every minute
  </p>
</footer>
