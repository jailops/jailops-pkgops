import { ref } from 'vue'

export type Theme = 'auto' | 'light' | 'dark'
const KEY = 'pkgops-theme'
function read(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') return v
  } catch { /* storage blocked: stay on auto */ }
  return 'auto'
}
export const theme = ref<Theme>(read())

export function applyTheme(t: Theme = theme.value): void {
  theme.value = t
  if (t === 'auto') document.documentElement.removeAttribute('data-theme')
  else document.documentElement.setAttribute('data-theme', t)
  try {
    if (t === 'auto') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, t)
  } catch { /* ignore */ }
}

export function nextTheme(): Theme {
  return theme.value === 'auto' ? 'light' : theme.value === 'light' ? 'dark' : 'auto'
}
