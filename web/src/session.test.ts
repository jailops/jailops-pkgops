import { afterEach, describe, expect, it, vi } from 'vitest'
import { hasToken } from './api'
import { session } from './session'

afterEach(() => { vi.unstubAllGlobals(); session.signOut() })
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status })

describe('session', () => {
  it('derives permissions from the role', async () => {
    for (const [role, up, del] of [['viewer', false, false], ['uploader', true, false], ['admin', true, true]] as const) {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(200, { name: 'n', role, scopes: [] })))
      await session.signIn(' tok ')
      expect(session.canUpload.value).toBe(up)
      expect(session.canDelete.value).toBe(del)
    }
  })
  it('a rejected token leaves no session and no token behind', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(401, { error: 'invalid token' })))
    await expect(session.signIn('bad')).rejects.toBeTruthy()
    expect(session.signedIn.value).toBe(false)
    expect(hasToken()).toBe(false)
  })
  it('signing out forgets the token', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(200, { name: 'n', role: 'admin', scopes: [] })))
    await session.signIn('tok')
    session.signOut()
    expect(hasToken()).toBe(false)
    expect(session.canDelete.value).toBe(false)
  })
  it('never writes the token to web storage', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(200, { name: 'n', role: 'admin', scopes: [] })))
    await session.signIn('super-secret-token')
    expect(JSON.stringify({ ...localStorage, ...sessionStorage })).not.toContain('super-secret-token')
  })
})

describe('session behind an access gateway', () => {
  it('probe without a token: 200 is the gateway identity, no token involved', async () => {
    const f = vi.fn().mockResolvedValue(json(200, { name: 'alice@example.com', role: 'admin', scopes: [], via: 'access' }))
    vi.stubGlobal('fetch', f)
    await session.probe()
    expect((f.mock.calls[0]![1] as RequestInit).headers).not.toHaveProperty('Authorization')
    expect(session.viaAccess.value).toBe(true)
    expect(session.canAdmin.value).toBe(true)
    expect(session.ready.value).toBe(true)
    expect(hasToken()).toBe(false)
  })
  it('probe: 401 means anonymous, token sign-in stays', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(401, { error: 'sign in' })))
    await session.probe()
    expect(session.signedIn.value).toBe(false)
    expect(session.denied.value).toBe('')
    expect(session.ready.value).toBe(true)
  })
  it('probe: 403 is an identity that is not allowed', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(403, { error: 'identity bob@example.com is not allowed' })))
    await session.probe()
    expect(session.signedIn.value).toBe(false)
    expect(session.denied.value).toMatch(/bob@example.com/)
  })
  it('probe: an unreachable server leaves the visitor anonymous', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('x')))
    await session.probe()
    expect(session.signedIn.value).toBe(false)
    expect(session.denied.value).toBe('')
  })
})
