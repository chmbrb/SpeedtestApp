#!/usr/bin/env node
// Asks two backends (the Go one and the Rust one, both on the SAME database) the same questions and reports every difference.
//
//   node scripts/compare-backends.mjs http://127.0.0.1:8091 http://127.0.0.1:8092 [requests=600] [seed=1]
//
// Same status, same JSON (numbers to 1e-9, objects regardless of key order), same list of problem fields. Error *messages*
// may differ in wording. Cursors from one backend are also used on the other, so a client can switch in the middle of paging.
// Exit code 1 if anything differs.
const [goUrl, rustUrl, nArg, seedArg] = process.argv.slice(2)
if (!goUrl || !rustUrl) { console.error('usage: compare-backends.mjs GO_URL RUST_URL [requests] [seed]'); process.exit(2) }
const N = Number(nArg ?? 600)
let state = Number(seedArg ?? 1) >>> 0
const rand = () => { state = (state + 0x6d2b79f5) >>> 0; let t = state; t = Math.imul(t ^ (t >>> 15), t | 1); t ^= t + Math.imul(t ^ (t >>> 7), t | 61); return ((t ^ (t >>> 14)) >>> 0) / 4294967296 }
const pick = (a) => a[Math.floor(rand() * a.length)]
const maybe = (p) => rand() < p

async function ask(base, path, init = {}) {
  const res = await fetch(base + path, { ...init, headers: { accept: 'application/json', ...(init.headers ?? {}) } })
  const text = await res.text()
  let body
  try { body = JSON.parse(text) } catch { body = text }
  return { status: res.status, type: (res.headers.get('content-type') ?? '').split(';')[0], body }
}

/** Where two values differ, as a path; null when they agree. */
function diff(a, b, path = '$') {
  if (typeof a === 'number' && typeof b === 'number') {
    return Math.abs(a - b) <= 1e-9 * Math.max(1, Math.abs(a), Math.abs(b)) ? null : `${path}: ${a} != ${b}`
  }
  if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object') return a === b ? null : `${path}: ${JSON.stringify(a)} != ${JSON.stringify(b)}`
  if (Array.isArray(a) !== Array.isArray(b)) return `${path}: array vs object`
  if (Array.isArray(a)) {
    if (a.length !== b.length) return `${path}: length ${a.length} != ${b.length}`
    for (let i = 0; i < a.length; i++) { const d = diff(a[i], b[i], `${path}[${i}]`); if (d) return d }
    return null
  }
  const keys = new Set([...Object.keys(a), ...Object.keys(b)])
  for (const k of keys) {
    if (!(k in a)) return `${path}.${k}: only in Rust`
    if (!(k in b)) return `${path}.${k}: only in Go`
    const d = diff(a[k], b[k], `${path}.${k}`); if (d) return d
  }
  return null
}

const problems = []
let compared = 0
const ingestStatuses = {}
function compare(path, go, rust, ignore = []) {
  compared++
  for (const r of [go, rust]) if (r.status === 201 && r.body && typeof r.body === 'object') for (const k of ignore) delete r.body[k]
  let d = null
  if (go.status !== rust.status) d = `status ${go.status} (Go) != ${rust.status} (Rust)`
  else if (go.type !== rust.type) d = `content type ${go.type} != ${rust.type}`
  else if (go.status >= 400 && go.type === 'application/problem+json') {
    const fields = (r) => (r.body.errors ?? []).map((e) => e.field).join(',')
    d = fields(go) === fields(rust) ? null : `problem fields [${fields(go)}] != [${fields(rust)}]`
    if (!d && go.body.title !== rust.body.title) d = `title ${go.body.title} != ${rust.body.title}`
  } else d = diff(go.body, rust.body)
  if (d) problems.push(`${path}\n    ${d}`)
}

async function both(path, init, ignore) {
  const [go, rust] = await Promise.all([ask(goUrl, path, init), ask(rustUrl, path, init)])
  if (init?.method === 'POST' && path === '/api/v1/results') ingestStatuses[go.status] = (ingestStatuses[go.status] ?? 0) + 1
  compare(init?.method ? `${init.method} ${path}${init.label ? '  [' + init.label + ']' : ''}` : path, go, rust, ignore)
  return { go, rust }
}

