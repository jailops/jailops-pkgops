<script setup lang="ts">
// Cleanup request for the next deploy. Nothing is deleted here: the files are left out of the
// next deploy. Flow: the server previews (dry run) -> the person types `prune` -> the real
// request -> github: commit link and a progress strip driven by the polled overview;
// manual: the steps to apply by hand (nothing was changed).
import { computed, onMounted, ref, watch } from 'vue'
import { api, describeAdmin } from '../api'
import { PRUNE_WORD, STAGES, confirmed, entriesFor, overallStage, refKey } from '../manage'
import { bytes } from '../format'
import type { PruneResult, Version } from '../types'
import CopyBlock from './CopyBlock.vue'
import Modal from './Modal.vue'

const props = defineProps<{ rows: Version[]; mode: string; live: Version[] }>()
const emit = defineEmits<{ close: []; requested: [] }>()

const reason = ref('')
const forced = ref(new Set<string>())
const typed = ref('')
const preview = ref<PruneResult | null>(null)
const result = ref<PruneResult | null>(null)
const previewing = ref(false)
const busy = ref(false)
const error = ref('')

const selected = computed(() => new Set(props.rows.map(refKey)))
const entries = computed(() => entriesFor(props.rows, selected.value, forced.value))
const total = computed(() => props.rows.reduce((n, v) => n + v.size, 0))
const ok = computed(() => !!preview.value && preview.value.entries.length > 0 && !error.value && confirmed(typed.value, PRUNE_WORD))
const manual = computed(() => (result.value ?? preview.value)?.mode === 'manual' || props.mode === 'manual')
const stage = computed(() => overallStage(result.value?.entries ?? [], props.live))

let seq = 0
async function dryRun() {
  const mine = ++seq
  previewing.value = true
  error.value = ''
  try {
    const r = await api.prune({ entries: entries.value, reason: reason.value.trim() || undefined, dry_run: true, confirm: PRUNE_WORD })
    if (mine === seq) preview.value = r
  } catch (e) {
    if (mine === seq) { preview.value = null; error.value = describeAdmin(e, 'preview the cleanup') }
  } finally {
    if (mine === seq) previewing.value = false
  }
}
onMounted(dryRun)

function toggleForce(v: Version, on: boolean) {
  const next = new Set(forced.value)
  on ? next.add(refKey(v)) : next.delete(refKey(v))
  forced.value = next
}
watch(forced, dryRun)

