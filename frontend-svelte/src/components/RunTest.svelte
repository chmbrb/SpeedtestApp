<script lang="ts">
  import type { Capabilities, ConnectionType, TestRun } from '../shared/api/client'
  import { summary as summaryQuery } from '../lib/data.svelte'
  import { invalidate } from '../lib/query.svelte'
  import { RunTest } from '../lib/runTest.svelte'
  import { MLAB_POLICY_URL } from '../shared/lib/ndt7'
  import { rangeWindow, type Filters } from '../shared/state/filters'
  import Icon from './Icon.svelte'
  import LiveTest from './LiveTest.svelte'
  import Segmented from './Segmented.svelte'

  /** The header button, and the dialog it opens. */
  let { capabilities, active, now }: { capabilities: Capabilities | undefined; active: TestRun[]; now: number } = $props()

  type Choice = 'server' | 'browser'

  const store = {
    get: (k: string, fallback = '') => { try { return localStorage.getItem(k) ?? fallback } catch { return fallback } },
    set: (k: string, v: string) => { try { localStorage.setItem(k, v) } catch { /* private mode: it just will not be remembered */ } },
  }

  let dialog = $state<HTMLDialogElement>()
  let open = $state(false)
  const run = new RunTest(() => invalidate('results', 'summary', 'timeseries', 'recent', 'devices', 'tests'))
  const st = $derived(run.state)

  const serverRunning = $derived(active.find((t) => t.mode === 'server'))
  const busy = $derived(st.status === 'starting' || st.status === 'running')

  let choice = $state<Choice>('server')
  let device = $state(store.get('runtest.device'))
  let connection = $state<ConnectionType>(store.get('runtest.connection') === 'wired' ? 'wired' : 'wireless')
  let consent = $state(store.get('mlab.consent') === 'v1')

  // The server can only run one test at a time, and may not be able to run Ookla at all: default to what is possible.
  const serverFree = $derived(!!capabilities?.ookla.available && !serverRunning)
  $effect(() => {
    if (capabilities && !serverFree && choice === 'server') choice = 'browser'
  })

  function show() {
    open = true
    dialog?.showModal()
  }
  function hide() {
    dialog?.close()
    open = false
    if (st.status === 'completed' || st.status === 'failed' || st.status === 'cancelled') run.reset()
  }

  function start() {
    if (choice === 'server') { void run.startServer(capabilities?.serverConnectionType ?? 'wired'); return }
    const name = device.trim()
    store.set('runtest.device', name)
    store.set('runtest.connection', connection)
    store.set('mlab.consent', 'v1')
    void run.startBrowser({ device: name, connectionType: connection })
  }

  const canStart = $derived(choice === 'server' ? serverFree : consent && device.trim().length > 0 && device.trim().length <= 64)

  // How the finished test compares with that device's usual speed.
  const r = $derived(st.result)
  const entity = $derived<Filters>({ range: '30d', devices: r ? [r.device] : [], connection: r ? r.connectionType : 'all', providers: r ? [r.provider] : ['ookla'], interval: 'auto' })
  const typical = summaryQuery(() => entity, () => rangeWindow('30d', now), () => !!r)
</script>

<button type="button" class="btn primary run-button" onclick={show} data-testid="run-test">
  {#if serverRunning || (busy && !open)}<span class="pulse" aria-hidden="true"></span>{/if}
  {serverRunning ? 'Test running' : busy && !open ? 'Test running' : 'Run a test'}
</button>

<dialog bind:this={dialog} class="run-dialog" aria-labelledby="run-title" onclose={() => (open = false)}
  oncancel={(e) => { if (busy && st.kind === 'browser') e.preventDefault() }} data-testid="run-dialog">
  {#if open}
    <header class="dialog-head">
      <h2 id="run-title">Run a test</h2>
      <button type="button" class="icon-btn" onclick={hide} aria-label="Close">×</button>
    </header>
    {#if st.status === 'idle'}
      <form class="run-form" onsubmit={(e) => { e.preventDefault(); if (canStart) start() }}>
        <fieldset>
          <legend class="sr-only">What to test</legend>

          <label class="choice{choice === 'server' ? ' on' : ''}{serverFree ? '' : ' off'}">
            <input type="radio" name="what" value="server" checked={choice === 'server'} disabled={!serverFree}
              onchange={() => (choice = 'server')} />
            <span>
              <strong>Test the server’s connection</strong>
              <span class="choice-desc">Ookla runs on the server and shows its progress here. About 30 seconds.</span>
              {#if capabilities && !capabilities.ookla.available && capabilities.ookla.reason}
                <span class="choice-why" data-testid="ookla-why"><Icon name="info" size={14} /> {capabilities.ookla.reason}</span>
              {/if}
              {#if serverRunning && capabilities?.ookla.available}
                <span class="choice-why" data-testid="server-busy">
                  <Icon name="info" size={14} /> A test is already running on the server.{' '}
                  <button type="button" class="link" onclick={() => run.join(serverRunning.id)}>Watch it</button>
                </span>
              {/if}
            </span>
          </label>

          <label class="choice{choice === 'browser' ? ' on' : ''}">
            <input type="radio" name="what" value="browser" checked={choice === 'browser'} onchange={() => (choice = 'browser')} />
            <span>
              <strong>Test this device</strong>
              <span class="choice-desc">M-Lab NDT7 runs in this browser, so it measures this device’s own connection to the internet. About 25 seconds.</span>
            </span>
          </label>
        </fieldset>

        {#if choice === 'browser'}
          <div class="browser-options">
            <label class="field">
              <span>Device name</span>
              <input type="text" bind:value={device} maxlength="64" placeholder="For example: Alex’s phone" autocomplete="off" data-testid="device-name" />
            </label>
            <Segmented label="Connection" caption value={connection} onChange={(v) => (connection = v)}
              options={[{ value: 'wireless', label: 'Wi-Fi / wireless' }, { value: 'wired', label: 'Wired' }]} />
            <div class="callout">
              <Icon name="info" class="callout-icon" />
              <div>
                <p>
                  M-Lab (Measurement Lab) is a public research project. When this test runs, <strong>your IP address and the
                  measurements are sent to M-Lab and published as open data</strong>.
                </p>
                <p><a href={MLAB_POLICY_URL} target="_blank" rel="noreferrer noopener">Read M-Lab’s data policy</a> (opens in a new tab).</p>
                <label class="check">
                  <input type="checkbox" bind:checked={consent} data-testid="mlab-consent" />
                  <span>I understand and accept M-Lab’s data policy</span>
                </label>
              </div>
            </div>
          </div>
        {/if}

        <div class="dialog-actions">
          <button type="submit" class="btn primary" disabled={!canStart} data-testid="start-test">Start test</button>
          <button type="button" class="btn" onclick={hide}>Cancel</button>
        </div>
      </form>
    {:else}
      <LiveTest s={st} typical={typical.data?.downloadMbps.p50} onCancel={() => void run.cancel()} onAgain={run.reset} onClose={hide} />
    {/if}
  {/if}
</dialog>