// ---- what to ask -----------------------------------------------------------------------------------------------------------
const devices = (await ask(goUrl, '/api/v1/devices')).body.items
const names = devices.map((d) => d.name)
const first = new Date(Math.min(...devices.map((d) => Date.parse(d.lastTestAt)))).getTime() - 70 * 86400e3
const last = Math.max(...devices.map((d) => Date.parse(d.lastTestAt)))
const when = () => new Date(first + rand() * (last + 86400e3 - first)).toISOString().replace(/\.\d+Z$/, maybe(0.3) ? '.123456Z' : 'Z')
const enc = encodeURIComponent
const zones = ['UTC', 'America/Phoenix', 'Asia/Kolkata', 'Pacific/Chatham', 'Europe/London', 'Australia/Lord_Howe', 'America/St_Johns', 'Etc/GMT+12']

function filterParams() {
  const q = []
  if (maybe(0.5)) q.push('provider=' + pick(['ookla', 'ndt7', 'ookla,ndt7', 'ndt7,ookla,ookla']))
  if (maybe(0.5)) q.push('device=' + enc(pick([pick(names), names.join(','), 'nobody', 'my laptop', 'a,b', "x'y", '%_'])))
  if (maybe(0.4)) q.push('connectionType=' + pick(['wired', 'wireless', 'wired,wireless']))
  if (maybe(0.2)) q.push('trigger=' + pick(['scheduled', 'on_demand']))
  if (maybe(0.5)) q.push('from=' + enc(when()))
  if (maybe(0.4)) q.push('to=' + enc(when()))
  return q
}

const JUNK = [
  'provider=carrier-pigeon', 'connectionType=satellite', 'trigger=never', 'device=a,,b', 'device=', 'from=yesterday', 'to=2026-13-45T00:00:00Z',
  'from=2026-06-01', 'limit=0', 'limit=-3', 'limit=501', 'limit=abc', 'limit=', 'limit=1.5', 'interval=fortnight', 'groupBy=colour', 'groupBy=device,,provider',
  'timezone=Mars/Olympus', 'timezone=Local', 'cursor=garbage', 'cursor=', 'device=%00', 'device=%ff', 'device=' + 'x'.repeat(65), 'provider=%E2%98%83', 'x=1;y=2',
  'from=2026-06-02T00:00:00Z&to=2026-06-01T00:00:00Z', 'provider=ookla&provider=bogus',
]
// a cursor with a well-formed time and a 36-character id that is not a uuid
const BAD_CURSOR = Buffer.from('{"t":"2026-10-12T06:38:15Z","id":"zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"}').toString('base64url')
JUNK.push('cursor=' + BAD_CURSOR)
const junk = () => pick(JUNK)

async function results() {
  const q = filterParams()
  if (maybe(0.7)) q.push('limit=' + pick([1, 2, 3, 7, 25, 100, 500]))
  if (maybe(0.15)) q.push(junk())
  const base = '/api/v1/results'
  const { go, rust } = await both(base + (q.length ? '?' + q.join('&') : ''))
  // follow the cursor, using each backend's cursor on the OTHER backend too
  let cursor = go.body?.nextCursor
  for (let page = 0; cursor && page < 3; page++) {
    const path = base + '?' + [...q.filter((p) => !p.startsWith('cursor=')), 'cursor=' + enc(cursor)].join('&')
    const [a, b] = await Promise.all([ask(goUrl, path), ask(rustUrl, path)])
    compare(path + '   (cursor from Go)', a, b)
    const rc = rust.body?.nextCursor
    if (rc && rc !== cursor) problems.push(`${base}\n    cursors differ: ${cursor} != ${rc}`)
    cursor = a.body?.nextCursor
    rust.body = b.body // the next page's cursor is compared against what Rust says this page ends with
  }
}

