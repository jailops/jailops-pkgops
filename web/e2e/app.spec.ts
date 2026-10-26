import { expect, test } from '@playwright/test'
import { env, open, signIn, watchErrors } from './env'

test.describe.configure({ mode: 'serial' })

test('overview shows a healthy repository with real numbers', async ({ page }) => {
  const errs = watchErrors(page)
  await open(page)
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  await expect(page.getByTestId('health')).toContainText('Healthy')
  await expect(page.getByTestId('n-packages')).toHaveText('3') // hello x2 platforms, web-gateway
  await expect(page.getByTestId('n-versions')).toHaveText('5')
  await expect(page.getByTestId('n-archs')).toHaveText('2')
  await expect(page.getByRole('table').first()).toContainText('FreeBSD:15:aarch64')
  await expect(page.getByLabel('client configuration').first()).toContainText(`url: "${env().base}/\${ABI}/latest"`)
  await expect(page.getByLabel('client configuration').first()).toContainText('signature_type: "pubkey"')
  expect(errs, 'console errors / CSP violations').toEqual([])
})

test('package list: search, platform filter, all versions, shareable URL', async ({ page }) => {
  await open(page, '#/packages')
  const rows = page.getByTestId('packages').locator('tbody tr')
  await expect(rows).toHaveCount(3)
  await expect(page.getByTestId('count')).toHaveText('3 packages')
  await page.getByLabel('Search').fill('gateway')
  await expect(rows).toHaveCount(1)
  await expect(rows.first()).toContainText('web-gateway')
  await expect(page).toHaveURL(/q=gateway/)
  await page.reload()
  await expect(page.getByLabel('Search')).toHaveValue('gateway') // survives reload
  await page.getByLabel('Search').fill('zzz-nothing')
  await expect(page.getByTestId('no-match')).toContainText('Nothing matches')
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await expect(rows).toHaveCount(3)
  await page.getByLabel('Platform').selectOption('FreeBSD:15:aarch64')
  await expect(rows).toHaveCount(1)
  await page.getByLabel('Platform').selectOption('')
  await page.getByLabel('Show all versions').check()
  await expect(rows).toHaveCount(5)
  await expect(page.getByTestId('count')).toHaveText('5 versions')
})

test('package detail: files with symlink and config marker, dependencies, versions', async ({ page }) => {
  await open(page, '#/packages')
  await page.getByRole('link', { name: 'hello', exact: true }).first().click()
  await expect(page.getByRole('heading', { level: 1 })).toContainText('hello')
  const files = page.getByTestId('files')
  await expect(files).toContainText('/usr/local/bin/hello')
  await expect(files).toContainText('→ hello-0.1.1')
  await expect(files.locator('tr', { hasText: 'hello.conf' }).getByText('config')).toBeVisible()
  await expect(page.getByText('Creates users')).toBeVisible()
  await expect(page.getByRole('link', { name: '0.1.0' })).toBeVisible()
  await expect(page.getByTestId('delete')).toHaveCount(0) // anonymous visitors cannot delete
  await page.getByRole('link', { name: '0.1.0' }).click()
  await expect(page.getByRole('heading', { level: 1 })).toContainText('0.1.0')
  await expect(page.getByText('Older version')).toBeVisible()

  await open(page, '#/packages')
  await page.getByRole('link', { name: 'web-gateway' }).click()
  await expect(page.getByRole('heading', { name: 'Dependencies' })).toBeVisible()
  await expect(page.getByText('devel/pcre2')).toBeVisible()
})

