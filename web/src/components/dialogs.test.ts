import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setToken } from '../api'
import ArchiveDeleteDialog from './ArchiveDeleteDialog.vue'
import DeleteDialog from './DeleteDialog.vue'
import LoginDialog from './LoginDialog.vue'
import PruneDialog from './PruneDialog.vue'
import StatePanel from './StatePanel.vue'
import type { Version } from '../types'

afterEach(() => { vi.unstubAllGlobals(); setToken(''); document.body.innerHTML = '' })
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status })
const attach = { attachTo: document.body }

describe('DeleteDialog', () => {
  const props = { abi: 'FreeBSD:15:amd64', name: 'hello', version: '0.1.0', latest: true, onlyVersion: false }

  it('needs the package name typed before the delete button works', async () => {
    const f = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', f)
    const w = mount(DeleteDialog, { props, ...attach })
    const btn = () => w.find('button.btn-danger')
    expect(btn().attributes('disabled')).toBeDefined()
    await w.find('input').setValue('hell')
    expect(btn().attributes('disabled')).toBeDefined()
    await btn().trigger('click')
    expect(f).not.toHaveBeenCalled()
    await w.find('input').setValue('hello')
    expect(btn().attributes('disabled')).toBeUndefined()
    await btn().trigger('click')
    await flushPromises()
    expect(f).toHaveBeenCalledOnce()
    expect(f.mock.calls[0]![0]).toBe('/api/v1/packages/FreeBSD%3A15%3Aamd64/hello/0.1.0')
    expect(w.emitted('deleted')).toBeTruthy()
  })

  it('warns about the newest and the only version', () => {
    expect(mount(DeleteDialog, { props, ...attach }).text()).toMatch(/newest version/)
    expect(mount(DeleteDialog, { props: { ...props, onlyVersion: true }, ...attach }).text()).toMatch(/only version/)
  })

  it('shows a sentence, not JSON, when the registry refuses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(403, { error: 'token lacks scope "delete"' })))
    const w = mount(DeleteDialog, { props, ...attach })
    await w.find('input').setValue('hello')
    await w.find('button.btn-danger').trigger('click')
    await flushPromises()
    expect(w.text()).toMatch(/Your role does not allow this/)
    expect(w.text()).not.toMatch(/scope|403|\{/)
    expect(w.emitted('deleted')).toBeFalsy()
  })

  it('moves focus into the dialog and closes on Escape', async () => {
    const w = mount(DeleteDialog, { props, ...attach })
    await flushPromises()
    expect(document.activeElement?.id).toBe('confirm-name')
    await w.find('[role=dialog]').trigger('keydown', { key: 'Escape' })
    expect(w.emitted('close')).toBeTruthy()
    expect(w.find('[role=dialog]').attributes('aria-modal')).toBe('true')
    expect(w.find('[role=dialog]').attributes('aria-labelledby')).toBe('delete-title')
  })
})

describe('LoginDialog', () => {
  it('rejects an empty token locally, labels the field, and reports a bad token calmly', async () => {
    const f = vi.fn().mockResolvedValue(json(401, { error: 'invalid token' }))
    vi.stubGlobal('fetch', f)
    const w = mount(LoginDialog, attach)
    expect(w.find('label[for=token]').exists()).toBe(true)
    expect(w.find('input#token').attributes('type')).toBe('password')
    await w.find('form').trigger('submit')
    expect(w.text()).toMatch(/Enter a token/)
    expect(f).not.toHaveBeenCalled()
    await w.find('input').setValue('nope')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).toMatch(/not accepted/)
    expect(w.emitted('close')).toBeFalsy()
  })
  it('closes after a good token', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(200, { name: 'ci', role: 'uploader', scopes: ['upload'] })))
    const w = mount(LoginDialog, attach)
    await w.find('input').setValue('good')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.emitted('close')).toBeTruthy()
  })
})

