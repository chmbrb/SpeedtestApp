import { useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError } from './api/client'
import { useActiveTests, useCapabilities, useDevices, useNotifications, useRecent, useResultsPages, useSummary, useTimeseries } from './api/hooks'
import { AlertBanner, NotificationsSection } from './components/Alerts'
import { ChartPanel, toChartSeries } from './components/ChartPanel'
import { FilterBar } from './components/FilterBar'
import { Hero, KpiStrip } from './components/Hero'
import { ResultsTable } from './components/ResultsTable'
import { RunTest } from './components/RunTest'
import { InstallBanner } from './components/InstallBanner'
import { Header, OfflineBanner, useOnline } from './components/Shell'
import { buildColorSlots, colorVar, slotFor } from './lib/colors'
import { mbps, ms, pct, providerName } from './lib/format'
import { browserTimezone, useNow } from './lib/time'
import { RANGES, previousWindow, rangeWindow, resolveInterval } from './state/filters'
import { useFilters } from './state/useFilters'
import { useTheme } from './state/theme'

const message = (e: unknown) => (e == null ? null : e instanceof ApiError ? e.message : 'The server could not be reached.')

export function App() {
  const [filters, update, reset] = useFilters()
  const [theme, setTheme] = useTheme()
  const online = useOnline()
  const now = useNow()
  const tz = useMemo(browserTimezone, [])
  const queryClient = useQueryClient()

  const devices = useDevices()
  const notifications = useNotifications()
  const capabilities = useCapabilities()
  const activeTests = useActiveTests()
  const slots = useMemo(() => buildColorSlots(devices.data ?? []), [devices.data])

  const win = rangeWindow(filters.range, now)
  const prev = previousWindow(filters.range, now)
  const summary = useSummary(filters, win)
  const prevSummary = useSummary(filters, prev ?? {}, prev !== null)

  // "All time" has no fixed span, so size the buckets from the oldest result once the summary is in.
  const spanMs = win.ms ?? (summary.data?.first ? now - Date.parse(summary.data.first) : undefined)
  const interval = resolveInterval(filters.interval, spanMs)
  const series = useTimeseries(filters, win, interval, tz, spanMs !== undefined || summary.isError)

  const recent = useRecent(filters, 100)
  const pages = useResultsPages(filters, win)
  const rows = useMemo(() => pages.data?.pages.flatMap((p) => p.items) ?? [], [pages.data])

  const latest = recent.data?.[0]
  // The hero is about one device: judge it against that device's own typical speed, and sketch its own history.
  const sameEntity = latest ? { ...filters, devices: [latest.device], connection: latest.connectionType, providers: [latest.provider] } : filters
  const entitySummary = useSummary(sameEntity, win, !!latest)
  const entityRecent = useMemo(
    () => (recent.data ?? []).filter((r) => latest && r.device === latest.device && r.connectionType === latest.connectionType && r.provider === latest.provider).slice(0, 30),
    [recent.data, latest],
  )
  const accent = colorVar(latest ? slotFor({ provider: latest.provider, device: latest.device, connectionType: latest.connectionType }, slots) : null)
  const rangeLabel = RANGES.find((r) => r.key === filters.range)?.label.toLowerCase() ?? ''
  const previousLabel = prev ? `previous ${rangeLabel.replace('last ', '')}` : null

  const steps: Record<string, string> = { raw: 'Every test', hour: 'Hourly median', day: 'Daily median', week: 'Weekly median', month: 'Monthly median', quarter: 'Quarterly median', year: 'Yearly median' }
  const sub = (shade: string) => `${steps[interval]}${interval === 'raw' ? '' : `, ${shade}`}`
  const common = { interval, loading: series.isFetching, error: message(series.error), onRetry: () => series.refetch(), emptyHint: 'No tests in this period. Try a longer period or clear a filter.' }
  const dl = toChartSeries(series.data, 'downloadMbps', 'p50', slots)
  const ul = toChartSeries(series.data, 'uploadMbps', 'p50', slots)
  const lat = toChartSeries(series.data, 'latencyMs', 'p50', slots)
  const loss = toChartSeries(series.data, 'packetLossPct', 'avg', slots)
  const bothProviders = filters.providers.length > 1

  return (
    <>
      <Header theme={theme} onTheme={setTheme}
        actions={<RunTest capabilities={capabilities.data} active={activeTests.data ?? []} now={now} />} />
      {!online && <OfflineBanner />}
      <InstallBanner />
      <AlertBanner alerts={notifications.data?.active ?? []} />
      <FilterBar filters={filters} update={update} reset={reset} devices={devices.data ?? []} />

      <main>
        <Hero latest={latest} recent={entityRecent} typical={entitySummary.data?.downloadMbps.p50} now={now} accent={accent}
          loading={recent.isLoading} error={message(recent.error)} />
        <KpiStrip current={summary.data} previous={prevSummary.data} previousLabel={previousLabel} />

        <h2>Trends</h2>
        <div className="charts">
          <ChartPanel title="Download" subtitle={sub('shaded from slowest to fastest')} unit="Mbps" format={mbps} showBand {...common} {...dl} />
          <ChartPanel title="Upload" subtitle={sub('shaded from slowest to fastest')} unit="Mbps" format={mbps} showBand {...common} {...ul} />
          <ChartPanel title="Latency" subtitle={sub('shaded from lowest to highest')} unit="ms" format={ms} showBand {...common} {...lat} />
          <ChartPanel title="Packet loss" subtitle={`${interval === 'raw' ? 'Every test' : steps[interval]?.replace('median', 'average')}`} unit="%" format={pct} showBand={false} yMax={1} {...common} {...loss} />
        </div>
        {bothProviders && (
          <p className="note">Ookla and NDT7 measure differently (Ookla uses many connections, NDT7 one), so their speeds are not directly comparable.</p>
        )}

        <h2>Recent tests</h2>
        <ResultsTable results={rows} slots={slots} hasMore={!!pages.hasNextPage} loadingMore={pages.isFetchingNextPage}
          onMore={() => pages.fetchNextPage()} loading={pages.isLoading} error={message(pages.error)}
          onRetry={() => queryClient.invalidateQueries()} onReset={reset} />

        <NotificationsSection status={notifications.data} loading={notifications.isLoading} failed={notifications.isError} />
      </main>

      <footer className="foot">
        <p>
          {filters.providers.map(providerName).join(' and ')} results · times shown in {tz} · refreshes every minute
        </p>
      </footer>
    </>
  )
}
