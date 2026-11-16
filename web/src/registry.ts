export interface RegistryLayer { digest: string; size: number }
export interface RegistryTag { name: string; digest: string; size: number; created: string | null; arches: string[]; layers: RegistryLayer[]; pull: string }
export interface RegistryRepoRow { name: string; listed: boolean; mode: 'public' | 'ips' | 'users'; tags: number; empty: boolean }
export interface RegistryRepoDetail { name: string; listed: boolean; mode: 'public' | 'ips' | 'users'; ips: string[]; user_ids: number[]; tags: RegistryTag[] }
export interface PublicRepoRow { name: string; tags: number }
export interface PushTokenRow { id: number; name: string; revoked: boolean; created_by: string; created_at: string }
export interface GCStatus { state: 'idle' | 'running' | 'done' | 'aborted'; removed: number; bytes_freed: number; duration_ms: number; error: string }

export function pullCommand(host: string, name: string, tag: string): string {
  return `docker pull ${host}/${name}:${tag}`
}

/** Newest first; tags without a created time last, ties by name. Does not mutate its input. */
export function sortTags(tags: RegistryTag[]): RegistryTag[] {
  const t = (x: RegistryTag) => (x.created ? Date.parse(x.created) : -Infinity)
  return [...tags].sort((a, b) => (t(b) === t(a) ? a.name.localeCompare(b.name) : t(b) > t(a) ? 1 : -1))
}

export function archLabel(arches: string[]): string {
  return arches.length ? arches.join(', ') : '—'
}
