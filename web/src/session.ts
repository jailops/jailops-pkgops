// Sign-in state. A token is held in memory only (see api.ts): closing the tab
// or reloading signs out, by design. Behind an access gateway (Cloudflare
// Access) there is no token at all: the browser carries the gateway's cookie
// and probe() finds the identity the server verified.
import { computed, reactive } from 'vue'
import { ApiError, api, setToken } from './api'
import type { SessionInfo } from './types'

const state = reactive<{ info: SessionInfo | null; denied: string; ready: boolean }>({ info: null, denied: '', ready: false })

export const session = {
  info: computed(() => state.info),
  signedIn: computed(() => state.info !== null),
  // true when the gateway, not a token, identified the visitor: nothing to sign in or out of
  viaAccess: computed(() => state.info?.via === 'access'),
  // set when the gateway identified the visitor but the registry does not allow that identity
  denied: computed(() => state.denied),
  ready: computed(() => state.ready),
  canUpload: computed(() => state.info?.role === 'uploader' || state.info?.role === 'admin'),
  canDelete: computed(() => state.info?.role === 'admin'),
  canAdmin: computed(() => state.info?.role === 'admin'),
  // Asks the server who we are without a token: 200 = gateway identity, 401 = anonymous, 403 = not allowed.
  async probe(): Promise<void> {
    try {
      state.info = await api.session()
      state.denied = ''
    } catch (e) {
      state.info = null
      state.denied = e instanceof ApiError && e.status === 403 ? e.message : ''
    } finally {
      state.ready = true
    }
  },
  async signIn(token: string): Promise<SessionInfo> {
    setToken(token.trim())
    try {
      state.info = await api.session()
      return state.info
    } catch (e) {
      setToken('')
      state.info = null
      throw e
    }
  },
  signOut(): void {
    setToken('')
    state.info = null
  },
}
