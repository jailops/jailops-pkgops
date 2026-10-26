// Mocked /api/v1 for the flows a local-dir registry cannot produce: GitHub mode, the archive,
// Cloudflare Access identities. Tests that use this say "(mocked API)" in their name.
import type { Page, Route } from '@playwright/test'

const DAY = 86400_000
const ago = (d: number) => new Date(Date.now() - d * DAY).toISOString()
const A = 'FreeBSD:15:amd64'
const B = 'FreeBSD:15:aarch64'

export interface Mock {
  overview: Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any
  who: { status: number; body: unknown }
  calls: { path: string; body: any }[] // eslint-disable-line @typescript-eslint/no-explicit-any
  overviewHits: number
  onPrune?: (body: any, dry: boolean) => { status: number; body: unknown } // eslint-disable-line @typescript-eslint/no-explicit-any
  plan?: (body: any) => unknown // eslint-disable-line @typescript-eslint/no-explicit-any
}

export const EFFECT = 'Removed from the next deploy. The published site is an artifact rebuilt from the baseline plus the new packages, so no binary lives in git history and nothing there needs rewriting. Until the publish workflow has run, the files are still served. The archive (GitHub Releases) is not touched.'

const v = (abi: string, name: string, version: string, size: number, days: number, o: Record<string, unknown> = {}) =>
  ({ abi, name, version, size, built: ago(days), min_release: '15.1', state: 'active', archive: 'archived', ...o })

export function overview(o: Record<string, any> = {}): Record<string, any> { // eslint-disable-line @typescript-eslint/no-explicit-any
  const versions = [
    v(A, 'hello', '0.3.0', 3_200_000, 2, { newest: true }),
    v(A, 'hello', '0.2.0', 3_100_000, 40),
    v(A, 'hello', '0.1.0', 3_000_000, 90),
    v(A, 'hello', '0.0.9', 2_900_000, 200, { state: 'pruned', phase: 'published', archive: 'archived', pruned_at: ago(30) }),
    v(A, 'web-gateway', '2.4.0', 12_500_000, 5, { newest: true, archive: 'not-archived' }),
    v(B, 'hello', '0.3.0', 3_000_000, 2, { newest: true }),
    v(B, 'hello', '0.2.0', 2_900_000, 40, { state: 'archive-only', built: undefined, archive: 'archived' }),
    v(B, 'hello', '0.0.1', 2_000_000, 300, { state: 'pruned', phase: 'published', archive: 'archive-deleted' }),
  ]
  return {
    mode: 'source', source: 'https://packages.example.org',
    policy: { abis: [A, B], min_release: '15.1', keep: 3 },
    status: { healthy: true, packages: 3, versions: 5, updated: ago(1), signature: 'pubkey', fingerprint: 'ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12', problems: [], uploads: false, keep: 3, channel: 'latest',
      abis: [A, B].map((abi) => ({ abi, arch: abi.split(':')[2], channel: 'latest', packages: 2, versions: 3, updated: ago(1), signature: 'pubkey', consistent: true })) },
    abis: [
      { abi: A, packages: 2, versions: 4, bytes: 3_200_000 + 3_100_000 + 3_000_000 + 12_500_000, pending: 0, pruned: 1 },
      { abi: B, packages: 1, versions: 1, bytes: 3_000_000, pending: 0, pruned: 1 },
    ],
    versions, bytes: 3_200_000 + 3_100_000 + 3_000_000 + 12_500_000 + 3_000_000,
    prune: { mode: 'github', repo: 'example/packages', branch: 'master', file: 'prune.yaml' },
    publish: { available: true, workflow: 'publish.yml', runs: [
      { id: 9001, status: 'completed', conclusion: 'success', url: 'https://github.com/example/packages/actions/runs/9001', created: ago(1) },
      { id: 8990, status: 'completed', conclusion: 'failure', url: 'https://github.com/example/packages/actions/runs/8990', created: ago(3) },
    ] },
    archive: { available: true, repo: 'example/packages', files: 6, report: {
      on_pages_not_archived: [`${A} web-gateway-2.4.0`], archived_not_on_pages: [], mismatched: [],
      pruned_still_archived: [`${A} hello-0.0.9`], pruned_not_archived: [], ok: false } },
    ...o,
  }
}

export function mockApi(page: Page, init: Partial<Mock> = {}): Promise<Mock> {
  const m: Mock = { overview: overview(), who: { status: 200, body: { name: 'alice@example.com', role: 'admin', scopes: ['admin'], via: 'access' } }, calls: [], overviewHits: 0, ...init }
  const json = (r: Route, status: number, body: unknown) => r.fulfill({ status, contentType: 'application/json', body: status === 204 ? '' : JSON.stringify(body) })
  return page.route('**/api/v1/**', async (r) => {
    const req = r.request()
    const path = new URL(req.url()).pathname.replace('/api/v1', '')
    const body = req.method() === 'POST' ? JSON.parse(req.postData() || '{}') : undefined
    if (path === '/session') return json(r, m.who.status, m.who.body)
    if (path === '/overview') { m.overviewHits++; return json(r, 200, m.overview) }
    if (path === '/status') return json(r, 200, m.overview.status)
    if (path === '/packages') return json(r, 200, { packages: [] })
    m.calls.push({ path, body })
    if (path === '/prune/plan') return json(r, 200, m.plan ? m.plan(body) : { mode: 'github', dry_run: true, entries: [], bytes: 0, diff: '', effect: EFFECT })
    if (path === '/prune') {
      const res = m.onPrune?.(body, !!body.dry_run) ?? { status: 200, body: prunedResult(body, !!body.dry_run) }
      return json(r, res.status, res.body)
    }
    if (path === '/republish') return json(r, 202, { available: true, runs: [] })
    if (path === '/archive/delete') return json(r, 204, null)
    return json(r, 404, { error: 'not mocked: ' + path })
  }).then(() => m)
}

export function prunedResult(body: any, dry: boolean) { // eslint-disable-line @typescript-eslint/no-explicit-any
  const sizes = new Map<string, number>(overview().versions.map((x: any) => [`${x.abi} ${x.name} ${x.version}`, x.size as number])) // eslint-disable-line @typescript-eslint/no-explicit-any
  const bytes = body.entries.reduce((n: number, e: any) => n + (sizes.get(`${e.abi} ${e.name} ${e.version}`) ?? 0), 0) // eslint-disable-line @typescript-eslint/no-explicit-any
  return { mode: 'github', dry_run: dry, entries: body.entries, bytes, diff: '--- a/prune.yaml\n+++ b/prune.yaml', effect: EFFECT, message: 'Prune: x',
    ...(dry ? {} : { commit: 'f00dbabe12345678', commit_url: 'https://github.com/example/packages/commit/f00dbabe12345678' }) }
}
