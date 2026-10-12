import { useMemo } from 'react'
import { scaleLinear } from 'd3-scale'
import { line } from 'd3-shape'
import type { Result } from '../api/client'
import { label as connectionLabel, mbps, ms, pct, providerName } from '../lib/format'
import { judgeSpeed } from '../lib/stats'
import { useWidth } from '../lib/useWidth'
import type { LiveState, Sample } from '../state/liveTest'
import { AlertIcon, CheckIcon, InfoIcon, OkIcon } from './ui'

const STEPS = [
  { key: 'ping', label: 'Latency' },
  { key: 'download', label: 'Download' },
  { key: 'upload', label: 'Upload' },
] as const

// Where each phase sits in the three steps. A browser test spends its first moments finding a server (step 0).
const STEP_OF = { locating: 0, ping: 0, download: 1, upload: 2 } as const

const PHASE_TEXT = {
  locating: 'Finding the nearest M-Lab server…',
  ping: 'Measuring latency…',
  download: 'Testing download speed…',
  upload: 'Testing upload speed…',
} as const

function Steps({ s }: { s: LiveState }) {
  const active = s.phase ? STEP_OF[s.phase] : -1
  const done = s.status === 'completed'
  return (
    <ol className="steps" aria-label="Progress">
      {STEPS.map((step, i) => {
        const state = done || i < active ? 'done' : i === active ? 'active' : 'todo'
        return (
          <li key={step.key} className={`step ${state}`} aria-current={state === 'active' ? 'step' : undefined}>
            <span className="step-mark">{state === 'done' ? <CheckIcon size={14} /> : null}</span>
            {step.label}
          </li>
        )
      })}
    </ol>
  )
}

const H = 150
const M = { top: 10, right: 12, bottom: 22, left: 40 }

/** Speed against time while the test runs; one line per phase, with a hairline where upload begins. */
function LiveChart({ samples, phase }: { samples: Sample[]; phase: LiveState['phase'] }) {
  const [box, width] = useWidth<HTMLDivElement>()
  const innerW = Math.max(10, width - M.left - M.right)
  const innerH = H - M.top - M.bottom
  const { x, y, paths, boundary, ticks, last } = useMemo(() => {
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
    }
  }, [samples, innerW, innerH])

  return (
    <div ref={box} className="live-chart">
      {width > 0 && (
        <svg width={width} height={H} role="img"
          aria-label={samples.length ? `Speed so far. Latest reading ${mbps(last?.mbps)} megabits per second.` : 'Waiting for the first reading'}>
          <g transform={`translate(${M.left},${M.top})`}>
            {ticks.map((t) => (
              <g key={t} transform={`translate(0,${y(t)})`}>
                <line className="grid" x1={0} x2={innerW} />
                <text className="tick" x={-8} dy="0.32em" textAnchor="end">{t}</text>
              </g>
            ))}
            <line className="axis" x1={0} x2={innerW} y1={innerH} y2={innerH} />
            {boundary != null && <line className="crosshair" x1={boundary} x2={boundary} y1={0} y2={innerH} />}
            {samples.some((p) => p.phase === 'download') && (
              <text className="tick" x={4} y={12}>Download</text>
            )}
            {boundary != null && <text className="tick" x={boundary + 6} y={12}>Upload</text>}
            <path d={paths.download} fill="none" stroke="var(--ink)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
            <path d={paths.upload} fill="none" stroke="var(--ink)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
            {last && phase && <circle className="end-dot" cx={x(last.t)} cy={y(last.mbps)} r={5} fill="var(--series-1)" />}
          </g>
        </svg>
      )}
    </div>
  )
}

