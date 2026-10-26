<script setup lang="ts">
import { computed, ref } from 'vue'
import DeleteDialog from '../components/DeleteDialog.vue'
import StatePanel from '../components/StatePanel.vue'
import { api } from '../api'
import { absolute, bytes, plural, when } from '../format'
import { useLoad } from '../load'
import { go, href, pkgHref, route } from '../router'
import { session } from '../session'

const params = computed(() => route.value.params as [string, string, string])
const { data: d, loading, error, reload } = useLoad(async () => {
  const [abi, name, version] = params.value
  const [detail, all] = await Promise.all([api.detail(abi, name, version), api.packages()])
  return { detail, versions: all.filter((p) => p.abi === abi && p.name === name) }
}, 'load the package')
const deleting = ref(false)
const showAll = ref(false)
const FILE_LIMIT = 200

const files = computed(() => (showAll.value ? d.value?.detail.files : d.value?.detail.files.slice(0, FILE_LIMIT)) ?? [])

function afterDelete() {
  deleting.value = false
  const left = d.value?.versions.filter((p) => p.version !== params.value[2]) ?? []
  const next = left[0]
  go(next ? pkgHref(next.abi, next.name, next.version) : href('/packages'))
}
</script>

<template>
  <a class="back" :href="href('/packages')">&larr; All packages</a>
  <StatePanel :loading="loading" :error="error" @retry="reload">
    <div v-if="d" class="stack">
      <div class="page-head">
        <div>
          <h1>{{ d.detail.name }} <span class="mono muted">{{ d.detail.version }}</span></h1>
          <p class="muted mt4">{{ d.detail.comment }}</p>
        </div>
        <div class="row">
          <span v-if="d.detail.latest" class="pill ok">Latest</span>
          <span v-else class="pill">Older version</span>
          <button v-if="session.canDelete.value" class="btn btn-danger" type="button" data-test="delete" @click="deleting = true">Delete&hellip;</button>
        </div>
      </div>

      <div class="grid">
        <section class="panel" aria-labelledby="meta-h">
          <header><h2 id="meta-h">Details</h2></header>
          <div class="body">
            <dl class="kv">
              <dt>Platform</dt><dd>{{ d.detail.abi }} <span class="muted">({{ d.detail.arch }})</span></dd>
              <dt>Origin</dt><dd class="mono">{{ d.detail.origin }}</dd>
              <dt>Install size</dt><dd>{{ bytes(d.detail.flatsize) }}</dd>
              <dt>Download size</dt><dd>{{ bytes(d.detail.size) }}</dd>
              <dt>Added</dt><dd>{{ when(d.detail.modified) }} <span class="muted">({{ absolute(d.detail.modified) }})</span></dd>
              <dt v-if="d.detail.maintainer">Maintainer</dt><dd v-if="d.detail.maintainer">{{ d.detail.maintainer }}</dd>
              <dt v-if="d.detail.www">Website</dt><dd v-if="d.detail.www"><a :href="d.detail.www" rel="noopener noreferrer">{{ d.detail.www }}</a></dd>
              <dt v-if="d.detail.licenses?.length">License</dt><dd v-if="d.detail.licenses?.length">{{ d.detail.licenses.join(', ') }}</dd>
              <dt v-if="d.detail.categories?.length">Categories</dt><dd v-if="d.detail.categories?.length"><span v-for="c in d.detail.categories" :key="c" class="chip">{{ c }}</span></dd>
              <dt>SHA-256</dt><dd class="mono">{{ d.detail.sha256 }}</dd>
              <dt>Install</dt><dd><code>pkg install {{ d.detail.name }}</code></dd>
            </dl>
          </div>
        </section>

        <section class="panel" aria-labelledby="ver-h">
          <header><h2 id="ver-h">Versions</h2><span class="muted">{{ plural(d.versions.length, 'version') }}</span></header>
          <div class="body">
            <ul class="vlist">
              <li v-for="v in d.versions" :key="v.version">
                <a v-if="v.version !== d.detail.version" :href="pkgHref(v.abi, v.name, v.version)" class="mono">{{ v.version }}</a>
                <strong v-else class="mono" aria-current="true">{{ v.version }}</strong>
                <span v-if="v.latest" class="badge ml6">latest</span>
                <span v-if="v.version === d.detail.version" class="muted"> (this page)</span>
              </li>
            </ul>
          </div>
        </section>
      </div>

      <section v-if="d.detail.desc && d.detail.desc !== d.detail.comment" class="panel" aria-labelledby="desc-h">
        <header><h2 id="desc-h">Description</h2></header>
        <div class="body"><p class="m0 preline">{{ d.detail.desc }}</p></div>
      </section>

      <section v-if="d.detail.deps?.length" class="panel" aria-labelledby="deps-h">
        <header><h2 id="deps-h">Dependencies</h2></header>
        <div class="table-wrap">
          <table>
            <thead><tr><th scope="col">Package</th><th scope="col">Origin</th><th scope="col">Version</th></tr></thead>
            <tbody><tr v-for="x in d.detail.deps" :key="x.name"><td>{{ x.name }}</td><td class="mono">{{ x.origin }}</td><td class="mono">{{ x.version }}</td></tr></tbody>
          </table>
        </div>
      </section>

      <section class="panel" aria-labelledby="files-h">
        <header><h2 id="files-h">Files</h2><span class="muted">{{ plural(d.detail.files.length, 'file') }}</span></header>
        <div class="table-wrap">
          <table data-test="files">
            <caption class="sr-only">Files installed by this package</caption>
            <thead><tr><th scope="col">Path</th><th scope="col">Mode</th><th scope="col" class="hide-sm">Owner</th></tr></thead>
            <tbody>
              <tr v-for="f in files" :key="f.path">
                <td class="mono">{{ f.path }}<span v-if="f.symlink" class="muted"> &rarr; {{ f.symlink }}</span>
                  <span v-if="f.config" class="badge ml6" title="Configuration file: local changes are kept on upgrade">config</span></td>
                <td class="mono nowrap">{{ f.mode }}</td>
                <td class="hide-sm nowrap">{{ f.owner }}:{{ f.group }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="d.detail.files.length > FILE_LIMIT && !showAll" class="body">
          <button class="btn btn-sm" type="button" @click="showAll = true">Show all {{ d.detail.files.length }} files</button>
        </div>
      </section>

      <section v-if="d.detail.directories?.length || d.detail.users?.length || d.detail.scripts?.length" class="panel" aria-labelledby="more-h">
        <header><h2 id="more-h">Install behaviour</h2></header>
        <div class="body">
          <dl class="kv">
            <dt v-if="d.detail.directories?.length">Owned directories</dt>
            <dd v-if="d.detail.directories?.length"><div v-for="x in d.detail.directories" :key="x.path" class="mono">{{ x.path }} <span class="muted">{{ x.mode }} {{ x.owner }}:{{ x.group }}</span></div></dd>
            <dt v-if="d.detail.users?.length">Creates users</dt><dd v-if="d.detail.users?.length">{{ d.detail.users.join(', ') }}</dd>
            <dt v-if="d.detail.groups?.length">Creates groups</dt><dd v-if="d.detail.groups?.length">{{ d.detail.groups.join(', ') }}</dd>
            <dt v-if="d.detail.scripts?.length">Scripts</dt><dd v-if="d.detail.scripts?.length"><span v-for="s in d.detail.scripts" :key="s" class="chip">{{ s }}</span></dd>
          </dl>
        </div>
      </section>
    </div>
  </StatePanel>
  <DeleteDialog v-if="deleting && d" :abi="d.detail.abi" :name="d.detail.name" :version="d.detail.version" :latest="d.detail.latest"
                :only-version="d.versions.length === 1" @close="deleting = false" @deleted="afterDelete" />
</template>
