export interface RegistryLayer { digest: string; size: number }
export interface RegistryTag { name: string; digest: string; size: number; created: string | null; arches: string[]; layers: RegistryLayer[]; pull: string }
export interface RegistryRepoRow { name: string; listed: boolean; mode: 'public' | 'ips' | 'users'; tags: number; empty: boolean }
export interface RegistryRepoDetail { name: string; listed: boolean; mode: 'public' | 'ips' | 'users'; ips: string[]; user_ids: number[]; tags: RegistryTag[] }
export interface PublicRepoRow { name: string; tags: number }
export interface PushTokenRow { id: number; name: string; revoked: boolean; created_by: string; created_at: string }
export interface GCStatus { state: 'idle' | 'running' | 'done' | 'aborted'; removed: number; bytes_freed: number; duration_ms: number; error: string }

/** The pull command for a server-provided image reference (host/name:tag). */
export function pullCommand(ref: string): string {
  return `docker pull ${ref}`
}

/** Pages that work without an admin session: sign-in and the public registry browser. */
export function isOpenPath(path: string): boolean {
  return path === '/login' || path === '/browse/registry'
}

/** Newest first; tags without a created time last, ties by name. Does not mutate its input. */
export function sortTags(tags: RegistryTag[]): RegistryTag[] {
  const t = (x: RegistryTag) => (x.created ? Date.parse(x.created) : -Infinity)
  return [...tags].sort((a, b) => (t(b) === t(a) ? a.name.localeCompare(b.name) : t(b) > t(a) ? 1 : -1))
}

export function archLabel(arches: string[]): string {
  return arches.length ? arches.join(', ') : '—'
}

export interface ImageMirror {
  id: number; upstream: string; tag: string; local_name: string; platforms: string[]; keep: number
  username: string; has_credentials: boolean; upstream_digest: string; last_run: string | null; last_status: string; last_error: string
}
export interface UpstreamInfo { digest: string; platforms: string[] }

/** Local image name the server will default to: registry host and tag dropped; Docker Hub
 *  official images get library/. Mirrors imagemirror.Ref.DefaultLocalName for form prefill. */
export function defaultLocalName(ref: string): string {
  let s = ref.trim().split('@')[0]
  if (!s) return ''
  const parts = s.split('/')
  const last = parts.length - 1
  parts[last] = parts[last].split(':')[0]
  let hub = true
  if (parts.length > 1 && (parts[0].includes('.') || parts[0].includes(':') || parts[0] === 'localhost')) {
    hub = parts[0] === 'docker.io' || parts[0] === 'index.docker.io'
    parts.shift()
  }
  s = parts.join('/')
  return hub && parts.length === 1 ? `library/${s}` : s
}
