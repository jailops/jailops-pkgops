<script setup lang="ts">
// Deleting is irreversible, so the person must type the package name.
import { computed, ref } from 'vue'
import { api, describe } from '../api'
import { toast } from '../toast'
import Modal from './Modal.vue'

const props = defineProps<{ abi: string; name: string; version: string; latest: boolean; onlyVersion: boolean }>()
const emit = defineEmits<{ close: []; deleted: [] }>()
const typed = ref('')
const busy = ref(false)
const error = ref('')
const ok = computed(() => typed.value === props.name)

async function confirm() {
  if (!ok.value) return
  busy.value = true
  error.value = ''
  try {
    await api.remove(props.abi, props.name, props.version)
    toast('ok', `Deleted ${props.name} ${props.version} (${props.abi}).`)
    emit('deleted')
  } catch (e) {
    error.value = describe(e, 'delete the package')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="Delete package" label-id="delete-title" :busy="busy" @close="emit('close')">
    <p>
      Delete <strong class="mono">{{ name }} {{ version }}</strong> for <span class="mono">{{ abi }}</span>?
      Clients can no longer install this version, and this cannot be undone.
    </p>
    <p v-if="latest && !onlyVersion" class="callout warn">
      <span class="grow">This is the newest version, so <code>pkg upgrade</code> will fall back to the next older one.</span>
    </p>
    <p v-if="onlyVersion" class="callout warn">
      <span class="grow">This is the only version: the package disappears from the repository.</span>
    </p>
    <div class="field">
      <label for="confirm-name">Type <code>{{ name }}</code> to confirm</label>
      <input id="confirm-name" v-model="typed" data-autofocus type="text" autocomplete="off" spellcheck="false" aria-describedby="confirm-err"
             @keydown.enter.prevent="confirm" />
      <span id="confirm-err" class="field-error" role="alert">{{ error }}</span>
    </div>
    <template #footer>
      <button class="btn" type="button" :disabled="busy" @click="emit('close')">Cancel</button>
      <button class="btn btn-danger solid" type="button" :disabled="!ok || busy" @click="confirm">{{ busy ? 'Deleting…' : 'Delete package' }}</button>
    </template>
  </Modal>
</template>
