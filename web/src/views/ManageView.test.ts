import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setToken } from '../api'
import { session } from '../session'
import type { Overview } from '../types'
import ManageView from './ManageView.vue'

const base = (state: 'active' | 'pending-prune'): Overview => ({
  mode: 'local', abis: [{ abi: 'A', packages: 1, versions: 1, bytes: 10, pending: 0, pruned: 0 }], bytes: 10,
  versions: [{ abi: 'A', name: 'p', version: '1', size: 10, state, phase: state === 'pending-prune' ? 'committed' : undefined, archive: 'unknown', newest: true }],
  prune: { mode: 'manual' }, publish: { available: false }, archive: { available: false, files: 0 },
})
const asRole = async (role: 'viewer' | 'admin') => {
  vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => new Response(JSON.stringify({ name: 'n', role, scopes: [] }), { status: 200 })))
  await session.signIn('t')
}

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); setToken(''); session.signOut(); document.body.innerHTML = '' })

function serve(o: () => Overview) {
  const f = vi.fn().mockImplementation(async () => new Response(JSON.stringify(o()), { status: 200 }))
  vi.stubGlobal('fetch', f)
  return f
}

describe('ManageView polling', () => {
  it('polls every 15 s while a row is pending, and stops with the view', async () => {
    const f = serve(() => base('pending-prune'))
    const w = mount(ManageView, { attachTo: document.body })
    await flushPromises()
    expect(f).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(14_000)
    expect(f).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1_500)
    expect(f).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(15_000)
    expect(f).toHaveBeenCalledTimes(3)
    w.unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(f).toHaveBeenCalledTimes(3) // no timer leaks between routes
  })
  it('does not poll when nothing is pending, and stops once the cleanup is done', async () => {
    let state: 'active' | 'pending-prune' = 'pending-prune'
    const f = serve(() => base(state))
    const w = mount(ManageView, { attachTo: document.body })
    await flushPromises()
    state = 'active'
    await vi.advanceTimersByTimeAsync(15_100)
    expect(f).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(f).toHaveBeenCalledTimes(2)
    w.unmount()
  })
  it('shows actions to admins only', async () => {
    serve(() => base('active'))
    await asRole('viewer')
    serve(() => base('active'))
    let w = mount(ManageView, { attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test=sel]').exists()).toBe(false)
    expect(w.find('[data-test=plan]').exists()).toBe(false)
    expect(w.find('[data-test=readonly]').text()).toMatch(/viewer/)
    w.unmount()
    await asRole('admin')
    serve(() => base('active'))
    w = mount(ManageView, { attachTo: document.body })
    await flushPromises()
    expect(w.find('[data-test=sel]').exists()).toBe(true)
    expect(w.find('[data-test=plan]').exists()).toBe(true)
    w.unmount()
  })
})
