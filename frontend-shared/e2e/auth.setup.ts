import { expect, test as setup } from '@playwright/test'

// Runs first when the server under test has a dashboard login (E2E_PASSWORD is its password): signs in once and keeps the
// session, so every other spec runs signed in, exactly as a person would use the dashboard behind the login.
setup('sign in', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('login-password').fill(process.env.E2E_PASSWORD ?? '')
  await page.getByTestId('login-submit').click()
  await expect(page.getByTestId('hero-figure')).toBeVisible()
  await page.context().storageState({ path: 'playwright/.auth/state.json' })
})
