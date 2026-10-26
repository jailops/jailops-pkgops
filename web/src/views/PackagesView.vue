<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import StatePanel from '../components/StatePanel.vue'
import UploadDialog from '../components/UploadDialog.vue'
import { api } from '../api'
import { bytes, plural, when } from '../format'
import { useLoad } from '../load'
import { pkgHref, replaceQuery, route } from '../router'
import { session } from '../session'
import type { Package } from '../types'

const { data, loading, error, reload } = useLoad(api.packages, 'load the packages')
const q = ref(route.value.query.get('q') ?? '')
const abi = ref(route.value.query.get('abi') ?? '')
const all = ref(route.value.query.get('all') === '1')
const uploading = ref(false)

watch([q, abi, all], () => replaceQuery('/packages', { q: q.value, abi: abi.value, all: all.value ? '1' : '' }))

const abis = computed(() => [...new Set((data.value ?? []).map((p) => p.abi))].sort())
const versionsOf = computed(() => {
  const m = new Map<string, number>()
  for (const p of data.value ?? []) m.set(`${p.abi}/${p.name}`, (m.get(`${p.abi}/${p.name}`) ?? 0) + 1)
  return m
})
const rows = computed<Package[]>(() => {
  const needle = q.value.trim().toLowerCase()
  return (data.value ?? []).filter((p) => {
    if (abi.value && p.abi !== abi.value) return false
    if (!all.value && !p.latest) return false
    return !needle || [p.name, p.comment, p.origin, p.version].some((s) => s.toLowerCase().includes(needle))
  })
})
const total = computed(() => (data.value ?? []).filter((p) => p.latest).length)

function onUploaded() { uploading.value = false; void reload() }
</script>

<template>
  <div class="page-head">
    <h1>Packages</h1>
    <button v-if="session.canUpload.value" class="btn btn-primary" type="button" data-test="upload" @click="uploading = true">Upload package</button>
  </div>
  <StatePanel :loading="loading" :error="error" :empty="!!data && data.length === 0" empty-title="No packages yet"
              empty-text="Upload a package built with pkgops build to see it here." @retry="reload">
    <div v-if="data" class="stack">
      <form class="toolbar" role="search" aria-label="Filter packages" @submit.prevent>
        <div class="field search">
          <label for="q">Search</label>
          <input id="q" v-model="q" type="search" placeholder="Name, description, origin" autocomplete="off" />
        </div>
        <div class="field">
          <label for="abi">Platform</label>
          <select id="abi" v-model="abi">
            <option value="">All platforms</option>
            <option v-for="a in abis" :key="a" :value="a">{{ a }}</option>
          </select>
        </div>
        <label class="check"><input v-model="all" type="checkbox" /> Show all versions</label>
      </form>
      <p class="muted m0" role="status" aria-live="polite" data-test="count">
        {{ plural(rows.length, all ? 'version' : 'package') }}<template v-if="rows.length !== (all ? data.length : total)"> of {{ all ? data.length : total }}</template>
      </p>
      <div v-if="rows.length === 0" class="empty panel" data-test="no-match">
        <h2>Nothing matches</h2>
        <p>No package fits “{{ q }}”<template v-if="abi"> on {{ abi }}</template>.</p>
        <button class="btn btn-sm" type="button" @click="q = ''; abi = ''">Clear filters</button>
      </div>
      <div v-else class="panel">
        <div class="table-wrap">
          <table data-test="packages">
            <caption class="sr-only">Packages in this repository</caption>
            <thead><tr>
              <th scope="col">Name</th><th scope="col">Version</th><th scope="col">Platform</th>
              <th scope="col" class="num">Size</th><th scope="col" class="hide-sm">Description</th><th scope="col" class="hide-sm">Added</th>
            </tr></thead>
            <tbody>
              <tr v-for="p in rows" :key="p.abi + p.name + p.version">
                <td><a class="link-reset" :href="pkgHref(p.abi, p.name, p.version)">{{ p.name }}</a></td>
                <td class="mono nowrap">{{ p.version }}
                  <span v-if="all && p.latest" class="badge">latest</span>
                  <span v-if="!all && (versionsOf.get(p.abi + '/' + p.name) ?? 0) > 1" class="muted"> +{{ (versionsOf.get(p.abi + '/' + p.name) ?? 1) - 1 }} older</span>
                </td>
                <td class="nowrap">{{ p.abi }}</td>
                <td class="num nowrap">{{ bytes(p.size) }}</td>
                <td class="hide-sm">{{ p.comment }}</td>
                <td class="hide-sm nowrap">{{ when(p.modified) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </StatePanel>
  <UploadDialog v-if="uploading" @close="uploading = false" @uploaded="onUploaded" />
</template>
