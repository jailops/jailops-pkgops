export function start(): Promise<{
  dir: string; base: string; bin: string; repo: string; fingerprint: string; pkgs: string[]
  secrets: { viewer: string; uploader: string; admin: string }; key: string
  stop(): void
}>
export const secrets: { viewer: string; uploader: string; admin: string }
