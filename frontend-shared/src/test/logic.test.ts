import { describe, expect, it } from 'vitest'
import { type Filters, defaultFilters, intervalMs, parseFilters, previousWindow, rangeWindow, resolveInterval, serializeFilters } from '../state/filters'
import { buildColorSlots, colorVar, keyId, MAX_SERIES, slotFor } from '../lib/colors'
import { delta, deltaPoints, judgeSpeed } from '../lib/stats'
import { ago, bucketLabel, count, mbps, ms, pct } from '../lib/format'
import type { Device, Result } from '../api/client'
import { DAY, HOUR } from '../lib/time'
import { describeRules, eventLabel } from '../lib/alerts'
import { buildNdt7Summary } from '../lib/ndt7'
import { initialLive, liveReducer, progressActions, type LiveAction, type LiveState } from '../state/liveTest'

describe('filters <-> URL', () => {
  it('defaults serialize to an empty query', () => {
    expect(serializeFilters(defaultFilters)).toBe('')
    expect(parseFilters('')).toEqual(defaultFilters)
  })

  it('round-trips every field', () => {
    const f: Filters = { range: '7d', devices: ['laptop', 'phone'], connection: 'wireless', providers: ['ndt7', 'ookla'], interval: 'hour' }
    const s = serializeFilters(f)
    expect(s).toBe('?range=7d&device=laptop%2Cphone&connection=wireless&provider=ndt7%2Cookla&interval=hour')
    expect(parseFilters(s)).toEqual(f)
  })

  it('ignores invalid values field by field instead of failing', () => {
    const f = parseFilters('?range=century&connection=lte&provider=speedof,ndt7&interval=minute&device=a,,b')
    expect(f).toEqual({ ...defaultFilters, providers: ['ndt7'], devices: ['a', 'b'] })
  })

  it('de-duplicates and orders providers so the same view has one URL', () => {
    expect(parseFilters('?provider=ookla,ndt7,ookla').providers).toEqual(['ndt7', 'ookla'])
  })

  it('caps the device list', () => {
    const many = Array.from({ length: 50 }, (_, i) => `d${i}`).join(',')
    expect(parseFilters(`?device=${many}`).devices).toHaveLength(20)
  })
})

describe('windows and intervals', () => {
  const now = Date.UTC(2026, 9, 11, 12, 0, 0)
  it('builds an open-ended window for a range, and none for all time', () => {
    const w = rangeWindow('7d', now)
    expect(w.to).toBeUndefined()
    expect(Date.parse(w.from!)).toBe(now - 7 * DAY)
    expect(rangeWindow('all', now)).toEqual({})
  })

  it('previous window is the equal span right before the current one', () => {
    const cur = rangeWindow('30d', now)
    const prev = previousWindow('30d', now)!
    expect(prev.to).toBe(cur.from)
    expect(Date.parse(prev.to!) - Date.parse(prev.from!)).toBe(30 * DAY)
    expect(previousWindow('all', now)).toBeNull()
  })

  it('auto interval scales with the span', () => {
    expect(resolveInterval('auto', DAY)).toBe('hour')
    expect(resolveInterval('auto', 30 * DAY)).toBe('day')
    expect(resolveInterval('auto', 365 * DAY)).toBe('week')
    expect(resolveInterval('auto', 5 * 365 * DAY)).toBe('month')
    expect(resolveInterval('auto', undefined)).toBe('week')
    expect(resolveInterval('hour', 5 * 365 * DAY)).toBe('hour') // an explicit choice always wins
  })

  it('bucket lengths are ordered', () => {
    expect(intervalMs('hour')).toBe(HOUR)
    expect(intervalMs('day')).toBeGreaterThan(intervalMs('hour'))
    expect(intervalMs('month')).toBeGreaterThan(intervalMs('week'))
  })
})

