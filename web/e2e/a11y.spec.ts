import { expect, test } from '@playwright/test'
import { audit } from './a11y'
import { open, signIn, watchErrors } from './env'
import { mockApi } from './mock'

const pages = [
  ['overview', '#/'],
  ['packages', '#/packages'],
  ['package', '#/packages/FreeBSD%3A15%3Aamd64/hello/0.1.1'],
  ['status', '#/status'],
  ['manage', '#/manage'],
  ['not found', '#/nowhere'],
] as const

for (const [name, hash] of pages) {
  test(`a11y: ${name}`, async ({ page }) => {
    const errs = watchErrors(page)
    await open(page, hash)
    await page.waitForSelector('h1')
    await page.waitForFunction(() => !document.querySelector('.loading'))
    expect(await audit(page)).toEqual([])
    expect(errs).toEqual([])
  })
}

test('a11y: dialogs and signed-in state', async ({ page }) => {
  await open(page, '#/packages/FreeBSD%3A15%3Aamd64/hello/0.1.1')
  await page.getByTestId('signin').click()
  expect(await audit(page)).toEqual([])
  await page.keyboard.press('Escape')
  await signIn(page, 'admin')
  expect(await audit(page)).toEqual([])
  await page.getByTestId('delete').click()
  expect(await audit(page)).toEqual([])
  await page.getByRole('button', { name: 'Cancel' }).click()
  await open(page, '#/packages')
  await signIn(page, 'admin')
  await page.getByTestId('upload').click()
  expect(await audit(page)).toEqual([])
})

test('a11y: error and empty states', async ({ page }) => {
  await page.route('**/api/v1/status', (r) => r.fulfill({ status: 503, body: 'down' }))
  await open(page)
  await page.getByTestId('error').waitFor()
  expect(await audit(page)).toEqual([])
  await open(page, '#/packages?q=zzzzzz')
  await page.getByTestId('no-match').waitFor()
  expect(await audit(page)).toEqual([])
})

test('a11y: manage view and its dialogs (mocked API)', async ({ page }) => {
  const errs = watchErrors(page)
  await mockApi(page)
  await open(page, '#/manage')
  await page.getByTestId('manage').waitFor()
  expect(await audit(page)).toEqual([])
  await page.getByTestId('f-abi').selectOption('FreeBSD:15:amd64')
  await page.getByTestId('row').filter({ hasText: '0.2.0' }).getByTestId('sel').check()
  expect(await audit(page)).toEqual([])
  await page.getByTestId('prune-open').click()
  await page.getByTestId('prune-preview').waitFor()
  expect(await audit(page)).toEqual([])
  await page.locator('#prune-confirm').fill('prune')
  await page.getByTestId('prune-go').click()
  await page.getByTestId('steps').waitFor()
  expect(await audit(page)).toEqual([])
  await page.getByTestId('prune-close').click()
  await page.getByTestId('archive-list').getByLabel('Select archived hello 0.0.9').check()
  await page.getByTestId('archive-open').click()
  expect(await audit(page)).toEqual([])
  expect(errs).toEqual([])
})

test('a11y: access states (mocked API)', async ({ page }) => {
  await mockApi(page, { who: { status: 403, body: { error: 'identity bob@example.com is not allowed' } } })
  await open(page, '#/')
  await page.getByTestId('denied').waitFor()
  expect(await audit(page)).toEqual([])
})
