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
