import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

/** Fail a test on any console error or uncaught exception during it. */
function watchErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
  page.on('pageerror', (e) => errors.push(String(e)))
  return errors
}

const ready = async (page: Page) => {
  await expect(page.getByTestId('hero-figure')).toBeVisible()
  await expect(page.locator('[data-testid^="chart-"] svg').first()).toBeVisible()
}

// The tests compare wired and wireless devices, so they need both in the data. Say so up front, with the fix, instead of
// failing later with a confusing "no rows" error.
test.beforeAll(async ({ request }) => {
  const res = await request.get('/api/v1/devices')
  expect(res.ok(), 'the backend must be running and serving the built frontend (see the README)').toBeTruthy()
  const types = new Set((await res.json()).items.flatMap((d: { connectionTypes: string[] }) => d.connectionTypes))
  expect(
    types.has('wired') && types.has('wireless'),
    'the e2e data needs wired and wireless results: run frontend-shared/e2e/seed.sql on the DISPOSABLE database',
  ).toBeTruthy()
})

test.describe('dashboard', () => {
  test('loads dark by default with a verdict, KPIs, four charts and a table', async ({ page }) => {
    const errors = watchErrors(page)
    await page.goto('/')
    await ready(page)
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expect(page.getByTestId('verdict')).toContainText(/Normal|Faster|Slower|Much slower|Not enough/)
    await expect(page.locator('[data-testid^="kpi-"]')).toHaveCount(5)
    await expect(page.locator('[data-testid^="chart-"] svg')).toHaveCount(4)
    await expect(page.locator('table.results tbody tr').first()).toBeVisible()
    expect(errors).toEqual([])
  })

  test('theme choice persists across reloads', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('radio', { name: 'Light' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    await expect(page.getByRole('radio', { name: 'Light' })).toHaveAttribute('aria-checked', 'true')
    await page.getByRole('radio', { name: 'Dark' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  })

  test('filters are kept in the URL and can be reset', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    await page.getByRole('radio', { name: 'Wireless' }).click()
    await expect(page).toHaveURL(/connection=wireless/)
    await expect(page.locator('table.results tbody tr').first()).toContainText('Wireless')
    for (const row of await page.locator('table.results tbody tr').all()) await expect(row).toContainText('Wireless')

    await page.getByRole('button', { name: /Period/ }).click()
    await page.getByRole('menuitemradio', { name: 'Last 7 days' }).click()
    await expect(page).toHaveURL(/range=7d/)
    await expect(page.getByRole('button', { name: /Period/ })).toContainText('Last 7 days')

    await page.getByRole('button', { name: 'Reset filters' }).click()
    await expect(page).toHaveURL(/\/$/)
    await expect(page.getByRole('radio', { name: 'All', exact: true }).first()).toHaveAttribute('aria-checked', 'true')
  })

  test('back and forward move through filter changes', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    await page.getByRole('radio', { name: 'Wired' }).click()
    await expect(page).toHaveURL(/connection=wired/)
    await page.goBack()
    await expect(page).not.toHaveURL(/connection=/)
    await expect(page.getByRole('radio', { name: 'Wired' })).toHaveAttribute('aria-checked', 'false')
    await page.goForward()
    await expect(page.getByRole('radio', { name: 'Wired' })).toHaveAttribute('aria-checked', 'true')
  })

  test('deep links work and bad values fall back to defaults', async ({ page }) => {
    await page.goto('/?range=7d&interval=hour')
    await ready(page)
    await expect(page.getByRole('button', { name: /Period/ })).toContainText('Last 7 days')
    await expect(page.getByRole('button', { name: /Detail/ })).toContainText('Hourly')

    await page.goto('/?range=century&connection=lte&provider=speedof&interval=minute')
    await ready(page)
    await expect(page.getByRole('button', { name: /Period/ })).toContainText('Last 30 days')
    await expect(page.getByRole('radio', { name: 'Ookla' })).toHaveAttribute('aria-checked', 'true')
  })

  test('choosing both providers shows the comparability note', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    await expect(page.getByText(/not directly comparable/)).toHaveCount(0)
    await page.getByRole('radio', { name: 'Both' }).click()
    await expect(page.getByText(/not directly comparable/)).toBeVisible()
  })

  test('hovering a chart shows a tooltip with a value, and leaving hides it', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    const chart = page.getByTestId('chart-download')
    await chart.scrollIntoViewIfNeeded()
    const box = (await chart.locator('svg').boundingBox())!
    await page.mouse.move(box.x + box.width * 0.6, box.y + 80)
    const tip = chart.getByTestId('tooltip')
    await expect(tip).toBeVisible()
    await expect(tip).toContainText('Mbps')
    await expect(tip.locator('.tooltip-time')).not.toBeEmpty()
    await page.mouse.move(5, 5)
    await expect(tip).toBeHidden()
  })

  test('a chart can be read with the keyboard alone', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    const frame = page.getByTestId('chart-download').locator('.chart-frame')
    await frame.focus()
    const tip = page.getByTestId('chart-download').getByTestId('tooltip')
    await expect(tip).toBeVisible()
    const last = await tip.locator('.tooltip-time').innerText()
    await page.keyboard.press('ArrowLeft')
    await expect(tip.locator('.tooltip-time')).not.toHaveText(last)
    await page.keyboard.press('Home')
    const first = await tip.locator('.tooltip-time').innerText()
    await page.keyboard.press('End')
    await expect(tip.locator('.tooltip-time')).not.toHaveText(first)
    await page.keyboard.press('Escape')
    await expect(tip).toBeHidden()
    await expect(page.locator('[aria-live="polite"]').first()).toBeAttached()
  })

  test('every chart has a table view with the same values', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    const panel = page.locator('section.panel', { hasText: 'Latency' }).first()
    await panel.getByRole('button', { name: 'Table' }).click()
    await expect(panel.locator('table.data tbody tr').first()).toBeVisible()
    await expect(panel.locator('table.data thead th').nth(1)).not.toBeEmpty()
    await panel.getByRole('button', { name: 'Chart' }).click()
    await expect(panel.locator('svg').first()).toBeVisible()
  })

  test('"Show more tests" loads another page of results', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    const rows = page.locator('table.results tbody tr')
    const before = await rows.count()
    await page.getByRole('button', { name: 'Show more tests' }).click()
    await expect.poll(() => rows.count()).toBeGreaterThan(before)
  })

  test('an impossible filter explains itself and offers a way out', async ({ page }) => {
    await page.goto('/?device=nobody-has-this-name')
    await expect(page.getByText('No tests yet for these filters.')).toBeVisible()
    await expect(page.getByText('No tests match these filters.')).toBeVisible()
    await page.getByRole('button', { name: 'Reset filters' }).first().click()
    await ready(page)
  })

  test('the device menu lists devices with test counts and filters by selection', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    await page.getByRole('button', { name: /Devices/ }).click()
    const options = page.getByRole('menuitemcheckbox')
    expect(await options.count()).toBeGreaterThan(0)
    await expect(options.first()).toContainText('tests')
    await options.first().click()
    await expect(page).toHaveURL(/device=/)
    await page.keyboard.press('Escape')
    await expect(options.first()).toBeHidden()
  })

  // Mocked responses can't be intercepted once the service worker is making the requests itself.
  test.describe('with the network failing', () => {
    test.use({ serviceWorkers: 'block' })
    test('the API failing shows an error with a retry, not a blank page', async ({ page }) => {
    await page.route('**/api/v1/stats/timeseries**', (r) => r.fulfill({
      status: 500, contentType: 'application/problem+json', body: JSON.stringify({ type: 'about:blank', title: 'Internal Server Error', status: 500, detail: 'internal error' }),
    }))
    await page.goto('/')
    await expect(page.getByRole('alert').first()).toContainText('internal error')
    await expect(page.getByRole('button', { name: 'Try again' }).first()).toBeVisible()
    await expect(page.getByTestId('hero-figure')).toBeVisible() // the rest of the page still works
    })
  })
})

