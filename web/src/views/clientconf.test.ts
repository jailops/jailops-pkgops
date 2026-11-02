// @vitest-environment-options {"url": "https://admin.example.org/ui/"}
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Status } from '../types'
import OverviewView from './OverviewView.vue'
import StatusView from './StatusView.vue'

const base: Status = {
  healthy: true, packages: 1, versions: 1, updated: '2026-11-01T00:00:00Z', signature: 'pubkey', fingerprint: 'ab'.repeat(32),
  abis: [{ abi: 'FreeBSD:15:amd64', arch: 'amd64', channel: 'latest', packages: 1, versions: 1, updated: '2026-11-01T00:00:00Z', signature: 'pubkey', consistent: true }],
  uploads: false, keep: 0, channel: 'latest',
} as Status

function serve(st: Partial<Status>) {
  vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => new Response(JSON.stringify({ ...base, ...st }), { status: 200 })))
}

afterEach(() => { vi.unstubAllGlobals() })

const sourceMode = { mode: 'source', serves_repository: false, download_url: 'https://pkg.example.org', key_url: 'https://pkg.example.org/jailops.pub' } as Partial<Status>

describe('client configuration in read-through mode', () => {
  for (const [name, view] of [['status', StatusView], ['overview', OverviewView]] as const) {
    it(`${name}: points pkg at the published site, not at the admin host`, async () => {
      serve({ ...sourceMode, verification: 'verified', key_source: 'site' } as Partial<Status>)
      const w = mount(view)
      await flushPromises()
      const text = w.text()
      expect(text).toContain('url: "https://pkg.example.org/${ABI}/latest"')
      expect(text).not.toContain('admin.example.org')
      expect(w.find('[data-test="key-link"]').attributes('href')).toBe('https://pkg.example.org/jailops.pub')
      w.unmount()
    })

    it(`${name}: local mode uses the address of this server`, async () => {
      serve({ mode: 'local', serves_repository: true, uploads: true, verification: 'verified', key_source: 'signer' } as Partial<Status>)
      const w = mount(view)
      await flushPromises()
      expect(w.text()).toContain('url: "https://admin.example.org/${ABI}/latest"')
      expect(w.find('[data-test="key-link"]').attributes('href')).toBe('/api/v1/key')
      w.unmount()
    })

    it(`${name}: a read-through server without a download URL sends nobody to itself`, async () => {
      serve({ mode: 'source', serves_repository: false } as Partial<Status>)
      const w = mount(view)
      await flushPromises()
      expect(w.text()).not.toContain('admin.example.org/${ABI}')
      expect(w.find('[data-test="no-download-url"]').exists()).toBe(true)
      w.unmount()
    })
  }

  it('overview: upload steps only where uploads go to this server', async () => {
    serve({ ...sourceMode, abis: [], packages: 0, versions: 0 } as Partial<Status>)
    let w = mount(OverviewView)
    await flushPromises()
    expect(w.find('[data-test="upload-steps"]').exists()).toBe(false)
    expect(w.find('[data-test="publish-elsewhere"]').text()).toContain('https://pkg.example.org')
    w.unmount()
    serve({ mode: 'local', serves_repository: true, uploads: true, abis: [], packages: 0, versions: 0 } as Partial<Status>)
    w = mount(OverviewView)
    await flushPromises()
    expect(w.find('[data-test="upload-steps"]').text()).toContain('--url https://admin.example.org')
    w.unmount()
  })
})

describe('signature state', () => {
  const cases: [Partial<Status>, string][] = [
    [{ verification: 'verified', key_source: 'configured' }, 'Verified'],
    [{ verification: 'unverified', fingerprint: '', notes: ['The catalogs are signed, but the signatures were not checked'] }, 'Signed, not verified'],
    [{ verification: 'invalid', healthy: false, problems: ['FreeBSD:15:amd64: signature does not verify'] }, 'Invalid signature'],
    [{ verification: 'unsigned', signature: 'unsigned', fingerprint: '' }, 'Unsigned'],
  ]
  for (const [st, label] of cases) {
    it(`shows ${label}`, async () => {
      serve({ ...sourceMode, ...st })
      const w = mount(StatusView)
      await flushPromises()
      expect(w.find('[data-test="sig"]').text()).toBe(label)
      if (st.verification !== 'verified') expect(w.find('[data-test="health"]').text()).not.toContain('verify against the public key')
      if (st.notes) expect(w.find('[data-test="notes"]').text()).toContain('not checked')
      w.unmount()
    })
  }
  it('says when the key came from the site itself', async () => {
    serve({ ...sourceMode, verification: 'verified', key_source: 'site', notes: ['The public key was read from the same site as the catalogs.'] })
    const w = mount(StatusView)
    await flushPromises()
    expect(w.find('[data-test="key-source"]').text()).toContain('weaker')
    expect(w.find('[data-test="fingerprint"]').text()).toBe('ab'.repeat(32))
    expect(w.find('[data-test="notes"]').exists()).toBe(true)
    w.unmount()
  })
})
