import type { Result } from '../shared/api/client'
import { colorVar, slotFor } from '../shared/lib/colors'
import { dateTime, label, mbps, ms, pct, providerName } from '../shared/lib/format'

export function ResultsTable({ results, slots, hasMore, loadingMore, onMore, loading, error, onRetry, onReset }: {
  results: Result[]
  slots: Map<string, number>
  hasMore: boolean
  loadingMore: boolean
  onMore: () => void
  loading: boolean
  error?: string | null
  onRetry?: () => void
  onReset?: () => void
}) {
  if (error && results.length === 0) {
    return (
      <div className="state" role="alert">
        <p><strong>Couldn’t load tests.</strong> {error}</p>
        {onRetry && <button type="button" className="btn" onClick={onRetry}>Try again</button>}
      </div>
    )
  }
  if (results.length === 0) {
    return (
      <div className="state">
        {loading ? <p className="muted">Loading…</p> : (
          <>
            <p>No tests match these filters.</p>
            {onReset && <button type="button" className="btn" onClick={onReset}>Reset filters</button>}
          </>
        )}
      </div>
    )
  }
  return (
    <>
      <div className="table-wrap" tabIndex={0} role="region" aria-label="Recent tests, newest first">
        <table className="data results">
          <thead>
            <tr>
              <th scope="col">When</th>
              <th scope="col">Device</th>
              <th scope="col">Provider</th>
              <th scope="col" className="num">Download</th>
              <th scope="col" className="num">Upload</th>
              <th scope="col" className="num">Latency</th>
              <th scope="col" className="num">Loss</th>
              <th scope="col" className="wide">Server</th>
            </tr>
          </thead>
          <tbody>
            {results.map((r) => {
              const color = colorVar(slotFor({ provider: r.provider, device: r.device, connectionType: r.connectionType }, slots))
              return (
                <tr key={r.id}>
                  <th scope="row"><time dateTime={r.testedAt}>{dateTime(r.testedAt)}</time></th>
                  <td><span className="key" style={{ background: color }} />{r.device} <span className="muted">· {label(r.connectionType)}</span></td>
                  <td>{providerName(r.provider)}{r.trigger === 'on_demand' && <span className="muted"> · manual</span>}</td>
                  <td className="num">{mbps(r.downloadMbps)}<span className="u"> Mbps</span></td>
                  <td className="num">{mbps(r.uploadMbps)}<span className="u"> Mbps</span></td>
                  <td className="num">{ms(r.latencyMs)}<span className="u"> ms</span></td>
                  <td className="num">{r.packetLossPct == null ? '—' : <>{pct(r.packetLossPct)}<span className="u">%</span></>}</td>
                  <td className="wide muted">{r.server.name ?? '—'}{r.server.location ? `, ${r.server.location}` : ''}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      {hasMore && (
        <div className="more">
          <button type="button" className="btn" onClick={onMore} disabled={loadingMore}>
            {loadingMore ? 'Loading…' : 'Show more tests'}
          </button>
        </div>
      )}
    </>
  )
}
