import { mkdirSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

// The build lands in the Go embed directory. emptyOutDir would delete the tracked
// placeholder that lets `go build` compile before the first UI build, so put it back.
const outDir = resolve(import.meta.dirname, '../internal/webui/dist')
const keepPlaceholder = () => ({
  name: 'keep-gitkeep',
  closeBundle() {
    mkdirSync(outDir, { recursive: true })
    writeFileSync(join(outDir, '.gitkeep'), '')
  },
})

// During development the UI runs on its own port and the registry API and the
// repository tree are proxied to a running `pkgops serve` (PKGOPS_API).
const target = process.env.PKGOPS_API || 'http://127.0.0.1:39112'

export default defineConfig({
  base: '/ui/',
  plugins: [vue(), keepPlaceholder()],
  server: { port: 5173, proxy: { '/api': target } },
  build: { outDir, emptyOutDir: true, target: 'es2022', assetsInlineLimit: 0, chunkSizeWarningLimit: 300 },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'] },
})
