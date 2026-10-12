#!/usr/bin/env node
// Sends N unique, valid Ookla results to POST /api/v1/results with C in flight; prints "rps p50 p99 failures".
const [base, token, nArg, cArg] = process.argv.slice(2)
const N = Number(nArg), C = Number(cArg)
let next = 0, failures = 0
const lat = []
const body = (i) => JSON.stringify({ provider: 'ookla', connectionType: 'wired', result: {
  type: 'result', timestamp: new Date(Date.now() - i * 1000).toISOString(), ping: { jitter: 1.2, latency: 12.5 },
  download: { bandwidth: 50_000_000 + i, bytes: 1000, elapsed: 9000 }, upload: { bandwidth: 10_000_000 }, packetLoss: 0, isp: 'Bench',
  interface: { name: 'eth0' }, server: { id: 1, name: 'Bench', location: 'Phoenix, AZ', country: 'United States' },
  result: { id: crypto.randomUUID(), url: 'https://www.speedtest.net/result/c/' + crypto.randomUUID() },
} })
async function worker() {
  for (;;) {
    const i = next++
    if (i >= N) return
    const t = performance.now()
    const res = await fetch(base + '/api/v1/results', { method: 'POST', headers: { 'content-type': 'application/json', authorization: 'Bearer ' + token }, body: body(i) })
    await res.arrayBuffer()
    lat.push(performance.now() - t)
    if (res.status !== 201) failures++
  }
}
const t0 = performance.now()
await Promise.all(Array.from({ length: C }, worker))
const secs = (performance.now() - t0) / 1000
lat.sort((a, b) => a - b)
const q = (p) => lat[Math.min(lat.length - 1, Math.floor(p * lat.length))].toFixed(1)
console.log(`${(N / secs).toFixed(0)} ${q(0.5)} ${q(0.99)} ${failures}`)
