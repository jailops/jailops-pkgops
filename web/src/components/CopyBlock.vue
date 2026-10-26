<script setup lang="ts">
import { ref } from 'vue'

const props = defineProps<{ text: string; label: string }>()
const done = ref(false)
async function copy() {
  try {
    await navigator.clipboard.writeText(props.text)
    done.value = true
    setTimeout(() => (done.value = false), 1800)
  } catch {
    done.value = false
  }
}
</script>

<template>
  <div class="copy">
    <pre class="code" tabindex="0" :aria-label="label">{{ text }}</pre>
    <button class="btn btn-sm" type="button" @click="copy">
      <span aria-live="polite">{{ done ? 'Copied' : 'Copy' }}</span><span class="sr-only"> {{ label }}</span>
    </button>
  </div>
</template>
