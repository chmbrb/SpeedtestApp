import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

// The optional dashboard login, in a real browser. These need a server that has one: start it with DASHBOARD_PASSWORD and give
// the same password here as E2E_PASSWORD (see the README). Without it the whole file is skipped. The server should run with
// INGEST_RATE_PER_MIN=0, or the limit on login attempts (5 in a burst per address) stops these tests from signing in so often.

const PASSWORD = process.env.E2E_PASSWORD ?? ''
test.skip(!PASSWORD, 'needs a server with DASHBOARD_PASSWORD: set E2E_PASSWORD to it')

const signIn = async (page: Page, password = PASSWORD) => {
  await page.getByTestId('login-password').fill(password)
  await page.getByTestId('login-submit').click()
}

const dashboardIsShown = (page: Page) => expect(page.getByTestId('hero-figure')).toBeVisible()
const loginIsShown = (page: Page) => expect(page.getByTestId('login-password')).toBeVisible()

test.describe('login', () => {
  test('asks for the password first, and the API refuses everything but the health check until then', async ({ page, request }) => {
    await page.goto('/')
    await loginIsShown(page)
    await expect(page.getByTestId('hero-figure')).toHaveCount(0)
    expect((await request.get('/api/v1/devices')).status()).toBe(401)
    expect((await request.get('/api/v1/results')).status()).toBe(401)
    expect((await request.get('/api/v1/health')).status()).toBe(200)
    const session = await request.get('/api/v1/auth/session')
    expect(await session.json()).toEqual({ authenticated: false, required: true })
  })

  test('a wrong password says so, empties the field and lets the person try again', async ({ page }) => {
    await page.goto('/')
    await signIn(page, 'not the password')
    await expect(page.getByTestId('login-error')).toHaveText('That password is not right.')
    await expect(page.getByTestId('login-password')).toHaveValue('')
    await expect(page.getByTestId('login-submit')).toBeDisabled() // nothing typed
    await expect(page.getByTestId('hero-figure')).toHaveCount(0)
    await signIn(page)
    await dashboardIsShown(page)
  })

  test('the right password shows the dashboard, and the session cookie cannot be read by the page', async ({ page, context }) => {
    await page.goto('/')
    await signIn(page)
    await dashboardIsShown(page)
    const cookie = (await context.cookies()).find((c) => c.name === 'speedtest_session')
    expect(cookie, 'no session cookie').toBeTruthy()
    expect(cookie).toMatchObject({ httpOnly: true, sameSite: 'Strict', path: '/' })
    expect(await page.evaluate(() => document.cookie)).not.toContain('speedtest_session')
  })

  test('stays signed in across a reload', async ({ page }) => {
    await page.goto('/')
    await signIn(page)
    await dashboardIsShown(page)
    await page.reload()
    await dashboardIsShown(page)
    await expect(page.getByTestId('login-password')).toHaveCount(0)
  })

  test('sign out goes back to the login, and a reload does not bring the dashboard back', async ({ page, request }) => {
    await page.goto('/')
    await signIn(page)
    await dashboardIsShown(page)
    await page.getByTestId('signout').click()
    await loginIsShown(page)
    await page.reload()
    await loginIsShown(page)
    expect((await page.request.get('/api/v1/devices')).status()).toBe(401)
    expect((await request.get('/api/v1/devices')).status()).toBe(401)
  })

  test('shows the login when the session ends while the dashboard is open', async ({ page, context }) => {
    await page.goto('/')
    await signIn(page)
    await dashboardIsShown(page)
    await context.clearCookies() // as if it had expired, or the password had been changed
    await page.getByRole('radio', { name: 'Wireless' }).click() // the dashboard asks the server again
    await loginIsShown(page)
    await expect(page.getByTestId('hero-figure')).toHaveCount(0)
  })

  test('signing out empties what the service worker saved for offline use', async ({ page }) => {
    const saved = () => page.evaluate(async () => (await caches.has('api-v1')))
    await page.goto('/')
    await signIn(page)
    await dashboardIsShown(page)
    await page.reload() // so the service worker is in charge of the page and keeps the answers it fetches
    await dashboardIsShown(page)
    await expect.poll(saved, { timeout: 15_000 }).toBe(true)
    await page.getByTestId('signout').click()
    await loginIsShown(page)
    await expect.poll(saved).toBe(false)
  })

  test('the sign-in page has no accessibility violations', async ({ page }) => {
    await page.goto('/')
    await loginIsShown(page)
    const { violations } = await new AxeBuilder({ page }).analyze()
    expect(violations).toEqual([])
  })

  test.describe('on a phone', () => {
    test.use({ viewport: { width: 390, height: 844 } })

    test('the sign-in page and the dashboard (with its sign out button) fit the screen', async ({ page }) => {
      const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
      await page.goto('/')
      await loginIsShown(page)
      expect(await overflow()).toBeLessThanOrEqual(0)
      await signIn(page)
      await dashboardIsShown(page)
      await expect(page.getByTestId('signout')).toBeVisible()
      expect(await overflow()).toBeLessThanOrEqual(0)
    })
  })

  test('endpoints with their own bearer token still work for the collectors', async ({ request }) => {
    const token = process.env.E2E_API_TOKEN
    test.skip(!token, 'set E2E_API_TOKEN to one of the server\'s API_TOKENS to check this')
    // an empty result: the token is accepted (so the answer is about the body: 400), not stopped at the login (401)
    const res = await request.post('/api/v1/results', { headers: { authorization: `Bearer ${token}` }, data: {} })
    expect(res.status()).toBe(400)
    // and starting a test with nothing at all is refused
    expect((await request.post('/api/v1/tests', { data: {} })).status()).toBe(401)
  })
})
