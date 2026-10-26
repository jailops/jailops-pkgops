<script setup lang="ts">
// Accessible modal: role=dialog, labelled, focus moves in, Tab is trapped, Esc
// closes, focus returns to the opener.
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps<{ title: string; labelId: string; busy?: boolean; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
const root = ref<HTMLElement | null>(null)
let opener: HTMLElement | null = null

const focusable = () =>
  Array.from(root.value?.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])') ?? [])

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && !props.busy) { e.stopPropagation(); emit('close'); return }
  if (e.key !== 'Tab') return
  const f = focusable()
  if (f.length === 0) return
  const first = f[0]!
  const last = f[f.length - 1]!
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
}

onMounted(async () => {
  opener = document.activeElement as HTMLElement | null
  await nextTick()
  const target = root.value?.querySelector<HTMLElement>('[data-autofocus]') ?? focusable()[0]
  target?.focus()
})
onBeforeUnmount(() => opener?.focus?.())
</script>

<template>
  <div class="backdrop" @mousedown.self="!busy && emit('close')">
    <div ref="root" class="dialog" :class="{ wide }" role="dialog" aria-modal="true" :aria-labelledby="labelId" @keydown="onKey">
      <header><h2 :id="labelId">{{ title }}</h2></header>
      <div class="body"><slot /></div>
      <footer><slot name="footer" /></footer>
    </div>
  </div>
</template>
