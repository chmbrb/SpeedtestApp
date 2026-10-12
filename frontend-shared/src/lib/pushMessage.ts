// What the service worker does with a push message. Kept free of browser globals so it can be unit tested.

export interface PushPayload {
  title: string
  body: string
  url: string
  test: boolean
}

const FALLBACK: PushPayload = { title: 'Speedtest', body: 'Something needs your attention. Open the app to see what.', url: '/', test: false }

/** Only paths on this site can be opened from a notification. Anything else (another site, a script URL) becomes the home page. */
export function safeUrl(url: unknown, origin: string): string {
  if (typeof url !== 'string' || url === '') return '/'
  try {
    const u = new URL(url, origin)
    return u.origin === origin ? u.pathname + u.search + u.hash : '/'
  } catch {
    return '/'
  }
}

const text = (v: unknown, max: number): string | null => (typeof v === 'string' && v.trim() !== '' ? v.trim().slice(0, max) : null)

/**
 * Reads a push message. It never throws and always yields something to show: a browser that receives a push and shows nothing
 * may withdraw the permission, so a broken or empty message still becomes a plain notification.
 */
export function parsePush(raw: string | null | undefined, origin: string): PushPayload {
  if (!raw) return FALLBACK
  let data: unknown
  try {
    data = JSON.parse(raw)
  } catch {
    // Not JSON: treat the whole text as the message.
    return { ...FALLBACK, body: text(raw, 300) ?? FALLBACK.body }
  }
  if (typeof data !== 'object' || data === null) return FALLBACK
  const d = data as Record<string, unknown>
  return {
    title: text(d.title, 100) ?? FALLBACK.title,
    body: text(d.body, 300) ?? FALLBACK.body,
    url: safeUrl(d.url, origin),
    test: d.test === true,
  }
}

/** Same title shows once (a reminder replaces the earlier message and alerts again); different problems stay separate. */
export function notificationOptions(p: PushPayload): NotificationOptions & { renotify: boolean } {
  return {
    body: p.body,
    icon: '/icon-192.png',
    badge: '/icon-192.png',
    tag: p.test ? 'speedtest-test' : `speedtest:${p.title}`,
    renotify: true,
    data: { url: p.url },
  }
}
