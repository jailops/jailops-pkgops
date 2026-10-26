// Pure logic of the Manage view: filters, selection, state and phase mapping.
// Kept free of Vue and the network so it is unit-tested directly.
import type { ABISummary, PruneEntry, Version, VersionState } from './types'

export const refKey = (v: { abi: string; name: string; version: string }): string => `${v.abi}\u0000${v.name}\u0000${v.version}`

export interface Filters { abi: string; name: string; olderThanDays: number; state: '' | VersionState }
export const noFilters = (): Filters => ({ abi: '', name: '', olderThanDays: 0, state: '' })
export const filtersActive = (f: Filters): boolean => !!(f.abi || f.name.trim() || f.olderThanDays > 0 || f.state)

export function filterVersions(vs: Version[], f: Filters, now = Date.now()): Version[] {
  const needle = f.name.trim().toLowerCase()
  const cutoff = f.olderThanDays > 0 ? now - f.olderThanDays * 86400_000 : 0
  return vs.filter((v) => {
    if (f.abi && v.abi !== f.abi) return false
    if (needle && !v.name.toLowerCase().includes(needle)) return false
    if (f.state && v.state !== f.state) return false
    if (cutoff) {
      const t = v.built ? Date.parse(v.built) : NaN
      if (Number.isNaN(t) || t <= 0 || v.built!.startsWith('0001') || t > cutoff) return false
    }
    return true
  })
}

// Only a file served on Pages can be requested for pruning; the others are already
// requested, already gone, or not on Pages at all.
export const selectable = (v: Version): boolean => v.state === 'active'

// Turns the server's plan (abi + keep) into a selection: only rows that exist and can be
// pruned are selected, everything already selected stays selected.
export function applyPlan(current: Set<string>, planned: PruneEntry[], vs: Version[]): Set<string> {
  const ok = new Map(vs.filter(selectable).map((v) => [refKey(v), v]))
  const next = new Set(current)
  for (const e of planned) if (ok.has(refKey(e))) next.add(refKey(e))
  return next
}

// Drops selected keys that are no longer selectable (the overview moved on).
export function pruneSelection(current: Set<string>, vs: Version[]): Set<string> {
  const ok = new Set(vs.filter(selectable).map(refKey))
  return new Set([...current].filter((k) => ok.has(k)))
}

export function setAll(current: Set<string>, rows: Version[], on: boolean): Set<string> {
  const next = new Set(current)
  for (const v of rows.filter(selectable)) on ? next.add(refKey(v)) : next.delete(refKey(v))
  return next
}

export interface Badge { text: string; tone: 'ok' | 'warn' | 'danger' | 'info' | '' }

export const PHASES = { committed: 'committed', 'workflow-running': 'workflow running', published: 'published' } as const

export function stateBadges(v: Version): Badge[] {
  const out: Badge[] = []
  switch (v.state) {
    case 'active': out.push({ text: 'Active', tone: 'ok' }); break
    case 'pending-prune': out.push({ text: v.phase ? `Pending prune: ${PHASES[v.phase] ?? v.phase}` : 'Pending prune', tone: 'warn' }); break
    case 'pruned': out.push({ text: 'Pruned', tone: '' }); break
    case 'archive-only': out.push({ text: 'Archive only', tone: 'info' }); break
  }
  if (v.newest) out.push({ text: 'Newest', tone: 'info' })
  return out
}

export function archiveLabel(v: Version): string {
  const pruned = v.state === 'pruned' || v.state === 'archive-only'
  switch (v.archive) {
    case 'archived': return pruned ? 'Pruned from Pages, still archived' : 'Archived'
    case 'not-archived': return 'Not archived'
    case 'archive-deleted': return 'Archive deleted'
    default: return 'Unknown'
  }
}

// Progress of a requested prune: 0 requested, 1 committed, 2 workflow running, 3 published.
export const STAGES = ['Requested', 'Committed', 'Workflow running', 'Published'] as const
export function stageOf(v: Version | undefined): number {
  if (!v) return 0
  if (v.state === 'pruned' || v.phase === 'published') return 3
  switch (v.phase) {
    case 'workflow-running': return 2
    case 'committed': return 1
    default: return 0
  }
}
// The strip shows the slowest entry: the cleanup is done when every file is.
export function overallStage(entries: PruneEntry[], vs: Version[]): number {
  if (entries.length === 0) return 0
  const by = new Map(vs.map((v) => [refKey(v), v]))
  return Math.min(...entries.map((e) => stageOf(by.get(refKey(e)))))
}

export const hasPending = (vs: Version[] | undefined): boolean => !!vs?.some((v) => v.state === 'pending-prune')

// A file counts towards the site's storage while Pages serves it (or will until the next deploy).
const onSite = (v: Version) => v.state === 'active' || v.state === 'pending-prune'

export interface PackageTotal { abi: string; name: string; versions: number; bytes: number }
export function packageTotals(vs: Version[]): PackageTotal[] {
  const m = new Map<string, PackageTotal>()
  for (const v of vs) {
    const k = `${v.abi}\u0000${v.name}`
    const t = m.get(k) ?? { abi: v.abi, name: v.name, versions: 0, bytes: 0 }
    if (onSite(v)) { t.versions++; t.bytes += v.size }
    m.set(k, t)
  }
  return [...m.values()]
}
export const abiBytes = (vs: Version[], abi: string): number => vs.filter((v) => v.abi === abi && onSite(v)).reduce((n, v) => n + v.size, 0)
export const abiList = (abis: ABISummary[], vs: Version[]): string[] => [...new Set([...abis.map((a) => a.abi), ...vs.map((v) => v.abi)])].sort()

// The server wants these exact words; the dialogs enable their button only on an exact match.
export const PRUNE_WORD = 'prune'
export const ARCHIVE_WORD = 'delete from archive'
export const confirmed = (typed: string, word: string): boolean => typed === word

export function entriesFor(vs: Version[], selected: Set<string>, force: Set<string>): PruneEntry[] {
  return vs.filter((v) => selected.has(refKey(v))).map((v) => ({ abi: v.abi, name: v.name, version: v.version, force: force.has(refKey(v)) || undefined }))
}
