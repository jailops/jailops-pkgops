// Builds a seeded registry for the e2e run: a real pkgops binary, real .pkg
// files, a signed repository, three tokens, and a running `pkgops serve`.
import { execFileSync, spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const root = resolve(import.meta.dirname, '../../..')
export const secrets = { viewer: 'viewer-secret-0001', uploader: 'uploader-secret-0002', admin: 'admin-secret-0003' }
const sha = (s) => createHash('sha256').update(s).digest('hex')

function freePort() {
  return new Promise((res, rej) => {
    const s = createServer()
    s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => res(port)) })
    s.on('error', rej)
  })
}

const common = (name, version, abi, extra = '') => `name: ${name}
version: "${version}"
comment: ${extra.split('\n')[0] || name + ' for the e2e registry'}
maintainer: e2e@example.org
www: https://example.org/${name}
abi: ${abi}
licenses: [BSD2CLAUSE]
categories: [sysutils]
`

export const specs = {
  hello: (v, abi) => common('hello', v, abi, 'Tiny example daemon') + `desc: |
  hello answers HTTP requests with a greeting.
  It exists to exercise the registry UI.
files:
  - {content: "#!/bin/sh\\necho hello ${v}\\n", destination: bin/hello-${v}, mode: 0755}
  - {symlink: hello-${v}, destination: bin/hello}
  - {content: "greeting=hi\\n", destination: etc/hello/hello.conf, config: true}
services:
  - {name: hello, content: "#!/bin/sh\\n# PROVIDE: hello\\n"}
users:
  - {name: hello, home: /var/db/hello}
dirs:
  - {path: /var/db/hello, owner: hello, group: hello, mode: 0750}
scripts:
  post-install: "echo installed"
`,
  nginxlike: (v, abi) => common('web-gateway', v, abi, 'Reverse proxy used to test search') + `deps:
  - {name: pcre2, origin: devel/pcre2, version: "10.49"}
files:
  - {content: "binary\\n", destination: sbin/web-gateway, mode: 0755}
`,
}

export async function start() {
  const dir = mkdtempSync(join(tmpdir(), 'pkgops-e2e-'))
  const bin = join(dir, 'pkgops')
  execFileSync('go', ['build', '-o', bin, './cmd/pkgops'], { cwd: root, stdio: 'inherit' })
  const run = (args, opt = {}) => execFileSync(bin, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], ...opt })

  const key = join(dir, 'repo.key')
  const out = run(['key', 'generate', '--out', key, '--bits', '2048'])
  const fingerprint = /fingerprint: (\w+)/.exec(out)[1]

  mkdirSync(join(dir, 'specs'))
  const pkgs = []
  const build = (spec, name, v, abi) => {
    const f = join(dir, 'specs', `${name}-${v}-${abi.replaceAll(':', '_')}.yaml`)
    writeFileSync(f, spec)
    const p = run(['build', '-f', f, '-o', join(dir, 'dist')], { env: { ...process.env, SOURCE_DATE_EPOCH: '1700000000' } }).trim()
    pkgs.push(p)
    return p
  }
  for (const abi of ['FreeBSD:15:amd64', 'FreeBSD:15:aarch64']) {
    build(specs.hello('0.1.0', abi), 'hello', '0.1.0', abi)
    build(specs.hello('0.1.1', abi), 'hello', '0.1.1', abi)
  }
  build(specs.nginxlike('2.4.0', 'FreeBSD:15:amd64'), 'gateway', '2.4.0', 'FreeBSD:15:amd64')
  const repo = join(dir, 'repo')
  run(['repo', '--repo', repo, '--key', key, '--files-list', ...pkgs], { env: { ...process.env, SOURCE_DATE_EPOCH: '1700000000' } })

  const tokens = join(dir, 'tokens')
  writeFileSync(tokens, [`vera viewer ${sha(secrets.viewer)}`, `ursula uploader ${sha(secrets.uploader)}`, `alice admin ${sha(secrets.admin)}`].join('\n') + '\n')

  const port = await freePort()
  const child = spawn(bin, ['serve', '--ui', '--repo', repo, '--listen', `127.0.0.1:${port}`, '--token-file', tokens, '--key', key], { stdio: ['ignore', 'inherit', 'inherit'] })
  const base = `http://127.0.0.1:${port}`
  let up = false
  for (let i = 0; i < 100 && !up; i++) {
    try { up = (await fetch(`${base}/api/v1/health`)).ok } catch { /* not up yet */ }
    if (!up) await new Promise((r) => setTimeout(r, 100))
  }
  // fail here, in a second, instead of letting every test wait for a page that is never served
  if (!up) { child.kill('SIGTERM'); throw new Error(`pkgops serve did not answer on ${base}`) }
  const ui = await fetch(`${base}/ui/`).catch(() => null)
  if (!ui || !ui.ok) { child.kill('SIGTERM'); throw new Error(`pkgops serve does not serve the web UI at ${base}/ui/ (status ${ui?.status}): is --ui set, and is the UI built into the binary?`) }
  return {
    dir, base, bin, repo, fingerprint, pkgs, secrets, key,
    spec: (name, v, abi) => specs[name](v, abi),
    stop() { child.kill('SIGTERM'); try { if (dir.includes('pkgops-e2e-')) rmSync(dir, { recursive: true, force: true }) } catch { /* ignore */ } },
  }
}
