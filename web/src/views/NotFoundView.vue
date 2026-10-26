<script setup lang="ts">
import { href } from '../router'
</script>

<template>
  <div class="page-head"><h1>Page not found</h1></div>
  <div class="empty panel"><h2>There is nothing at this address</h2><p><a :href="href('/')">Go to the overview</a> or <a :href="href('/packages')">browse packages</a>.</p></div>
</template>