test('repository status: signature, fingerprint, consistency and damage detection', async ({ page }) => {
  await open(page, '#/status')
  await expect(page.getByTestId('health')).toContainText('Healthy')
  await expect(page.getByTestId('sig')).toHaveText('Signed')
  await expect(page.getByTestId('fingerprint')).toHaveText(env().fingerprint)
  await expect(page.getByTestId('catalogs').locator('tbody tr')).toHaveCount(2)
  await expect(page.getByRole('link', { name: 'Download repo.pub' })).toHaveAttribute('href', '/api/v1/key')

  // damage a package behind the registry's back: the page must say so, in words
  const fs = await import('node:fs')
  const f = `${env().repo}/FreeBSD:15:amd64/latest/All/web-gateway-2.4.0.pkg`
  const good = fs.readFileSync(f)
  const bad = Buffer.from(good)
  const mid = Math.floor(bad.length / 2)
  bad[mid] = (bad[mid] ?? 0) ^ 0xff
  fs.writeFileSync(f, bad)
  try {
    await page.getByRole('button', { name: 'Refresh' }).click()
    await expect(page.getByTestId('health')).toContainText('Needs attention')
    await expect(page.getByTestId('problems')).toContainText('web-gateway-2.4.0.pkg')
    await expect(page.getByTestId('catalogs')).toContainText('Inconsistent')
  } finally {
    fs.writeFileSync(f, good)
  }
  await page.getByRole('button', { name: 'Refresh' }).click()
  await expect(page.getByTestId('health')).toContainText('Healthy')
})

test('sign in: bad token is explained, roles gate the controls, token is memory only', async ({ page }) => {
  const errs = watchErrors(page)
  await open(page, '#/packages')
  await page.getByTestId('signin').click()
  await page.getByLabel('Registry token').fill('not-a-token')
  await page.getByRole('button', { name: 'Sign in', exact: true }).last().click()
  await expect(page.getByRole('alert').filter({ hasText: 'not accepted' })).toBeVisible()
  await expect(page.locator('body')).not.toContainText('{"error"')
  await page.getByRole('button', { name: 'Cancel' }).click()

  await signIn(page, 'viewer')
  await expect(page.getByTestId('who')).toContainText('viewer')
  await expect(page.getByTestId('upload')).toHaveCount(0)
  await page.getByTestId('signout').click()

  await signIn(page, 'uploader')
  await expect(page.getByTestId('upload')).toBeVisible()
  await page.getByRole('link', { name: 'hello', exact: true }).first().click()
  await expect(page.getByTestId('delete')).toHaveCount(0) // uploaders cannot delete

  // nothing secret lands in storage or cookies; a reload signs out
  const stored = await page.evaluate(() => JSON.stringify({ l: { ...localStorage }, s: { ...sessionStorage }, c: document.cookie }))
  expect(stored).not.toContain(env().secrets.uploader)
  await page.reload()
  await expect(page.getByTestId('signin')).toBeVisible()
  expect(errs).toEqual([])
})

test('keyboard: skip link, dialog focus handling and Escape', async ({ page }) => {
  await open(page)
  await page.keyboard.press('Tab')
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused()
  const signin = page.getByTestId('signin')
  await signin.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('dialog', { name: 'Sign in' })).toBeVisible()
  await expect(page.getByLabel('Registry token')).toBeFocused()
  await page.keyboard.press('Tab')
  await page.keyboard.press('Tab')
  await page.keyboard.press('Tab')
  await expect(page.getByRole('dialog').locator(':focus')).toHaveCount(1) // focus never escapes the dialog
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(signin).toBeFocused()
})

test('errors are sentences with a retry, never raw JSON', async ({ page }) => {
  await page.route('**/api/v1/status', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"internal error"}' }))
  await open(page)
  const err = page.getByTestId('error')
  await expect(err).toContainText('Something went wrong')
  await expect(err).not.toContainText('{')
  await expect(err).not.toContainText('500')
  await page.unroute('**/api/v1/status')
  await err.getByRole('button', { name: 'Try again' }).click()
  await expect(page.getByTestId('health')).toContainText('Healthy')

  await page.route('**/api/v1/packages', (r) => r.abort())
  await open(page, '#/packages')
  await expect(page.getByTestId('error')).toContainText('cannot be reached')
})

