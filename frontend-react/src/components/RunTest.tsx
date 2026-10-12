import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { Capabilities, ConnectionType, TestRun } from '../shared/api/client'
import { useSummary } from '../api/hooks'
import { MLAB_POLICY_URL } from '../shared/lib/ndt7'
import { rangeWindow, type Filters } from '../shared/state/filters'
import { useRunTest } from '../state/useRunTest'
import { LiveTest } from './LiveTest'
import { InfoIcon, Segmented } from './ui'

type Choice = 'server' | 'browser'

const store = {
  get: (k: string, fallback = '') => { try { return localStorage.getItem(k) ?? fallback } catch { return fallback } },
  set: (k: string, v: string) => { try { localStorage.setItem(k, v) } catch { /* private mode: it just will not be remembered */ } },
}

/** The header button, and the dialog it opens. */
export function RunTest({ capabilities, active, now }: { capabilities: Capabilities | undefined; active: TestRun[]; now: number }) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [open, setOpen] = useState(false)
  const queryClient = useQueryClient()
  const run = useRunTest(() => {
    for (const key of ['results', 'summary', 'timeseries', 'recent', 'devices', 'tests']) queryClient.invalidateQueries({ queryKey: [key] })
  })
  const { state } = run

  const serverRunning = active.find((t) => t.mode === 'server')
  const busy = state.status === 'starting' || state.status === 'running'

  const [choice, setChoice] = useState<Choice>('server')
  const [device, setDevice] = useState(() => store.get('runtest.device'))
  const [connection, setConnection] = useState<ConnectionType>(() => (store.get('runtest.connection') === 'wired' ? 'wired' : 'wireless'))
  const [consent, setConsent] = useState(() => store.get('mlab.consent') === 'v1')

  // The server can only run one test at a time, and may not be able to run Ookla at all: default to what is possible.
  const serverFree = !!capabilities?.ookla.available && !serverRunning
  useEffect(() => {
    if (capabilities && !serverFree && choice === 'server') setChoice('browser')
  }, [capabilities, serverFree, choice])

  const show = () => {
    setOpen(true)
    dialog.current?.showModal()
  }
  const hide = () => {
    dialog.current?.close()
    setOpen(false)
    if (state.status === 'completed' || state.status === 'failed' || state.status === 'cancelled') run.reset()
  }

  const start = () => {
    if (choice === 'server') { void run.startServer(capabilities?.serverConnectionType ?? 'wired'); return }
    const name = device.trim()
    store.set('runtest.device', name)
    store.set('runtest.connection', connection)
    store.set('mlab.consent', 'v1')
    void run.startBrowser({ device: name, connectionType: connection })
  }

  const canStart = choice === 'server' ? serverFree : consent && device.trim().length > 0 && device.trim().length <= 64

  // How the finished test compares with that device's usual speed.
  const r = state.result
  const entity: Filters = { range: '30d', devices: r ? [r.device] : [], connection: r ? r.connectionType : 'all', providers: r ? [r.provider] : ['ookla'], interval: 'auto' }
  const typical = useSummary(entity, rangeWindow('30d', now), !!r)

  return (
    <>
      <button type="button" className="btn primary run-button" onClick={show} data-testid="run-test">
        {serverRunning || (busy && !open) ? <span className="pulse" aria-hidden="true" /> : null}
        {serverRunning ? 'Test running' : busy && !open ? 'Test running' : 'Run a test'}
      </button>

      <dialog ref={dialog} className="run-dialog" aria-labelledby="run-title" onClose={() => setOpen(false)}
        onCancel={(e) => { if (busy && state.kind === 'browser') e.preventDefault() }} data-testid="run-dialog">
        {open && (
          <>
            <header className="dialog-head">
              <h2 id="run-title">Run a test</h2>
              <button type="button" className="icon-btn" onClick={hide} aria-label="Close">×</button>
            </header>
            {state.status === 'idle' ? (
              <form className="run-form" onSubmit={(e) => { e.preventDefault(); if (canStart) start() }}>
                <fieldset>
                  <legend className="sr-only">What to test</legend>

                  <label className={`choice${choice === 'server' ? ' on' : ''}${serverFree ? '' : ' off'}`}>
                    <input type="radio" name="what" value="server" checked={choice === 'server'} disabled={!serverFree}
                      onChange={() => setChoice('server')} />
                    <span>
                      <strong>Test the server’s connection</strong>
                      <span className="choice-desc">Ookla runs on the server and shows its progress here. About 30 seconds.</span>
                      {capabilities && !capabilities.ookla.available && capabilities.ookla.reason && (
                        <span className="choice-why" data-testid="ookla-why"><InfoIcon size={14} /> {capabilities.ookla.reason}</span>
                      )}
                      {serverRunning && capabilities?.ookla.available && (
                        <span className="choice-why" data-testid="server-busy">
                          <InfoIcon size={14} /> A test is already running on the server.{' '}
                          <button type="button" className="link" onClick={() => run.join(serverRunning.id)}>Watch it</button>
                        </span>
                      )}
                    </span>
                  </label>

                  <label className={`choice${choice === 'browser' ? ' on' : ''}`}>
                    <input type="radio" name="what" value="browser" checked={choice === 'browser'} onChange={() => setChoice('browser')} />
                    <span>
                      <strong>Test this device</strong>
                      <span className="choice-desc">M-Lab NDT7 runs in this browser, so it measures this device’s own connection to the internet. About 25 seconds.</span>
                    </span>
                  </label>
                </fieldset>

                {choice === 'browser' && (
                  <div className="browser-options">
                    <label className="field">
                      <span>Device name</span>
                      <input type="text" value={device} maxLength={64} onChange={(e) => setDevice(e.target.value)}
                        placeholder="For example: Alex’s phone" autoComplete="off" data-testid="device-name" />
                    </label>
                    <Segmented<ConnectionType> label="Connection" caption value={connection} onChange={setConnection}
                      options={[{ value: 'wireless', label: 'Wi-Fi / wireless' }, { value: 'wired', label: 'Wired' }]} />
                    <div className="callout">
                      <InfoIcon className="callout-icon" />
                      <div>
                        <p>
                          M-Lab (Measurement Lab) is a public research project. When this test runs, <strong>your IP address and the
                          measurements are sent to M-Lab and published as open data</strong>.
                        </p>
                        <p><a href={MLAB_POLICY_URL} target="_blank" rel="noreferrer noopener">Read M-Lab’s data policy</a> (opens in a new tab).</p>
                        <label className="check">
                          <input type="checkbox" checked={consent} onChange={(e) => setConsent(e.target.checked)} data-testid="mlab-consent" />
                          <span>I understand and accept M-Lab’s data policy</span>
                        </label>
                      </div>
                    </div>
                  </div>
                )}

                <div className="dialog-actions">
                  <button type="submit" className="btn primary" disabled={!canStart} data-testid="start-test">Start test</button>
                  <button type="button" className="btn" onClick={hide}>Cancel</button>
                </div>
              </form>
            ) : (
              <LiveTest s={state} typical={typical.data?.downloadMbps.p50} onCancel={() => void run.cancel()} onAgain={run.reset} onClose={hide} />
            )}
          </>
        )}
      </dialog>
    </>
  )
}
