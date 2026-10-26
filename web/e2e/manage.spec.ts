import { expect, test, type Locator, type Page } from '@playwright/test'
import { env, open, signIn, watchErrors } from './env'
import { EFFECT, mockApi, overview, prunedResult } from './mock'

const A = 'FreeBSD:15:amd64'
const B = 'FreeBSD:15:aarch64'
const table = (page: Page, abi: string) => page.getByTestId('abi-' + abi)
const row = (scope: Page | Locator, text: string) => scope.getByTestId('row').filter({ hasText: text })

test.describe('real local-dir registry (manual mode)', () => {
  test('manual cleanup shows the diff and commands, and changes nothing', async ({ page }) => {
    const errs = watchErrors(page)
    const before = await (await fetch(`${env().base}/api/v1/packages`)).json()
    await open(page, '#/manage')
    await signIn(page, 'admin')
    await expect(page.getByTestId('manage')).toBeVisible()
    await expect(page.getByTestId('prune-mode')).toContainText('manual')
    const t = table(page, A)
    await expect(t).toBeVisible()
    await expect(row(t, '0.1.1').getByTestId('badge').first()).toHaveText('Active')
    await expect(row(t, '0.1.1')).toContainText('Newest')
    await row(t, '0.1.0').getByTestId('sel').check()
    await page.getByTestId('prune-open').click()
    const dlg = page.getByTestId('prune-dialog')
    await expect(dlg.getByTestId('prune-preview')).toContainText('1 file would be left out')
    await expect(dlg.getByTestId('prune-effect')).toContainText('Removed from the next deploy')
    const go = page.getByTestId('prune-go')
    await expect(go).toBeDisabled()
    await page.getByLabel('prune', { exact: false }).last().fill('prune')
    await go.click()
    const manual = page.getByTestId('prune-manual')
    await expect(manual).toContainText('Nothing was changed')
    await expect(manual).toContainText('hello')
    await expect(manual).toContainText('prune.yaml')
    await expect(manual.getByLabel('prune manifest diff')).toContainText('0.1.0')
    await expect(manual.getByLabel('commands')).toContainText('git add')
    await page.getByTestId('prune-close').click()
    const after = await (await fetch(`${env().base}/api/v1/packages`)).json()
    expect(after.packages.length).toBe(before.packages.length) // nothing deleted
    expect(errs).toEqual([])
  })

  test('a viewer sees everything but has no actions', async ({ page }) => {
    await open(page, '#/manage')
    await signIn(page, 'viewer')
    await expect(page.getByTestId('manage')).toBeVisible()
    await expect(page.getByTestId('readonly')).toContainText('viewer')
    await expect(page.getByTestId('sel')).toHaveCount(0)
    await expect(page.getByTestId('plan')).toHaveCount(0)
    await expect(page.getByTestId('prune-open')).toHaveCount(0)
    await expect(page.getByTestId('republish')).toHaveCount(0)
    await expect(page.getByTestId('total')).not.toHaveText('')
  })
})

test.describe('Cloudflare Access (mocked API)', () => {
  test('identity from the gateway is shown, token sign-in is hidden (mocked API)', async ({ page }) => {
    await mockApi(page)
    await open(page, '#/manage')
    await expect(page.getByTestId('who')).toHaveText(/alice@example.com . admin . via Access/)
    await expect(page.getByTestId('signin')).toHaveCount(0)
    await expect(page.getByTestId('signout')).toHaveCount(0)
    await expect(page.getByTestId('manage')).toBeVisible()
  })
  test('an identity that is not allowed gets a clear state (mocked API)', async ({ page }) => {
    await mockApi(page, { who: { status: 403, body: { error: 'identity bob@example.com is not allowed' } } })
    await open(page, '#/')
    await expect(page.getByTestId('denied')).toContainText('not allowed')
    await expect(page.getByTestId('denied')).toContainText('bob@example.com')
    await expect(page.getByTestId('signin')).toHaveCount(0)
  })
  test('anonymous visitors still get the token sign-in (mocked API)', async ({ page }) => {
    await mockApi(page, { who: { status: 401, body: { error: 'sign in' } } })
    await open(page, '#/')
    await expect(page.getByTestId('signin')).toBeVisible()
    await expect(page.getByTestId('denied')).toHaveCount(0)
  })
})