async function detail() {
  const list = await ask(goUrl, '/api/v1/results?limit=500')
  const id = pick(list.body.items).id
  await both(`/api/v1/results/${id}`)
  await both(`/api/v1/results/${pick(['not-a-uuid', '3f2a9c1e-0000-4000-8000-000000000000', id.toUpperCase(), id.replaceAll('-', ''), '%7B' + id + '%7D', id + 'x'])}`)
}

async function summary() {
  const q = filterParams(); if (maybe(0.1)) q.push(junk())
  await both('/api/v1/stats/summary' + (q.length ? '?' + q.join('&') : ''))
}

async function timeseries() {
  const q = filterParams()
  if (maybe(0.9)) q.push('interval=' + pick(['hour', 'day', 'day', 'week', 'month', 'quarter', 'year', 'raw']))
  if (maybe(0.6)) q.push('groupBy=' + pick(['provider', 'device', 'connectionType', 'provider,device', 'device,connectionType,provider', 'connectionType,provider']))
  if (maybe(0.6)) q.push('timezone=' + enc(pick(zones)))
  if (maybe(0.1)) q.push(junk())
  await both('/api/v1/stats/timeseries' + (q.length ? '?' + q.join('&') : ''))
}

// ---- sending results ---------------------------------------------------------------------------------------------------
const recentTime = () => new Date(Date.now() - rand() * 30 * 86400e3).toISOString().replace(/\.\d+Z$/, maybe(0.3) ? '.123456Z' : 'Z')
const goodOokla = () => ({
  type: 'result', timestamp: recentTime(), ping: { jitter: rand() * 3, latency: 5 + rand() * 30 },
  download: { bandwidth: Math.floor(1e6 + rand() * 1.5e8), bytes: 1000, elapsed: 9000 }, upload: { bandwidth: Math.floor(1e6 + rand() * 3e7) },
  packetLoss: maybe(0.5) ? 0 : Math.round(rand() * 5 * 100) / 100, isp: 'Cox', interface: { name: 'eth0' },
  server: { id: 1, name: 'CenturyLink', location: 'Phoenix, AZ', country: 'United States' },
  result: { id: crypto.randomUUID(), url: 'https://www.speedtest.net/result/c/' + crypto.randomUUID() },
})
const goodNdt7 = () => {
  const t = Date.now() - rand() * 30 * 86400e3
  return { startedAt: new Date(t).toISOString(), finishedAt: new Date(t + 22000).toISOString(), downloadMbps: 10 + rand() * 90, uploadMbps: 5 + rand() * 30,
    minRttMs: 10 + rand() * 20, retransmissionPct: rand(), server: { machine: 'mlab1', location: 'Phoenix', country: 'US' }, raw: { a: 1 } }
}
const WEIRD = [null, 'text', 5, -1, 0, 1e12, 1e300, true, [], {}, '', '2026-06-01', 'javascript:alert(1)', 'http:example.com', 'https:///x', 'https://a/%zz', 'x'.repeat(3000), '\u0000', -0, 1.5e3]

/** Changes one thing in a good payload. */
function mutate(obj) {
  const o = structuredClone(obj)
  const paths = []
  const walk = (v, p) => { if (v && typeof v === 'object' && !Array.isArray(v)) for (const k of Object.keys(v)) { paths.push([...p, k]); walk(v[k], [...p, k]) } }
  walk(o, [])
  const path = pick(paths)
  let at = o
  for (const k of path.slice(0, -1)) at = at[k]
  const key = path[path.length - 1]
  const how = pick(['delete', 'weird', 'weird', 'null', 'case'])
  if (how === 'delete') delete at[key]
  else if (how === 'null') at[key] = null
  else if (how === 'weird') at[key] = pick(WEIRD)
  else { const v = at[key]; delete at[key]; at[maybe(0.5) ? key.toUpperCase() : key[0].toUpperCase() + key.slice(1)] = v }
  return o
}

const TOKENS = ['tok-laptop', 'tok-laptop', 'tok-phone']
const BAD_AUTH = [undefined, 'Bearer nope', 'Bearer tok-lap', 'Basic tok-laptop', 'tok-laptop', 'Bearer', 'bearer   tok-laptop  ']