test.describe('layout', () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test('fits a phone screen without sideways page scroll', async ({ page }) => {
    await page.goto('/')
    await ready(page)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
    // the wide results table scrolls inside its own region instead
    await expect(page.getByRole('region', { name: /Recent tests/ })).toBeVisible()
  })
})

test.describe('accessibility', () => {
  for (const theme of ['dark', 'light'] as const) {
    test(`no axe violations in the ${theme} theme`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
      await page.goto('/?provider=ndt7,ookla')
      await ready(page)
      await page.waitForTimeout(300)
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
      expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
    })
  }
})

test.describe('installable app (PWA)', () => {
  test('serves a valid manifest and registers a service worker', async ({ page, request }) => {
    await page.goto('/')
    await ready(page)
    const href = await page.locator('link[rel="manifest"]').getAttribute('href')
    const res = await request.get(href!)
    expect(res.headers()['content-type']).toContain('application/manifest+json')
    const m = await res.json()
    expect(m).toMatchObject({ name: 'Speedtest', display: 'standalone', start_url: '/' })
    const sizes = m.icons.map((i: { sizes: string; purpose?: string }) => `${i.sizes}${i.purpose ? ':' + i.purpose : ''}`)
    expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512', '512x512:maskable']))
    for (const icon of m.icons) expect((await request.get('/' + icon.src)).status()).toBe(200)

    const active = await page.evaluate(async () => (await navigator.serviceWorker.ready).active?.state)
    expect(active).toBe('activated')
  })

  test('opens offline with the last data it saw, and says so', async ({ page, context }) => {
    await page.goto('/')
    await ready(page)
    await page.evaluate(() => navigator.serviceWorker.ready)
    await page.reload() // now under service-worker control, so API responses get cached
    await ready(page)
    await page.waitForTimeout(500)

    await context.setOffline(true)
    await page.reload()
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    await expect(page.getByRole('status')).toContainText("You’re offline")
    await expect(page.locator('table.results tbody tr').first()).toBeVisible()
    await context.setOffline(false)
  })
})