describe('series colors', () => {
  const dev = (name: string, connectionTypes: Device['connectionTypes'], providers: Device['providers']): Device => ({
    name, connectionTypes, providers, testCount: 1, lastTestAt: '2026-10-11T00:00:00Z',
  })
  const devices = [dev('server', ['wired'], ['ookla']), dev('laptop', ['wired', 'wireless'], ['ookla']), dev('phone', ['wireless'], ['ndt7'])]

  it('assigns slots in a fixed, name-sorted order', () => {
    const slots = buildColorSlots(devices)
    expect(slots.get(keyId({ provider: 'ookla', device: 'laptop', connectionType: 'wired' }))).toBe(0)
    expect(slots.get(keyId({ provider: 'ookla', device: 'laptop', connectionType: 'wireless' }))).toBe(1)
    expect(slots.get(keyId({ provider: 'ndt7', device: 'phone', connectionType: 'wireless' }))).toBe(2)
    expect(slots.get(keyId({ provider: 'ookla', device: 'server', connectionType: 'wired' }))).toBe(3)
  })

  it('a series keeps its color no matter what else is on screen or what order devices arrive in', () => {
    const a = buildColorSlots(devices)
    const b = buildColorSlots([...devices].reverse())
    for (const [id, slot] of a) expect(b.get(id)).toBe(slot)
  })

  it('never invents a ninth color', () => {
    const many = Array.from({ length: 10 }, (_, i) => dev(`d${String(i).padStart(2, '0')}`, ['wired'], ['ookla']))
    const slots = buildColorSlots(many)
    const key = (i: number) => ({ provider: 'ookla', device: `d${String(i).padStart(2, '0')}`, connectionType: 'wired' })
    expect(slotFor(key(MAX_SERIES - 1), slots)).toBe(MAX_SERIES - 1)
    expect(slotFor(key(MAX_SERIES), slots)).toBeNull()
    expect(colorVar(null)).toBe('var(--muted)')
    expect(colorVar(0)).toBe('var(--series-1)')
  })

  it('resolves partial keys (group-by subsets) to a stable slot', () => {
    const slots = buildColorSlots(devices)
    expect(slotFor({ device: 'phone' }, slots)).toBe(2)
    expect(slotFor({ device: 'nobody' }, slots)).toBeNull()
  })
})

describe('speed verdict', () => {
  const f = (n: number) => String(Math.round(n))
  it('judges the latest test against the typical speed with plain words', () => {
    expect(judgeSpeed(940, 900, f)).toMatchObject({ tone: 'good', title: 'Normal' })
    expect(judgeSpeed(900, 900, f)).toMatchObject({ tone: 'good', detail: 'Right on your typical 900 Mbps.' })
    expect(judgeSpeed(1100, 900, f)).toMatchObject({ tone: 'good', title: 'Faster than usual' })
    expect(judgeSpeed(700, 900, f)).toMatchObject({ tone: 'warning', title: 'Slower than usual', detail: '22% below your typical 900 Mbps.' })
    expect(judgeSpeed(300, 900, f)).toMatchObject({ tone: 'critical', title: 'Much slower than usual' })
  })
  it('does not judge without history', () => {
    expect(judgeSpeed(500, null, f).tone).toBe('neutral')
    expect(judgeSpeed(500, 0, f).tone).toBe('neutral')
  })
  it('band edges are inclusive on the better side', () => {
    expect(judgeSpeed(810, 900, f).tone).toBe('good') // exactly 90%
    expect(judgeSpeed(630, 900, f).tone).toBe('warning') // exactly 70%
  })
})

describe('deltas', () => {
  it('knows whether a change is good for speed vs latency', () => {
    expect(delta(110, 100, true)).toEqual({ pct: 10, direction: 'up', good: true })
    expect(delta(110, 100, false)).toEqual({ pct: 10, direction: 'up', good: false })
    expect(delta(90, 100, false)).toEqual({ pct: -10, direction: 'down', good: true })
  })
  it('treats tiny changes as flat and missing data as none', () => {
    expect(delta(100.2, 100, true)).toMatchObject({ direction: 'flat', good: null })
    expect(delta(null, 100, true).direction).toBe('none')
    expect(delta(5, 0, true).direction).toBe('none')
    expect(delta(5, undefined, true).direction).toBe('none')
  })
})

describe('percentage-point deltas', () => {
  it('reports tiny percentages as points, not as a huge relative change', () => {
    expect(deltaPoints(0.08, 0.02, false)).toEqual({ pct: 0.06, unit: 'pts', direction: 'up', good: false })
    expect(deltaPoints(0.02, 0.08, false)).toEqual({ pct: -0.06, unit: 'pts', direction: 'down', good: true })
  })
  it('treats sub-hundredth changes as flat and missing data as none', () => {
    expect(deltaPoints(0.031, 0.029, false)).toMatchObject({ direction: 'flat', good: null })
    expect(deltaPoints(null, 0.02, false).direction).toBe('none')
    expect(deltaPoints(0, 0, false).direction).toBe('flat')
  })
})

