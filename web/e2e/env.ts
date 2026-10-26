import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { Page } from '@playwright/test'

export interface Env { base: string; dir: string; bin: string; repo: string; fingerprint: string; secrets: { viewer: string; uploader: string; admin: string }; pkgs: string[] }
export const env = (): Env => JSON.parse(readFileSync(join(import.meta.dirname, '.env.json'), 'utf8'))

export async function open(page: Page, hash = '#/') {
  await page.goto(`${env().base}/ui/${hash}`)
  await page.waitForSelector('main#content')
}

export async function signIn(page: Page, who: 'viewer' | 'uploader' | 'admin') {
  if (await page.getByTestId('who').count()) return // same document, still signed in
  await page.getByTestId('signin').click()
  await page.getByLabel('Registry token').fill(env().secrets[who])
  await page.getByRole('button', { name: 'Sign in', exact: true }).last().click()
  await page.getByTestId('who').waitFor()
}

// Console errors and CSP violations fail a test: collect them per page.
export function watchErrors(page: Page): string[] {
  const errs: string[] = []
  page.on('pageerror', (e) => errs.push(String(e)))
  page.on('console', (m) => {
    // the browser logs every 4xx fetch; a rejected token is an expected 401 here
    if ((m.type() === 'error' || m.type() === 'warning') && !/status of 401/.test(m.text())) errs.push(m.text())
  })
  return errs
}
