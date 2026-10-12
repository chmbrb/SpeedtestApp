import { WebSocketServer, type WebSocket } from 'ws'
import type { AddressInfo } from 'node:net'
import type { BrowserContext } from '@playwright/test'

/**
 * A tiny stand-in for M-Lab's NDT7 service, so the browser tests exercise the real client library and its web workers
 * without touching the internet. It speaks just enough of the ndt7 protocol: a download that streams binary data and
 * then reports its TCP counters, and an upload that counts what it receives and reports it back.
 */
export async function startMlabMock() {
  const wss = new WebSocketServer({ port: 0, host: '127.0.0.1', handleProtocols: () => 'net.measurementlab.ndt.v7' })
  await new Promise<void>((r) => wss.once('listening', () => r()))
  const port = (wss.address() as AddressInfo).port

  wss.on('connection', (ws: WebSocket, req) => {
    const chunk = Buffer.alloc(64 * 1024, 1)
    if (req.url?.includes('/download')) {
      // 64 KB every 20 ms for 2 s = about 25 Mbit/s
      const started = Date.now()
      const timer = setInterval(() => {
        if (Date.now() - started >= 2000) {
          clearInterval(timer)
          ws.send(JSON.stringify({ TCPInfo: { BytesSent: 8_000_000, BytesRetrans: 24_000, MinRTT: 15_000, ElapsedTime: 2_000_000 } }))
          ws.close()
          return
        }
        ws.send(chunk, { binary: true })
      }, 20)
      ws.on('close', () => clearInterval(timer))
    } else if (req.url?.includes('/upload')) {
      let total = 0
      const started = process.hrtime.bigint()
      ws.on('message', (data, isBinary) => { if (isBinary) total += (data as Buffer).length })
      const timer = setInterval(() => {
        const elapsedUs = Number((process.hrtime.bigint() - started) / 1000n)
        if (ws.readyState === ws.OPEN) ws.send(JSON.stringify({ TCPInfo: { BytesReceived: total, ElapsedTime: elapsedUs } }))
      }, 250)
      ws.on('close', () => clearInterval(timer))
    }
  })

  return {
    port,
    /** Make the browser's locate request return this mock instead of the real M-Lab service. */
    async install(context: BrowserContext) {
      await context.route('https://locate.measurementlab.net/**', (route) => route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: { 'access-control-allow-origin': '*' },
        body: JSON.stringify({
          results: [{
            machine: 'mlab-mock-1',
            location: { city: 'Testville', country: 'ZZ' },
            urls: {
              'wss:///ndt/v7/download': `ws://127.0.0.1:${port}/ndt/v7/download`,
              'wss:///ndt/v7/upload': `ws://127.0.0.1:${port}/ndt/v7/upload`,
            },
          }],
        }),
      }))
    },
    close: () => new Promise<void>((r) => wss.close(() => r())),
  }
}
