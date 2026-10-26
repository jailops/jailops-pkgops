<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import ArchiveDeleteDialog from '../components/ArchiveDeleteDialog.vue'
import PruneDialog from '../components/PruneDialog.vue'
import StatePanel from '../components/StatePanel.vue'
import { ApiError, api, describeAdmin } from '../api'
import { absolute, bytes, plural, shortHash, when } from '../format'
import { abiBytes, abiList, applyPlan, archiveLabel, filterVersions, filtersActive, hasPending, noFilters, packageTotals, pruneSelection, refKey, selectable, setAll, stateBadges } from '../manage'
import { session } from '../session'
import { toast } from '../toast'
import type { Overview, PruneResult, Version, VersionState } from '../types'

const POLL_MS = 15_000
const ov = shallowRef<Overview | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)
const filters = ref(noFilters())
const selected = ref(new Set<string>())
const archSel = ref(new Set<string>())
const keepN = ref(3)
const plan = ref<{ abi: string; result: PruneResult } | null>(null)
const planning = ref('')
const planError = ref('')
// the dialogs work on a snapshot: polling may change the table under them
const pruneRows = ref<Version[] | null>(null)
const archiveRows = ref<Version[] | null>(null)
const republishing = ref(false)
const admin = session.canAdmin

let seq = 0
async function load() {
  const mine = ++seq
  try {
    const o = await api.overview()
    if (mine !== seq) return
    ov.value = o
    error.value = null
    selected.value = pruneSelection(selected.value, o.versions)
    if (o.policy?.keep && !keepSet) { keepN.value = o.policy.keep; keepSet = true }
  } catch (e) {
    if (mine !== seq) return
    if (e instanceof ApiError && e.status === 401) error.value = 'Sign in to see this page: the registry asks who you are before it shows the files it publishes.'
    else if (e instanceof ApiError && e.status === 403) error.value = `Not allowed: ${e.message}`
    else error.value = describeAdmin(e, 'load the overview')
    ov.value = null
  } finally {
    if (mine === seq) loading.value = false
  }
}
let keepSet = false
void load()
// the identity decides what the overview may contain: load again after signing in or out
watch(() => session.info.value?.name + '|' + session.info.value?.role, () => { loading.value = ov.value === null; void load() })

// Poll only while a cleanup is pending; the timer dies with the view.
let timer: ReturnType<typeof setInterval> | undefined
const pending = computed(() => hasPending(ov.value?.versions))
function stop() { if (timer !== undefined) { clearInterval(timer); timer = undefined } }
watch(pending, (p) => { stop(); if (p) timer = setInterval(() => void load(), POLL_MS) }, { immediate: true })
onBeforeUnmount(() => { stop(); seq++ })

const versions = computed(() => ov.value?.versions ?? [])
const abis = computed(() => abiList(ov.value?.abis ?? [], versions.value))
const shown = computed(() => filterVersions(versions.value, filters.value))
const states: [VersionState, string][] = [['active', 'Active'], ['pending-prune', 'Pending prune'], ['pruned', 'Pruned'], ['archive-only', 'Archive only']]

interface Group { abi: string; summary?: Overview['abis'][number]; bytes: number; pkgs: { name: string; bytes: number; rows: Version[] }[]; rows: Version[] }
const groups = computed<Group[]>(() => {
  const pk = new Map(packageTotals(versions.value).map((t) => [`${t.abi}\u0000${t.name}`, t.bytes]))
  return abis.value.map((abi) => {
    const rows = shown.value.filter((v) => v.abi === abi)
    const byName = new Map<string, Version[]>()
    for (const v of rows) byName.set(v.name, [...(byName.get(v.name) ?? []), v])
    return {
      abi, summary: ov.value?.abis.find((a) => a.abi === abi), bytes: abiBytes(versions.value, abi), rows,
      pkgs: [...byName].map(([name, r]) => ({ name, rows: r, bytes: pk.get(`${abi}\u0000${name}`) ?? 0 })),
    }
  })
})

const selRows = computed(() => versions.value.filter((v) => selected.value.has(refKey(v))))
const selBytes = computed(() => selRows.value.reduce((n, v) => n + v.size, 0))
function toggle(v: Version, on: boolean) {
  const n = new Set(selected.value)
  on ? n.add(refKey(v)) : n.delete(refKey(v))
  selected.value = n
}
function toggleGroup(rows: Version[], on: boolean) { selected.value = setAll(selected.value, rows, on) }
const allOn = (rows: Version[]) => { const s = rows.filter(selectable); return s.length > 0 && s.every((v) => selected.value.has(refKey(v))) }

