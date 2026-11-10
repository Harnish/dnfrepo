export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
}

export type Mode = 'public' | 'ips' | 'users'
export interface Repo { slug: string; name: string; mode: Mode; previous: string | null; ips: string[]; keep: number; keep_overrides: Record<string, number> }
export interface Pkg { filename: string; name: string; version: string; release: string; arch: string; sha256: string; size: number; added_at: string }
export interface Source { id: number; kind: 'github' | 'plex'; spec: string; last_run: string | null; last_status: string; last_error: string }
export interface RepoUser { id: number; name: string; revoked: boolean }
export interface AuditEvent { id: number; actor: string; action: string; detail: string; at: string }
export interface PromoteResult { to: string; promoted: string[]; skipped: string[] }

let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(f: () => void) { onUnauthorized = f }

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Requested-With': 'dnfrepo' }
  let payload: BodyInit | undefined
  if (body instanceof FormData) payload = body
  else if (body !== undefined) { headers['Content-Type'] = 'application/json'; payload = JSON.stringify(body) }
  const res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin' })
  const data = await res.json().catch(() => ({}))
  if (res.status === 401) onUnauthorized()
  if (!res.ok) throw new ApiError(res.status, (data as { error?: string }).error ?? `HTTP ${res.status}`)
  return data as T
}

const e = encodeURIComponent

export const api = {
  me: () => req<{ email: string }>('GET', '/api/auth/me'),
  methods: () => req<{ local: boolean }>('GET', '/api/auth/methods'),
  localLogin: (email: string, password: string) => req<unknown>('POST', '/api/auth/local', { email, password }),
  logout: () => req<unknown>('POST', '/api/auth/logout'),

  repos: () => req<Repo[]>('GET', '/api/repos'),
  createRepo: (r: { slug: string; name: string; mode: Mode; previous?: string }) => req<Repo>('POST', '/api/repos', r),
  updateRepo: (slug: string, r: { name?: string; mode?: Mode }) => req<unknown>('PATCH', `/api/repos/${e(slug)}`, r),
  deleteRepo: (slug: string) => req<unknown>('DELETE', `/api/repos/${e(slug)}`),
  setPrevious: (slug: string, previous: string | null) => req<unknown>('PUT', `/api/repos/${e(slug)}/previous`, { previous }),
  setIPs: (slug: string, ips: string[]) => req<unknown>('PUT', `/api/repos/${e(slug)}/ips`, { ips }),

  packages: (slug: string) => req<Pkg[]>('GET', `/api/repos/${e(slug)}/packages`),
  upload: (slug: string, file: File) => {
    const fd = new FormData()
    fd.append('file', file, file.name)
    return req<Pkg>('POST', `/api/repos/${e(slug)}/packages`, fd)
  },
  deletePackage: (slug: string, filename: string) => req<unknown>('DELETE', `/api/repos/${e(slug)}/packages/${e(filename)}`),
  promote: (slug: string, filenames: string[]) => req<PromoteResult>('POST', `/api/repos/${e(slug)}/promote`, { filenames }),
  setKeep: (slug: string, keep: number, overrides: Record<string, number>) => req<{ purged: string[] }>('PUT', `/api/repos/${e(slug)}/keep`, { keep, overrides }),
  audit: (slug: string) => req<AuditEvent[]>('GET', `/api/repos/${e(slug)}/audit`),

  sources: (slug: string) => req<Source[]>('GET', `/api/repos/${e(slug)}/sources`),
  addSource: (slug: string, kind: string, spec: string) => req<Source>('POST', `/api/repos/${e(slug)}/sources`, { kind, spec }),
  deleteSource: (id: number) => req<unknown>('DELETE', `/api/sources/${id}`),
  runSource: (id: number) => req<unknown>('POST', `/api/sources/${id}/run`),

  repoUsers: () => req<RepoUser[]>('GET', '/api/repo-users'),
  createRepoUser: (name: string) => req<{ id: number; name: string; token: string }>('POST', '/api/repo-users', { name }),
  revokeRepoUser: (id: number) => req<unknown>('DELETE', `/api/repo-users/${id}`),
  repoGrants: (slug: string) => req<{ user_ids: number[] }>('GET', `/api/repos/${e(slug)}/users`),
  setRepoGrants: (slug: string, userIds: number[]) => req<unknown>('PUT', `/api/repos/${e(slug)}/users`, { user_ids: userIds }),
}

/** Fetches a users-mode repo's .repo file with the given credentials and fills in the placeholders. */
export async function repoFileWithToken(slug: string, user: string, token: string): Promise<string> {
  const res = await fetch(`/r/${e(slug)}/${e(slug)}.repo`, { headers: { Authorization: 'Basic ' + btoa(`${user}:${token}`) } })
  if (!res.ok) throw new ApiError(res.status, `could not fetch .repo (HTTP ${res.status}); is ${user} granted on ${slug}?`)
  return (await res.text()).replace('YOUR_USERNAME', user).replace('YOUR_TOKEN', token)
}

export function download(filename: string, text: string) {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  a.download = filename
  a.click()
  URL.revokeObjectURL(a.href)
}

export function fmtSize(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GiB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MiB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KiB'
  return n + ' B'
}
