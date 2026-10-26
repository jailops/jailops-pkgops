import { afterEach, describe as suite, expect, it, vi } from 'vitest'
import { ApiError, api, describe, describeAdmin, hasToken, setToken } from './api'

afterEach(() => { vi.unstubAllGlobals(); setToken('') })

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

suite('api client', () => {
  it('sends the token only on authenticated calls and never stores it', async () => {
    const f = vi.fn().mockResolvedValue(json(200, { name: 'ci', role: 'admin', scopes: [] }))
    vi.stubGlobal('fetch', f)
    setToken('secret')
    expect(hasToken()).toBe(true)
    await api.session()
    expect((f.mock.calls[0]![1] as RequestInit).headers).toMatchObject({ Authorization: 'Bearer secret' })
    f.mockResolvedValue(json(200, { packages: [] }))
    await api.packages()
    expect((f.mock.calls[1]![1] as RequestInit).headers).not.toHaveProperty('Authorization')
    expect((f.mock.calls[1]![1] as RequestInit).credentials).toBe('omit')
    expect(JSON.stringify({ ...localStorage })).not.toContain('secret')
    expect(JSON.stringify({ ...sessionStorage })).not.toContain('secret')
  })

  it('encodes ABI, name and version in the path', async () => {
    const f = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', f)
    await api.remove('FreeBSD:15:amd64', 'a b', '1.0_1,2')
    expect(f.mock.calls[0]![0]).toBe('/api/v1/packages/FreeBSD%3A15%3Aamd64/a%20b/1.0_1%2C2')
    expect((f.mock.calls[0]![1] as RequestInit).method).toBe('DELETE')
  })

  it('turns failures into typed errors and people-friendly sentences', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(403, { error: 'token lacks scope "delete" (role uploader)' })))
    const e = await api.remove('FreeBSD:15:amd64', 'x', '1').catch((x) => x)
    expect(e).toBeInstanceOf(ApiError)
    expect(e.kind).toBe('forbidden')
    expect(describe(e, 'delete')).toBe('Your role does not allow this. Ask an administrator.')
    expect(describe(e)).not.toMatch(/\{|\}|403/)

    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const n = await api.status().catch((x) => x)
    expect(n.kind).toBe('network')
    expect(describe(n)).toMatch(/cannot be reached/)

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>Bad gateway</html>', { status: 502 })))
    const g = await api.status().catch((x) => x)
    expect(g.status).toBe(502)
    expect(describe(g, 'load the status')).not.toMatch(/html|Bad gateway/)

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(401, { error: 'invalid token' })))
    expect(describe(await api.session().catch((x) => x))).toMatch(/Sign in again/)
    expect(describe(new Error('boom'), 'do it')).toBe('Could not do it.')
  })

  it('keeps conflict messages from the registry (they say what to do)', () => {
    expect(describe(new ApiError(409, 'hello-1.0.pkg already exists with different content; bump the version'))).toMatch(/bump the version/)
  })
})

suite('admin api client', () => {
  it('reads the overview with the token and the gateway cookie, not for public calls', async () => {
    const f = vi.fn().mockResolvedValue(json(200, { versions: [] }))
    vi.stubGlobal('fetch', f)
    setToken('t')
    await api.overview()
    const init = f.mock.calls[0]![1] as RequestInit
    expect(f.mock.calls[0]![0]).toBe('/api/v1/overview')
    expect(init.headers).toMatchObject({ Authorization: 'Bearer t' })
    expect(init.credentials).toBe('same-origin')
  })
  it('posts JSON to each endpoint', async () => {
    const f = vi.fn().mockImplementation(async () => json(200, {}))
    vi.stubGlobal('fetch', f)
    await api.prunePlan({ abi: 'A', keep: 2 })
    await api.prune({ entries: [{ abi: 'A', name: 'n', version: '1' }], dry_run: true, confirm: 'prune' })
    await api.republish()
    f.mockResolvedValue(new Response(null, { status: 204 }))
    await api.pruneCancel([{ abi: 'A', name: 'n', version: '1' }])
    await api.archiveDelete({ refs: [{ abi: 'A', name: 'n', version: '1' }], reason: 'r', confirm: 'delete from archive' })
    const urls = f.mock.calls.map((c) => c[0])
    expect(urls).toEqual(['/api/v1/prune/plan', '/api/v1/prune', '/api/v1/republish', '/api/v1/prune/cancel', '/api/v1/archive/delete'])
    for (const c of f.mock.calls) {
      const init = c[1] as RequestInit
      expect(init.method).toBe('POST')
      expect(init.headers).toMatchObject({ 'Content-Type': 'application/json' })
    }
    const body = async (i: number) => JSON.parse((f.mock.calls[i]![1] as RequestInit).body as string)
    expect(await body(0)).toEqual({ abi: 'A', keep: 2 })
    expect(await body(1)).toEqual({ entries: [{ abi: 'A', name: 'n', version: '1' }], dry_run: true, confirm: 'prune' })
    expect(await body(3)).toEqual({ refs: [{ abi: 'A', name: 'n', version: '1' }] })
  })
  it('shows the server message for admin failures', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(422, { error: 'hello-3 is the newest version; set force to prune it' })))
    const e = await api.prune({ entries: [], confirm: 'prune' }).catch((x) => x)
    expect(describeAdmin(e)).toBe('hello-3 is the newest version; set force to prune it')
    expect(describeAdmin(new ApiError(403, 'role viewer cannot prune'))).toBe('Not allowed: role viewer cannot prune')
    expect(describeAdmin(new ApiError(401, 'no token'))).toMatch(/^Not signed in: no token/)
    expect(describeAdmin(new ApiError(0, 'x'))).toMatch(/cannot be reached/)
  })
})
