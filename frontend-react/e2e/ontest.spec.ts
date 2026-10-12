import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { writeFileSync, rmSync } from 'node:fs'
import { startMlabMock } from './helpers/mlab-mock'

const FAIL_FLAG = process.env.E2E_FAIL_FLAG ?? '/tmp/e2e-fake-speedtest-fail'

async function openDialog(page: Page) {
  await page.getByTestId('run-test').click()
  await expect(page.getByTestId('run-dialog')).toBeVisible()
}

/** Wait until the service worker controls the page, so these tests also cover requests going through it. */
async function underServiceWorker(page: Page) {
  await page.goto('/')
  await page.evaluate(() => navigator.serviceWorker.ready)
  await page.reload()
  await expect(page.getByTestId('hero-figure')).toBeVisible()
  expect(await page.evaluate(() => !!navigator.serviceWorker.controller)).toBeTruthy()
}

async function latestResultId(page: Page): Promise<string> {
  const res = await page.request.get('/api/v1/results?limit=1')
  return (await res.json()).items[0].id
}

test.describe('run a test on the server (Ookla)', () => {
  // Only one server test can run at a time, so these go one after another.
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ request }) => {
    rmSync(FAIL_FLAG, { force: true })
    const caps = await (await request.get('/api/v1/capabilities')).json()
    test.skip(!caps.ookla.available, 'start the backend with OOKLA_ACCEPT_LICENSE=yes and OOKLA_PATH=e2e/fake-speedtest.sh (see the README)')
    // let any server test left over from an earlier run finish (only server tests occupy the single slot)
    await expect.poll(async () => {
      const items = (await (await request.get('/api/v1/tests?state=queued,running&limit=100')).json()).items
      return items.filter((t: { mode: string }) => t.mode === 'server').length
    }, { timeout: 20_000 }).toBe(0)
  })
  test.afterAll(() => rmSync(FAIL_FLAG, { force: true }))

  test('shows live progress from the server, then the saved result, and the dashboard updates', async ({ page }) => {
    await underServiceWorker(page) // the event stream must get through the service worker untouched
    const before = await latestResultId(page)
    await openDialog(page)
    await page.getByTestId('start-test').click()

    const running = page.getByTestId('live-running')
    await expect(running).toBeVisible()
    await expect(running.getByRole('listitem').filter({ hasText: 'Download' })).toHaveAttribute('aria-current', 'step')
    // the speed moves while the test runs: two different readings
    const number = running.locator('.live-number')
    const first = await number.innerText()
    await expect.poll(async () => number.innerText(), { timeout: 10_000 }).not.toBe(first)
    await expect(running.locator('.live-chart svg path[stroke]').first()).toBeVisible()
    await expect(running).toContainText('12.5 ms latency')

    const done = page.getByTestId('live-done')
    await expect(done).toBeVisible({ timeout: 25_000 })
    await expect(page.getByTestId('done-download')).toContainText('100')
    await expect(page.getByTestId('done-upload')).toContainText('20.0')
    await expect(page.getByTestId('done-verdict')).toContainText(/usual|history/)

    await page.getByRole('button', { name: 'Done' }).click()
    await expect(page.getByTestId('run-dialog')).toBeHidden()
    // the new result is in the dashboard without reloading
    await expect(page.getByTestId('hero-figure')).toContainText('100')
    expect(await latestResultId(page)).not.toBe(before)
    const latest = await (await page.request.get('/api/v1/results?limit=1')).json()
    expect(latest.items[0]).toMatchObject({ provider: 'ookla', trigger: 'on_demand', device: 'server', connectionType: 'wired', downloadMbps: 100 })
  })

  test('a running test can be stopped, and nothing is saved', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    const before = await latestResultId(page)
    await openDialog(page)
    await page.getByTestId('start-test').click()
    await expect(page.getByTestId('live-running').locator('.live-number')).not.toHaveText('—', { timeout: 10_000 })
    await page.getByRole('button', { name: 'Stop test' }).click()
    await expect(page.getByTestId('live-cancelled')).toBeVisible({ timeout: 10_000 })
    await expect(page.getByTestId('live-cancelled')).toContainText('Nothing was saved')
    expect(await latestResultId(page)).toBe(before)
    // the slot is free again straight away
    await page.getByRole('button', { name: 'Start another' }).click()
    await expect(page.getByTestId('start-test')).toBeVisible()
    await page.getByTestId('start-test').click()
    await expect(page.getByTestId('live-running')).toBeVisible()
    await page.getByRole('button', { name: 'Stop test' }).click()
    await expect(page.getByTestId('live-cancelled')).toBeVisible({ timeout: 10_000 })
  })

  test('a failure is explained in plain words, with a way to try again', async ({ page }) => {
    writeFileSync(FAIL_FLAG, '')
    await page.goto('/')
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    await openDialog(page)
    await page.getByTestId('start-test').click()
    const failed = page.getByTestId('live-failed')
    await expect(failed).toBeVisible({ timeout: 10_000 })
    await expect(failed).toContainText('didn’t finish')
    await expect(failed).toContainText('Network is unreachable')
    rmSync(FAIL_FLAG, { force: true })
    await page.getByRole('button', { name: 'Try again' }).click()
    await expect(page.getByTestId('start-test')).toBeVisible()
  })

  test('another visitor can watch a test that is already running', async ({ page, browser }) => {
    await page.goto('/')
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    await openDialog(page)
    await page.getByTestId('start-test').click()
    await expect(page.getByTestId('live-running')).toBeVisible()

    const other = await (await browser.newContext({ timezoneId: 'America/Phoenix' })).newPage()
    await other.goto('/')
    await expect(other.getByTestId('run-test')).toContainText('Test running', { timeout: 10_000 })
    await other.getByTestId('run-test').click()
    await expect(other.getByTestId('server-busy')).toContainText('already running on the server')
    await expect(other.getByLabel('Test the server’s connection')).toBeDisabled()
    await other.getByRole('button', { name: 'Watch it' }).click()
    await expect(other.getByTestId('live-running')).toBeVisible() // joined, not asked to start another
    await expect(other.getByTestId('live-done')).toBeVisible({ timeout: 25_000 })
    await expect(page.getByTestId('live-done')).toBeVisible({ timeout: 10_000 })
    await other.context().close()
  })

  test('opening the dialog while a test runs lets you watch it or test your own device instead', async ({ page, request }) => {
    const started = await request.post('/api/v1/tests', { data: { provider: 'ookla', mode: 'server', connectionType: 'wired' } })
    expect(started.status()).toBe(201)
    await page.goto('/')
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    await expect(page.getByTestId('run-test')).toContainText('Test running')
    await openDialog(page)
    await expect(page.getByTestId('server-busy')).toBeVisible()
    await expect(page.getByLabel('Test the server’s connection')).toBeDisabled()
    await expect(page.getByLabel('Test this device')).toBeChecked() // the possible choice is preselected, and usable
    await expect(page.getByTestId('device-name')).toBeVisible()
    await page.getByRole('button', { name: 'Watch it' }).click()
    await expect(page.getByTestId('live-running')).toBeVisible()
    await expect(page.getByTestId('live-done')).toBeVisible({ timeout: 25_000 })
  })

  test('starting at the same moment as someone else follows their test instead of failing', async ({ page, request }) => {
    await page.goto('/')
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    await openDialog(page) // the form is open and thinks the server is free...
    await expect(page.getByTestId('start-test')).toBeEnabled()
    const started = await request.post('/api/v1/tests', { data: { provider: 'ookla', mode: 'server', connectionType: 'wired' } })
    expect(started.status()).toBe(201) // ...but someone else starts first
    await page.getByTestId('start-test').click()
    await expect(page.getByTestId('live-running')).toBeVisible() // a 409 became "watch the one in progress", not an error
    await expect(page.getByTestId('live-failed')).toHaveCount(0)
    await expect(page.getByTestId('live-done')).toBeVisible({ timeout: 25_000 })
  })

  test('has no accessibility violations while idle, running and finished', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('hero-figure')).toBeVisible()
    await openDialog(page)
    const axe = async (what: string) => {
      const r = await new AxeBuilder({ page }).include('[data-testid="run-dialog"]').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
      expect(r.violations.map((v) => `${what} ${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
    }
    await axe('idle')
    await page.getByLabel('Test this device').check()
    await axe('browser options')
    await page.getByLabel('Test the server’s connection').check()
    await page.getByTestId('start-test').click()
    await expect(page.getByTestId('live-running').locator('.live-number')).not.toHaveText('—', { timeout: 10_000 })
    await axe('running')
    await expect(page.getByTestId('live-done')).toBeVisible({ timeout: 25_000 })
    await axe('done')
  })
})

test.describe('when the server cannot run Ookla', () => {
  test.use({ serviceWorkers: 'block' })
  test('says why, in words, and offers the browser test instead', async ({ page }) => {
    await page.route('**/api/v1/capabilities', (r) => r.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        deviceName: 'server', serverConnectionType: 'wired', ndt7: { available: true, reason: null },
        ookla: { available: false, reason: 'Ookla’s terms have not been accepted on this server. Read them at https://www.speedtest.net/about/eula and, if you agree, set OOKLA_ACCEPT_LICENSE=yes and restart.' },
      }),
    }))
    await page.goto('/')
    await openDialog(page)
    await expect(page.getByTestId('ookla-why')).toContainText('OOKLA_ACCEPT_LICENSE=yes')
    await expect(page.getByLabel('Test the server’s connection')).toBeDisabled()
    await expect(page.getByLabel('Test this device')).toBeChecked() // the possible choice is preselected
  })
})

test.describe('run a test on this device (M-Lab NDT7)', () => {
  test.use({ serviceWorkers: 'block' })

  test('needs a device name and the M-Lab data policy to be accepted first', async ({ page }) => {
    await page.goto('/')
    await openDialog(page)
    await page.getByLabel('Test this device').check()
    const start = page.getByTestId('start-test')
    await expect(page.getByRole('dialog')).toContainText('published as open data')
    await expect(page.getByRole('link', { name: /M-Lab’s data policy/ })).toHaveAttribute('href', /measurementlab\.net/)
    await expect(start).toBeDisabled()
    await page.getByTestId('device-name').fill('E2E phone')
    await expect(start).toBeDisabled() // a name alone is not enough
    await page.getByTestId('mlab-consent').check()
    await expect(start).toBeEnabled()
    await page.getByTestId('device-name').fill('   ')
    await expect(start).toBeDisabled()
  })

  test('says so plainly when M-Lab cannot be reached, and leaves nothing open', async ({ page }) => {
    await page.route('https://locate.measurementlab.net/**', (r) => r.abort('internetdisconnected'))
    await page.goto('/')
    await openDialog(page)
    await page.getByLabel('Test this device').check()
    await page.getByTestId('device-name').fill('E2E offline phone')
    await page.getByTestId('mlab-consent').check()
    await page.getByTestId('start-test').click()
    const failed = page.getByTestId('live-failed')
    await expect(failed).toBeVisible({ timeout: 25_000 })
    await expect(failed).toContainText('Couldn’t reach M-Lab')
    // the test row the page created was closed again, not left running until it times out
    await expect.poll(async () => {
      const r = await (await page.request.get('/api/v1/tests?state=running')).json()
      return r.items.filter((t: { device: string }) => t.device === 'E2E offline phone').length
    }, { timeout: 10_000 }).toBe(0)
  })

  test('runs the real NDT7 client end to end and saves the result', async ({ page, context }) => {
    test.setTimeout(60_000)
    const mlab = await startMlabMock()
    try {
      await mlab.install(context)
      await page.goto('/')
      await expect(page.getByTestId('hero-figure')).toBeVisible()
      await openDialog(page)
      await page.getByLabel('Test this device').check()
      await page.getByTestId('device-name').fill('E2E phone')
      await page.getByRole('radio', { name: 'Wi-Fi / wireless' }).click()
      await page.getByTestId('mlab-consent').check()
      await page.getByTestId('start-test').click()

      const running = page.getByTestId('live-running')
      await expect(running).toBeVisible()
      await expect(running.getByRole('listitem').filter({ hasText: 'Download' })).toHaveAttribute('aria-current', 'step', { timeout: 15_000 })
      await expect(running.locator('.live-number')).not.toHaveText('—', { timeout: 15_000 })
      await expect(running.getByRole('listitem').filter({ hasText: 'Upload' })).toHaveAttribute('aria-current', 'step', { timeout: 20_000 })

      await expect(page.getByTestId('live-done')).toBeVisible({ timeout: 30_000 })
      const shown = Number((await page.getByTestId('done-download').innerText()).replace(/[^\d.]/g, ''))
      expect(shown).toBeGreaterThan(10) // the mock streams about 25 Mbit/s
      expect(shown).toBeLessThan(40)

      const list = await (await page.request.get('/api/v1/results?provider=ndt7&device=E2E%20phone&limit=1')).json()
      const r = list.items[0]
      expect(r).toMatchObject({ provider: 'ndt7', device: 'E2E phone', connectionType: 'wireless', trigger: 'on_demand' })
      expect(Math.abs(r.downloadMbps - shown)).toBeLessThan(0.1) // what was shown is what was saved
      expect(r.uploadMbps).toBeGreaterThan(0)
      expect(r.latencyMs).toBe(15) // MinRTT 15,000 µs
      expect(r.packetLossPct).toBeCloseTo(0.3, 1) // 24,000 retransmitted of 8,000,000 sent
      expect(r.server).toMatchObject({ name: 'mlab-mock-1', location: 'Testville', country: 'ZZ' })
      const run = (await (await page.request.get('/api/v1/tests?state=completed&limit=5')).json()).items.find((t: { resultId: string }) => t.resultId === r.id)
      expect(run).toMatchObject({ mode: 'browser', state: 'completed' })
    } finally {
      await mlab.close()
    }
  })
})
