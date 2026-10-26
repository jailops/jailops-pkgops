// The only module that talks to the network. The token lives in this module's
// memory and nowhere else: not in storage, not in the URL, not in a cookie.
import type { ArchiveDeleteRequest, ArchiveRef, Detail, Overview, Package, PruneRequest, PruneResult, PrunePlanRequest, SessionInfo, Status } from './types'

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
  get kind(): 'unauthorized' | 'forbidden' | 'notfound' | 'conflict' | 'invalid' | 'network' | 'server' {
    switch (this.status) {
      case 0: return 'network'
      case 401: return 'unauthorized'
      case 403: return 'forbidden'
      case 404: return 'notfound'
      case 409: return 'conflict'
      case 400: case 413: case 422: return 'invalid'
      default: return 'server'
    }
  }
}

let token = ''
export function setToken(t: string): void { token = t }
export function hasToken(): boolean { return token !== '' }

// A string body is JSON (the upload sends a File).
const json = (v: unknown): BodyInit => JSON.stringify(v)

async function call<T>(method: string, path: string, body?: BodyInit, auth = false): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (typeof body === 'string') headers['Content-Type'] = 'application/json'
  if (auth && token) headers.Authorization = `Bearer ${token}`
  let resp: Response
  try {
    resp = await fetch(path, { method, headers, body, credentials: auth ? 'same-origin' : 'omit', cache: 'no-store' })
  } catch {
    throw new ApiError(0, 'The registry cannot be reached.')
  }
  if (resp.ok) {
    if (resp.status === 204) return undefined as T
    try {
      return (await resp.json()) as T
    } catch {
      throw new ApiError(502, 'The registry sent an answer the UI cannot read.')
    }
  }
  let message = ''
  try {
    message = ((await resp.json()) as { error?: string }).error ?? ''
  } catch { /* not JSON (a proxy error page): fall back to the status text below */ }
  throw new ApiError(resp.status, message || resp.statusText || `HTTP ${resp.status}`)
}

const seg = encodeURIComponent

export const api = {
  status: () => call<Status>('GET', '/api/v1/status'),
  packages: () => call<{ packages: Package[] }>('GET', '/api/v1/packages').then((r) => r.packages),
  detail: (abi: string, name: string, version: string) =>
    call<Detail>('GET', `/api/v1/packages/${seg(abi)}/${seg(name)}/${seg(version)}`),
  session: () => call<SessionInfo>('GET', '/api/v1/session', undefined, true),
  remove: (abi: string, name: string, version: string) =>
    call<void>('DELETE', `/api/v1/packages/${seg(abi)}/${seg(name)}/${seg(version)}`, undefined, true),
  overview: () => call<Overview>('GET', '/api/v1/overview', undefined, true),
  prunePlan: (req: PrunePlanRequest) => call<PruneResult>('POST', '/api/v1/prune/plan', json(req), true),
  prune: (req: PruneRequest) => call<PruneResult>('POST', '/api/v1/prune', json(req), true),
  pruneCancel: (refs: ArchiveRef[]) => call<void>('POST', '/api/v1/prune/cancel', json({ refs }), true),
  republish: () => call<unknown>('POST', '/api/v1/republish', json({}), true),
  archiveDelete: (req: ArchiveDeleteRequest) => call<void>('POST', '/api/v1/archive/delete', json(req), true),
  upload: (file: File) => call<{ package: Package; pruned: string[] }>('PUT', '/api/v1/packages', file, true),
}

// A sentence for people. Raw status codes and JSON never reach the screen.
export function describe(e: unknown, action = 'complete the request'): string {
  if (!(e instanceof ApiError)) return `Could not ${action}.`
  switch (e.kind) {
    case 'network': return 'The registry cannot be reached. Check your connection and try again.'
    case 'unauthorized': return 'Your token was not accepted. Sign in again.'
    case 'forbidden': return 'Your role does not allow this. Ask an administrator.'
    case 'notfound': return 'That item no longer exists. It may have been deleted.'
    case 'conflict': return `${e.message}`
    case 'invalid': return `The registry rejected the request: ${e.message}`
    default: return `The registry reported a problem while trying to ${action}. Try again, or check its log.`
  }
}

// For the admin views: the server's own message is the explanation, so show it
// (with a short lead-in for the two sign-in states).
export function describeAdmin(e: unknown, action = 'complete the request'): string {
  if (!(e instanceof ApiError)) return `Could not ${action}.`
  switch (e.kind) {
    case 'network': return describe(e, action)
    case 'unauthorized': return `Not signed in: ${e.message}. Sign in with a registry token or through your organisation's access gateway.`
    case 'forbidden': return `Not allowed: ${e.message}`
    default: return e.message
  }
}