test('upload through the UI as an uploader', async ({ page }) => {
  const fs = await import('node:fs')
  const { execFileSync } = await import('node:child_process')
  const e = env()
  const spec = `${e.dir}/up.yaml`
  fs.writeFileSync(spec, `name: uploaded-tool
version: "1.0"
comment: Uploaded from the UI test
maintainer: e2e@example.org
www: https://example.org
abi: FreeBSD:15:amd64
files: [{content: "x\\n", destination: share/uploaded-tool/x}]
`)
  const pkg = execFileSync(e.bin, ['build', '-f', spec, '-o', `${e.dir}/updist`], { encoding: 'utf8', env: { ...process.env, SOURCE_DATE_EPOCH: '1700000000' } }).trim()
  await open(page, '#/packages')
  await signIn(page, 'uploader')
  await page.getByTestId('upload').click()
  await page.getByLabel('Package file (.pkg)').setInputFiles(pkg)
  await page.getByRole('button', { name: 'Upload', exact: true }).last().click()
  await expect(page.getByTestId('packages')).toContainText('uploaded-tool')
  await expect(page.getByRole('status').filter({ hasText: 'Uploaded uploaded-tool 1.0' })).toBeVisible()

  // a corrupt file is refused with a sentence
  fs.writeFileSync(`${e.dir}/junk.pkg`, 'not a package')
  await page.getByTestId('upload').click()
  await page.getByLabel('Package file (.pkg)').setInputFiles(`${e.dir}/junk.pkg`)
  await page.getByRole('button', { name: 'Upload', exact: true }).last().click()
  await expect(page.getByRole('dialog').getByRole('alert')).toContainText('rejected')
  await expect(page.getByRole('dialog')).not.toContainText('{')
  await page.getByRole('button', { name: 'Cancel' }).click()
})

test('admin deletes a package: confirmation, effect on the catalog, repository stays healthy', async ({ page }) => {
  const e = env()
  await open(page, '#/packages/FreeBSD%3A15%3Aamd64/uploaded-tool/1.0')
  await signIn(page, 'admin')
  await page.getByTestId('delete').click()
  const dlg = page.getByRole('dialog', { name: 'Delete package' })
  await expect(dlg).toContainText('only version')
  const confirm = dlg.getByRole('button', { name: 'Delete package' })
  await expect(confirm).toBeDisabled()
  await dlg.getByLabel(/Type uploaded-tool to confirm/).fill('uploaded')
  await expect(confirm).toBeDisabled()
  // cancel leaves everything in place
  await dlg.getByRole('button', { name: 'Cancel' }).click()
  expect((await fetch(`${e.base}/FreeBSD:15:amd64/latest/All/uploaded-tool-1.0.pkg`)).status).toBe(200)

  await page.getByTestId('delete').click()
  await dlg.getByLabel(/Type uploaded-tool to confirm/).fill('uploaded-tool')
  await confirm.click()
  await expect(page.getByRole('heading', { level: 1, name: 'Packages' })).toBeVisible()
  await expect(page.getByTestId('packages')).not.toContainText('uploaded-tool')
  expect((await fetch(`${e.base}/FreeBSD:15:amd64/latest/All/uploaded-tool-1.0.pkg`)).status).toBe(404)
  const site = await (await fetch(`${e.base}/api/v1/status`)).json()
  expect(site.healthy).toBe(true)

  // deleting the newest of two versions falls back to the older one and the page moves there
  await open(page, '#/packages/FreeBSD%3A15%3Aaarch64/hello/0.1.1')
  await signIn(page, 'admin')
  await page.getByTestId('delete').click()
  await expect(dlg).toContainText('newest version')
  await dlg.getByLabel(/Type hello to confirm/).fill('hello')
  await confirm.click()
  await expect(page.getByRole('heading', { level: 1 })).toContainText('0.1.0')
  await expect(page.getByText('Latest', { exact: true })).toBeVisible()
  const st = await (await fetch(`${e.base}/api/v1/status`)).json()
  expect(st.healthy).toBe(true)
  expect(st.versions).toBe(4)
})
