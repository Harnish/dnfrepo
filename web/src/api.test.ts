import { describe, it, expect, vi, beforeEach } from 'vitest'
import { api, setUnauthorizedHandler, ApiError } from './api'

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
})
