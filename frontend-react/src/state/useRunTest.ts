import { useCallback, useEffect, useReducer, useRef } from 'react'
import { api, ApiError, unwrap, type ConnectionType, type Result, type TestRun } from '../shared/api/client'
import { Ndt7Error, REACH_MLAB, runNdt7 } from '../shared/lib/ndt7'
import { initialLive, liveReducer, progressActions, type ProgressEvent } from '../shared/state/liveTest'

const BASE = import.meta.env.VITE_API_BASE ?? ''

const isTerminal = (s: string) => s === 'completed' || s === 'failed' || s === 'cancelled'

/** Runs a test and exposes its live state. One test at a time per page. */
export function useRunTest(onFinished: () => void) {
  const [state, dispatch] = useReducer(liveReducer, initialLive)
  const source = useRef<EventSource | null>(null)
  const abort = useRef<AbortController | null>(null)
  const testId = useRef<string | null>(null)
  const kind = useRef<'server' | 'browser' | null>(null)
  const onDone = useRef(onFinished)
  onDone.current = onFinished

  const closeStream = () => { source.current?.close(); source.current = null }

  useEffect(() => () => { closeStream(); abort.current?.abort() }, [])

  /** Follow a server-run test's live events. */
  const follow = useCallback((id: string) => {
    closeStream()
    testId.current = id
    dispatch({ type: 'attach', testId: id })
    const es = new EventSource(`${BASE}/api/v1/tests/${id}/events`)
    source.current = es
    const done = () => { es.close(); if (source.current === es) source.current = null; onDone.current() }

    es.addEventListener('state', (e) => {
      const t = JSON.parse((e as MessageEvent).data).test as TestRun
      if (t.state === 'running') dispatch({ type: 'phase', phase: 'ping' })
      if (t.state === 'cancelled') { dispatch({ type: 'cancelled' }); done() }
    })
    es.addEventListener('progress', (e) => {
      for (const a of progressActions(JSON.parse((e as MessageEvent).data) as ProgressEvent)) dispatch(a)
    })
    es.addEventListener('result', (e) => {
      dispatch({ type: 'complete', result: JSON.parse((e as MessageEvent).data).result as Result })
      done()
    })
    // A server-sent event named "error" and a broken connection both arrive as 'error'. Only the first has data.
    es.addEventListener('error', (e) => {
      const data = (e as MessageEvent).data
      if (typeof data === 'string' && data) {
        dispatch({ type: 'fail', error: JSON.parse(data).message as string })
        done()
      } else if (es.readyState === EventSource.CLOSED) {
        dispatch({ type: 'fail', error: 'The connection to the server was lost. The test may still be running; reopen this window to follow it.' })
        done()
      } // otherwise the browser is reconnecting by itself and resumes from the last event it saw
    })
  }, [])

  /** Watch a server test that is already running (started by someone else, or before this page was opened). */
  const join = useCallback((id: string) => {
    kind.current = 'server'
    dispatch({ type: 'start', kind: 'server' })
    follow(id)
  }, [follow])

  const startServer = useCallback(async (connectionType: ConnectionType) => {
    kind.current = 'server'
    dispatch({ type: 'start', kind: 'server' })
    try {
      const t = await unwrap(api.POST('/api/v1/tests', { body: { provider: 'ookla', mode: 'server', connectionType } }))
      follow(t.id)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // someone else's test is running: watch that one instead of showing an error
        const active = await unwrap(api.GET('/api/v1/tests', { params: { query: { state: ['queued', 'running'], limit: 1 } } })).catch(() => null)
        const first = active?.items[0]
        if (first) { follow(first.id); return }
      }
      dispatch({ type: 'fail', error: e instanceof ApiError ? e.message : 'The server could not be reached.' })
    }
  }, [follow])

  const startBrowser = useCallback(async (input: { device: string; connectionType: ConnectionType }) => {
    kind.current = 'browser'
    dispatch({ type: 'start', kind: 'browser' })
    let test: TestRun
    try {
      test = await unwrap(api.POST('/api/v1/tests', { body: { provider: 'ndt7', mode: 'browser', ...input } }))
    } catch (e) {
      dispatch({ type: 'fail', error: e instanceof ApiError ? e.message : 'The server could not be reached.' })
      return
    }
    testId.current = test.id
    dispatch({ type: 'attach', testId: test.id })
    const ctrl = new AbortController()
    abort.current = ctrl
    const cancelOnServer = () => { void api.DELETE('/api/v1/tests/{id}', { params: { path: { id: test.id } } }).catch(() => undefined) }
    try {
      const summary = await runNdt7({
        onPhase: (p) => dispatch({ type: 'phase', phase: p }),
        onSample: (s) => dispatch({ type: 'sample', phase: s.phase, elapsedMs: s.elapsedMs, mbps: s.mbps }),
      }, ctrl.signal)
      dispatch({ type: 'saving' })
      const result = await unwrap(api.POST('/api/v1/tests/{id}/result', {
        params: { path: { id: test.id } },
        headers: { Authorization: `Bearer ${test.submitToken}` },
        body: summary,
      }))
      dispatch({ type: 'complete', result })
      onDone.current()
    } catch (e) {
      cancelOnServer() // don't leave the test open on the server until it times out
      if (e instanceof DOMException && e.name === 'AbortError') dispatch({ type: 'cancelled' })
      else if (e instanceof Ndt7Error || e instanceof ApiError) dispatch({ type: 'fail', error: e.message })
      else dispatch({ type: 'fail', error: REACH_MLAB })
    } finally {
      abort.current = null
    }
  }, [])

  const cancel = useCallback(async () => {
    dispatch({ type: 'stopping' })
    if (kind.current === 'browser') { abort.current?.abort(); return }
    if (testId.current) {
      await api.DELETE('/api/v1/tests/{id}', { params: { path: { id: testId.current } } }).catch(() => undefined)
    }
  }, [])

  const reset = useCallback(() => {
    closeStream()
    abort.current = null
    testId.current = null
    kind.current = null
    dispatch({ type: 'reset' })
  }, [])

  return { state, startServer, startBrowser, join, cancel, reset, isTerminal }
}