async function planKeep(abi: string) {
  planning.value = abi
  planError.value = ''
  plan.value = null
  try {
    const result = await api.prunePlan({ abi, keep: Math.max(0, Math.floor(keepN.value) || 0) })
    plan.value = { abi, result }
    selected.value = applyPlan(selected.value, result.entries, versions.value)
  } catch (e) {
    planError.value = describeAdmin(e, 'compute the selection')
  } finally {
    planning.value = ''
  }
}

const archivable = computed(() => versions.value.filter((v) => v.archive === 'archived' && v.state !== 'active' && v.state !== 'pending-prune'))
const archRows = computed(() => archivable.value.filter((v) => archSel.value.has(refKey(v))))
function toggleArch(v: Version, on: boolean) {
  const n = new Set(archSel.value)
  on ? n.add(refKey(v)) : n.delete(refKey(v))
  archSel.value = n
}
function archDeleted() {
  archiveRows.value = null
  archSel.value = new Set()
  toast('ok', 'Archived files deleted permanently.')
  void load()
}

async function republish() {
  republishing.value = true
  try {
    await api.republish()
    toast('ok', 'Republish requested. The site changes after the workflow has run.')
    void load()
  } catch (e) {
    toast('danger', describeAdmin(e, 'request the republish'))
  } finally {
    republishing.value = false
  }
}

function onRequested() { selected.value = new Set(); plan.value = null; void load() }
const report = computed(() => ov.value?.archive.report)
const problemCount = computed(() => (report.value ? report.value.on_pages_not_archived.length + report.value.archived_not_on_pages.length + report.value.mismatched.length : 0))
const tone = (t: string) => (t ? ' ' + t : '')
const runTone = (r: { status: string; conclusion?: string }) => (r.status !== 'completed' ? 'info' : r.conclusion === 'success' ? 'ok' : 'danger')
const sigText = computed(() => ({ pubkey: 'Signed (public key)', fingerprints: 'Signed (fingerprints)', unsigned: 'Not signed' })[ov.value?.status?.signature ?? 'unsigned'])
</script>