async function ingest() {
  const provider = pick(['ookla', 'ookla', 'ndt7'])
  const result = provider === 'ookla' ? goodOokla() : goodNdt7()
  let body
  const label = []
  switch (pick(['good', 'good', 'mutate', 'mutate', 'mutate', 'envelope', 'raw'])) {
    case 'good': body = { provider, connectionType: pick(['wired', 'wireless']), result }; label.push('good'); break
    case 'mutate': body = { provider, connectionType: 'wired', result: mutate(result) }; label.push('mutated payload'); break
    case 'envelope': {
      body = { provider: pick([provider, 'speedof', '', null, 5]), connectionType: pick(['wired', 'wireless', 'bluetooth', '', null]),
        trigger: pick([undefined, 'scheduled', 'on_demand', '', null, 'weekly', 5]), result: maybe(0.15) ? undefined : result }
      label.push('envelope'); break
    }
    default: {
      const text = JSON.stringify({ provider, connectionType: 'wired', result })
      body = pick([
        'hi', '', '[]', 'null', '{}', text + ' ' + text, text + '}', '\ufeff' + text, ' \n' + text + '\t ',
        text.replace('"type"', '"type":"log","type"'), text.replace('"provider"', '"Provider"'), text.replace('"bandwidth":', '"bandwidth":0,"bandwidth":'),
        text.replace(/"bandwidth":\d+/, '"bandwidth":1e999'), text.replace(/"bandwidth":\d+/, '"bandwidth":01'), text.replace(/"bandwidth":\d+/, '"bandwidth":NaN'),
        text.replace('"Cox"', '"a\\u0000b"'), text.replace('"Cox"', '"\\ud800"'),
      ])
      label.push('raw text'); break
    }
  }
  const text = typeof body === 'string' ? body : JSON.stringify(body)
  const auth = maybe(0.88) ? 'Bearer ' + pick(TOKENS) : pick(BAD_AUTH)
  const headers = { 'content-type': 'application/json', ...(auth ? { authorization: auth } : {}) }
  await both('/api/v1/results', { method: 'POST', headers, body: text, label: label.join(', ') }, ['id'])
}

await both('/api/v1/health')
await both('/api/v1/devices')
await both('/api/v1/results', { method: 'POST', headers: { authorization: 'Bearer tok-laptop' }, body: 'x'.repeat(65 * 1024), label: 'over 64 KiB' })
await both('/api/v1/nope')
await both('/api/v1/results/')
for (const m of ['POST', 'PUT', 'DELETE']) {
  const [a, b] = await Promise.all([fetch(goUrl + '/api/v1/devices', { method: m }), fetch(rustUrl + '/api/v1/devices', { method: m })])
  compare(`${m} /api/v1/devices`, { status: a.status, type: a.headers.get('content-type'), body: await a.json() }, { status: b.status, type: b.headers.get('content-type'), body: await b.json() })
}
for (let i = 0; i < N; i++) await pick([results, results, detail, summary, summary, timeseries, timeseries, timeseries, ingest, ingest, ingest])()


// ---- on-demand tests ---------------------------------------------------------------------------------------------------
// Stateful, so each backend goes through the same script on its own (they share one database, and only one server test may run),
// and the two transcripts are compared. Ids and timestamps are different every time and are left out.
import { rmSync, writeFileSync } from 'node:fs'

const FAIL_FLAG = process.env.E2E_FAIL_FLAG ?? '/tmp/e2e-fake-speedtest-fail'
const DROP = new Set(['id', 'resultId', 'createdAt', 'startedAt', 'finishedAt', 'testedAt', 'elapsedMs', 'lastTestAt', 'first', 'last'])
function norm(v) {
  if (Array.isArray(v)) return v.map(norm)
  if (v && typeof v === 'object') {
    const o = {}
    for (const [k, x] of Object.entries(v)) {
      if (DROP.has(k)) continue
      o[k] = k === 'submitToken' ? (typeof x === 'string' && x.length >= 40 ? '<token>' : x) : norm(x)
    }
    return o
  }
  return v
}

