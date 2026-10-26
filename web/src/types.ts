// Wire types of the registry API (/api/v1). They mirror jailops.net/pkgops/sdk.
export interface Package {
  name: string
  version: string
  abi: string
  arch: string
  origin: string
  comment: string
  maintainer?: string
  www?: string
  licenses?: string[]
  size: number
  sha256: string
  flatsize: number
  path: string
  latest: boolean
  modified: string
}

export interface FileEntry { path: string; mode: string; owner: string; group: string; symlink?: string; config?: boolean }
export interface Dep { name: string; origin: string; version: string }

export interface Detail extends Package {
  desc: string
  prefix: string
  categories?: string[]
  deps?: Dep[]
  users?: string[]
  groups?: string[]
  files: FileEntry[]
  directories?: FileEntry[]
  scripts?: string[]
}

export interface ABIStatus {
  abi: string
  arch: string
  channel: string
  packages: number
  versions: number
  updated: string
  signature: string
  consistent: boolean
  problem?: string
}

export interface Status {
  healthy: boolean
  packages: number
  versions: number
  updated: string
  signature: 'pubkey' | 'fingerprints' | 'unsigned'
  fingerprint: string
  abis: ABIStatus[]
  problems?: string[]
  uploads: boolean
  keep: number
  channel: string
}

export type Role = 'viewer' | 'uploader' | 'admin'
export interface SessionInfo { name: string; role: Role; scopes: string[]; via?: 'token' | 'access' }

// Admin API (mirrors sdk/admin.go).
export type VersionState = 'active' | 'pending-prune' | 'pruned' | 'archive-only'
export type Phase = 'committed' | 'workflow-running' | 'published'
export type ArchiveState = 'archived' | 'not-archived' | 'archive-deleted' | 'unknown'
export interface Version {
  abi: string; name: string; version: string; size: number; sha256?: string
  built?: string; min_release?: string; comment?: string; newest?: boolean
  state: VersionState; phase?: Phase; reason?: string; pruned_at?: string
  archive: ArchiveState; archive_url?: string
}
export interface ABISummary { abi: string; packages: number; versions: number; bytes: number; pending: number; pruned: number }
export interface Run { id: number; status: string; conclusion?: string; url: string; created: string }
export interface ArchiveReport {
  on_pages_not_archived: string[]; archived_not_on_pages: string[]; mismatched: string[]
  pruned_still_archived: string[]; pruned_not_archived: string[]; ok: boolean
}
export interface Overview {
  mode: string; source?: string
  policy?: { abis: string[]; min_release: string; keep: number }
  status?: Status
  abis: ABISummary[]; versions: Version[]; bytes: number
  prune: { mode: 'github' | 'manual'; repo?: string; branch?: string; file?: string; error?: string }
  publish: { available: boolean; workflow?: string; error?: string; runs?: Run[] }
  archive: { available: boolean; repo?: string; error?: string; files: number; report?: ArchiveReport }
  identity?: SessionInfo
  problems?: string[]
}
export interface PruneEntry { abi: string; name: string; version: string; reason?: string; force?: boolean }
export interface PrunePlanRequest { abi?: string; name?: string; version?: string; keep?: number; older_than_days?: number; force?: boolean }
export interface PruneRequest { entries: PruneEntry[]; reason?: string; dry_run?: boolean; confirm: string }
export interface Manual { file: string; diff: string; content: string; commands: string[] }
export interface PruneResult {
  mode: string; dry_run: boolean; entries: PruneEntry[]; skipped?: PruneEntry[]
  message?: string; commit?: string; commit_url?: string; diff: string; manual?: Manual; bytes: number; effect: string
}
export interface ArchiveRef { abi: string; name: string; version: string }
export interface ArchiveDeleteRequest { refs: ArchiveRef[]; reason: string; confirm: string }
