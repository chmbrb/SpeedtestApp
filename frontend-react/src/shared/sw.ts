/// <reference lib="webworker" />
// The app's service worker: opens the app offline, keeps the last data it saw, and shows push notifications.
import { clientsClaim } from 'workbox-core'
import { ExpirationPlugin } from 'workbox-expiration'
import { CacheableResponsePlugin } from 'workbox-cacheable-response'
import { cleanupOutdatedCaches, createHandlerBoundToURL, precacheAndRoute } from 'workbox-precaching'
import { NavigationRoute, registerRoute } from 'workbox-routing'
import { NetworkFirst } from 'workbox-strategies'
import { notificationOptions, parsePush, safeUrl } from './lib/pushMessage'

declare const self: ServiceWorkerGlobalScope & { __WB_MANIFEST: Array<string | { url: string; revision: string | null }> }

self.skipWaiting()
clientsClaim()
cleanupOutdatedCaches()
precacheAndRoute(self.__WB_MANIFEST)

// App shell works offline; /api is never answered by the precache. /ca.crt is the HTTPS proxy's root certificate (a download
// for devices to trust, served by Caddy), so it must reach the network instead of being answered with the app.
registerRoute(new NavigationRoute(createHandlerBoundToURL('/index.html'), { denylist: [/^\/api\//, /^\/ca\.crt$/] }))

// Show the last data seen on this device when the network is down. Never the live parts: the health check, and anything
// under /tests (the event stream must reach the network, and "a test is running" must not be answered from an old cache),
// nothing under /push (subscriptions and test notifications are actions, not data), and nothing under /auth (whether this
// browser is signed in is only ever what the server says now; the cache 'api-v1' is emptied when the session ends, see
// clearApiCache in state/auth.ts).
registerRoute(
  ({ url }) =>
    url.pathname.startsWith('/api/v1/') &&
    !url.pathname.endsWith('/health') &&
    !url.pathname.startsWith('/api/v1/tests') &&
    !url.pathname.startsWith('/api/v1/auth/') &&
    !url.pathname.startsWith('/api/v1/push/subscriptions') &&
    !url.pathname.startsWith('/api/v1/push/test'),
  new NetworkFirst({
    cacheName: 'api-v1',
    networkTimeoutSeconds: 5,
    plugins: [
      new ExpirationPlugin({ maxEntries: 60, maxAgeSeconds: 60 * 60 * 24 * 7 }),
      new CacheableResponsePlugin({ statuses: [200] }),
    ],
  }),
)

self.addEventListener('push', (event) => {
  let raw: string | null = null
  try {
    raw = event.data ? event.data.text() : null
  } catch {
    raw = null
  }
  const message = parsePush(raw, self.location.origin)
  // A push must always show something (userVisibleOnly), even when the message could not be read.
  event.waitUntil(self.registration.showNotification(message.title, notificationOptions(message)))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = safeUrl((event.notification.data as { url?: unknown } | null)?.url, self.location.origin)
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const mine = windows.find((w) => new URL(w.url).origin === self.location.origin)
      if (mine) {
        await mine.focus()
        try {
          if (new URL(mine.url).pathname + new URL(mine.url).search !== target) await mine.navigate(target)
        } catch {
          // A window the worker does not control cannot be navigated; it has been brought to the front, which is enough.
        }
        return
      }
      await self.clients.openWindow(target)
    })(),
  )
})

// Browsers can replace a subscription (for example after a long idle period). Register the new one so alerts keep arriving.
self.addEventListener('pushsubscriptionchange', (event) => {
  const change = event as ExtendableEvent & { oldSubscription?: PushSubscription | null }
  change.waitUntil(
    (async () => {
      const key = change.oldSubscription?.options.applicationServerKey
      if (!key) return
      const sub = await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
      await fetch('/api/v1/push/subscriptions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(sub.toJSON()),
      })
      if (change.oldSubscription) {
        await fetch('/api/v1/push/subscriptions', {
          method: 'DELETE',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ endpoint: change.oldSubscription.endpoint }),
        })
      }
    })().catch(() => undefined),
  )
})
