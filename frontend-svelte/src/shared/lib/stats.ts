import type { Stat } from '../api/client'

export type Tone = 'good' | 'warning' | 'critical' | 'neutral'

export interface Verdict {
  tone: Tone
  /** Short status word shown beside the icon. */
  title: string
  /** One plain sentence. */
  detail: string
}

/** Judge the latest speed against the viewer's own typical (median) speed over the same filters. */
export function judgeSpeed(latest: number, typical: number | null | undefined, fmt: (n: number) => string): Verdict {
  if (typical == null || typical <= 0) {
    return { tone: 'neutral', title: 'Not enough history', detail: 'Run more tests to see how this compares with your usual speed.' }
  }
  const r = latest / typical
  const diff = Math.abs(Math.round((1 - r) * 100))
  const usual = `your typical ${fmt(typical)} Mbps`
  if (r >= 1.1) return { tone: 'good', title: 'Faster than usual', detail: `${diff}% above ${usual}.` }
  if (r >= 0.9) return { tone: 'good', title: 'Normal', detail: diff === 0 ? `Right on ${usual}.` : `Within ${Math.max(diff, 1)}% of ${usual}.` }
  if (r >= 0.7) return { tone: 'warning', title: 'Slower than usual', detail: `${diff}% below ${usual}.` }
  return { tone: 'critical', title: 'Much slower than usual', detail: `${diff}% below ${usual}.` }
}

export interface Delta {
  /** Signed change (percent, or percentage points when unit is 'pts'); null when it cannot be computed. */
  pct: number | null
  unit?: '%' | 'pts'
  direction: 'up' | 'down' | 'flat' | 'none'
  /** Whether the change is an improvement; null for flat or unknown. */
  good: boolean | null
}

/** Change from `previous` to `current`. `upIsGood` says whether a rise is an improvement (speed) or not (latency, loss). */
export function delta(current: number | null | undefined, previous: number | null | undefined, upIsGood: boolean): Delta {
  if (current == null || previous == null || previous === 0 || !isFinite(current) || !isFinite(previous)) {
    return { pct: null, direction: 'none', good: null }
  }
  const pct = Math.round(((current - previous) / Math.abs(previous)) * 1000) / 10
  if (Math.abs(pct) < 0.5) return { pct, direction: 'flat', good: null }
  const up = pct > 0
  return { pct, direction: up ? 'up' : 'down', good: up === upIsGood }
}

export const median = (s: Stat | undefined) => s?.p50 ?? null

/**
 * Change in percentage points, for quantities that are already percentages and tiny (packet loss):
 * 0.02% -> 0.08% is "+0.06 points", not "+300%".
 */
export function deltaPoints(current: number | null | undefined, previous: number | null | undefined, upIsGood: boolean): Delta {
  if (current == null || previous == null || !isFinite(current) || !isFinite(previous)) {
    return { pct: null, direction: 'none', good: null }
  }
  const diff = Math.round((current - previous) * 100) / 100
  if (Math.abs(diff) < 0.01) return { pct: 0, unit: 'pts', direction: 'flat', good: null }
  return { pct: diff, unit: 'pts', direction: diff > 0 ? 'up' : 'down', good: diff > 0 === upIsGood }
}