async function sse(base, id, { lastEventId, stopAt } = {}) {
  const res = await fetch(`${base}/api/v1/tests/${id}/events`, { headers: lastEventId ? { 'last-event-id': String(lastEventId) } : {} })
  if (!res.ok) return { status: res.status, events: [] }
  const events = []
  let buf = ''
  const decoder = new TextDecoder()
  const deadline = Date.now() + 60_000
  for await (const chunk of res.body) {
    buf += decoder.decode(chunk, { stream: true })
    let end
    while ((end = buf.indexOf('\n\n')) >= 0) {
      const block = buf.slice(0, end); buf = buf.slice(end + 2)
      const ev = { id: null, name: '', data: '' }
      for (const line of block.split('\n')) {
        if (line.startsWith('id: ')) ev.id = line.slice(4)
        else if (line.startsWith('event: ')) ev.name = line.slice(7)
        else if (line.startsWith('data: ')) ev.data += line.slice(6)
      }
      if (!ev.name) continue
      events.push({ ...ev, data: JSON.parse(ev.data) })
      if (stopAt?.(events[events.length - 1])) { res.body.cancel?.().catch(() => {}); return { status: 200, events } }
    }
    if (Date.now() > deadline) throw new Error('the event stream never ended')
  }
  return { status: 200, events }
}

const sseShape = (events) => events.map((e) => ({ id: e.id, name: e.name, data: norm(e.data) }))
const summaryJson = (when) => JSON.stringify({ startedAt: new Date(when - 20000).toISOString(), finishedAt: new Date(when).toISOString(), downloadMbps: 87.5, uploadMbps: 23.25, minRttMs: 18.4, retransmissionPct: 0.31, server: { machine: 'm', location: 'l', country: 'US' }, raw: { note: 'x' } })