describe('formatting', () => {
  it('keeps three significant figures for speeds', () => {
    expect(mbps(928.221)).toBe('928')
    expect(mbps(92.84)).toBe('92.8')
    expect(mbps(9.284)).toBe('9.28')
    expect(mbps(1234.5)).toBe('1,235')
    expect(mbps(null)).toBe('—')
    expect(mbps(NaN)).toBe('—')
  })
  it('formats latency, loss and counts', () => {
    expect(ms(12.04)).toBe('12.0')
    expect(ms(120.4)).toBe('120')
    expect(pct(0)).toBe('0')
    expect(pct(0.026)).toBe('0.03')
    expect(pct(9.6667)).toBe('9.67')
    expect(count(1512)).toBe('1,512')
  })
  it('describes elapsed time coarsely', () => {
    const now = Date.UTC(2026, 9, 11, 12)
    expect(ago(now - 10_000, now)).toBe('just now')
    expect(ago(now - 61_000, now)).toBe('1 minute ago')
    expect(ago(now - 3 * HOUR, now)).toBe('3 hours ago')
    expect(ago(now - 2 * DAY, now)).toBe('2 days ago')
  })
  it('labels buckets by size', () => {
    const t = new Date(2026, 5, 1, 13, 0).getTime()
    expect(bucketLabel(t, 'month')).toContain('2026')
    expect(bucketLabel(t, 'week')).toMatch(/^Week of /)
    expect(bucketLabel(t, 'quarter')).toBe('Q2 2026')
    expect(bucketLabel(t, 'year')).toBe('2026')
  })
})

describe('alert rule sentences', () => {
  const rules = { slowRatio: 0.7, slowConsecutive: 2, recoverRatio: 0.9, lossPct: 5, lossConsecutive: 2, silenceMinHours: 3, remindHours: 12, baselineDays: 30 }
  it('states every threshold in plain words', () => {
    const lines = describeRules(rules)
    expect(lines[0]).toContain('below 70% of its typical speed for 2 tests in a row')
    expect(lines[0]).toContain('median of the last 30 days')
    expect(lines[0]).toContain('back above 90%')
    expect(lines[1]).toBe('Packet loss is 5% or more for 2 tests in a row.')
    expect(lines[2]).toContain('three times its usual interval')
    expect(lines[2]).toContain('never sooner than 3 hours')
    expect(lines[3]).toBe('While a problem lasts you get a reminder every 12 hours, for up to a week.')
  })
  it('handles singular, fractional and switched-off values', () => {
    const lines = describeRules({ ...rules, slowConsecutive: 1, silenceMinHours: 1.5, remindHours: 0 })
    expect(lines[0]).toContain('for one test.')
    expect(lines[2]).toContain('never sooner than 1.5 hours')
    expect(lines[3]).toContain('Reminders are off')
    expect(describeRules({ ...rules, silenceMinHours: 1 })[2]).toContain('1 hour.')
  })
  it('labels events', () => {
    expect(eventLabel('alert')).toBe('Alert')
    expect(eventLabel('recovery')).toBe('Recovered')
  })
})

describe('live test state', () => {
  const sample = (phase: 'download' | 'upload', elapsedMs: number, mbps: number, absolute = false): LiveAction => ({ type: 'sample', phase, elapsedMs, mbps, absolute })
  const run = (actions: LiveAction[], from: LiveState = initialLive) => actions.reduce(liveReducer, from)

  it('follows a server test through its phases', () => {
    const s = run([
      { type: 'start', kind: 'server' },
      ...progressActions({ phase: 'ping', elapsedMs: 100, latencyMs: 12.5 }),
      ...progressActions({ phase: 'download', elapsedMs: 1000, currentMbps: 90 }),
      ...progressActions({ phase: 'download', elapsedMs: 2000, currentMbps: 95 }),
      ...progressActions({ phase: 'upload', elapsedMs: 9000, currentMbps: 40 }),
    ])
    expect(s).toMatchObject({ status: 'running', phase: 'upload', current: 40, latencyMs: 12.5 })
    expect(s.samples.map((x) => x.t)).toEqual([1000, 2000, 9000]) // the server's clock is already one timeline
  })

  it('puts a browser test’s two phase clocks on one timeline', () => {
    const s = run([
      { type: 'start', kind: 'browser' },
      sample('download', 250, 80), sample('download', 10000, 90),
      sample('upload', 250, 20), sample('upload', 500, 22),
    ])
    expect(s.samples.map((x) => x.t)).toEqual([250, 10000, 10250, 10500])
    expect(s.samples.map((x) => x.phase)).toEqual(['download', 'download', 'upload', 'upload'])
  })

  it('a phase change clears the speed so the previous phase’s figure is not shown for the new one', () => {
    const s = run([{ type: 'start', kind: 'browser' }, sample('download', 100, 80), { type: 'phase', phase: 'upload' }])
    expect(s).toMatchObject({ phase: 'upload', current: null })
  })

  it('keeps the latency when a download starts, and a reading without speed only changes the phase', () => {
    const s = run([{ type: 'start', kind: 'server' }, { type: 'latency', ms: 12 }, ...progressActions({ phase: 'download', elapsedMs: 10, currentMbps: null })])
    expect(s).toMatchObject({ phase: 'download', latencyMs: 12, samples: [] })
  })

  it('ends exactly once: nothing that arrives afterwards changes the outcome', () => {
    const result = { id: 'r1', downloadMbps: 100 } as Result
    const done = run([{ type: 'start', kind: 'server' }, sample('download', 1, 5, true), { type: 'complete', result }])
    expect(done).toMatchObject({ status: 'completed', result, phase: null })
    for (const late of [sample('upload', 2, 9, true), { type: 'fail', error: 'x' } as LiveAction, { type: 'cancelled' } as LiveAction, { type: 'phase', phase: 'ping' } as LiveAction]) {
      expect(liveReducer(done, late)).toBe(done)
    }
    const failed = run([{ type: 'start', kind: 'server' }, { type: 'fail', error: 'Network is unreachable' }])
    expect(failed).toMatchObject({ status: 'failed', error: 'Network is unreachable' })
    expect(liveReducer(failed, { type: 'complete', result })).toBe(failed)
  })

  it('records stopping and saving, and start/reset begin clean', () => {
    const s = run([{ type: 'start', kind: 'browser' }, { type: 'stopping' }, { type: 'saving' }])
    expect(s).toMatchObject({ stopping: true, saving: true, kind: 'browser' })
    expect(run([{ type: 'start', kind: 'server' }], s)).toMatchObject({ stopping: false, saving: false, samples: [], kind: 'server' })
    expect(run([{ type: 'reset' }], s)).toEqual(initialLive)
    expect(run([{ type: 'attach', testId: 't1' }], initialLive).testId).toBe('t1')
  })
})

