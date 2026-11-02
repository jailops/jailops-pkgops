<script setup lang="ts">
import { computed } from 'vue'
import CopyBlock from '../components/CopyBlock.vue'
import StatePanel from '../components/StatePanel.vue'
import { api } from '../api'
import { absolute, clientConf, downloadBase, keyHref, keySourceText, verificationLabel, when } from '../format'
import { useLoad } from '../load'

const { data: st, loading, error, reload } = useLoad(api.status, 'load the repository status')
const base = computed(() => (st.value ? downloadBase(st.value, location.origin) : ''))
const conf = computed(() => (st.value && base.value ? clientConf(base.value, st.value.channel, st.value.signature) : ''))
const source = computed(() => st.value?.mode === 'source')
const sig = computed(() => verificationLabel(st.value?.verification, st.value?.signature ?? 'unsigned'))
const sigText = computed(() => {
  switch (st.value?.verification) {
    case 'verified': return st.value.key_source === 'site'
      ? 'Signed, and every catalog verifies against the public key published on the same site.'
      : 'Signed, and every catalog verifies against the public key.'
    case 'invalid': return 'A catalog signature does not verify, or the public key is not usable. Clients refuse this repository.'
    case 'unverified': return 'The catalogs carry a signature, but it was not checked: there is no public key to check it with.'
    case 'signed': return 'The catalogs carry a signature that this server did not check.'
  }
  return 'Not signed. Clients cannot verify the catalog.'
})
const healthText = computed(() => {
  const s = st.value
  if (!s) return ''
  if (!s.healthy) return 'A client may fail to update or install. Details below.'
  const sigPart = s.verification === 'verified' ? 'All catalogs verify against the public key' : 'The catalogs were read, but their signatures were not verified'
  return source.value
    ? `${sigPart}. Read-through mode does not download the packages themselves.`
    : `${sigPart}, and every advertised package is present with a matching checksum.`
})
</script>

<template>
  <div class="page-head"><h1>Repository status</h1><button class="btn btn-sm" type="button" @click="reload">Refresh</button></div>
  <StatePanel :loading="loading" :error="error" @retry="reload">
    <div v-if="st" class="stack">
      <div class="hero" :class="st.healthy ? 'ok' : 'danger'" role="status" data-test="health">
        <span class="big">{{ st.healthy ? 'Healthy' : 'Needs attention' }}</span>
        <span class="grow">{{ healthText }}</span>
      </div>

      <div v-if="st.problems?.length" class="callout danger" role="alert" data-test="problems">
        <div class="grow"><strong>Problems found</strong>
          <ul class="plist"><li v-for="p in st.problems" :key="p" class="mono wrap">{{ p }}</li></ul>
        </div>
      </div>
      <div v-if="st.notes?.length" class="callout warn" data-test="notes">
        <div class="grow"><ul class="plist"><li v-for="n in st.notes" :key="n">{{ n }}</li></ul></div>
      </div>

      <div class="grid">
        <section class="panel" aria-labelledby="sig-h">
          <header><h2 id="sig-h">Signature</h2>
            <span class="pill" :class="sig.cls" data-test="sig">{{ sig.text }}</span></header>
          <div class="body stack">
            <p class="m0">{{ sigText }}</p>
            <dl v-if="st.signature !== 'unsigned'" class="kv">
              <dt>Type</dt><dd>{{ st.signature }}</dd>
              <dt>Key</dt><dd data-test="key-source">{{ keySourceText(st.key_source) }}</dd>
              <template v-if="st.fingerprint">
                <dt>Fingerprint</dt><dd class="mono" data-test="fingerprint">{{ st.fingerprint }}</dd>
                <dt>Public key</dt><dd><a :href="keyHref(st)" download="repo.pub" data-test="key-link">{{ keyHref(st) }}</a></dd>
              </template>
            </dl>
          </div>
        </section>
        <section class="panel" aria-labelledby="gen-h">
          <header><h2 id="gen-h">Repository</h2></header>
          <div class="body">
            <dl class="kv">
              <dt>Last update</dt><dd>{{ when(st.updated) }} <span class="muted">({{ absolute(st.updated) }})</span></dd>
              <dt>Packages</dt><dd>{{ st.packages }} ({{ st.versions }} versions)</dd>
              <dt>Channel</dt><dd class="mono">{{ st.channel }}</dd>
              <dt>Download</dt><dd class="mono wrap" data-test="download-url">{{ base || 'not configured' }}</dd>
              <dt>Retention</dt><dd>{{ st.keep > 0 ? `newest ${st.keep} versions of each package` : 'all versions are kept' }}</dd>
              <dt>Uploads</dt><dd>{{ source ? 'not here: this server reads a site published elsewhere' : st.uploads ? 'enabled (token required)' : 'disabled: read-only' }}</dd>
            </dl>
          </div>
        </section>
      </div>

      <section class="panel" aria-labelledby="cat-h">
        <header><h2 id="cat-h">Catalogs</h2></header>
        <div v-if="st.abis.length === 0" class="body muted">No catalogs yet: nothing has been uploaded.</div>
        <div v-else class="table-wrap">
          <table data-test="catalogs">
            <thead><tr><th scope="col">ABI</th><th scope="col">State</th><th scope="col">Signature</th><th scope="col" class="num">Packages</th><th scope="col" class="num">Versions</th><th scope="col">Updated</th></tr></thead>
            <tbody>
              <tr v-for="a in st.abis" :key="a.abi">
                <td class="mono">{{ a.abi }}<div v-if="a.problem" class="field-error mono wrap">{{ a.problem }}</div></td>
                <td><span class="pill" :class="a.consistent ? 'ok' : 'danger'">{{ a.consistent ? 'Consistent' : 'Inconsistent' }}</span></td>
                <td><span class="pill" :class="verificationLabel(a.verification, a.signature).cls">{{ verificationLabel(a.verification, a.signature).text }}</span></td>
                <td class="num">{{ a.packages }}</td><td class="num">{{ a.versions }}</td>
                <td class="nowrap" :title="absolute(a.updated)">{{ when(a.updated) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="panel" aria-labelledby="conf-h">
        <header><h2 id="conf-h">Client configuration</h2></header>
        <div class="body stack">
          <CopyBlock v-if="conf" :text="conf" label="client configuration" />
          <p v-else class="callout warn m0" data-test="no-download-url"><span class="grow">This server does not know the address clients install from. Start it with <code>--download-url</code>.</span></p>
        </div>
      </section>
    </div>
  </StatePanel>
</template>