test.describe('manage view (mocked API)', () => {
  test('facts, totals and every version including pruned and archive-only (mocked API)', async ({ page }) => {
    await mockApi(page)
    await open(page, '#/manage')
    const facts = page.getByTestId('facts')
    await expect(facts).toContainText(`${A}, ${B}`)
    await expect(page.getByTestId('min-release')).toHaveText('15.1')
    await expect(page.getByTestId('sig')).toContainText('Signed')
    await expect(facts).toContainText('ab12cd34ef56')
    await expect(page.getByTestId('total')).toHaveText('23.7 MiB')
    await expect(page.getByTestId('storage')).toContainText('FreeBSD:15:aarch64')
    const t = table(page, A)
    await expect(t.getByTestId('row')).toHaveCount(5)
    await expect(row(t, '0.0.9')).toContainText('Pruned')
    await expect(row(t, '0.0.9').getByTestId('archive-state')).toHaveText('Pruned from Pages, still archived')
    await expect(row(t, 'web-gateway')).toContainText('Not archived')
    await expect(row(t, '0.3.0')).toContainText('Newest')
    const b = table(page, B)
    await expect(row(b, '0.2.0')).toContainText('Archive only')
    await expect(row(b, '0.0.1').getByTestId('archive-state')).toHaveText('Archive deleted')
    await expect(page.getByTestId('runs')).toContainText('success')
    await expect(page.getByTestId('runs').getByRole('link', { name: 'run 9001' })).toHaveAttribute('href', /actions\/runs\/9001/)
  })

  test('filters by ABI, package name, age and state (mocked API)', async ({ page }) => {
    await mockApi(page)
    await open(page, '#/manage')
    const rows = page.getByTestId('row')
    await expect(rows).toHaveCount(8)
    await page.getByTestId('f-abi').selectOption(B)
    await expect(rows).toHaveCount(3)
    await page.getByTestId('f-abi').selectOption('')
    await page.getByTestId('f-name').fill('web')
    await expect(rows).toHaveCount(1)
    await expect(page.getByTestId('count')).toHaveText('1 version of 8')
    await page.getByTestId('f-name').fill('')
    await page.getByTestId('f-age').fill('60')
    await expect(rows).toHaveCount(3) // 0.1.0 (90d), 0.0.9, 0.0.1; the archive-only row has no build time and never matches
    await page.getByTestId('f-age').fill('')
    await page.getByTestId('f-state').selectOption('pruned')
    await expect(rows).toHaveCount(2)
    await page.getByTestId('f-name').fill('nothing-like-this')
    await expect(page.getByTestId('no-match')).toBeVisible()
    await page.getByRole('button', { name: 'Clear filters' }).first().click()
    await expect(rows).toHaveCount(8)
  })

  test('select all but the newest N uses the server plan and previews bytes (mocked API)', async ({ page }) => {
    const m = await mockApi(page, {
      plan: () => ({ mode: 'github', dry_run: true, bytes: 6_100_000, diff: '', effect: EFFECT, entries: [
        { abi: A, name: 'hello', version: '0.2.0' }, { abi: A, name: 'hello', version: '0.1.0' }] }),
    })
    await open(page, '#/manage')
    const t = table(page, A)
    await t.getByTestId('keep').fill('1')
    await t.getByTestId('plan').click()
    await expect(page.getByTestId('plan-result')).toContainText('2 files selected')
    await expect(page.getByTestId('plan-bytes')).toHaveText('5.8 MiB')
    await expect(page.getByTestId('plan-files')).toContainText('hello-0.2.0.pkg')
    expect(m.calls.find((c) => c.path === '/prune/plan')?.body).toEqual({ abi: A, keep: 1 })
    await expect(t.locator('[data-test=sel]:checked')).toHaveCount(2)
    await expect(page.getByTestId('selbar')).toContainText('2 selected')
    await page.getByRole('button', { name: 'Clear selection' }).click()
    await expect(page.getByTestId('selbar')).toHaveCount(0)
  })

  test('github cleanup: typed confirmation, dry run first, commit link, four-phase strip, polling (mocked API)', async ({ page }) => {
    await page.clock.install()
    let phase: 'none' | 'committed' | 'workflow-running' | 'pruned' = 'none'
    const m = await mockApi(page)
    const base = overview()
    const refresh = () => {
      m.overview = overview({
        versions: base.versions.map((x: any) => x.name === 'hello' && x.abi === A && x.version === '0.1.0' && phase !== 'none' // eslint-disable-line @typescript-eslint/no-explicit-any
          ? { ...x, state: phase === 'pruned' ? 'pruned' : 'pending-prune', phase: phase === 'pruned' ? 'published' : phase } : x),
      })
    }
    m.onPrune = (body, dry) => {
      if (!dry) { phase = 'committed'; refresh() }
      return { status: 200, body: prunedResult(body, dry) }
    }
    await open(page, '#/manage')
    await row(table(page, A), '0.1.0').getByTestId('sel').check()
    await page.getByTestId('prune-open').click()
    await expect(page.getByTestId('prune-preview')).toContainText('1 file would be left out')
    await expect(page.getByTestId('prune-effect')).toHaveText(EFFECT) // the server's sentence
    expect(m.calls.map((c) => [c.path, !!c.body.dry_run])).toEqual([['/prune', true]])
    await expect(page.getByTestId('prune-go')).toBeDisabled()
    await page.locator('#prune-confirm').fill('prun')
    await expect(page.getByTestId('prune-go')).toBeDisabled()
    await page.locator('#prune-confirm').fill('prune')
    await page.locator('#prune-reason').fill('too old')
    await page.getByTestId('prune-go').click()
    await expect(page.getByTestId('prune-done')).toContainText('takes effect after the next deploy')
    await expect(page.getByTestId('commit-link')).toHaveAttribute('href', 'https://github.com/example/packages/commit/f00dbabe12345678')
    expect(m.calls[1]!.body).toMatchObject({ confirm: 'prune', reason: 'too old', entries: [{ abi: A, name: 'hello', version: '0.1.0' }] })
    expect(m.calls[1]!.body.dry_run).toBeUndefined()
    const cur = page.locator('[data-test=steps] [aria-current=step]')
    await expect(cur).toContainText('Committed')
    await expect(page.getByTestId('pending-note')).toContainText('takes effect after the next deploy')
    phase = 'workflow-running'; refresh()
    const hits = m.overviewHits
    await page.clock.runFor(15_500)
    await expect(cur).toContainText('Workflow running')
    expect(m.overviewHits).toBeGreaterThan(hits)
    phase = 'pruned'; refresh()
    await page.clock.runFor(15_500)
    await expect(page.locator('[data-test=steps] li.done')).toHaveCount(4)
    await expect(page.getByTestId('pending-note')).toHaveCount(0)
    // polling stopped: nothing pending any more
    const settled = m.overviewHits
    await page.clock.runFor(60_000)
    expect(m.overviewHits).toBe(settled)
  })

  test('no timer survives leaving the view (mocked API)', async ({ page }) => {
    await page.clock.install()
    const m = await mockApi(page)
    m.overview = overview({ versions: overview().versions.map((x: any) => x.version === '0.1.0' && x.abi === A ? { ...x, state: 'pending-prune', phase: 'committed' } : x) }) // eslint-disable-line @typescript-eslint/no-explicit-any
    await open(page, '#/manage')
    await expect(page.getByTestId('pending-note')).toBeVisible()
    await page.clock.runFor(15_500)
    await expect.poll(() => m.overviewHits).toBe(2)
    await page.getByRole('link', { name: 'Overview' }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await page.clock.runFor(60_000)
    expect(m.overviewHits).toBe(2)
  })

  test('forcing the newest version needs the per-row checkbox and shows the server refusal (mocked API)', async ({ page }) => {
    const m = await mockApi(page)
    m.onPrune = (body, dry) => {
      if (body.entries.some((e: any) => e.version === '0.3.0' && e.abi === A && !e.force)) // eslint-disable-line @typescript-eslint/no-explicit-any
        return { status: 422, body: { error: `${A} hello-0.3.0 is the newest version; prune it only with force` } }
      return { status: 200, body: prunedResult(body, dry) }
    }
    await open(page, '#/manage')
    await row(table(page, A), '0.3.0').getByTestId('sel').check()
    await page.getByTestId('prune-open').click()
    await expect(page.getByTestId('prune-error')).toContainText('is the newest version; prune it only with force')
    await page.locator('#prune-confirm').fill('prune')
    await expect(page.getByTestId('prune-go')).toBeDisabled()
    await expect(page.getByTestId('prune-dialog')).toContainText('This is the newest version')
    await page.getByTestId('force').check()
    await expect(page.getByTestId('prune-preview')).toContainText('1 file would be left out')
    await expect(page.getByTestId('prune-error')).toHaveCount(0)
    await expect(page.getByTestId('prune-go')).toBeEnabled()
    expect(m.calls.at(-1)!.body.entries[0].force).toBe(true)
  })

  test('a server error on request is shown and nothing is claimed done (mocked API)', async ({ page }) => {
    const m = await mockApi(page)
    m.onPrune = (body, dry) => (dry ? { status: 200, body: prunedResult(body, true) } : { status: 502, body: { error: 'GitHub said: conflict on prune.yaml' } })
    await open(page, '#/manage')
    await row(table(page, A), '0.2.0').getByTestId('sel').check()
    await page.getByTestId('prune-open').click()
    await page.locator('#prune-confirm').fill('prune')
    await page.getByTestId('prune-go').click()
    await expect(page.getByTestId('prune-error')).toContainText('GitHub said: conflict on prune.yaml')
    await expect(page.getByTestId('prune-done')).toHaveCount(0)
  })

  test('viewer through Access sees no actions (mocked API)', async ({ page }) => {
    await mockApi(page, { who: { status: 200, body: { name: 'vic@example.com', role: 'viewer', scopes: [], via: 'access' } } })
    await open(page, '#/manage')
    await expect(page.getByTestId('who')).toContainText('viewer')
    await expect(page.getByTestId('manage')).toBeVisible()
    for (const id of ['sel', 'plan', 'prune-open', 'republish', 'archive-delete']) await expect(page.getByTestId(id)).toHaveCount(0)
    await expect(page.getByTestId('archive')).toBeVisible() // the archive report is readable
  })

  test('republish is an admin button and only with a workflow (mocked API)', async ({ page }) => {
    const m = await mockApi(page)
    await open(page, '#/manage')
    await page.getByTestId('republish').click()
    await expect.poll(() => m.calls.some((c) => c.path === '/republish')).toBe(true)
    await expect(page.locator('.toast')).toContainText('after the workflow has run')
    const m2 = await mockApi(page)
    m2.overview = overview({ publish: { available: false } })
    await open(page, '#/manage')
    await page.reload()
    await expect(page.getByTestId('manage')).toBeVisible()
    await expect(page.getByTestId('republish')).toHaveCount(0)
  })

  test('archive: report problems vs normal, and a separate loud deletion flow (mocked API)', async ({ page }) => {
    const m = await mockApi(page)
    await open(page, '#/manage')
    const arch = page.getByTestId('archive')
    await expect(page.getByTestId('archive-verdict')).toContainText('1 problem found')
    await expect(arch.getByTestId('archive-problem')).toContainText('web-gateway-2.4.0')
    await expect(arch).toContainText('Pruned from the site, still archived (1): normal')
    // the Pages prune dialog never offers archive deletion
    await row(table(page, A), '0.2.0').getByTestId('sel').check()
    await page.getByTestId('prune-open').click()
    await expect(page.getByTestId('prune-dialog')).not.toContainText('Delete from archive')
    await page.getByTestId('prune-close').click()

    await expect(page.getByTestId('archive-open')).toBeDisabled()
    const list = page.getByTestId('archive-list')
    await expect(list).toContainText('hello-0.0.9')
    await expect(list).not.toContainText('hello-0.0.1') // archive already deleted
    await list.getByLabel('Select archived hello 0.0.9').check()
    await page.getByTestId('archive-open').click()
    const dlg = page.getByTestId('archive-dialog')
    await expect(dlg).toContainText('permanently deletes')
    await expect(dlg).toContainText('not part of a Pages cleanup')
    const go = page.getByTestId('archive-go')
    await expect(go).toBeDisabled()
    await page.locator('#archive-confirm').fill('delete from archive')
    await expect(go).toBeDisabled() // reason missing
    await page.locator('#archive-reason').fill('licence withdrawn')
    await expect(go).toBeEnabled()
    await page.locator('#archive-confirm').fill('delete')
    await expect(go).toBeDisabled()
    await page.locator('#archive-confirm').fill('delete from archive')
    await go.click()
    await expect(dlg).toHaveCount(0)
    const call = m.calls.find((c) => c.path === '/archive/delete')!
    expect(call.body).toEqual({ refs: [{ abi: A, name: 'hello', version: '0.0.9' }], reason: 'licence withdrawn', confirm: 'delete from archive' })
  })

  test('archive deletion failure shows the server message (mocked API)', async ({ page }) => {
    await mockApi(page)
    await page.route('**/api/v1/archive/delete', (r) => r.fulfill({ status: 403, contentType: 'application/json', body: '{"error":"role viewer cannot delete from the archive"}' }))
    await open(page, '#/manage')
    await page.getByTestId('archive-list').getByLabel('Select archived hello 0.0.9').check()
    await page.getByTestId('archive-open').click()
    await page.locator('#archive-reason').fill('x')
    await page.locator('#archive-confirm').fill('delete from archive')
    await page.getByTestId('archive-go').click()
    await expect(page.getByTestId('archive-error')).toContainText('role viewer cannot delete from the archive')
  })

  test('overview failures show the server message, 401 asks to sign in (mocked API)', async ({ page }) => {
    await mockApi(page, { who: { status: 401, body: { error: 'sign in' } } })
    await page.route('**/api/v1/overview', (r) => r.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"authentication required"}' }))
    await open(page, '#/manage')
    await expect(page.getByTestId('error')).toContainText('Sign in to see this page')
    await page.unroute('**/api/v1/overview')
    await page.route('**/api/v1/overview', (r) => r.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"index unreadable: boom"}' }))
    await page.getByRole('button', { name: 'Try again' }).click()
    await expect(page.getByTestId('error')).toContainText('index unreadable: boom')
  })

  test('phone-friendly: no sideways page scroll with the view and the dialogs open (mocked API)', async ({ page }) => {
    await mockApi(page)
    await open(page, '#/manage')
    await expect(page.getByTestId('manage')).toBeVisible()
    const over = () => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(await over()).toBeLessThanOrEqual(0)
    await row(table(page, A), '0.2.0').getByTestId('sel').check()
    await page.getByTestId('prune-open').click()
    await expect(page.getByTestId('prune-preview')).toBeVisible()
    expect(await over()).toBeLessThanOrEqual(0)
    const box = await page.getByRole('dialog').boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  })
})
