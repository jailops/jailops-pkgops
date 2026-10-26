<script setup lang="ts">
import { computed, watchEffect } from 'vue'
import { ref } from 'vue'
import LoginDialog from './components/LoginDialog.vue'
import OverviewView from './views/OverviewView.vue'
import PackagesView from './views/PackagesView.vue'
import PackageView from './views/PackageView.vue'
import ManageView from './views/ManageView.vue'
import StatusView from './views/StatusView.vue'
import NotFoundView from './views/NotFoundView.vue'
import { href, route } from './router'
import { session } from './session'
import { applyTheme, nextTheme, theme } from './theme'
import { dismiss, toast, toasts } from './toast'

const login = ref(false)
const views = { overview: OverviewView, packages: PackagesView, package: PackageView, status: StatusView, manage: ManageView, notfound: NotFoundView }
const current = computed(() => views[route.value.name])
const titles = { overview: 'Overview', packages: 'Packages', package: 'Package', status: 'Repository status', manage: 'Manage', notfound: 'Not found' }
watchEffect(() => { document.title = `${titles[route.value.name]} - PkgOps registry` })

const navs = [
  { name: 'overview', label: 'Overview', to: '/' },
  { name: 'packages', label: 'Packages', to: '/packages' },
  { name: 'status', label: 'Status', to: '/status' },
  { name: 'manage', label: 'Manage', to: '/manage' },
] as const
const active = (n: string) => route.value.name === n || (n === 'packages' && route.value.name === 'package')
const themeLabel = computed(() => ({ auto: 'Theme: automatic', light: 'Theme: light', dark: 'Theme: dark' })[theme.value])

void session.probe()
function signOut() { session.signOut(); toast('ok', 'Signed out.') }
// Re-keying the view on the route makes every navigation load fresh data.
const viewKey = computed(() => route.value.name + route.value.params.join('/'))
</script>

<template>
  <a class="skip" href="#content">Skip to content</a>
  <header class="topbar">
    <div class="topbar-in">
      <a class="brand" :href="href('/')">
        <svg width="20" height="20" viewBox="0 0 16 16" aria-hidden="true"><rect width="16" height="16" rx="3" fill="currentColor" /><path d="M8 3l4 2v6l-4 2-4-2V5z" fill="none" stroke="var(--surface)" stroke-width="1.4" /></svg>
        PkgOps <small>registry</small>
      </a>
      <nav class="main" aria-label="Main">
        <a v-for="n in navs" :key="n.name" :href="href(n.to)" :aria-current="active(n.name) ? 'page' : undefined">{{ n.label }}</a>
      </nav>
      <div class="top-actions">
        <button class="btn btn-sm btn-ghost" type="button" :aria-label="themeLabel + '. Change theme'" data-test="theme" @click="applyTheme(nextTheme())">
          {{ theme === 'dark' ? 'Dark' : theme === 'light' ? 'Light' : 'Auto' }}
        </button>
        <template v-if="session.signedIn.value">
          <span class="pill info" data-test="who">{{ session.info.value?.name }} &middot; {{ session.info.value?.role }}<template v-if="session.viaAccess.value"> &middot; via Access</template></span>
          <button v-if="!session.viaAccess.value" class="btn btn-sm" type="button" data-test="signout" @click="signOut">Sign out</button>
        </template>
        <span v-else-if="session.denied.value" class="pill danger" data-test="denied-pill">Identity not allowed</span>
        <button v-else-if="session.ready.value" class="btn btn-sm btn-primary" type="button" data-test="signin" @click="login = true">Sign in</button>
      </div>
    </div>
  </header>
  <main id="content" tabindex="-1">
    <div v-if="session.denied.value" class="callout danger" role="alert" data-test="denied">
      <div class="grow"><h1>Your identity is not allowed to use this registry</h1>
        <p class="msg">{{ session.denied.value }}</p>
        <p class="m0">Ask an administrator to add you, then reload this page.</p></div>
    </div>
    <div v-else-if="!session.ready.value" class="loading" role="status"><span class="spinner" aria-hidden="true"></span> Loading&hellip;</div>
    <component :is="current" v-else :key="viewKey" />
  </main>
  <footer class="foot">Packages are served as a plain static repository: <code>pkg</code> clients never use this UI.</footer>
  <LoginDialog v-if="login" @close="login = false" />
  <div class="toasts" aria-live="polite" aria-atomic="false">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind" :role="t.kind === 'danger' ? 'alert' : 'status'">
      <span class="grow">{{ t.text }}</span>
      <button class="btn btn-sm btn-ghost" type="button" aria-label="Dismiss notification" @click="dismiss(t.id)">&times;</button>
    </div>
  </div>
</template>
