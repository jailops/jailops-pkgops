<script setup lang="ts">
import { computed } from 'vue'
import CopyBlock from '../components/CopyBlock.vue'
import StatePanel from '../components/StatePanel.vue'
import { api } from '../api'
import { absolute, clientConf, plural, when } from '../format'
import { useLoad } from '../load'
import { href } from '../router'

const { data: st, loading, error, reload } = useLoad(api.status, 'load the repository status')
const origin = location.origin
const conf = computed(() => (st.value ? clientConf(origin, st.value.channel, st.value.signature) : ''))
const archs = computed(() => new Set(st.value?.abis.map((a) => a.arch)).size)
</script>

<template>
  <div class="page-head"><h1>Overview</h1></div>
  <StatePanel :loading="loading" :error="error" :empty="!!st && st.abis.length === 0" empty-title="This repository has no packages yet"
              empty-text="Build a package and upload it, then clients can install it with pkg." @retry="reload">
    <template #empty>
      <pre class="code inline">pkgops build --abi FreeBSD:15:amd64 -o dist
pkgops registry upload --url {{ origin }} --token $PKGOPS_TOKEN dist/*/*.pkg</pre>
    </template>
    <div v-if="st" class="stack">
      <div class="hero" :class="st.healthy ? 'ok' : 'danger'" data-test="health" role="status">
        <span class="big">{{ st.healthy ? 'Healthy' : 'Needs attention' }}</span>
        <span class="grow">
          <template v-if="st.healthy">Every catalog verifies and every advertised package is present with the right checksum.</template>
          <template v-else>{{ st.problems?.[0] }} <a :href="href('/status')">See repository status</a></template>
        </span>
        <span class="muted">Updated {{ when(st.updated) }}<span class="sr-only"> ({{ absolute(st.updated) }})</span></span>
      </div>

      <div class="stats">
        <div class="stat"><div class="v" data-test="n-packages">{{ st.packages }}</div><div class="l">Packages</div></div>
        <div class="stat"><div class="v" data-test="n-versions">{{ st.versions }}</div><div class="l">Versions</div></div>
        <div class="stat"><div class="v" data-test="n-archs">{{ archs }}</div><div class="l">{{ archs === 1 ? 'Architecture' : 'Architectures' }}</div></div>
        <div class="stat"><div class="v small">{{ st.signature === 'unsigned' ? 'Unsigned' : st.signature }}</div><div class="l">Signature</div></div>
      </div>

      <section class="panel" aria-labelledby="abis-h">
        <header><h2 id="abis-h">Platforms</h2><a :href="href('/status')">Repository status</a></header>
        <div class="table-wrap">
          <table>
            <thead><tr><th scope="col">ABI</th><th scope="col">Architecture</th><th scope="col" class="num">Packages</th><th scope="col" class="num">Versions</th><th scope="col">Catalog</th><th scope="col">Updated</th></tr></thead>
            <tbody>
              <tr v-for="a in st.abis" :key="a.abi">
                <td class="mono">{{ a.abi }}</td>
                <td>{{ a.arch }}</td>
                <td class="num">{{ a.packages }}</td>
                <td class="num">{{ a.versions }}</td>
                <td><span class="pill" :class="a.consistent ? 'ok' : 'danger'">{{ a.consistent ? 'Consistent' : 'Inconsistent' }}</span></td>
                <td class="nowrap">{{ when(a.updated) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="panel" aria-labelledby="use-h">
        <header><h2 id="use-h">Use this repository</h2><a :href="href('/packages')">Browse {{ plural(st.packages, 'package') }}</a></header>
        <div class="body stack">
          <p class="muted m0">Save as <code>/usr/local/etc/pkg/repos/myrepo.conf</code> on a FreeBSD machine<template v-if="st.signature === 'pubkey'">, with the public key (<a href="/api/v1/key" download="repo.pub">repo.pub</a>) in <code>/usr/local/etc/pkg/keys/myrepo.pub</code></template>, then run <code>pkg update &amp;&amp; pkg install &lt;name&gt;</code>.</p>
          <CopyBlock :text="conf" label="client configuration" />
          <p v-if="st.signature === 'unsigned'" class="callout warn m0"><span class="grow">This repository is not signed. Clients cannot verify what they download; add a signing key with <code>--key</code>.</span></p>
        </div>
      </section>
    </div>
  </StatePanel>
</template>
