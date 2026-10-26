import { reactive } from 'vue'

export interface Toast { id: number; kind: 'ok' | 'danger'; text: string }
export const toasts = reactive<Toast[]>([])
let seq = 0

export function toast(kind: Toast['kind'], text: string, ms = 6000): void {
  const id = ++seq
  toasts.push({ id, kind, text })
  setTimeout(() => dismiss(id), ms)
}
export function dismiss(id: number): void {
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}
