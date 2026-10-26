import { describe, expect, it } from 'vitest'
import { absolute, bytes, clientConf, plural, shortHash, when } from './format'

describe('format', () => {
  it('bytes', () => {
    expect(bytes(0)).toBe('0 B')
    expect(bytes(1536)).toBe('1.5 KiB')
    expect(bytes(5 * 1024 * 1024)).toBe('5.0 MiB')
    expect(bytes(-1)).toBe('-')
  })
  it('when', () => {
    const now = Date.parse('2026-10-25T12:00:00Z')
    expect(when('2026-10-25T11:59:30Z', now)).toBe('30s ago')
    expect(when('2026-10-25T11:00:00Z', now)).toBe('1 h ago')
    expect(when('2026-10-20T12:00:00Z', now)).toBe('5 days ago')
    expect(when('2025-01-01T00:00:00Z', now)).toBe('2025-01-01')
    expect(when('0001-01-01T00:00:00Z', now)).toBe('never')
    expect(when(undefined)).toBe('never')
    expect(absolute('2026-10-25T12:00:00.123Z')).toBe('2026-10-25 12:00:00 UTC')
  })
  it('plural and hash', () => {
    expect(plural(1, 'package')).toBe('1 package')
    expect(plural(2, 'package')).toBe('2 packages')
    expect(shortHash('a'.repeat(64))).toHaveLength(13)
  })
  it('clientConf matches what pkg expects', () => {
    const c = clientConf('https://pkg.example.org', 'latest', 'pubkey')
    expect(c).toContain('url: "https://pkg.example.org/${ABI}/latest"')
    expect(c).toContain('signature_type: "pubkey"')
    expect(clientConf('https://x', 'latest', 'fingerprints')).toContain('signature_type: "fingerprints"')
    expect(clientConf('https://x', 'latest', 'unsigned')).not.toContain('signature_type')
  })
})
