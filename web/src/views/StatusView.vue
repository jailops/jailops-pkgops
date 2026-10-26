<script setup lang="ts">
import { computed } from 'vue'
import CopyBlock from '../components/CopyBlock.vue'
import StatePanel from '../components/StatePanel.vue'
import { api } from '../api'
import { absolute, clientConf, when } from '../format'
import { useLoad } from '../load'

const { data: st, loading, error, reload } = useLoad(api.status, 'load the repository status')
const origin = location.origin
const conf = computed(() => (st.value ? clientConf(origin, st.value.channel, st.value.signature) : ''))
const sigText = computed(() => {
  switch (st.value?.signature) {
    case 'pubkey': return 'Signed. Clients trust one public key (signature_type "pubkey").'
    case 'fingerprints': return 'Signed. Clients trust the key by fingerprint (signature_type "fingerprints").'
    default: return 'Not signed. Clients cannot verify the catalog.'
  }
})
</script>

<template>
  <div class="page-head"><h1>Repository status</h1><button class="btn btn-sm" type="button" @click="reload">Refresh</button></div>
  <StatePanel :loading="loading" :error="error" @retry="reload">
    <div v-if="st" class="stack">
      <div class="hero" :class="st.healthy ? 'ok' : 'danger'" role="status" data-test="health">
        <span class="big">{{ st.healthy ? 'Healthy' : 'Needs attention' }}</span>
        <span class="grow">{{ st.healthy ? 'All catalogs verify against their signature and every advertised package is present with a matching checksum.' : 'A client may fail to update or install. Details below.' }}</span>
      </div>

      <div v-if="st.problems?.length" class="callout danger" role="alert" data-test="problems">
        <div class="grow"><strong>Problems found</strong>
          <ul class="plist"><li v-for="p in st.problems" :key="p" class="mono wrap">{{ p }}</li></ul>
        </div>
      </div>

      <div class="grid">
        <section class="panel" aria-labelledby="sig-h">
          <header><h2 id="sig-h">Signature</h2>
            <span class="pill" :class="st.signature === 'unsigned' ? 'warn' : 'ok'" data-test="sig">{{ st.signature === 'unsigned' ? 'Unsigned' : 'Signed' }}</span></header>
          <div class="body stack">
            <p class="m0">{{ sigText }}</p>
            <dl v-if="st.fingerprint" class="kv">
              <dt>Type</dt><dd>{{ st.signature }}</dd>
              <dt>Fingerprint</dt><dd class="mono" data-test="fingerprint">{{ st.fingerprint }}</dd>
              <dt>Public key</dt><dd><a href="/api/v1/key" download="repo.pub">Download repo.pub</a></dd>
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
              <dt>Retention</dt><dd>{{ st.keep > 0 ? `newest ${st.keep} versions of each package` : 'all versions are kept' }}</dd>
              <dt>Uploads</dt><dd>{{ st.uploads ? 'enabled (token required)' : 'disabled: read-only' }}</dd>
            </dl>
          </div>
        </section>
      </div>

      <section class="panel" aria-labelledby="cat-h">
        <header><h2 id="cat-h">Catalogs</h2></header>
        <div v-if="st.abis.length === 0" class="body muted">No catalogs yet: nothing has been uploaded.</div>
        <div v-else class="table-wrap">
          <table data-test="catalogs">
            <thead><tr><th scope="col">ABI</th><th scope="col">State</th><th scope="col" class="num">Packages</th><th scope="col" class="num">Versions</th><th scope="col">Updated</th></tr></thead>
            <tbody>
              <tr v-for="a in st.abis" :key="a.abi">
                <td class="mono">{{ a.abi }}<div v-if="a.problem" class="field-error mono wrap">{{ a.problem }}</div></td>
                <td><span class="pill" :class="a.consistent ? 'ok' : 'danger'">{{ a.consistent ? 'Consistent' : 'Inconsistent' }}</span></td>
                <td class="num">{{ a.packages }}</td><td class="num">{{ a.versions }}</td>
                <td class="nowrap" :title="absolute(a.updated)">{{ when(a.updated) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="panel" aria-labelledby="conf-h">
        <header><h2 id="conf-h">Client configuration</h2></header>
        <div class="body"><CopyBlock :text="conf" label="client configuration" /></div>
      </section>
    </div>
  </StatePanel>
</template>