async function request() {
  if (!ok.value) return
  busy.value = true
  error.value = ''
  try {
    result.value = await api.prune({ entries: entries.value, reason: reason.value.trim() || undefined, confirm: PRUNE_WORD })
    emit('requested')
  } catch (e) {
    error.value = describeAdmin(e, 'request the cleanup')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="Remove from the next deploy" label-id="prune-title" :busy="busy" wide @close="emit('close')">
    <div class="stack" data-test="prune-dialog">
      <p class="callout info m0"><span class="grow">Nothing is deleted now. The files are left out of the next deploy; this <strong>takes effect after the next deploy</strong>.
        The permanent archive is not touched.</span></p>

      <template v-if="!result">
        <section aria-labelledby="prune-files-h">
          <h3 id="prune-files-h">Files ({{ rows.length }}, {{ bytes(total) }})</h3>
          <ul class="vlist files" data-test="prune-files">
            <li v-for="v in rows" :key="refKey(v)">
              <span class="mono wrap">{{ v.name }}-{{ v.version }}.pkg</span> <span class="muted">{{ v.abi }}, {{ bytes(v.size) }}</span>
              <div v-if="v.newest" class="callout warn mt4">
                <div class="grow">
                  <strong>This is the newest version.</strong> Pruning it makes <code>pkg upgrade</code> fall back to an older one, or removes the package.
                  <label class="check"><input type="checkbox" :checked="forced.has(refKey(v))" data-test="force" @change="toggleForce(v, ($event.target as HTMLInputElement).checked)" />
                    Force: prune the newest version of {{ v.name }}</label>
                </div>
              </div>
            </li>
          </ul>
        </section>

        <div class="field m0">
          <label for="prune-reason">Reason (optional, goes into the commit message)</label>
          <input id="prune-reason" v-model="reason" type="text" autocomplete="off" maxlength="200" />
        </div>

        <div v-if="previewing" class="loading" role="status"><span class="spinner" aria-hidden="true"></span> Asking the registry what would change&hellip;</div>
        <div v-if="error" class="callout danger" role="alert" data-test="prune-error"><span class="grow">{{ error }}</span></div>

        <section v-if="preview" aria-labelledby="prune-preview-h" data-test="prune-preview">
          <h3 id="prune-preview-h">Preview (dry run, nothing written)</h3>
          <p class="m0"><strong>{{ preview.entries.length }}</strong> file{{ preview.entries.length === 1 ? '' : 's' }} would be left out of the next deploy, freeing <strong data-test="prune-bytes">{{ bytes(preview.bytes) }}</strong>.</p>
          <p v-if="preview.skipped?.length" class="muted m0">{{ preview.skipped.length }} already listed or pruned: skipped.</p>
          <p class="muted" data-test="prune-effect">{{ preview.effect }}</p>
        </section>

        <div class="field m0">
          <label for="prune-confirm">Type <code>{{ PRUNE_WORD }}</code> to confirm</label>
          <input id="prune-confirm" v-model="typed" data-autofocus type="text" autocomplete="off" spellcheck="false" @keydown.enter.prevent="request" />
        </div>
      </template>

      <template v-else>
        <div v-if="manual" class="stack" data-test="prune-manual">
          <p class="callout warn m0" role="status"><span class="grow"><strong>Nothing was changed.</strong> PkgOps cannot commit here. Apply the change by hand; it takes effect after the next deploy.</span></p>
          <p class="muted m0" data-test="prune-effect">{{ result.effect }}</p>
          <div v-if="result.manual" class="stack">
            <h3>Diff for <span class="mono">{{ result.manual.file }}</span></h3>
            <CopyBlock :text="result.manual.diff" label="prune manifest diff" />
            <h3>Full file content</h3>
            <CopyBlock :text="result.manual.content" label="prune manifest content" />
            <h3>Commands</h3>
            <CopyBlock :text="result.manual.commands.join('\n')" label="commands" />
          </div>
        </div>
        <div v-else class="stack" data-test="prune-done">
          <p v-if="result.entries.length === 0" class="callout info m0" role="status"><span class="grow">Nothing to do: every file was already listed or pruned.</span></p>
          <p v-else class="callout ok m0" role="status"><span class="grow"><strong>Cleanup requested.</strong> It takes effect after the next deploy.
            <template v-if="result.commit_url"> Commit: <a :href="result.commit_url" target="_blank" rel="noopener noreferrer" data-test="commit-link">{{ (result.commit ?? '').slice(0, 10) || 'view commit' }}</a>.</template></span></p>
          <p class="muted m0" data-test="prune-effect">{{ result.effect }}</p>
          <ol v-if="result.entries.length" class="steps" aria-label="Cleanup progress" data-test="steps">
            <li v-for="(s, i) in STAGES" :key="s" :class="{ done: i < stage || stage === 3, current: i === stage && stage < 3 }" :aria-current="i === stage && stage < 3 ? 'step' : undefined" :data-test="'step-' + i">
              <span class="n" aria-hidden="true">{{ i < stage || stage === 3 ? '✓' : i + 1 }}</span> {{ s }}
              <span class="sr-only">{{ i < stage || stage === 3 ? ' (done)' : i === stage ? ' (now)' : ' (waiting)' }}</span>
            </li>
          </ol>
          <p v-if="result.entries.length" class="muted m0">This view refreshes by itself while the cleanup is pending. You can close it.</p>
        </div>
      </template>
    </div>
    <template #footer>
      <button class="btn" type="button" :disabled="busy" data-test="prune-close" @click="emit('close')">{{ result ? 'Close' : 'Cancel' }}</button>
      <button v-if="!result" class="btn btn-danger solid" type="button" :disabled="!ok || busy" data-test="prune-go" @click="request">{{ busy ? 'Requesting…' : 'Request cleanup' }}</button>
    </template>
  </Modal>
</template>
