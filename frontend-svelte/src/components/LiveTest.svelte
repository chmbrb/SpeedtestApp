<script lang="ts">
  import { label as connectionLabel, mbps, ms, pct, providerName } from '../shared/lib/format'
  import { judgeSpeed } from '../shared/lib/stats'
  import type { LiveState } from '../shared/state/liveTest'
  import Icon from './Icon.svelte'
  import LiveChart from './LiveChart.svelte'

  let { s, typical, onCancel, onAgain, onClose }: {
    s: LiveState
    /** The device's usual download speed, to judge the result against (null when there is no history). */
    typical: number | null | undefined
    onCancel: () => void
    onAgain: () => void
    onClose: () => void
  } = $props()

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

  const active = $derived(s.phase ? STEP_OF[s.phase] : -1)
  const done = $derived(s.status === 'completed')
  const where = $derived(s.kind === 'browser' ? 'this device' : 'the server')
  const phaseText = $derived(
    s.saving ? 'Saving the result…'
      : s.stopping ? (s.kind === 'browser' ? 'Stopping after this step…' : 'Stopping…')
      : s.phase ? PHASE_TEXT[s.phase] : `Starting the test on ${where}…`,
  )
  const showSpeed = $derived(s.phase === 'download' || s.phase === 'upload')
  const r = $derived(s.result)
  const verdict = $derived(r ? judgeSpeed(r.downloadMbps, typical, mbps) : null)
</script>

{#if s.status === 'failed'}
  <div class="live" data-testid="live-failed">
    <div class="callout err" role="alert">
      <Icon name="alert" class="callout-icon" />
      <p><strong>The test didn’t finish.</strong> {s.error}</p>
    </div>
    <div class="dialog-actions">
      <button type="button" class="btn primary" onclick={onAgain}>Try again</button>
      <button type="button" class="btn" onclick={onClose}>Close</button>
    </div>
  </div>
{:else if s.status === 'cancelled'}
  <div class="live" data-testid="live-cancelled">
    <p><Icon name="info" /> The test was stopped. Nothing was saved.</p>
    <div class="dialog-actions">
      <button type="button" class="btn primary" onclick={onAgain}>Start another</button>
      <button type="button" class="btn" onclick={onClose}>Close</button>
    </div>
  </div>
{:else if s.status === 'completed' && r && verdict}
  <div class="live" data-testid="live-done">
    <p class="live-status" role="status">Test complete. {providerName(r.provider)} · {r.device}, {connectionLabel(r.connectionType)}</p>
    <ul class="done-tiles">
      <li><span class="kpi-label">Download</span><span class="live-number small" data-testid="done-download">{mbps(r.downloadMbps)}<span class="kpi-unit"> Mbps</span></span></li>
      <li><span class="kpi-label">Upload</span><span class="live-number small" data-testid="done-upload">{mbps(r.uploadMbps)}<span class="kpi-unit"> Mbps</span></span></li>
      <li><span class="kpi-label">Latency</span><span class="live-number small">{ms(r.latencyMs)}<span class="kpi-unit"> ms</span></span></li>
      {#if r.packetLossPct != null}
        <li><span class="kpi-label">{r.provider === 'ndt7' ? 'Retransmitted' : 'Packet loss'}</span><span class="live-number small">{pct(r.packetLossPct)}<span class="kpi-unit"> %</span></span></li>
      {/if}
    </ul>
    <p class="verdict tone-{verdict.tone}" data-testid="done-verdict">
      <Icon name={verdict.tone === 'good' ? 'ok' : verdict.tone === 'neutral' ? 'info' : 'alert'} class="verdict-icon" />
      <span><strong>{verdict.title}.</strong> {verdict.detail}</span>
    </p>
    <p class="muted note-line">The result is saved and now appears in your charts and tests.</p>
    <div class="dialog-actions">
      <button type="button" class="btn primary" onclick={onClose}>Done</button>
      <button type="button" class="btn" onclick={onAgain}>Run another</button>
    </div>
  </div>
{:else}
  <div class="live" data-testid="live-running">
    <ol class="steps" aria-label="Progress">
      {#each STEPS as step, i (step.key)}
        {@const st = done || i < active ? 'done' : i === active ? 'active' : 'todo'}
        <li class="step {st}" aria-current={st === 'active' ? 'step' : undefined}>
          <span class="step-mark">{#if st === 'done'}<Icon name="check" size={14} />{/if}</span>
          {step.label}
        </li>
      {/each}
    </ol>
    <p class="live-figure" aria-hidden={!showSpeed}>
      <span class="live-number">{showSpeed && s.current != null ? mbps(s.current) : '—'}</span>
      <span class="live-unit">Mbps</span>
      {#if s.latencyMs != null}<span class="live-latency">{ms(s.latencyMs)} ms latency</span>{/if}
    </p>
    <LiveChart samples={s.samples} phase={s.phase} />
    <p class="live-status" role="status" aria-live="polite">{phaseText}</p>
    <div class="dialog-actions">
      <button type="button" class="btn" onclick={onCancel} disabled={s.stopping || s.saving}>
        {s.stopping ? 'Stopping…' : 'Stop test'}
      </button>
    </div>
  </div>
{/if}
