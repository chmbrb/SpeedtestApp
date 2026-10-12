import { api, ApiError, unwrap, type ConnectionType, type Result, type TestRun } from '../shared/api/client'
import { Ndt7Error, REACH_MLAB, runNdt7 } from '../shared/lib/ndt7'
import { initialLive, liveReducer, progressActions, type LiveAction, type ProgressEvent } from '../shared/state/liveTest'

const BASE = import.meta.env.VITE_API_BASE ?? ''

/** Runs a test and exposes its live state. One test at a time per page. Same behaviour as the React hook
 *  (frontend-react/src/state/useRunTest.ts). */
export class RunTest {
  state = $state(initialLive)

  #source: EventSource | null = null
  #abort: AbortController | null = null
  #testId: string | null = null
  #kind: 'server' | 'browser' | null = null
  #onDone: () => void

  constructor(onFinished: () => void) {
    this.#onDone = onFinished
    $effect(() => () => { this.#closeStream(); this.#abort?.abort() })
  }

  #dispatch(a: LiveAction) { this.state = liveReducer(this.state, a) }
  #closeStream() { this.#source?.close(); this.#source = null }

  /** Follow a server-run test's live events. */
  #follow(id: string) {
    this.#closeStream()
    this.#testId = id
    this.#dispatch({ type: 'attach', testId: id })
    const es = new EventSource(`${BASE}/api/v1/tests/${id}/events`)
    this.#source = es
    const done = () => { es.close(); if (this.#source === es) this.#source = null; this.#onDone() }

    es.addEventListener('state', (e) => {
      const t = JSON.parse((e as MessageEvent).data).test as TestRun
      if (t.state === 'running') this.#dispatch({ type: 'phase', phase: 'ping' })
      if (t.state === 'cancelled') { this.#dispatch({ type: 'cancelled' }); done() }
    })
    es.addEventListener('progress', (e) => {
      for (const a of progressActions(JSON.parse((e as MessageEvent).data) as ProgressEvent)) this.#dispatch(a)
    })
    es.addEventListener('result', (e) => {
      this.#dispatch({ type: 'complete', result: JSON.parse((e as MessageEvent).data).result as Result })
      done()
    })
    // A server-sent event named "error" and a broken connection both arrive as 'error'. Only the first has data.
    es.addEventListener('error', (e) => {
      const data = (e as MessageEvent).data
      if (typeof data === 'string' && data) {
        this.#dispatch({ type: 'fail', error: JSON.parse(data).message as string })
        done()
      } else if (es.readyState === EventSource.CLOSED) {
        this.#dispatch({ type: 'fail', error: 'The connection to the server was lost. The test may still be running; reopen this window to follow it.' })
        done()
      } // otherwise the browser is reconnecting by itself and resumes from the last event it saw
    })
  }

  /** Watch a server test that is already running (started by someone else, or before this page was opened). */
  join = (id: string) => {
    this.#kind = 'server'
    this.#dispatch({ type: 'start', kind: 'server' })
    this.#follow(id)
  }

  startServer = async (connectionType: ConnectionType) => {
    this.#kind = 'server'
    this.#dispatch({ type: 'start', kind: 'server' })
    try {
      const t = await unwrap(api.POST('/api/v1/tests', { body: { provider: 'ookla', mode: 'server', connectionType } }))
      this.#follow(t.id)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // someone else's test is running: watch that one instead of showing an error
        const active = await unwrap(api.GET('/api/v1/tests', { params: { query: { state: ['queued', 'running'], limit: 1 } } })).catch(() => null)
        const first = active?.items[0]
        if (first) { this.#follow(first.id); return }
      }
      this.#dispatch({ type: 'fail', error: e instanceof ApiError ? e.message : 'The server could not be reached.' })
    }
  }

  startBrowser = async (input: { device: string; connectionType: ConnectionType }) => {
    this.#kind = 'browser'
    this.#dispatch({ type: 'start', kind: 'browser' })
    let test: TestRun
    try {
      test = await unwrap(api.POST('/api/v1/tests', { body: { provider: 'ndt7', mode: 'browser', ...input } }))
    } catch (e) {
      this.#dispatch({ type: 'fail', error: e instanceof ApiError ? e.message : 'The server could not be reached.' })
      return
    }
    this.#testId = test.id
    this.#dispatch({ type: 'attach', testId: test.id })
    const ctrl = new AbortController()
    this.#abort = ctrl
    const cancelOnServer = () => { void api.DELETE('/api/v1/tests/{id}', { params: { path: { id: test.id } } }).catch(() => undefined) }
    try {
      const summary = await runNdt7({
        onPhase: (p) => this.#dispatch({ type: 'phase', phase: p }),
        onSample: (s) => this.#dispatch({ type: 'sample', phase: s.phase, elapsedMs: s.elapsedMs, mbps: s.mbps }),
      }, ctrl.signal)
      this.#dispatch({ type: 'saving' })
      const result = await unwrap(api.POST('/api/v1/tests/{id}/result', {
        params: { path: { id: test.id } },
        headers: { Authorization: `Bearer ${test.submitToken}` },
        body: summary,
      }))
      this.#dispatch({ type: 'complete', result })
      this.#onDone()
    } catch (e) {
      cancelOnServer() // don't leave the test open on the server until it times out
      if (e instanceof DOMException && e.name === 'AbortError') this.#dispatch({ type: 'cancelled' })
      else if (e instanceof Ndt7Error || e instanceof ApiError) this.#dispatch({ type: 'fail', error: e.message })
      else this.#dispatch({ type: 'fail', error: REACH_MLAB })
    } finally {
      this.#abort = null
    }
  }

  cancel = async () => {
    this.#dispatch({ type: 'stopping' })
    if (this.#kind === 'browser') { this.#abort?.abort(); return }
    if (this.#testId) {
      await api.DELETE('/api/v1/tests/{id}', { params: { path: { id: this.#testId } } }).catch(() => undefined)
    }
  }

  reset = () => {
    this.#closeStream()
    this.#abort = null
    this.#testId = null
    this.#kind = null
    this.#dispatch({ type: 'reset' })
  }
}