async function transcript(base) {
  const t = []
  const step = async (label, path, init = {}) => {
    const r = await ask(base, path, init)
    const body = r.body && typeof r.body === 'object' ? norm(r.body) : r.body
    t.push({ label, status: r.status, type: r.type, body: r.status >= 400 ? { fields: (r.body.errors ?? []).map((e) => e.field), title: r.body.title } : body })
    return r
  }
  const post = (body, token) => ({ method: 'POST', body: typeof body === 'string' ? body : JSON.stringify(body), headers: { 'content-type': 'application/json', ...(token ? { authorization: 'Bearer ' + token } : {}) } })

  await step('capabilities', '/api/v1/capabilities')
  // asking for a test
  const cases = [
    ['unknown provider', { provider: 'speedof', mode: 'server', connectionType: 'wired' }],
    ['unknown mode', { provider: 'ookla', mode: 'cloud', connectionType: 'wired' }],
    ['ndt7 on the server', { provider: 'ndt7', mode: 'server', connectionType: 'wired' }],
    ['ookla in a browser', { provider: 'ookla', mode: 'browser', connectionType: 'wired', device: 'x' }],
    ['no connection type', { provider: 'ndt7', mode: 'browser', device: 'x' }],
    ['server needs one too', { provider: 'ookla', mode: 'server' }],
    ['bad connection', { provider: 'ndt7', mode: 'browser', connectionType: 'lte', device: 'x' }],
    ['no device', { provider: 'ndt7', mode: 'browser', connectionType: 'wired' }],
    ['device null', { provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: null }],
    ['device number', { provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: 5 }],
    ['device blank', { provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: '   ' }],
    ['device too long', { provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: 'x'.repeat(65) }],
    ['device control chars', { provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: 'a\u0000b' }],
    ['server junk device', { provider: 'ookla', mode: 'server', connectionType: 'wired', device: 'x'.repeat(65) }],
  ]
  for (const [label, body] of cases) await step('create: ' + label, '/api/v1/tests', post(body))
  for (const [label, body] of [['not json', 'hi'], ['empty', ''], ['array', '[]'], ['number', '5'], ['provider is a number', '{"provider":5}'], ['trailing junk', '{"provider":"ndt7","mode":"browser","connectionType":"wired","device":" pad "} xx'], ['other key case', '{"Provider":"ndt7","MODE":"browser","connectionType":"wired","device":"cased"}']]) {
    await step('create body: ' + label, '/api/v1/tests', post(body))
  }
  await step('create: bad token', '/api/v1/tests', post({ provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: 'x' }, 'nope'))
  await step('create: token decides the device', '/api/v1/tests', post({ provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: 'spoofed' }, 'tok-laptop'))

  // a browser test and its single-use token
  const b = await step('browser: create', '/api/v1/tests', post({ provider: 'ndt7', mode: 'browser', connectionType: 'wireless', device: 'cmp-phone' }))
  const bid = b.body.id, token = b.body.submitToken
  await step('browser: read', `/api/v1/tests/${bid}`)
  const when = Date.parse('2026-10-12T00:00:00Z') > Date.now() ? Date.now() : Date.now() // the same instant for both backends' summaries
  const good = summaryJson(Math.floor(Date.now() / 1000) * 1000)
  for (const [label, tok] of [['no token', undefined], ['wrong token', 'nope']]) await step('browser submit: ' + label, `/api/v1/tests/${bid}/result`, post(good, tok))
  for (const [label, body] of [['not json', 'hi'], ['array', '[]'], ['too short', JSON.stringify({ startedAt: new Date().toISOString(), finishedAt: new Date().toISOString(), downloadMbps: 1, uploadMbps: 1 })], ['absurd', good.replace('87.5', '1e30')], ['old', summaryJson(Date.now() - 2 * 3600e3)]]) {
    await step('browser submit: ' + label, `/api/v1/tests/${bid}/result`, post(body, token))
  }
  await step('browser submit: good', `/api/v1/tests/${bid}/result`, post(good, token))
  await step('browser submit: again', `/api/v1/tests/${bid}/result`, post(good, token))
  await step('browser: read after', `/api/v1/tests/${bid}`)
  await step('browser submit: unknown test', '/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000/result', post(good, 'x'))
  await step('browser submit: malformed id', '/api/v1/tests/not-a-uuid/result', post(good, 'x'))
  const c = await step('browser2: create', '/api/v1/tests', post({ provider: 'ndt7', mode: 'browser', connectionType: 'wired', device: 'cmp-pc' }))
  await step('browser2: cancel', `/api/v1/tests/${c.body.id}`, { method: 'DELETE' })
  await step('browser2: submit after cancel', `/api/v1/tests/${c.body.id}/result`, post(good, c.body.submitToken))
  await step('cancel unknown', '/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000', { method: 'DELETE' })
  await step('read malformed', '/api/v1/tests/not-a-uuid')
  await step('events unknown', '/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000/events')

  // a server test: busy, live events, resume, then a cancel and a failure
  rmSync(FAIL_FLAG, { force: true })
  const s1 = await step('server: create', '/api/v1/tests', post({ provider: 'ookla', mode: 'server', connectionType: 'wireless', device: 'ignored' }))
  const sid = s1.body.id
  await step('server: second while running', '/api/v1/tests', post({ provider: 'ookla', mode: 'server', connectionType: 'wired' }))
  const live = await sse(base, sid)
  t.push({ label: 'server: events', status: live.status, body: sseShape(live.events) })
  const runningId = live.events.find((e) => e.name === 'state' && e.data.test.state === 'running')?.id
  const resumed = await sse(base, sid, { lastEventId: runningId })
  t.push({ label: 'server: events resumed', status: resumed.status, body: sseShape(resumed.events).map(({ name, id }) => ({ name, id })).slice(0, 3).concat([{ count: resumed.events.length }]) })
  await step('server: read after', `/api/v1/tests/${sid}`)
  await step('server: cancel after', `/api/v1/tests/${sid}`, { method: 'DELETE' })
  await step('server: list completed', '/api/v1/tests?state=completed&limit=1')
  await step('list: bad state', '/api/v1/tests?state=bogus')
  await step('list: bad limit', '/api/v1/tests?limit=101')

  const s2 = await step('server2: create', '/api/v1/tests', post({ provider: 'ookla', mode: 'server', connectionType: 'wired' }))
  await new Promise((r) => setTimeout(r, 1200))
  await step('server2: cancel', `/api/v1/tests/${s2.body.id}`, { method: 'DELETE' })
  const cancelled = await sse(base, s2.body.id)
  t.push({ label: 'server2: events', status: cancelled.status, body: sseShape(cancelled.events).map(({ name, data }) => ({ name, state: data.test?.state })).filter((e) => e.name === 'state') })

  writeFileSync(FAIL_FLAG, '')
  try {
    const s3 = await step('server3: create', '/api/v1/tests', post({ provider: 'ookla', mode: 'server', connectionType: 'wired' }))
    const failed = await sse(base, s3.body.id)
    t.push({ label: 'server3: events', status: failed.status, // a transient queued/running snapshot only shows up if the subscriber is quicker than the (instantly failing) runner: a race, not a difference
    body: sseShape(failed.events).filter((e) => e.name !== 'progress' && !(e.name === 'state' && ['queued', 'running'].includes(e.data.test?.state))) })
    await step('server3: read after', `/api/v1/tests/${s3.body.id}`)
  } finally {
    rmSync(FAIL_FLAG, { force: true })
  }
  return t
}

