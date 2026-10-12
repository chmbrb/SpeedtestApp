import { describe, expect, it } from 'vitest'
import { notificationOptions, parsePush, safeUrl } from '../lib/pushMessage'
import { isIOS, pushSupport, sameKey, urlBase64ToBytes, type Env } from '../lib/platform'
import { explain } from '../lib/explain'
import { ApiError } from '../api/client'

const ORIGIN = 'https://192.168.0.7:8443'

describe('safeUrl', () => {
  it.each([
    ['/', '/'],
    ['/?device=server#alerts', '/?device=server#alerts'],
    ['/settings', '/settings'],
    [`${ORIGIN}/results?x=1`, '/results?x=1'],
  ])('keeps %s on this site', (input, want) => expect(safeUrl(input, ORIGIN)).toBe(want))

  it.each([
    'https://evil.example/phish',
    'http://192.168.0.7:8443/', // same host, different scheme: another origin
    '//evil.example/x',
    'javascript:alert(1)',
    'data:text/html,hi',
    '',
    undefined,
    null,
    42,
    {},
  ])('sends %s to the home page', (input) => expect(safeUrl(input, ORIGIN)).toBe('/'))
})

describe('parsePush', () => {
  it('reads the server message', () => {
    const p = parsePush(JSON.stringify({ title: 'Downloads are slow', body: 'server is at 40% of normal', url: '/?device=server' }), ORIGIN)
    expect(p).toEqual({ title: 'Downloads are slow', body: 'server is at 40% of normal', url: '/?device=server', test: false })
  })
  it('marks test messages', () => {
    expect(parsePush('{"title":"Notifications are on","body":"hi","test":true}', ORIGIN).test).toBe(true)
    expect(parsePush('{"title":"x","body":"y","test":"yes"}', ORIGIN).test).toBe(false)
  })
  it.each([[null], [undefined], [''], ['null'], ['[]'], ['42'], ['{}'], ['{"title":5,"body":{}}'], ['{"title":"  ","body":""}']])(
    'still shows something for %j',
    (raw) => {
      const p = parsePush(raw as string | null | undefined, ORIGIN)
      expect(p.title).toBe('Speedtest')
      expect(p.body.length).toBeGreaterThan(0)
      expect(p.url).toBe('/')
    },
  )
  it('shows a plain-text message as it is', () => {
    expect(parsePush('Link is down', ORIGIN)).toMatchObject({ title: 'Speedtest', body: 'Link is down' })
  })
  it('limits very long text', () => {
    const p = parsePush(JSON.stringify({ title: 't'.repeat(500), body: 'b'.repeat(5000) }), ORIGIN)
    expect(p.title).toHaveLength(100)
    expect(p.body).toHaveLength(300)
  })
  it('refuses to open another site from a notification', () => {
    expect(parsePush('{"title":"x","body":"y","url":"https://evil.example"}', ORIGIN).url).toBe('/')
  })
})

describe('notificationOptions', () => {
  it('replaces an earlier notification about the same problem, and still alerts', () => {
    const a = notificationOptions({ title: 'Downloads are slow', body: '1', url: '/', test: false })
    const b = notificationOptions({ title: 'Downloads are slow', body: '2', url: '/', test: false })
    const c = notificationOptions({ title: 'Packet loss', body: '3', url: '/', test: false })
    expect(a.tag).toBe(b.tag)
    expect(a.tag).not.toBe(c.tag)
    expect(a.renotify).toBe(true)
    expect(a.data).toEqual({ url: '/' })
  })
  it('keeps the test notification apart from real ones', () => {
    expect(notificationOptions({ title: 'Downloads are slow', body: '', url: '/', test: true }).tag).toBe('speedtest-test')
  })
})

const ENV: Env = { secureContext: true, hasServiceWorker: true, hasPushManager: true, hasNotification: true, ios: false, standalone: false }

describe('pushSupport', () => {
  it('works in a normal secure browser', () => expect(pushSupport(ENV)).toEqual({ kind: 'ok' }))
  it('needs https', () => expect(pushSupport({ ...ENV, secureContext: false }).kind).toBe('insecure'))
  it('on iPhone, needs the app on the Home Screen first', () => {
    expect(pushSupport({ ...ENV, ios: true, hasPushManager: false }).kind).toBe('install-first')
    expect(pushSupport({ ...ENV, ios: true, standalone: true })).toEqual({ kind: 'ok' })
  })
  it('says unsupported when a piece is missing', () => {
    for (const missing of ['hasServiceWorker', 'hasPushManager', 'hasNotification'] as const) {
      expect(pushSupport({ ...ENV, [missing]: false }).kind).toBe('unsupported')
    }
  })
  it('puts "use https" before everything else', () => {
    expect(pushSupport({ ...ENV, secureContext: false, ios: true, hasPushManager: false }).kind).toBe('insecure')
  })
})

describe('isIOS', () => {
  it('recognises iPhones and iPads, including iPads posing as Macs', () => {
    expect(isIOS('Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)', 'iPhone', 5)).toBe(true)
    expect(isIOS('Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)', 'iPad', 5)).toBe(true)
    expect(isIOS('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', 'MacIntel', 5)).toBe(true)
  })
  it('does not mistake a Mac or an Android phone for one', () => {
    expect(isIOS('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', 'MacIntel', 0)).toBe(false)
    expect(isIOS('Mozilla/5.0 (Linux; Android 14; Pixel 8)', 'Linux armv8l', 5)).toBe(false)
  })
})

describe('server key handling', () => {
  // A real VAPID public key: 65 bytes, base64url without padding.
  const KEY = 'BNygsvHA5SgGVIq0ljMJcPez0ghwlGpGYSkuJwIIA28eDNaug2Rs7RRVaarU1mOoPpv5i1vNDyLUAl5MkXUxKpc'
  it('decodes base64url to the 65 bytes of an uncompressed P-256 point', () => {
    const b = urlBase64ToBytes(KEY)
    expect(b).toHaveLength(65)
    expect(b[0]).toBe(4)
  })
  it('handles padding and the url-safe alphabet', () => {
    expect(Array.from(urlBase64ToBytes('-_8'))).toEqual([251, 255])
    expect(Array.from(urlBase64ToBytes('-_8='))).toEqual([251, 255])
  })
  it('recognises a subscription made with the same key, and one made with another', () => {
    const b = urlBase64ToBytes(KEY)
    expect(sameKey(b.buffer, b)).toBe(true)
    const other = new Uint8Array(b)
    other[10] = (other[10] ?? 0) ^ 1
    expect(sameKey(other.buffer, b)).toBe(false)
    expect(sameKey(b.buffer.slice(0, 10), b)).toBe(false)
    expect(sameKey(null, b)).toBe(false)
    expect(sameKey(undefined, b)).toBe(false)
  })
})

describe('explain', () => {
  it('turns server and browser failures into plain advice', () => {
    expect(explain(new ApiError(409, 'x'))).toMatch(/Too many devices/)
    expect(explain(new ApiError(429, 'x'))).toMatch(/Wait a minute/)
    expect(explain(new ApiError(404, 'x'))).toMatch(/off and on again/)
    expect(explain(new ApiError(400, 'endpoint is not a known browser push service'))).toBe('endpoint is not a known browser push service')
    const abort = Object.assign(new Error('push service error'), { name: 'AbortError' })
    expect(explain(abort)).toMatch(/could not reach its notification service/)
    const blocked = Object.assign(new Error('x'), { name: 'NotAllowedError' })
    expect(explain(blocked)).toMatch(/blocked for this site/)
    expect(explain('???')).toMatch(/Something went wrong/)
  })
})
