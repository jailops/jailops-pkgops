<script setup lang="ts">
import { ref } from 'vue'
import { describe } from '../api'
import { session } from '../session'
import { toast } from '../toast'
import Modal from './Modal.vue'

const emit = defineEmits<{ close: [] }>()
const token = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  if (!token.value.trim()) { error.value = 'Enter a token.'; return }
  busy.value = true
  error.value = ''
  try {
    const s = await session.signIn(token.value)
    token.value = ''
    toast('ok', `Signed in as ${s.name} (${s.role}).`)
    emit('close')
  } catch (e) {
    error.value = describe(e, 'sign in')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal title="Sign in" label-id="login-title" :busy="busy" @close="emit('close')">
    <form id="login-form" @submit.prevent="submit" novalidate>
      <div class="field">
        <label for="token">Registry token</label>
        <input id="token" v-model="token" data-autofocus type="password" autocomplete="off" spellcheck="false"
               :aria-invalid="error ? 'true' : undefined" aria-describedby="token-hint token-err" />
        <span id="token-hint" class="hint">Create one with <code>pkgops serve token</code>. It is kept in this tab's memory only and is gone when you reload.</span>
        <span id="token-err" class="field-error" role="alert">{{ error }}</span>
      </div>
    </form>
    <template #footer>
      <button class="btn" type="button" :disabled="busy" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" type="submit" form="login-form" :disabled="busy">{{ busy ? 'Checking…' : 'Sign in' }}</button>
    </template>
  </Modal>
</template>