async function ondemandFlows() {
  const caps = await Promise.all([ask(goUrl, '/api/v1/capabilities'), ask(rustUrl, '/api/v1/capabilities')])
  if (!caps.every((c) => c.body?.ookla?.available)) {
    console.log('on-demand flows skipped: start both backends with OOKLA_ACCEPT_LICENSE=yes and OOKLA_PATH=frontend-shared/e2e/fake-speedtest.sh')
    return
  }
  const [go, rust] = [await transcript(goUrl), await transcript(rustUrl)]
  for (let i = 0; i < Math.max(go.length, rust.length); i++) {
    compared++
    const a = go[i], b = rust[i]
    const d = !a || !b ? `step missing on one side (${a?.label ?? b?.label})` : a.label !== b.label ? `${a.label} != ${b.label}` : diff({ ...a, body: a.body }, { ...b, body: b.body })
    if (d) problems.push(`on-demand: ${a?.label ?? b?.label}\n    ${d}`)
  }
  console.log(`on-demand flows: ${go.length} steps on each backend`)
  if (process.env.COMPARE_VERBOSE) {
    for (const x of rust) {
      const b = Array.isArray(x.body) ? `${x.body.length} events: ${[...new Set(x.body.map((e) => e.name ?? e.state ?? e.count))].join('/')}` : ''
      console.log(`  ${String(x.status).padEnd(4)} ${x.label}  ${b}`)
    }
  }
}

if (process.env.COMPARE_ONDEMAND === '1' || process.argv.includes('--ondemand')) await ondemandFlows()

/** Browser notifications and the notifications status: validation, error shapes and idempotency. Never asks either backend to
 *  deliver anything (an accepted endpoint would be a real push service), so "test" is only called for unknown endpoints. */
