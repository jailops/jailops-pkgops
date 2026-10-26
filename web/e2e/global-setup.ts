import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { start } from './fixtures/seed.mjs'

// Starts the seeded registry once; tests read its coordinates from e2e/.env.json.
export default async function globalSetup() {
  const s = await start()
  writeFileSync(join(import.meta.dirname, '.env.json'), JSON.stringify({ base: s.base, dir: s.dir, bin: s.bin, repo: s.repo, fingerprint: s.fingerprint, secrets: s.secrets, pkgs: s.pkgs }))
  return async () => { s.stop() }
}
