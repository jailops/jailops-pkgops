import { expect, test } from '@playwright/test'
import { open } from './env'

// On a phone nothing may force the page to scroll sideways: wide tables scroll inside their own wrapper.
for (const hash of ['#/', '#/packages', '#/packages/FreeBSD%3A15%3Aamd64/hello/0.1.1', '#/status', '#/manage']) {
  test(`no horizontal page scroll: ${hash}`, async ({ page }) => {
    await open(page, hash)
    await page.waitForFunction(() => !document.querySelector('.loading'))
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(over).toBeLessThanOrEqual(0)
  })
}

test('controls are big enough to tap on a phone', async ({ page }) => {
  await open(page, '#/packages')
  for (const b of await page.getByRole('button').all()) {
    const box = await b.boundingBox()
    if (box) expect(box.height).toBeGreaterThanOrEqual(26)
  }
})