<template>
  <div class="page-head">
    <h1>Manage</h1>
    <div class="row">
      <button class="btn btn-sm" type="button" @click="load">Refresh</button>
      <button v-if="admin && ov?.publish.available" class="btn" type="button" :disabled="republishing" data-test="republish" @click="republish">{{ republishing ? 'Requesting…' : 'Republish' }}</button>
    </div>
  </div>
  <StatePanel :loading="loading" :error="error" @retry="load">
    <div v-if="ov" class="stack" data-test="manage">
      <p v-if="!admin" class="callout info m0" data-test="readonly"><span class="grow">
        <template v-if="session.signedIn.value">Your role is <strong>{{ session.info.value?.role }}</strong>: you can look at everything here, but only an administrator can change it.</template>
        <template v-else>You are looking at this read-only. Sign in as an administrator to request a cleanup.</template></span></p>
      <p v-if="ov.problems?.length" class="callout danger m0" role="alert"><span class="grow"><strong>Problems:</strong> {{ ov.problems.join('; ') }}</span></p>

      <div class="grid">
        <section class="panel" aria-labelledby="facts-h">
          <header><h2 id="facts-h">Repository</h2></header>
          <div class="body">
            <dl class="kv" data-test="facts">
              <dt>Supported ABIs</dt><dd>{{ ov.policy?.abis.join(', ') || abis.join(', ') || 'none' }}</dd>
              <dt>Minimum release</dt><dd class="mono" data-test="min-release">{{ ov.policy?.min_release || 'not set' }}</dd>
              <dt>Retention hint</dt><dd>{{ ov.policy?.keep ? `keep ${ov.policy.keep} newest` : 'none' }}</dd>
              <dt>Signature</dt><dd data-test="sig">{{ sigText }}</dd>
              <dt>Fingerprint</dt><dd class="mono wrap">{{ ov.status?.fingerprint ? shortHash(ov.status.fingerprint) : 'none' }}</dd>
              <dt>Consistency</dt><dd>
                <template v-if="ov.status?.abis.length"><span v-for="a in ov.status.abis" :key="a.abi" class="pill mr6" :class="a.consistent ? 'ok' : 'danger'">{{ a.abi }} {{ a.consistent ? 'consistent' : 'inconsistent' }}</span></template>
                <template v-else>unknown</template></dd>
              <dt>Cleanup requests</dt><dd data-test="prune-mode">
                <template v-if="ov.prune.mode === 'github'">committed to <span class="mono">{{ ov.prune.repo }}</span> ({{ ov.prune.branch }}, <span class="mono">{{ ov.prune.file }}</span>)</template>
                <template v-else>manual: PkgOps shows the steps, you apply them</template>
                <span v-if="ov.prune.error" class="field-error"> {{ ov.prune.error }}</span></dd>
              <dt>Source</dt><dd class="wrap">{{ ov.mode === 'source' ? ov.source : 'local directory' }}</dd>
            </dl>
          </div>
        </section>
        <section class="panel" aria-labelledby="storage-h">
          <header><h2 id="storage-h">Storage on the site</h2><strong data-test="total">{{ bytes(ov.bytes) }}</strong></header>
          <div class="table-wrap">
            <table data-test="storage">
              <caption class="sr-only">Storage per ABI</caption>
              <thead><tr><th scope="col">ABI</th><th scope="col" class="num hide-sm">Packages</th><th scope="col" class="num">Versions</th><th scope="col" class="num">Size</th></tr></thead>
              <tbody>
                <tr v-for="a in ov.abis" :key="a.abi"><td class="nowrap">{{ a.abi }}</td><td class="num hide-sm">{{ a.packages }}</td><td class="num">{{ a.versions }}</td><td class="num nowrap">{{ bytes(a.bytes) }}</td></tr>
                <tr><th scope="row">Total</th><td class="hide-sm"></td><td class="num">{{ ov.abis.reduce((n, a) => n + a.versions, 0) }}</td><td class="num nowrap"><strong>{{ bytes(ov.bytes) }}</strong></td></tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>

      <section v-if="ov.publish.available || ov.publish.error" class="panel" aria-labelledby="runs-h">
        <header><h2 id="runs-h">Publish workflow</h2><span class="muted mono">{{ ov.publish.workflow }}</span></header>
        <div class="body">
          <p v-if="ov.publish.error" class="field-error m0">{{ ov.publish.error }}</p>
          <p v-else-if="!ov.publish.runs?.length" class="muted m0">No runs yet.</p>
          <ul v-else class="vlist" data-test="runs">
            <li v-for="r in ov.publish.runs" :key="r.id" class="row">
              <span class="pill" :class="runTone(r)">{{ r.status === 'completed' ? r.conclusion || 'completed' : r.status }}</span>
              <a :href="r.url" target="_blank" rel="noopener noreferrer">run {{ r.id }}</a>
              <span class="muted">{{ when(r.created) }}<span class="sr-only"> ({{ absolute(r.created) }})</span></span>
            </li>
          </ul>
        </div>
      </section>

      <form class="toolbar" role="search" aria-label="Filter versions" @submit.prevent>
        <div class="field"><label for="f-abi">ABI</label>
          <select id="f-abi" v-model="filters.abi" data-test="f-abi"><option value="">All ABIs</option><option v-for="a in abis" :key="a" :value="a">{{ a }}</option></select></div>
        <div class="field search"><label for="f-name">Package name</label>
          <input id="f-name" v-model="filters.name" type="search" autocomplete="off" placeholder="Part of a name" data-test="f-name" /></div>
        <div class="field"><label for="f-age">Older than (days)</label>
          <input id="f-age" v-model.number="filters.olderThanDays" type="number" min="0" inputmode="numeric" data-test="f-age" /></div>
        <div class="field"><label for="f-state">State</label>
          <select id="f-state" v-model="filters.state" data-test="f-state"><option value="">Any state</option><option v-for="[k, l] in states" :key="k" :value="k">{{ l }}</option></select></div>
        <button v-if="filtersActive(filters)" class="btn btn-sm" type="button" @click="filters = noFilters()">Clear filters</button>
      </form>
      <p class="muted m0" role="status" aria-live="polite" data-test="count">{{ plural(shown.length, 'version') }}<template v-if="shown.length !== versions.length"> of {{ versions.length }}</template></p>
      <p v-if="pending" class="callout warn m0" role="status" data-test="pending-note"><span class="grow">A cleanup is pending: it takes effect after the next deploy. This page refreshes every 15 seconds until it is done.</span></p>

      <div v-if="admin && selRows.length" class="callout info selbar" data-test="selbar">
        <span class="grow"><strong>{{ selRows.length }}</strong> selected, {{ bytes(selBytes) }}</span>
        <button class="btn btn-sm" type="button" @click="selected = new Set()">Clear selection</button>
        <button class="btn btn-danger solid" type="button" data-test="prune-open" @click="pruneRows = selRows">Remove from next deploy&hellip;</button>
      </div>
      <p v-if="planError" class="callout danger m0" role="alert" data-test="plan-error"><span class="grow">{{ planError }}</span></p>

      <div v-if="shown.length === 0" class="empty panel" data-test="no-match">
        <h2>{{ versions.length ? 'Nothing matches' : 'No versions yet' }}</h2>
        <p v-if="versions.length">No version fits these filters.</p>
        <button v-if="versions.length" class="btn btn-sm" type="button" @click="filters = noFilters()">Clear filters</button>
      </div>

      <section v-for="g in groups.filter((x) => x.rows.length)" :key="g.abi" class="panel" :aria-labelledby="'abi-' + g.abi" :data-test="'abi-' + g.abi">
        <header>
          <h2 :id="'abi-' + g.abi">{{ g.abi }}</h2>
          <span class="muted">{{ plural(g.summary?.packages ?? g.pkgs.length, 'package') }}, {{ bytes(g.bytes) }} on the site<template v-if="g.summary?.pending">, {{ g.summary.pending }} pending</template><template v-if="g.summary?.pruned">, {{ g.summary.pruned }} pruned</template></span>
          <form v-if="admin" class="row" @submit.prevent="planKeep(g.abi)">
            <label :for="'keep-' + g.abi" class="muted">Keep newest</label>
            <input :id="'keep-' + g.abi" v-model.number="keepN" class="narrow" type="number" min="0" inputmode="numeric" data-test="keep" />
            <button class="btn btn-sm" type="submit" :disabled="planning !== ''" data-test="plan">{{ planning === g.abi ? 'Planning…' : 'Select all but the newest ' + keepN }}</button>
          </form>
        </header>
        <div v-if="plan && plan.abi === g.abi" class="body plan" data-test="plan-result">
          <p v-if="!plan.result.entries.length" class="m0" data-test="plan-none">Nothing to select: every package already has at most {{ keepN }} version{{ keepN === 1 ? '' : 's' }} on the site.</p>
          <p v-else class="m0"><strong>{{ plan.result.entries.length }}</strong> file{{ plan.result.entries.length === 1 ? '' : 's' }} selected, <strong data-test="plan-bytes">{{ bytes(plan.result.bytes) }}</strong> would be freed after the next deploy. Review and click “Remove from next deploy”.</p>
          <ul v-if="plan.result.entries.length" class="vlist files" data-test="plan-files"><li v-for="e in plan.result.entries" :key="refKey(e)" class="mono wrap">{{ e.name }}-{{ e.version }}.pkg</li></ul>
        </div>
        <div class="table-wrap">
          <table class="vt" :data-test="'versions-' + g.abi">
            <caption class="sr-only">Every version of every package for {{ g.abi }}, including pruned ones</caption>
            <thead><tr>
              <th v-if="admin" scope="col" class="chk"><input type="checkbox" :checked="allOn(g.rows)" :disabled="!g.rows.some(selectable)" :aria-label="'Select every active version shown for ' + g.abi" @change="toggleGroup(g.rows, ($event.target as HTMLInputElement).checked)" /></th>
              <th scope="col">Package</th><th scope="col">Version</th><th scope="col" class="num">Size</th>
              <th scope="col" class="hide-sm">Built</th><th scope="col" class="hide-sm">Min release</th><th scope="col">State</th><th scope="col">Archive</th>
            </tr></thead>
            <template v-for="p in g.pkgs" :key="p.name">
              <tbody :data-test="'pkg-' + p.name">
                <tr v-for="(v, i) in p.rows" :key="refKey(v)" :class="{ gone: v.state === 'pruned' || v.state === 'archive-only' }" data-test="row">
                  <td v-if="admin" class="chk"><input type="checkbox" :checked="selected.has(refKey(v))" :disabled="!selectable(v)" :aria-label="'Select ' + v.name + ' ' + v.version" data-test="sel" @change="toggle(v, ($event.target as HTMLInputElement).checked)" /></td>
                  <td class="nowrap"><template v-if="i === 0"><strong>{{ p.name }}</strong> <span class="muted" :title="'Size of this package on the site'">{{ bytes(p.bytes) }}</span></template><span v-else class="sr-only">{{ p.name }}</span></td>
                  <td class="mono nowrap">{{ v.version }}</td>
                  <td class="num nowrap">{{ bytes(v.size) }}</td>
                  <td class="hide-sm nowrap">{{ v.built ? when(v.built) : 'unknown' }}</td>
                  <td class="hide-sm nowrap mono">{{ v.min_release || '-' }}</td>
                  <td><span v-for="b in stateBadges(v)" :key="b.text" class="pill mr6" :class="b.tone" data-test="badge">{{ b.text }}</span></td>
                  <td class="archive" data-test="archive-state">{{ archiveLabel(v) }}</td>
                </tr>
              </tbody>
            </template>
          </table>
        </div>
      </section>

      <section v-if="ov.archive.available || ov.archive.error" class="panel" aria-labelledby="arch-h" data-test="archive">
        <header><h2 id="arch-h">Permanent archive</h2><span class="muted">{{ plural(ov.archive.files, 'file') }}<template v-if="ov.archive.repo"> in <span class="mono">{{ ov.archive.repo }}</span></template></span></header>
        <div class="body stack">
          <p v-if="ov.archive.error" class="field-error m0">{{ ov.archive.error }}</p>
          <template v-if="report">
            <div class="callout m0" :class="problemCount ? 'danger' : 'info'" :role="problemCount ? 'alert' : 'status'" data-test="archive-verdict"><span class="grow">
              <strong>{{ problemCount ? `${plural(problemCount, 'problem')} found` : 'Archive and site agree' }}</strong>
              <template v-if="!problemCount">: every file on the site is archived and matches.</template></span></div>
            <div v-for="[title, list] in ([['On the site but not archived', report.on_pages_not_archived], ['Archived but not on the site (not pruned)', report.archived_not_on_pages], ['Archived but different from the site', report.mismatched]] as const)" :key="title">
              <template v-if="list.length"><h3>{{ title }} ({{ list.length }})</h3><ul class="plist mono wrap" data-test="archive-problem"><li v-for="f in list" :key="f">{{ f }}</li></ul></template>
            </div>
            <div v-if="report.pruned_still_archived.length">
              <h3>Pruned from the site, still archived ({{ report.pruned_still_archived.length }}): normal</h3>
              <ul class="plist mono wrap"><li v-for="f in report.pruned_still_archived" :key="f">{{ f }}</li></ul>
            </div>
            <div v-if="report.pruned_not_archived.length">
              <h3>Pruned and not archived ({{ report.pruned_not_archived.length }})</h3>
              <ul class="plist mono wrap"><li v-for="f in report.pruned_not_archived" :key="f">{{ f }}</li></ul>
            </div>
          </template>
        </div>
      </section>

      <section v-if="admin && ov.archive.available" class="panel danger-zone" aria-labelledby="del-h" data-test="archive-delete">
        <header><h2 id="del-h">Delete from archive</h2></header>
        <div class="body stack">
          <p class="m0"><strong>Dangerous and permanent.</strong> This is separate from cleaning up the site: it deletes archived files for good. Only files that are no longer on the site are listed.</p>
          <p v-if="!archivable.length" class="muted m0">No archived file is eligible.</p>
          <ul v-else class="vlist files" data-test="archive-list">
            <li v-for="v in archivable" :key="refKey(v)"><label class="check"><input type="checkbox" :checked="archSel.has(refKey(v))" :aria-label="'Select archived ' + v.name + ' ' + v.version" @change="toggleArch(v, ($event.target as HTMLInputElement).checked)" />
              <span class="mono">{{ v.name }}-{{ v.version }}</span> <span class="muted">{{ v.abi }}, {{ bytes(v.size) }}</span></label></li>
          </ul>
          <div><button class="btn btn-danger" type="button" :disabled="!archRows.length" data-test="archive-open" @click="archiveRows = archRows">Delete {{ archRows.length }} from archive&hellip;</button></div>
        </div>
      </section>
    </div>
  </StatePanel>
  <PruneDialog v-if="pruneRows" :rows="pruneRows" :mode="ov?.prune.mode ?? 'manual'" :live="versions" @close="pruneRows = null" @requested="onRequested" />
  <ArchiveDeleteDialog v-if="archiveRows" :rows="archiveRows" @close="archiveRows = null" @deleted="archDeleted" />
</template>
