<script setup lang="ts">
import { ref } from 'vue'
import { api, describe } from '../api'
import { toast } from '../toast'
import Modal from './Modal.vue'

const emit = defineEmits<{ close: []; uploaded: [] }>()
const file = ref<File | null>(null)
const busy = ref(false)
const error = ref('')

function pick(e: Event) {
  file.value = (e.target as HTMLInputElement).files?.[0] ?? null
  error.value = ''
}

async function submit() {
  if (!file.value) { error.value = 'Choose a .pkg file.'; return }
  busy.value = true
  error.value = ''
  try {
    const r = await api.upload(file.value)
    toast('ok', `Uploaded ${r.package.name} ${r.package.version}.`)
    emit('uploaded')
  } catch (e) {
    error.value = describe(e, 'upload the package')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="Upload package" label-id="upload-title" :busy="busy" @close="emit('close')">
    <form id="upload-form" @submit.prevent="submit" novalidate>
      <div class="field">
        <label for="pkgfile">Package file (.pkg)</label>
        <input id="pkgfile" data-autofocus type="file" accept=".pkg" aria-describedby="pkgfile-hint pkgfile-err" @change="pick" />
        <span id="pkgfile-hint" class="hint">Built with <code>pkgops build</code>. The registry verifies it, re-indexes and re-signs the repository.</span>
        <span id="pkgfile-err" class="field-error" role="alert">{{ error }}</span>
      </div>
    </form>
    <template #footer>
      <button class="btn" type="button" :disabled="busy" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" type="submit" form="upload-form" :disabled="busy">{{ busy ? 'Uploading…' : 'Upload' }}</button>
    </template>
  </Modal>
</template>
