import type { Result } from '../api/client'

export type LivePhase = 'locating' | 'ping' | 'download' | 'upload'

export interface Sample {
  phase: 'download' | 'upload'
  /** Milliseconds since the test began; continues across phases so one chart can show both. */
  t: number
  mbps: number
}

export interface LiveState {
  kind: 'server' | 'browser' | null
  status: 'idle' | 'starting' | 'running' | 'completed' | 'failed' | 'cancelled'
  /** A stop was requested and the test is winding down. */
  stopping: boolean
  phase: LivePhase | null
  current: number | null
  latencyMs: number | null
  samples: Sample[]
  testId: string | null
  result: Result | null
  error: string | null
  /** Saving a finished browser test's result. */
  saving: boolean
  // bookkeeping for turning per-phase browser clocks into one timeline
  base: number
  lastPhase: 'download' | 'upload' | null
}

export const initialLive: LiveState = {
  kind: null, status: 'idle', stopping: false, phase: null, current: null, latencyMs: null, samples: [], testId: null,
  result: null, error: null, saving: false, base: 0, lastPhase: null,
}

export type LiveAction =
  | { type: 'start'; kind: 'server' | 'browser' }
  | { type: 'attach'; testId: string }
  | { type: 'phase'; phase: LivePhase }
  | { type: 'sample'; phase: 'download' | 'upload'; elapsedMs: number; mbps: number; absolute?: boolean }
  | { type: 'latency'; ms: number }
  | { type: 'saving' }
  | { type: 'stopping' }
  | { type: 'complete'; result: Result }
  | { type: 'fail'; error: string }
  | { type: 'cancelled' }
  | { type: 'reset' }

const finished = (s: LiveState) => s.status === 'completed' || s.status === 'failed' || s.status === 'cancelled'

/** Events that arrive after a test has ended (a late progress reading, a duplicate) never change the outcome. */
export function liveReducer(s: LiveState, a: LiveAction): LiveState {
  if (a.type === 'start') return { ...initialLive, kind: a.kind, status: 'starting' }
  if (a.type === 'reset') return initialLive
  if (finished(s)) return s

  switch (a.type) {
    case 'attach':
      return { ...s, testId: a.testId }
    case 'phase':
      return { ...s, status: 'running', phase: a.phase, current: a.phase === 'download' || a.phase === 'upload' ? null : s.current }
    case 'latency':
      return { ...s, latencyMs: a.ms }
    case 'sample': {
      let base = s.base
      let t = a.elapsedMs
      if (!a.absolute) {
        if (s.lastPhase !== null && s.lastPhase !== a.phase) base = s.samples[s.samples.length - 1]?.t ?? s.base
        t = base + a.elapsedMs
      }
      return {
        ...s, status: 'running', phase: a.phase, current: a.mbps, base, lastPhase: a.phase,
        samples: [...s.samples, { phase: a.phase, t, mbps: a.mbps }],
      }
    }
    case 'saving':
      return { ...s, saving: true }
    case 'stopping':
      return { ...s, stopping: true }
    case 'complete':
      return { ...s, status: 'completed', result: a.result, saving: false, stopping: false, phase: null }
    case 'fail':
      return { ...s, status: 'failed', error: a.error, saving: false, stopping: false }
    case 'cancelled':
      return { ...s, status: 'cancelled', saving: false, stopping: false }
  }
}

/** Map an SSE progress event from the server to reducer actions. */
export interface ProgressEvent {
  phase: 'ping' | 'download' | 'upload'
  elapsedMs: number
  currentMbps?: number | null
  latencyMs?: number | null
}

export function progressActions(e: ProgressEvent): LiveAction[] {
  const out: LiveAction[] = []
  if (e.phase === 'ping') {
    out.push({ type: 'phase', phase: 'ping' })
    if (e.latencyMs != null) out.push({ type: 'latency', ms: e.latencyMs })
  } else if (e.currentMbps != null) {
    out.push({ type: 'sample', phase: e.phase, elapsedMs: e.elapsedMs, mbps: e.currentMbps, absolute: true })
  } else {
    out.push({ type: 'phase', phase: e.phase })
  }
  return out
}
