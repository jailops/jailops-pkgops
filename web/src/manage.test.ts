import { describe, expect, it } from 'vitest'
import { ARCHIVE_WORD, PRUNE_WORD, abiBytes, applyPlan, archiveLabel, confirmed, entriesFor, filterVersions, filtersActive, hasPending, noFilters, overallStage, packageTotals, pruneSelection, refKey, setAll, stageOf, stateBadges } from './manage'
import type { Version } from './types'

const day = 86400_000
const now = Date.parse('2026-06-01T00:00:00Z')
const v = (o: Partial<Version>): Version => ({ abi: 'FreeBSD:15:amd64', name: 'hello', version: '1', size: 100, state: 'active', archive: 'archived', built: new Date(now - day).toISOString(), ...o })
const all: Version[] = [
  v({ version: '3', newest: true, size: 300 }),
  v({ version: '2', size: 200, built: new Date(now - 40 * day).toISOString() }),
  v({ version: '1', size: 100, state: 'pruned', built: new Date(now - 90 * day).toISOString() }),
  v({ name: 'web', version: '9', state: 'pending-prune', phase: 'committed', newest: true }),
  v({ abi: 'FreeBSD:15:aarch64', name: 'hello', version: '3', state: 'archive-only' }),
]

describe('filters', () => {
  it('filters by ABI, name text, age and state, and combines them', () => {
    expect(filterVersions(all, noFilters(), now)).toHaveLength(5)
    expect(filterVersions(all, { ...noFilters(), abi: 'FreeBSD:15:aarch64' }, now)).toHaveLength(1)
    expect(filterVersions(all, { ...noFilters(), name: ' WE ' }, now).map((x) => x.name)).toEqual(['web'])
    expect(filterVersions(all, { ...noFilters(), olderThanDays: 30 }, now).map((x) => x.version)).toEqual(['2', '1'])
    expect(filterVersions(all, { ...noFilters(), state: 'pending-prune' }, now)).toHaveLength(1)
    expect(filterVersions(all, { ...noFilters(), olderThanDays: 30, state: 'active' }, now).map((x) => x.version)).toEqual(['2'])
    expect(filterVersions(all, { ...noFilters(), name: 'zzz' }, now)).toEqual([])
  })
  it('a version without a build time never matches an age filter', () => {
    expect(filterVersions([v({ built: undefined }), v({ built: '0001-01-01T00:00:00Z' })], { ...noFilters(), olderThanDays: 1 }, now)).toEqual([])
  })
  it('knows when a filter is set', () => {
    expect(filtersActive(noFilters())).toBe(false)
    expect(filtersActive({ ...noFilters(), name: ' ' })).toBe(false)
    expect(filtersActive({ ...noFilters(), olderThanDays: 3 })).toBe(true)
  })
})

describe('bulk selection (keep the newest N, decided by the server plan)', () => {
  const plan = [{ abi: 'FreeBSD:15:amd64', name: 'hello', version: '2' }, { abi: 'FreeBSD:15:amd64', name: 'hello', version: '1' }, { abi: 'x', name: 'ghost', version: '1' }]
  it('selects only planned rows that are active, keeps earlier picks', () => {
    const before = new Set([refKey(all[0]!)])
    const next = applyPlan(before, plan, all)
    expect([...next].sort()).toEqual([refKey(all[0]!), refKey(all[1]!)].sort()) // v1 is pruned, ghost unknown
    expect(before.size).toBe(1) // input untouched
  })
  it('drops selections that stopped being selectable', () => {
    const sel = new Set(all.map(refKey))
    expect([...pruneSelection(sel, all)]).toEqual([refKey(all[0]!), refKey(all[1]!)])
  })
  it('select-all only touches selectable rows', () => {
    expect(setAll(new Set(), all, true).size).toBe(2)
    expect(setAll(setAll(new Set(), all, true), all, false).size).toBe(0)
  })
  it('builds the entries and marks forced ones', () => {
    const sel = new Set([refKey(all[0]!), refKey(all[1]!)])
    expect(entriesFor(all, sel, new Set([refKey(all[0]!)]))).toEqual([
      { abi: 'FreeBSD:15:amd64', name: 'hello', version: '3', force: true },
      { abi: 'FreeBSD:15:amd64', name: 'hello', version: '2', force: undefined },
    ])
  })
})

describe('confirmation gating', () => {
  it('needs the exact words', () => {
    expect(confirmed('prune', PRUNE_WORD)).toBe(true)
    for (const t of ['', 'Prune', 'prune ', 'pru', 'delete from archive']) expect(confirmed(t, PRUNE_WORD)).toBe(false)
    expect(confirmed('delete from archive', ARCHIVE_WORD)).toBe(true)
    expect(confirmed('prune', ARCHIVE_WORD)).toBe(false)
  })
})

describe('state, archive and phase mapping', () => {
  it('names each state, newest and the pending phase', () => {
    expect(stateBadges(all[0]!).map((b) => b.text)).toEqual(['Active', 'Newest'])
    expect(stateBadges(all[2]!).map((b) => b.text)).toEqual(['Pruned'])
    expect(stateBadges(all[3]!).map((b) => b.text)).toEqual(['Pending prune: committed', 'Newest'])
    expect(stateBadges(v({ state: 'pending-prune', phase: 'workflow-running' }))[0]!.text).toBe('Pending prune: workflow running')
    expect(stateBadges(all[4]!).map((b) => b.text)).toEqual(['Archive only'])
  })
  it('words the archive state', () => {
    expect(archiveLabel(all[0]!)).toBe('Archived')
    expect(archiveLabel(all[2]!)).toBe('Pruned from Pages, still archived')
    expect(archiveLabel(v({ archive: 'not-archived' }))).toBe('Not archived')
    expect(archiveLabel(v({ archive: 'archive-deleted', state: 'pruned' }))).toBe('Archive deleted')
    expect(archiveLabel(v({ archive: 'unknown' }))).toBe('Unknown')
  })
  it('maps phases to the four-step strip, slowest entry wins', () => {
    expect(stageOf(undefined)).toBe(0)
    expect(stageOf(v({ state: 'active' }))).toBe(0)
    expect(stageOf(v({ state: 'pending-prune', phase: 'committed' }))).toBe(1)
    expect(stageOf(v({ state: 'pending-prune', phase: 'workflow-running' }))).toBe(2)
    expect(stageOf(v({ state: 'pending-prune', phase: 'published' }))).toBe(3)
    expect(stageOf(v({ state: 'pruned' }))).toBe(3)
    const live = [v({ version: '2', state: 'pending-prune', phase: 'workflow-running' }), v({ version: '1', state: 'pruned' })]
    expect(overallStage([live[0]!, live[1]!], live)).toBe(2)
    expect(overallStage([], live)).toBe(0)
  })
  it('polls only while something is pending', () => {
    expect(hasPending(all)).toBe(true)
    expect(hasPending(all.filter((x) => x.state !== 'pending-prune'))).toBe(false)
    expect(hasPending(undefined)).toBe(false)
  })
})

describe('storage totals', () => {
  it('counts what the site serves (active and pending), not pruned or archive-only', () => {
    expect(abiBytes(all, 'FreeBSD:15:amd64')).toBe(300 + 200 + 100)
    expect(packageTotals(all).find((t) => t.name === 'hello' && t.abi === 'FreeBSD:15:amd64')).toMatchObject({ versions: 2, bytes: 500 })
  })
})
