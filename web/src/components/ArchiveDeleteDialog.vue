<script setup lang="ts">
// Permanent deletion from the archive. Deliberately its own dialog, never part of a Pages prune.
import { computed, ref } from 'vue'
import { api, describeAdmin } from '../api'
import { ARCHIVE_WORD, confirmed, refKey } from '../manage'
import { bytes } from '../format'
import type { Version } from '../types'
import Modal from './Modal.vue'

const props = defineProps<{ rows: Version[] }>()
const emit = defineEmits<{ close: []; deleted: [] }>()
const reason = ref('')
const typed = ref('')
const busy = ref(false)
const error = ref('')
const total = computed(() => props.rows.reduce((n, v) => n + v.size, 0))
const ok = computed(() => confirmed(typed.value, ARCHIVE_WORD) && reason.value.trim() !== '')

async function go() {
  if (!ok.value) return
  busy.value = true
  error.value = ''
  try {
    await api.archiveDelete({ refs: props.rows.map((v) => ({ abi: v.abi, name: v.name, version: v.version })), reason: reason.value.trim(), confirm: ARCHIVE_WORD })
    emit('deleted')
  } catch (e) {
    error.value = describeAdmin(e, 'delete from the archive')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="Delete from archive" label-id="archive-title" :busy="busy" wide @close="emit('close')">
    <div class="stack" data-test="archive-dialog">
      <p class="callout danger m0"><span class="grow"><strong>This permanently deletes the archived files.</strong> They cannot be restored from anywhere.
        This is <strong>not</strong> part of a Pages cleanup: pruning a file from the site never touches the archive, and this never changes what the site serves.</span></p>
      <section aria-labelledby="archive-files-h">
        <h3 id="archive-files-h">Archived files ({{ rows.length }}, {{ bytes(total) }})</h3>
        <ul class="vlist files" data-test="archive-files">
          <li v-for="v in rows" :key="refKey(v)"><span class="mono wrap">{{ v.name }}-{{ v.version }}.pkg</span> <span class="muted">{{ v.abi }}, {{ bytes(v.size) }}</span></li>
        </ul>
      </section>
      <div class="field m0">
        <label for="archive-reason">Reason (required, recorded in the log)</label>
        <input id="archive-reason" v-model="reason" data-autofocus type="text" autocomplete="off" maxlength="200" />
      </div>
      <div class="field m0">
        <label for="archive-confirm">Type <code>{{ ARCHIVE_WORD }}</code> to confirm</label>
        <input id="archive-confirm" v-model="typed" type="text" autocomplete="off" spellcheck="false" @keydown.enter.prevent="go" />
      </div>
      <div v-if="error" class="callout danger" role="alert" data-test="archive-error"><span class="grow">{{ error }}</span></div>
    </div>
    <template #footer>
      <button class="btn" type="button" :disabled="busy" @click="emit('close')">Cancel</button>
      <button class="btn btn-danger solid" type="button" :disabled="!ok || busy" data-test="archive-go" @click="go">{{ busy ? 'Deleting…' : 'Delete from archive permanently' }}</button>
    </template>
  </Modal>
</template>