describe('StatePanel', () => {
  it('renders loading, error with retry, empty, and content', async () => {
    const mk = (props: object) => mount(StatePanel, { props, slots: { default: '<p id="c">content</p>' } })
    expect(mk({ loading: true }).text()).toMatch(/Loading/)
    const e = mk({ error: 'The registry cannot be reached.' })
    expect(e.attributes('role') ?? e.find('[role=alert]').exists()).toBeTruthy()
    await e.find('button').trigger('click')
    expect(e.emitted('retry')).toBeTruthy()
    expect(mk({ empty: true, emptyTitle: 'Nothing here' }).text()).toMatch(/Nothing here/)
    expect(mk({}).find('#c').exists()).toBe(true)
  })
})

describe('PruneDialog', () => {
  const rows: Version[] = [
    { abi: 'FreeBSD:15:amd64', name: 'hello', version: '0.1.0', size: 2048, state: 'active', archive: 'archived' },
    { abi: 'FreeBSD:15:amd64', name: 'hello', version: '0.1.1', size: 4096, state: 'active', archive: 'archived', newest: true },
  ]
  const effect = 'Removed from the next deploy. No binary lives in git history.'
  const res = (o: object = {}) => ({ mode: 'github', dry_run: true, entries: rows.map((r) => ({ abi: r.abi, name: r.name, version: r.version })), bytes: 6144, diff: '', effect, ...o })
  const bodies = (f: ReturnType<typeof vi.fn>) => Promise.all(f.mock.calls.map(async (c) => JSON.parse((c[1] as RequestInit).body as string)))

  it('previews with a dry run first, gates on the typed word, then requests for real', async () => {
    const f = vi.fn().mockImplementation(async (_u: string, init: RequestInit) => {
      const b = JSON.parse(init.body as string)
      return json(200, res(b.dry_run ? {} : { dry_run: false, commit: 'abcdef1234567890', commit_url: 'https://github.com/o/r/commit/abcdef1234567890' }))
    })
    vi.stubGlobal('fetch', f)
    const w = mount(PruneDialog, { props: { rows, mode: 'github', live: rows }, ...attach })
    await flushPromises()
    expect((await bodies(f))[0]).toMatchObject({ dry_run: true, confirm: 'prune' })
    expect(w.text()).toContain(effect) // the server's sentence, not ours
    expect(w.text()).toContain('takes effect after the next deploy')
    expect(w.find('[data-test=prune-bytes]').text()).toBe('6.0 KiB')
    const go = () => w.find('[data-test=prune-go]')
    expect(go().attributes('disabled')).toBeDefined()
    await w.find('#prune-confirm').setValue('Prune')
    expect(go().attributes('disabled')).toBeDefined()
    await w.find('#prune-confirm').setValue('prune')
    expect(go().attributes('disabled')).toBeUndefined()
    await go().trigger('click')
    await flushPromises()
    const sent = await bodies(f)
    expect(sent).toHaveLength(2)
    expect(sent[1].dry_run).toBeUndefined()
    expect(w.find('[data-test=commit-link]').attributes('href')).toBe('https://github.com/o/r/commit/abcdef1234567890')
    expect(w.emitted('requested')).toBeTruthy()
    expect(w.find('[data-test=steps]').exists()).toBe(true)
  })

  it('forcing the newest version is an explicit per-row choice and re-runs the preview', async () => {
    const f = vi.fn().mockImplementation(async () => json(200, res()))
    vi.stubGlobal('fetch', f)
    const w = mount(PruneDialog, { props: { rows, mode: 'github', live: rows }, ...attach })
    await flushPromises()
    expect(w.findAll('[data-test=force]')).toHaveLength(1) // only the newest row
    expect((await bodies(f))[0].entries.map((e: { force?: boolean }) => e.force)).toEqual([undefined, undefined])
    await w.find('[data-test=force]').setValue(true)
    await flushPromises()
    expect((await bodies(f)).at(-1).entries.map((e: { force?: boolean }) => e.force)).toEqual([undefined, true])
  })

  it('shows the server refusal (422) and keeps the button disabled', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(422, { error: 'hello-0.1.1 is the newest version; set force to prune it' })))
    const w = mount(PruneDialog, { props: { rows, mode: 'github', live: rows }, ...attach })
    await flushPromises()
    expect(w.find('[data-test=prune-error]').text()).toContain('is the newest version; set force')
    await w.find('#prune-confirm').setValue('prune')
    expect(w.find('[data-test=prune-go]').attributes('disabled')).toBeDefined()
  })

  it('manual mode says nothing was changed and shows diff, content and commands', async () => {
    const manual = { file: 'prune.yaml', diff: '+  - abi: x', content: 'entries:\n- abi: x', commands: ['git add prune.yaml'] }
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => json(200, res({ mode: 'manual', manual }))))
    const w = mount(PruneDialog, { props: { rows, mode: 'manual', live: rows }, ...attach })
    await flushPromises()
    await w.find('#prune-confirm').setValue('prune')
    await w.find('[data-test=prune-go]').trigger('click')
    await flushPromises()
    const t = w.find('[data-test=prune-manual]').text()
    expect(t).toContain('Nothing was changed')
    expect(t).toContain('+  - abi: x')
    expect(t).toContain('entries:')
    expect(t).toContain('git add prune.yaml')
    expect(w.find('[data-test=steps]').exists()).toBe(false)
  })

  it('the progress strip follows the polled rows', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => json(200, res({ dry_run: false, commit_url: 'https://github.com/o/r/commit/a' }))))
    const w = mount(PruneDialog, { props: { rows, mode: 'github', live: rows }, ...attach })
    await flushPromises()
    await w.find('#prune-confirm').setValue('prune')
    await w.find('[data-test=prune-go]').trigger('click')
    await flushPromises()
    const cur = () => w.find('[aria-current=step]').text()
    expect(cur()).toContain('Requested')
    const pending = (phase: Version['phase']) => rows.map((r) => ({ ...r, state: 'pending-prune' as const, phase }))
    await w.setProps({ live: pending('committed') })
    expect(cur()).toContain('Committed')
    await w.setProps({ live: pending('workflow-running') })
    expect(cur()).toContain('Workflow running')
    await w.setProps({ live: rows.map((r) => ({ ...r, state: 'pruned' as const })) })
    expect(w.find('[aria-current=step]').exists()).toBe(false)
    expect(w.findAll('[data-test=steps] li.done')).toHaveLength(4)
  })
})

