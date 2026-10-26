// A tiny hash router: the registry serves the UI as static files, so routes
// live after the # and need no server cooperation.
import { computed, ref } from 'vue'

const hash = ref(location.hash || '#/')
window.addEventListener('hashchange', () => { hash.value = location.hash || '#/' })

export interface Route { name: 'overview' | 'packages' | 'package' | 'status' | 'manage' | 'notfound'; params: string[]; query: URLSearchParams }

export const route = computed<Route>(() => {
  const [path = '/', q = ''] = hash.value.replace(/^#/, '').split('?')
  const query = new URLSearchParams(q)
  const parts = path.split('/').filter(Boolean).map((p) => { try { return decodeURIComponent(p) } catch { return p } })
  if (parts.length === 0) return { name: 'overview', params: [], query }
  if (parts[0] === 'packages' && parts.length === 1) return { name: 'packages', params: [], query }
  if (parts[0] === 'packages' && parts.length === 4) return { name: 'package', params: parts.slice(1), query }
  if (parts[0] === 'status' && parts.length === 1) return { name: 'status', params: [], query }
  if (parts[0] === 'manage' && parts.length === 1) return { name: 'manage', params: [], query }
  return { name: 'notfound', params: [], query }
})

export function href(path: string, query?: Record<string, string>): string {
  const q = query ? Object.entries(query).filter(([, v]) => v !== '').map(([k, v]) => `${k}=${encodeURIComponent(v)}`).join('&') : ''
  return `#${path}${q ? '?' + q : ''}`
}

export function pkgHref(abi: string, name: string, version: string): string {
  return href(`/packages/${encodeURIComponent(abi)}/${encodeURIComponent(name)}/${encodeURIComponent(version)}`)
}

export function go(h: string): void { location.hash = h }
export function replaceQuery(path: string, query: Record<string, string>): void {
  const h = href(path, query)
  history.replaceState(null, '', h)
  hash.value = h
}
