import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test } from '@playwright/test'
import { open, signIn } from './env'
import { mockApi } from './mock'

// Screenshots for review: SCREENSHOTS=1 npx playwright test e2e/screenshots.spec.ts
// They land in e2e/screenshots/<project>/ (gitignored).
test.skip(!process.env.SCREENSHOTS, 'set SCREENSHOTS=1')

const shots = [
  ['overview', '#/'], ['packages', '#/packages?all=1'], ['package', '#/packages/FreeBSD%3A15%3Aamd64/hello/0.1.1'],
  ['status', '#/status'], ['empty-search', '#/packages?q=zzz'],
] as const

test('screenshots', async ({ page }, info) => {
  const dir = join(import.meta.dirname, 'screenshots', info.project.name)
  mkdirSync(dir, { recursive: true })
  for (const [name, hash] of shots) {
    await open(page, hash)
    await page.waitForFunction(() => !document.querySelector('.loading'))
    await page.screenshot({ path: join(dir, `${name}.png`), fullPage: true })
  }
  await open(page, '#/packages/FreeBSD%3A15%3Aamd64/hello/0.1.1')
  await signIn(page, 'admin')
  await page.screenshot({ path: join(dir, 'package-admin.png'), fullPage: true })
  await page.getByTestId('delete').click()
  await page.screenshot({ path: join(dir, 'delete-dialog.png') })
  await page.keyboard.press('Escape')
  await page.route('**/api/v1/status', (r) => r.fulfill({ status: 500, body: '{"error":"x"}' }))
  await open(page, '#/')
  await page.getByTestId('error').waitFor()
  await page.screenshot({ path: join(dir, 'error.png') })
})

// The manage view needs data a local registry does not have (GitHub mode, archive): mocked API.
test('screenshots: manage (mocked API)', async ({ page }, info) => {
  const dir = join(import.meta.dirname, 'screenshots', info.project.name)
  mkdirSync(dir, { recursive: true })
  await mockApi(page)
  await open(page, '#/manage')
  await page.getByTestId('manage').waitFor()
  await page.screenshot({ path: join(dir, 'manage.png'), fullPage: true })
  const t = page.getByTestId('abi-FreeBSD:15:amd64')
  await t.getByTestId('plan').click()
  await page.getByTestId('plan-result').waitFor()
  await t.getByTestId('row').filter({ hasText: '0.2.0' }).getByTestId('sel').check()
  await t.getByTestId('row').filter({ hasText: '0.1.0' }).getByTestId('sel').check()
  await page.screenshot({ path: join(dir, 'manage-selection.png'), fullPage: true })
  await page.getByTestId('prune-open').click()
  await page.getByTestId('prune-preview').waitFor()
  await page.locator('#prune-confirm').fill('prune')
  await page.screenshot({ path: join(dir, 'manage-prune-dialog.png') })
  await page.getByTestId('prune-go').click()
  await page.getByTestId('steps').waitFor()
  await page.screenshot({ path: join(dir, 'manage-prune-done.png') })
  await page.getByTestId('prune-close').click()
  await page.getByTestId('archive-list').getByLabel('Select archived hello 0.0.9').check()
  await page.getByTestId('archive-open').click()
  await page.screenshot({ path: join(dir, 'manage-archive-dialog.png') })
})

test('screenshots: manage with real registry in manual mode, and access denied', async ({ page }, info) => {
  const dir = join(import.meta.dirname, 'screenshots', info.project.name)
  await open(page, '#/manage')
  await signIn(page, 'admin')
  await page.getByTestId('manage').waitFor()
  await page.screenshot({ path: join(dir, 'manage-real.png'), fullPage: true })
  const p2 = await page.context().newPage()
  await mockApi(p2, { who: { status: 403, body: { error: 'identity bob@example.com is not allowed' } } })
  await open(p2, '#/')
  await p2.getByTestId('denied').waitFor()
  await p2.screenshot({ path: join(dir, 'access-denied.png') })
})