export function LiveTest({ s, typical, onCancel, onAgain, onClose }: {
  s: LiveState
  /** The device's usual download speed, to judge the result against (null when there is no history). */
  typical: number | null | undefined
  onCancel: () => void
  onAgain: () => void
  onClose: () => void
}) {
  if (s.status === 'failed') {
    return (
      <div className="live" data-testid="live-failed">
        <div className="callout err" role="alert">
          <AlertIcon className="callout-icon" />
          <p><strong>The test didn’t finish.</strong> {s.error}</p>
        </div>
        <div className="dialog-actions">
          <button type="button" className="btn primary" onClick={onAgain}>Try again</button>
          <button type="button" className="btn" onClick={onClose}>Close</button>
        </div>
      </div>
    )
  }
  if (s.status === 'cancelled') {
    return (
      <div className="live" data-testid="live-cancelled">
        <p><InfoIcon /> The test was stopped. Nothing was saved.</p>
        <div className="dialog-actions">
          <button type="button" className="btn primary" onClick={onAgain}>Start another</button>
          <button type="button" className="btn" onClick={onClose}>Close</button>
        </div>
      </div>
    )
  }
  if (s.status === 'completed' && s.result) {
    return <Finished r={s.result} typical={typical} onAgain={onAgain} onClose={onClose} />
  }

  const where = s.kind === 'browser' ? 'this device' : 'the server'
  const phaseText = s.saving ? 'Saving the result…' : s.stopping ? (s.kind === 'browser' ? 'Stopping after this step…' : 'Stopping…')
    : s.phase ? PHASE_TEXT[s.phase] : `Starting the test on ${where}…`
  const showSpeed = s.phase === 'download' || s.phase === 'upload'
  return (
    <div className="live" data-testid="live-running">
      <Steps s={s} />
      <p className="live-figure" aria-hidden={!showSpeed}>
        <span className="live-number">{showSpeed && s.current != null ? mbps(s.current) : '—'}</span>
        <span className="live-unit">Mbps</span>
        {s.latencyMs != null && <span className="live-latency">{ms(s.latencyMs)} ms latency</span>}
      </p>
      <LiveChart samples={s.samples} phase={s.phase} />
      <p className="live-status" role="status" aria-live="polite">{phaseText}</p>
      <div className="dialog-actions">
        <button type="button" className="btn" onClick={onCancel} disabled={s.stopping || s.saving}>
          {s.stopping ? 'Stopping…' : 'Stop test'}
        </button>
      </div>
    </div>
  )
}

function Finished({ r, typical, onAgain, onClose }: { r: Result; typical: number | null | undefined; onAgain: () => void; onClose: () => void }) {
  const verdict = judgeSpeed(r.downloadMbps, typical, mbps)
  const Icon = verdict.tone === 'good' ? OkIcon : verdict.tone === 'neutral' ? InfoIcon : AlertIcon
  return (
    <div className="live" data-testid="live-done">
      <p className="live-status" role="status">Test complete. {providerName(r.provider)} · {r.device}, {connectionLabel(r.connectionType)}</p>
      <ul className="done-tiles">
        <li><span className="kpi-label">Download</span><span className="live-number small" data-testid="done-download">{mbps(r.downloadMbps)}<span className="kpi-unit"> Mbps</span></span></li>
        <li><span className="kpi-label">Upload</span><span className="live-number small" data-testid="done-upload">{mbps(r.uploadMbps)}<span className="kpi-unit"> Mbps</span></span></li>
        <li><span className="kpi-label">Latency</span><span className="live-number small">{ms(r.latencyMs)}<span className="kpi-unit"> ms</span></span></li>
        {r.packetLossPct != null && (
          <li><span className="kpi-label">{r.provider === 'ndt7' ? 'Retransmitted' : 'Packet loss'}</span><span className="live-number small">{pct(r.packetLossPct)}<span className="kpi-unit"> %</span></span></li>
        )}
      </ul>
      <p className={`verdict tone-${verdict.tone}`} data-testid="done-verdict">
        <Icon className="verdict-icon" />
        <span><strong>{verdict.title}.</strong> {verdict.detail}</span>
      </p>
      <p className="muted note-line">The result is saved and now appears in your charts and tests.</p>
      <div className="dialog-actions">
        <button type="button" className="btn primary" onClick={onClose}>Done</button>
        <button type="button" className="btn" onClick={onAgain}>Run another</button>
      </div>
    </div>
  )
}
