import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, Kind, Mode, Repo } from '../api'

// chains: each head (no previous) followed by its successors
function chains(repos: Repo[]): Repo[][] {
  const next = new Map(repos.filter((r) => r.previous).map((r) => [r.previous!, r]))
  return repos.filter((r) => !r.previous).map((head) => {
    const out = [head]
    for (let n = next.get(head.slug); n; n = next.get(n.slug)) out.push(n)
    return out
  })
}

export default function Repos() {
  const [repos, setRepos] = useState<Repo[]>([])
  const nav = useNavigate()
  const [mirror, setMirror] = useState(false)
  const [err, setErr] = useState('')
  const [f, setF] = useState({ slug: '', name: '', mode: 'public' as Mode, previous: '', kind: 'rpm' as Kind, suite: '', components: 'main', arches: 'amd64 arm64' })
  const load = useCallback(() => api.repos().then(setRepos).catch((x) => setErr(x.message)), [])
  useEffect(() => { load() }, [load])

  const hasNext = new Set(repos.map((r) => r.previous).filter(Boolean))
  const tails = repos.filter((r) => !hasNext.has(r.slug) && r.kind === f.kind) // a chain never mixes rpm and deb

  async function create(ev: FormEvent) {
    ev.preventDefault()
    setErr('')
    try {
      const words = (s: string) => s.split(/[\s,]+/).filter(Boolean)
      await api.createRepo({
        slug: f.slug, name: f.name || f.slug, mode: f.mode, previous: mirror ? undefined : f.previous || undefined,
        ...(f.kind === 'deb' ? { kind: 'deb' as Kind, suite: f.suite || undefined, components: words(f.components), arches: words(f.arches) } : {}),
      })
      const slug = f.slug
      setF({ ...f, slug: '', name: '', mode: 'public', previous: '' })
      if (mirror) { setMirror(false); nav(`/repos/${slug}?mirror=1`); return }
      load()
    } catch (x) { setErr((x as Error).message) }
  }

  return (
    <>
      <h1>Repos</h1>
      {err && <p className="error">{err}</p>}
      {chains(repos).map((c) => (
        <div className="card chain" key={c[0].slug}>
          {c.map((r, i) => (
            <span key={r.slug}>
              {i > 0 && <span className="arrow"> → </span>}
              <Link to={`/repos/${r.slug}`}>{r.name}</Link> <span className={`badge ${r.mode}`}>{r.mode}</span>{r.kind === 'deb' && <> <span className="badge">deb</span></>}
            </span>
          ))}
        </div>
      ))}
      {repos.length === 0 && <p className="muted">No repos yet. New here? <Link to="/help#first-repo">Start with the walkthrough</Link>.</p>}

      <form className="card" onSubmit={create}>
        <h2>New repo</h2>
        <div className="row">
          <label>Slug<input value={f.slug} onChange={(e) => setF({ ...f, slug: e.target.value })} pattern="[a-z0-9][a-z0-9-]*" required placeholder="dev" /></label>
          <label>Name<input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="Development" /></label>
          <label>Access
            <select value={f.mode} onChange={(e) => setF({ ...f, mode: e.target.value as Mode })}>
              <option value="public">public</option><option value="ips">ips</option><option value="users">users</option>
            </select>
          </label>
          <label>Format
            <select value={f.kind} onChange={(e) => setF({ ...f, kind: e.target.value as Kind, previous: '' })}>
              <option value="rpm">rpm (dnf)</option><option value="deb">deb (apt)</option>
            </select>
          </label>
          <label>Type
            <select value={mirror ? 'mirror' : 'standard'} onChange={(e) => setMirror(e.target.value === 'mirror')}>
              <option value="standard">Standard</option><option value="mirror">Mirror</option>
            </select>
          </label>
          <label>Promotes from
            <select value={f.previous} disabled={mirror} onChange={(e) => setF({ ...f, previous: e.target.value })}>
              <option value="">(none — first stage)</option>
              {tails.map((r) => <option key={r.slug} value={r.slug}>{r.name}</option>)}
            </select>
          </label>
        </div>
        {f.kind === 'deb' && (
          <div className="row">
            <label>Suite<input value={f.suite} onChange={(e) => setF({ ...f, suite: e.target.value })} placeholder={f.slug || 'stable'} /></label>
            <label>Components<input value={f.components} onChange={(e) => setF({ ...f, components: e.target.value })} placeholder="main" /></label>
            <label>Architectures<input value={f.arches} onChange={(e) => setF({ ...f, arches: e.target.value })} placeholder="amd64 arm64" /></label>
          </div>
        )}
        <button className="primary">Create</button>
      </form>
    </>
  )
}
