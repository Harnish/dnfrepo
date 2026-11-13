import { describe, it, expect, vi, beforeEach } from 'vitest'
import { api, setUnauthorizedHandler, ApiError, sourcesListLine } from './api'

function mockFetch(status: number, body: unknown) {
  const f = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  })
  vi.stubGlobal('fetch', f)
  return f
}

describe('api', () => {
  beforeEach(() => setUnauthorizedHandler(() => {}))

  it('sends CSRF header on POST', async () => {
    const f = mockFetch(201, { slug: 'dev' })
    await api.createRepo({ slug: 'dev', name: 'Dev', mode: 'public' })
    const [, init] = f.mock.calls[0]
    expect(init.method).toBe('POST')
    expect(init.headers['X-Requested-With']).toBe('dnfrepo')
    expect(JSON.parse(init.body)).toEqual({ slug: 'dev', name: 'Dev', mode: 'public' })
  })

  it('redirects to login on 401', async () => {
    mockFetch(401, { error: 'login required' })
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    await expect(api.repos()).rejects.toBeInstanceOf(ApiError)
    expect(handler).toHaveBeenCalledOnce()
  })

  it('surfaces server error messages', async () => {
    mockFetch(409, { error: 'repo has no next stage to promote into' })
    await expect(api.promote('prod', ['a.rpm'])).rejects.toThrow('no next stage')
  })

  it('setKeep puts the limits and returns purged files', async () => {
    const f = mockFetch(200, { purged: ['hello-1.2.3-4.noarch.rpm'] })
    const r = await api.setKeep('dev', 3, { plex: 5 })
    const [url, init] = f.mock.calls[0]
    expect(url).toBe('/api/repos/dev/keep')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body)).toEqual({ keep: 3, overrides: { plex: 5 } })
    expect(r.purged).toEqual(['hello-1.2.3-4.noarch.rpm'])
  })

  it('putMirror PUTs the body', async () => {
    const f = mockFetch(200, {})
    await api.putMirror('el9', { upstream_url: 'https://x/', gpg_key: '', schedule: { frequency: 'manual', time_of_day: '02:00', timezone: 'UTC', weekdays: [], day_of_month: 1, paused: false } })
    const [url, init] = f.mock.calls[0]
    expect(url).toBe('/api/repos/el9/mirror')
    expect(init.method).toBe('PUT')
    expect(init.headers['X-Requested-With']).toBe('dnfrepo')
    expect(JSON.parse(init.body).upstream_url).toBe('https://x/')
  })

  it('syncNow POSTs', async () => {
    const f = mockFetch(202, { status: 'started' })
    await api.syncNow('el9')
    const [url, init] = f.mock.calls[0]
    expect(url).toBe('/api/repos/el9/mirror/sync')
    expect(init.method).toBe('POST')
    expect(init.headers['X-Requested-With']).toBe('dnfrepo')
  })

  it('promoteBatch sends batch, not filenames', async () => {
    const f = mockFetch(200, { to: 'test', promoted: [], skipped: [] })
    await api.promoteBatch('el9', 12)
    const [url, init] = f.mock.calls[0]
    expect(url).toBe('/api/repos/el9/promote')
    expect(JSON.parse(init.body)).toEqual({ batch: 12 })
  })

  it('ApiError keeps the 400 payload', async () => {
    mockFetch(400, { error: 'values needed', missing_vars: ['releasever'] })
    const err = await api.putMirror('el9', {}).catch((x) => x)
    expect(err.data.missing_vars).toEqual(['releasever'])
  })

  it('parseRepo / batches / deleteMirror / mirror paths', async () => {
    const f = mockFetch(200, { sections: [] })
    await api.parseRepo('[a]')
    expect(f.mock.calls[0][0]).toBe('/api/mirror/parse-repo')
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ repo_file: '[a]' })
    await api.batches('el9'); expect(f.mock.calls[1][0]).toBe('/api/repos/el9/batches')
    await api.mirror('el9'); expect(f.mock.calls[2][0]).toBe('/api/repos/el9/mirror')
    await api.deleteMirror('el9'); expect(f.mock.calls[3][1].method).toBe('DELETE')
  })
})

describe('deb repos', () => {
  it('createRepo posts kind and deb fields', async () => {
    const f = mockFetch(201, { slug: 'd' })
    await api.createRepo({ slug: 'd', name: 'D', mode: 'public', kind: 'deb', suite: 'bookworm', components: ['main'], arches: ['amd64', 'arm64'] })
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ slug: 'd', name: 'D', mode: 'public', kind: 'deb', suite: 'bookworm', components: ['main'], arches: ['amd64', 'arm64'] })
  })

  it('sourcesListLine matches the browse page line', () => {
    const repo = { slug: 'd', kind: 'deb' as const, suite: 'bookworm', components: ['main', 'contrib'] }
    expect(sourcesListLine(repo, 'https://repo.example')).toBe('deb [signed-by=/etc/apt/keyrings/dnfrepo.asc] https://repo.example/r/d/ bookworm main contrib')
  })
})