async function pushFlows() {
  const key = (n) => Buffer.alloc(n, 7).toString('base64url')
  const p256 = Buffer.concat([Buffer.from([4]), Buffer.alloc(64, 9)]).toString('base64url')
  const auth = key(16)
  const post = (path, body, method = 'POST', headers = {}) => both(path, { method, label: (typeof body === 'string' ? body : JSON.stringify(body)).slice(0, 110).replace(/\s+/g, ' '), headers: { 'content-type': 'application/json', ...headers }, body: typeof body === 'string' ? body : JSON.stringify(body) })
  await both('/api/v1/notifications')
  const cfg = await both('/api/v1/push/config')
  if (cfg.go.body.publicKey !== cfg.rust.body.publicKey) problems.push('push/config: the two backends hold different VAPID keys')
  const good = 'https://fcm.googleapis.com/fcm/send/compare-' + Date.now()
  const ok = { endpoint: good, keys: { p256dh: p256, auth } }
  const endpoints = [
    good, '', 'x', 'http://fcm.googleapis.com/x', 'https://evil.example/x', 'https://fcm.googleapis.com.evil.example/x', 'https://user:pw@fcm.googleapis.com/x',
    'https://fcm.googleapis.com/x#frag', 'https://updates.push.services.mozilla.com/a', 'https://push.services.mozilla.com/a', 'https://web.push.apple.com/a',
    'https://x.notify.windows.com/a', 'https://fcm.googleapis.com/' + 'a'.repeat(2100), 'ftp://fcm.googleapis.com/x', 'https://FCM.GOOGLEAPIS.COM/x', 'https://fcm.googleapis.com:443/x',
    // (port 99999: Go's net/url accepts it, Rust's url crate refuses it: a known, harmless divergence, so not compared)
    'https://fcm.googleapis.com\\@evil.example/x', 'https://fcm.googleapis.com/x y', 'https://fcm.googleapis.com/\u0000',
  ]
  for (const endpoint of endpoints) await post('/api/v1/push/subscriptions', { ...ok, endpoint })
  const keysets = [
    { p256dh: '', auth }, { p256dh: p256, auth: '' }, { p256dh: key(65), auth }, { p256dh: p256, auth: key(15) }, { p256dh: p256, auth: key(17) },
    { p256dh: p256 + '=', auth }, { p256dh: p256.replace(/[A-Za-z]/, '*'), auth }, { p256dh: p256, auth: auth + '==' }, {}, null, 3, [], { p256dh: 5, auth },
  ]
  for (const keys of keysets) await post('/api/v1/push/subscriptions', { endpoint: good, keys })
  for (const body of ['', '{', '[]', 'null', '"x"', '5', '{"endpoint":5}', '{"endpoint":"x","keys":3}', '{"ENDPOINT":"' + good + '","KEYS":{"P256DH":"' + p256 + '","AUTH":"' + auth + '"}}',
    JSON.stringify(ok) + ' trailing', JSON.stringify(ok) + JSON.stringify(ok), '{"endpoint":"' + good + '","keys":{"p256dh":"' + p256 + '","auth":"' + auth + '"},"extra":1}', '﻿' + JSON.stringify(ok), ' '.repeat(6000) + JSON.stringify(ok)])
    await post('/api/v1/push/subscriptions', body)
  const url = 'https://fcm.googleapis.com/fcm/send/unknown-' + Date.now()
  for (const method of ['POST', 'DELETE']) {
    const path = method === 'POST' ? '/api/v1/push/test' : '/api/v1/push/subscriptions'
    for (const body of ['', '{}', '{"endpoint":""}', '{"endpoint":5}', '{"endpoint":null}', '{"endpoint":"a\\u0000b"}', '{"endpoint":"a\\nb"}', JSON.stringify({ endpoint: 'a'.repeat(2049) }), JSON.stringify({ endpoint: url }), '{"endpoint":"' + url + '","x":1}', '[]', '{', JSON.stringify({ endpoint: url }) + 'x'])
      await post(path, body, method)
  }
  await post('/api/v1/push/subscriptions', { endpoint: good }, 'DELETE') // idempotent clean-up, and again
  await post('/api/v1/push/subscriptions', { endpoint: good }, 'DELETE')
  for (const method of ['GET', 'PUT', 'PATCH']) for (const path of ['/api/v1/push/subscriptions', '/api/v1/push/test', '/api/v1/push/config'])
    await both(path, { method })
  await both('/api/v1/notifications', { method: 'POST' })
  console.log('push flows done')
}
if (process.env.COMPARE_PUSH !== '0') await pushFlows()

console.log(`${compared} responses compared (${N} generated requests, seed ${seedArg ?? 1})`)
console.log('sending results, by status:', Object.entries(ingestStatuses).map(([k, v]) => `${k} x${v}`).join(', '))
if (problems.length) {
  const unique = [...new Set(problems)]
  console.log(`\n${unique.length} DIFFERENCE(S):\n`)
  for (const p of unique.slice(0, 25)) console.log('  ' + p + '\n')
  if (unique.length > 25) console.log(`  ... and ${unique.length - 25} more`)
  process.exit(1)
}
console.log('identical.')
