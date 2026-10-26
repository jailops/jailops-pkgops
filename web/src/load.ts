import { onMounted, ref, shallowRef, type Ref } from 'vue'
import { describe } from './api'

export interface Loader<T> { data: Ref<T | null>; loading: Ref<boolean>; error: Ref<string | null>; reload: () => Promise<void> }

// Loads once on mount and again on reload(). Errors become sentences.
export function useLoad<T>(fn: () => Promise<T>, action: string): Loader<T> {
  const data = shallowRef<T | null>(null) as Ref<T | null>
  const loading = ref(true)
  const error = ref<string | null>(null)
  let seq = 0
  async function reload() {
    const mine = ++seq
    loading.value = data.value === null
    error.value = null
    try {
      const v = await fn()
      if (mine === seq) data.value = v
    } catch (e) {
      if (mine === seq) { error.value = describe(e, action); data.value = null }
    } finally {
      if (mine === seq) loading.value = false
    }
  }
  onMounted(reload)
  return { data, loading, error, reload }
}
