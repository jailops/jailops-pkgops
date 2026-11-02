import type { Status, Verification } from './types'

export function bytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '-'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${i === 0 ? v : v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`
}

export function when(iso: string | undefined, now = Date.now()): string {
  if (!iso) return 'never'
  const t = Date.parse(iso)
  if (Number.isNaN(t) || t <= 0 || iso.startsWith('0001')) return 'never'
  const s = Math.round((now - t) / 1000)
  if (s < 0) return 'just now'
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  if (s < 86400 * 60) return `${Math.floor(s / 86400)} days ago`
  return new Date(t).toISOString().slice(0, 10)
}

export function absolute(iso: string | undefined): string {
  const t = iso ? Date.parse(iso) : NaN
  return Number.isNaN(t) || t <= 0 ? '' : new Date(t).toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC')
}

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}

export function shortHash(h: string): string {
  return h.length > 16 ? `${h.slice(0, 12)}…` : h
}

// The base URL clients install from: the one the server names, else the
// address of this page when this server serves the repository itself; ''
// when neither is known (a read-through server must never send clients to
// itself).
export function downloadBase(st: Pick<Status, 'download_url' | 'serves_repository'>, origin: string): string {
  if (st.download_url) return st.download_url.replace(/\/+$/, '')
  return st.serves_repository ? origin : ''
}

// Where clients get the public key: the published copy when the server
// names one, else this server's own endpoint.
export function keyHref(st: Pick<Status, 'key_url'>): string {
  return st.key_url || '/api/v1/key'
}

export interface Label { text: string; cls: 'ok' | 'warn' | 'danger' }

export function verificationLabel(v: Verification | undefined, signature: string): Label {
  switch (v) {
    case 'verified': return { text: 'Verified', cls: 'ok' }
    case 'invalid': return { text: 'Invalid signature', cls: 'danger' }
    case 'unverified': return { text: 'Signed, not verified', cls: 'warn' }
    case 'signed': return { text: 'Signed, not checked', cls: 'warn' }
    case 'unsigned': return { text: 'Unsigned', cls: 'warn' }
  }
  // an older server without the field: only say what it said
  return signature === 'unsigned' ? { text: 'Unsigned', cls: 'warn' } : { text: 'Signed', cls: 'warn' }
}

export function keySourceText(src: Status['key_source']): string {
  switch (src) {
    case 'configured': return 'configured on this server'
    case 'site': return 'read from the published site itself (a weaker trust)'
    case 'signer': return 'this server\'s signing key'
  }
  return 'none'
}

// A client configuration the visitor can paste into /usr/local/etc/pkg/repos/NAME.conf.
export function clientConf(origin: string, channel: string, signature: string, name = 'myrepo'): string {
  const lines = [`${name}: {`, `  url: "${origin}/\${ABI}/${channel}",`]
  if (signature === 'pubkey') {
    lines.push('  signature_type: "pubkey",', `  pubkey: "/usr/local/etc/pkg/keys/${name}.pub",`)
  } else if (signature === 'fingerprints') {
    lines.push('  signature_type: "fingerprints",', `  fingerprints: "/usr/local/etc/pkg/fingerprints/${name}",`)
  }
  lines.push('  enabled: yes', '}')
  return lines.join('\n')
}