describe('NDT7 summary from the M-Lab client’s measurements', () => {
  const started = new Date('2026-10-11T15:00:00Z')
  const finished = new Date('2026-10-11T15:00:22Z')
  const download = {
    client: { ElapsedTime: 10, NumBytes: 109375000, MeanClientMbps: 87.5 },
    server: { TCPInfo: { BytesSent: 110000000, BytesRetrans: 341000, MinRTT: 18400 } },
  }
  const upload = {
    client: { ElapsedTime: 10, NumBytes: 40000000, MeanClientMbps: 32 }, // inflated by data still in the send buffer
    server: { TCPInfo: { BytesReceived: 29062500, ElapsedTime: 10_000_000 } },
  }
  const server = { machine: 'mlab1-lax05', location: { city: 'Los Angeles', country: 'US' } }

  it('uses the client’s download mean and the server’s count of received bytes for upload', () => {
    const s = buildNdt7Summary({ startedAt: started, finishedAt: finished, download, upload, server })
    expect(s.downloadMbps).toBe(87.5)
    expect(s.uploadMbps).toBe(23.25) // 29,062,500 bytes * 8 / 10 s = 23.25 Mbit/s, not the client’s 32
    expect(s.minRttMs).toBe(18.4) // microseconds -> milliseconds
    expect(s.retransmissionPct).toBe(0.31) // 341,000 / 110,000,000
    expect(s.server).toEqual({ machine: 'mlab1-lax05', location: 'Los Angeles', country: 'US' })
    expect(s.startedAt).toBe('2026-10-11T15:00:00.000Z')
    expect(JSON.stringify(s).length).toBeLessThan(2000) // the raw part stays small
  })

  it('falls back to the client’s upload figure and reports unknowns as null, never as guesses', () => {
    const s = buildNdt7Summary({ startedAt: started, finishedAt: finished, download: { client: download.client }, upload: { client: upload.client }, server: null })
    expect(s.uploadMbps).toBe(32)
    expect(s.minRttMs).toBeNull()
    expect(s.retransmissionPct).toBeNull()
    expect(s.server).toEqual({ machine: '', location: '', country: '' })
  })

  it('never reports a retransmission rate outside 0-100 or from nothing sent', () => {
    const pct = (sent: number, retrans: number) =>
      buildNdt7Summary({ startedAt: started, finishedAt: finished, download: { ...download, server: { TCPInfo: { BytesSent: sent, BytesRetrans: retrans } } }, upload, server }).retransmissionPct
    expect(pct(0, 5)).toBeNull()
    expect(pct(100, 500)).toBe(100)
    expect(pct(100, 0)).toBe(0)
  })

  it('refuses to build a result when a speed is missing', () => {
    expect(() => buildNdt7Summary({ startedAt: started, finishedAt: finished, download: {}, upload, server })).toThrow('download speed')
    expect(() => buildNdt7Summary({ startedAt: started, finishedAt: finished, download, upload: {}, server })).toThrow('upload speed')
    expect(() => buildNdt7Summary({ startedAt: started, finishedAt: finished, download: { client: { MeanClientMbps: NaN } }, upload, server })).toThrow()
  })
})
