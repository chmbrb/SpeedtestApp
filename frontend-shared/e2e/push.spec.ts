import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

// Notifications and installing, in a real browser. The browser's own push service (Google's, Mozilla's, Apple's) is not
// reachable from a test, so a fake one stands in for it here. Everything else is real: the page, the service worker and the
// backend. Actual delivery and encryption are tested in backend-go/internal/push.

const INSTALL_PROMPT = `
  window.__installPrompts = 0
  window.__fireInstallPrompt = (outcome = 'accepted') => {
    const e = new Event('beforeinstallprompt', { cancelable: true })
    e.prompt = async () => { window.__installPrompts++ }
    e.userChoice = Promise.resolve({ outcome })
    window.dispatchEvent(e)
    return e.defaultPrevented
  }
`

/** Replaces the browser's push subscription calls with a fake that remembers its subscription across reloads. */
const FAKE_PUSH = `
  (() => {
    const b64 = (u8) => btoa(String.fromCharCode(...u8)).replace(/\\+/g, '-').replace(/\\//g, '_').replace(/=+$/, '')
    const unb64 = (s) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0))
    const rand = (n) => crypto.getRandomValues(new Uint8Array(n))
    const load = () => { try { return JSON.parse(localStorage.getItem('fake-push')) } catch { return null } }
    const make = (s) => ({
      endpoint: s.endpoint,
      options: { applicationServerKey: unb64(s.key).buffer },
      toJSON: () => ({ endpoint: s.endpoint, expirationTime: null, keys: { p256dh: s.p256dh, auth: s.auth } }),
      unsubscribe: async () => { localStorage.removeItem('fake-push'); return true },
    })
    PushManager.prototype.getSubscription = async function () { const s = load(); return s ? make(s) : null }
    PushManager.prototype.subscribe = async function (opts) {
      const point = rand(65); point[0] = 4
      const s = { endpoint: 'https://fcm.googleapis.com/fcm/send/e2e-' + b64(rand(12)), p256dh: b64(point), auth: b64(rand(16)), key: b64(new Uint8Array(opts.applicationServerKey)) }
      localStorage.setItem('fake-push', JSON.stringify(s))
      return make(s)
    }
  })()
`

// The old headless mode cannot show notifications at all (and reports every permission as "denied"); the new one behaves like Chrome.
test.use({ channel: 'chromium' })

const section = (page: Page) => page.getByTestId('device-notifications')
const stored = (page: Page) => page.evaluate(() => JSON.parse(localStorage.getItem('fake-push') ?? 'null'))

test.describe('notifications on this device', () => {
  test.use({ permissions: ['notifications'] })
  test.beforeEach(async ({ page }) => { await page.addInitScript(FAKE_PUSH) })

  test('turn on, survive a reload, send a test, turn off', async ({ page, request }) => {
    await page.goto('/')
    await expect(section(page)).toContainText('Get a notification on this device')

    const subscribed = page.waitForRequest((r) => r.url().endsWith('/api/v1/push/subscriptions') && r.method() === 'POST')
    await page.getByTestId('push-enable').click()
    const body = (await subscribed).postDataJSON()
    expect(body.endpoint).toMatch(/^https:\/\/fcm\.googleapis\.com\/fcm\/send\/e2e-/)
    expect(body.keys.p256dh.length).toBeGreaterThan(80)
    await expect(section(page)).toContainText('Notifications are on for this device')
    // when the server has no NTFY_TOPIC, alerts are off and the page says why only the test can arrive
    const alertsOn = (await (await request.get('/api/v1/notifications')).json()).enabled
    if (alertsOn) await expect(section(page)).not.toContainText('Alerts are off on the server')
    else await expect(section(page)).toContainText('Alerts are off on the server')

    // a reload finds the subscription again (and tells the server, in case it lost it)
    const resync = page.waitForRequest((r) => r.url().endsWith('/api/v1/push/subscriptions') && r.method() === 'POST')
    await page.reload()
    await resync
    await expect(page.getByTestId('push-test')).toBeVisible()

    // the test button: a success and a refusal from the browser vendor's service
    await page.route('**/api/v1/push/test', (r) => r.fulfill({ status: 204 }))
    await page.getByTestId('push-test').click()
    await expect(page.getByTestId('push-test-sent')).toBeVisible()
    await page.unroute('**/api/v1/push/test')
    await page.route('**/api/v1/push/test', (r) => r.fulfill({
      status: 502, contentType: 'application/problem+json',
      body: JSON.stringify({ type: 'about:blank', title: 'Bad Gateway', status: 502, detail: 'the push service answered 403 forbidden' }),
    }))
    await page.getByTestId('push-test').click()
    await expect(page.getByTestId('push-error')).toContainText('did not accept the message')
    await page.unroute('**/api/v1/push/test')

    const sub = await stored(page)
    await page.getByTestId('push-disable').click()
    await expect(page.getByTestId('push-enable')).toBeVisible()
    expect(await stored(page)).toBeNull()
    // the server forgot it too: a test for that endpoint is now "not subscribed", with no message sent anywhere
    const res = await request.post('/api/v1/push/test', { data: { endpoint: sub.endpoint } })
    expect(res.status()).toBe(404)
  })

  test('says what to do when the browser or the server refuses', async ({ page }) => {
    await page.route('**/api/v1/push/subscriptions', (r) => r.request().method() === 'POST'
      ? r.fulfill({ status: 409, contentType: 'application/problem+json', body: JSON.stringify({ type: 'about:blank', title: 'Conflict', status: 409, detail: 'x' }) })
      : r.continue())
    await page.goto('/')
    await page.getByTestId('push-enable').click()
    await expect(page.getByTestId('push-error')).toContainText('Too many devices')
    await expect(page.getByTestId('push-enable')).toBeEnabled() // can try again
  })

  test('replaces a subscription made for a different server key', async ({ page }) => {
    // A browser subscribed against another key (for instance the server's database was rebuilt) can never receive anything.
    await page.addInitScript(() => {
      if (!localStorage.getItem('fake-push')) {
        localStorage.setItem('fake-push', JSON.stringify({ endpoint: 'https://fcm.googleapis.com/fcm/send/old', p256dh: 'x', auth: 'y', key: 'AAAA' }))
      }
    })
    await page.goto('/')
    await expect(page.getByTestId('push-test')).toBeVisible()
    const s = await stored(page)
    expect(s.endpoint).not.toBe('https://fcm.googleapis.com/fcm/send/old')
  })
})

