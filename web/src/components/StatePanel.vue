<script setup lang="ts">
// One place for the three non-happy states of a view: loading, error, empty.
defineProps<{ loading?: boolean; error?: string | null; empty?: boolean; emptyTitle?: string; emptyText?: string }>()
defineEmits<{ retry: [] }>()
</script>

<template>
  <div v-if="loading" class="loading" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span> Loading&hellip;</div>
  <div v-else-if="error" class="callout danger" role="alert" data-test="error">
    <div class="grow">
      <strong>Something went wrong.</strong>
      <p class="msg">{{ error }}</p>
      <button class="btn btn-sm" type="button" @click="$emit('retry')">Try again</button>
    </div>
  </div>
  <div v-else-if="empty" class="empty panel" data-test="empty">
    <h2>{{ emptyTitle }}</h2>
    <p v-if="emptyText">{{ emptyText }}</p>
    <slot name="empty" />
  </div>
  <slot v-else />
</template>