describe('ArchiveDeleteDialog', () => {
  const rows: Version[] = [{ abi: 'A', name: 'old', version: '1', size: 10, state: 'pruned', archive: 'archived' }]
  it('is a separate, loud dialog: needs a reason and the exact words', async () => {
    const f = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', f)
    const w = mount(ArchiveDeleteDialog, { props: { rows }, ...attach })
    expect(w.text()).toMatch(/permanently deletes/)
    expect(w.text()).toMatch(/not.* part of a Pages cleanup/)
    const go = () => w.find('[data-test=archive-go]')
    await w.find('#archive-confirm').setValue('delete from archive')
    expect(go().attributes('disabled')).toBeDefined() // no reason yet
    await w.find('#archive-reason').setValue('licence problem')
    expect(go().attributes('disabled')).toBeUndefined()
    await w.find('#archive-confirm').setValue('prune')
    expect(go().attributes('disabled')).toBeDefined()
    await w.find('#archive-confirm').setValue('delete from archive')
    await go().trigger('click')
    await flushPromises()
    expect(f.mock.calls[0]![0]).toBe('/api/v1/archive/delete')
    expect(JSON.parse((f.mock.calls[0]![1] as RequestInit).body as string)).toEqual({ refs: [{ abi: 'A', name: 'old', version: '1' }], reason: 'licence problem', confirm: 'delete from archive' })
    expect(w.emitted('deleted')).toBeTruthy()
  })
  it('shows the server message on failure', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(403, { error: 'role viewer cannot delete' })))
    const w = mount(ArchiveDeleteDialog, { props: { rows }, ...attach })
    await w.find('#archive-reason').setValue('x')
    await w.find('#archive-confirm').setValue('delete from archive')
    await w.find('[data-test=archive-go]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=archive-error]').text()).toContain('role viewer cannot delete')
    expect(w.emitted('deleted')).toBeFalsy()
  })
})