test.describe('situations where notifications cannot be turned on', () => {
  test('permission already blocked', async ({ page }) => {
    await page.addInitScript(() => Object.defineProperty(Notification, 'permission', { get: () => 'denied' }))
    await page.goto('/')
    await expect(section(page)).toContainText('blocked for this site')
    await expect(page.getByTestId('push-enable')).toHaveCount(0)
  })

  test('turned off on the server', async ({ page }) => {
    await page.route('**/api/v1/push/config', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '{"enabled":false}' }))
    await page.goto('/')
    await expect(section(page)).toContainText('turned off on the server')
    await expect(page.getByTestId('push-enable')).toHaveCount(0)
  })

  test.describe('with the network failing', () => {
    // Mocked responses can't be intercepted once the service worker is making the requests itself.
    test.use({ serviceWorkers: 'block' })
    test('the server cannot be reached', async ({ page }) => {
    await page.route('**/api/v1/push/config', (r) => r.abort())
    await page.goto('/')
    await expect(section(page)).toContainText('Couldn’t reach the server')
    })
  })

  test('the background worker never starts (a certificate warning was clicked through)', async ({ page }) => {
    // Browsers refuse to register a worker on a connection they do not trust, so "ready" never arrives.
    await page.clock.install()
    await page.addInitScript(() => Object.defineProperty(navigator.serviceWorker, 'ready', { get: () => new Promise(() => {}) }))
    await page.goto('/')
    await expect(section(page)).toContainText('Checking')
    await page.clock.fastForward(9_000)
    await expect(section(page)).toContainText('background service didn’t start')
    await expect(section(page)).toContainText('trust the certificate')
    await expect(page.getByTestId('push-enable')).toHaveCount(0)
  })

  test('iPhone in a browser tab: install first, with the steps', async ({ page }) => {
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'userAgent', { get: () => 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 Version/17.4 Mobile/15E148 Safari/604.1' })
      // Safari does not offer push to pages that are not installed
      // @ts-expect-error removing a browser global for the test
      delete window.PushManager
    })
    await page.goto('/')
    await expect(section(page)).toContainText('Add to Home Screen')
    await expect(page.getByTestId('push-enable')).toHaveCount(0)
    // the banner shows the same how-to, with no Install button (there is no install dialog on iOS)
    await expect(page.getByTestId('install-banner')).toContainText('Add to Home Screen')
    await expect(page.getByTestId('install-app')).toHaveCount(0)
  })
})