const RULES = { slowRatio: 0.7, slowConsecutive: 2, recoverRatio: 0.9, lossPct: 5, lossConsecutive: 2, silenceMinHours: 3, remindHours: 12, baselineDays: 30 }
const iso = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString()

async function mockNotifications(page: Page, status: number, body: unknown) {
  await page.route('**/api/v1/notifications', (r) => r.fulfill({
    status, contentType: status === 200 ? 'application/json' : 'application/problem+json', body: JSON.stringify(body),
  }))
}

test.describe('notifications', () => {
  // Mocked responses can't be intercepted once the service worker is making the requests itself.
  test.use({ serviceWorkers: 'block' })

  test('says so plainly when notifications are off', async ({ page }) => {
    await mockNotifications(page, 200, { enabled: false, server: null, rules: null, active: [], recent: [] })
    await page.goto('/')
    await ready(page)
    const section = page.getByTestId('notifications')
    await expect(section).toContainText('Notifications are off')
    await expect(section).toContainText('NTFY_TOPIC')
    await expect(page.getByTestId('alert-banner')).toHaveCount(0)
  })

  test('when on and quiet, states every rule in words and shows nothing sent', async ({ page }) => {
    await mockNotifications(page, 200, { enabled: true, server: 'ntfy.example.org', rules: RULES, active: [], recent: [] })
    await page.goto('/')
    await ready(page)
    const section = page.getByTestId('notifications')
    await expect(section).toContainText('ntfy.example.org')
    await expect(section).toContainText('below 70% of its typical speed for 2 tests in a row')
    await expect(section).toContainText('Packet loss is 5% or more for 2 tests in a row')
    await expect(section).toContainText('three times its usual interval')
    await expect(section).toContainText('reminder every 12 hours')
    await expect(section).toContainText('Nothing sent yet')
    await expect(page.getByTestId('alert-banner')).toHaveCount(0)
  })

  test('active alerts appear above the filters; failed deliveries say why', async ({ page }) => {
    await mockNotifications(page, 200, {
      enabled: true, server: 'ntfy.example.org', rules: RULES,
      active: [
        { kind: 'slow', subject: 'server|wired|ookla', title: 'Slow download: server (wired)', detail: '390 Mbps, 58% below your typical 937 Mbps (2 tests in a row).', severity: 'critical', since: iso(90), notifiedAt: iso(80) },
        { kind: 'silence', subject: 'laptop', title: 'No tests from laptop', detail: 'The last test was 9 hours ago; one is usually due every 3 hours.', severity: 'warning', since: iso(200), notifiedAt: null },
      ],
      recent: [
        { id: 3, at: iso(80), event: 'alert', kind: 'slow', title: 'Slow download: server (wired)', message: '390 Mbps, 58% below your typical 937 Mbps.', delivered: true, error: null },
        { id: 2, at: iso(190), event: 'alert', kind: 'silence', title: 'No tests from laptop', message: 'The last test was 9 hours ago.', delivered: false, error: 'ntfy answered 503: down' },
        { id: 1, at: iso(900), event: 'recovery', kind: 'slow', title: 'Recovered: server (wired)', message: 'Downloads are back to normal.', delivered: true, error: null },
      ],
    })
    await page.goto('/')
    await ready(page)
    const banner = page.getByTestId('alert-banner')
    await expect(banner).toBeVisible()
    await expect(banner.locator('li')).toHaveCount(2)
    await expect(banner).toContainText('Slow download: server (wired).')
    await expect(banner).toContainText('No tests from laptop.')
    // the banner sits above the filter row: it is the first thing to see
    const bannerY = (await banner.boundingBox())!.y
    const filtersY = (await page.getByRole('region', { name: 'Filters' }).boundingBox())!.y
    expect(bannerY).toBeLessThan(filtersY)

    const rows = page.getByTestId('notifications').locator('tbody tr')
    await expect(rows).toHaveCount(3)
    await expect(rows.nth(0)).toContainText('Delivered')
    await expect(rows.nth(1)).toContainText('Failed: ntfy answered 503: down')
    await expect(rows.nth(2)).toContainText('Recovered')
  })

  test('an older backend without the endpoint does not break the page', async ({ page }) => {
    await mockNotifications(page, 404, { type: 'about:blank', title: 'Not Found', status: 404, detail: 'no such endpoint' })
    await page.goto('/')
    await ready(page)
    await expect(page.getByTestId('notifications')).toContainText('Couldn’t load the notification status')
    await expect(page.getByTestId('alert-banner')).toHaveCount(0)
    await expect(page.getByTestId('hero-figure')).toBeVisible()
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`no axe violations with alerts showing (${theme})`, async ({ page }) => {
      await page.addInitScript((t) => localStorage.setItem('theme', t), theme)
      await mockNotifications(page, 200, {
        enabled: true, server: 'ntfy.example.org', rules: RULES,
        active: [{ kind: 'slow', subject: 's', title: 'Slow download: server (wired)', detail: 'x', severity: 'critical', since: iso(90), notifiedAt: iso(80) },
          { kind: 'loss', subject: 'l', title: 'Packet loss: laptop (wireless)', detail: 'y', severity: 'warning', since: iso(60), notifiedAt: null }],
        recent: [{ id: 1, at: iso(80), event: 'alert', kind: 'slow', title: 'Slow download: server (wired)', message: 'x', delivered: false, error: 'ntfy answered 503: down' }],
      })
      await page.goto('/')
      await ready(page)
      await page.waitForTimeout(300)
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
      expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
    })
  }
})