test.describe('installing the app', () => {
  test.beforeEach(async ({ page }) => { await page.addInitScript(INSTALL_PROMPT) })

  test('offers the browser’s install dialog once, and goes away after', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('install-banner')).toHaveCount(0) // nothing to offer until the browser says it can
    expect(await page.evaluate(() => (window as any).__fireInstallPrompt())).toBe(true) // the browser's own mini-bar is suppressed
    await expect(page.getByTestId('install-banner')).toContainText('Install Speedtest')
    await page.getByTestId('install-app').click()
    await expect.poll(() => page.evaluate(() => (window as any).__installPrompts)).toBe(1)
    await expect(page.getByTestId('install-banner')).toHaveCount(0)
  })

  test('"Not now" hides it, and it stays hidden after a reload', async ({ page }) => {
    await page.goto('/')
    await page.evaluate(() => (window as any).__fireInstallPrompt())
    await page.getByTestId('install-dismiss').click()
    await expect(page.getByTestId('install-banner')).toHaveCount(0)
    await page.reload()
    await page.evaluate(() => (window as any).__fireInstallPrompt())
    await page.waitForTimeout(300)
    await expect(page.getByTestId('install-banner')).toHaveCount(0)
  })

  test('is not shown once the app is installed', async ({ page }) => {
    await page.goto('/')
    await page.evaluate(() => (window as any).__fireInstallPrompt())
    await expect(page.getByTestId('install-banner')).toBeVisible()
    await page.evaluate(() => window.dispatchEvent(new Event('appinstalled')))
    await expect(page.getByTestId('install-banner')).toHaveCount(0)
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`no accessibility violations with the banner and notification controls in the ${theme} theme`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
      await page.addInitScript(FAKE_PUSH)
      await page.goto('/')
      await page.evaluate(() => (window as any).__fireInstallPrompt())
      await expect(page.getByTestId('install-banner')).toBeVisible()
      await expect(page.getByTestId('push-enable')).toBeVisible()
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
      expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
    })
  }
})

test.describe('the service worker shows a push', () => {
  test.use({ permissions: ['notifications'] })

  /** Delivers pushes straight to the service worker, as the browser's push service would. One session serves the whole test. */
  async function pusher(page: Page, context: import('@playwright/test').BrowserContext) {
    const origin = new URL(page.url()).origin
    const cdp = await context.newCDPSession(page)
    const registration = new Promise<string>((resolve) => {
      cdp.on('ServiceWorker.workerRegistrationUpdated', (e: { registrations: { registrationId: string; scopeURL: string }[] }) => {
        const r = e.registrations.find((x) => x.scopeURL.startsWith(origin))
        if (r) resolve(r.registrationId)
      })
    })
    await cdp.send('ServiceWorker.enable')
    const registrationId = await registration
    return (data: string) => cdp.send('ServiceWorker.deliverPushMessage', { origin, registrationId, data })
  }
  const shown = (page: Page) => page.evaluate(async () => {
    const reg = await navigator.serviceWorker.ready
    return (await reg.getNotifications()).map((n) => ({ title: n.title, body: n.body, tag: n.tag, url: (n.data as { url?: string })?.url }))
  })

  test('an alert, a repeat of it, another alert, and a message that cannot be read', async ({ page, context }) => {
    await page.goto('/')
    await page.evaluate(() => navigator.serviceWorker.ready)
    await page.reload() // now controlled by the worker
    const deliver = await pusher(page, context)

    await deliver(JSON.stringify({ title: 'Downloads are slow', body: 'server (wired) is at 40% of normal', url: '/?device=server' }))
    await expect.poll(() => shown(page)).toEqual([
      { title: 'Downloads are slow', body: 'server (wired) is at 40% of normal', tag: 'speedtest:Downloads are slow', url: '/?device=server' },
    ])

    // a reminder about the same problem replaces the first one instead of piling up
    await deliver(JSON.stringify({ title: 'Downloads are slow', body: 'still slow: 38% of normal', url: '/' }))
    await expect.poll(async () => (await shown(page)).map((n) => n.body)).toEqual(['still slow: 38% of normal'])

    // a different problem is its own notification
    await deliver(JSON.stringify({ title: 'Packet loss', body: 'laptop is losing 12% of packets' }))
    await expect.poll(async () => (await shown(page)).map((n) => n.title).sort()).toEqual(['Downloads are slow', 'Packet loss'])

    // a message that cannot be read, and one that points at another site, still show a safe notification
    await deliver('{not json at all')
    await deliver(JSON.stringify({ title: 'Hello', body: 'x', url: 'https://evil.example/phish' }))
    await expect.poll(async () => (await shown(page)).length).toBe(4)
    const all = await shown(page)
    expect(all.find((n) => n.title === 'Hello')?.url).toBe('/')
    expect(all.some((n) => n.title === 'Speedtest')).toBe(true)
  })
})
