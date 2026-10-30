import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, Mode, Repo } from '../api'

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
  const [err, setErr] = useState('')
  const [f, setF] = useState({ slug: '', name: '', mode: 'public' as Mode, previous: '' })
  const load = useCallback(() => api.repos().then(setRepos).catch((x) => setErr(x.message)), [])
  useEffect(() => { load() }, [load])

  const hasNext = new Set(repos.map((r) => r.previous).filter(Boolean))
  const tails = repos.filter((r) => !hasNext.has(r.slug))

  async function create(ev: FormEvent) {
    ev.preventDefault()
    setErr('')
    try {
      await api.createRepo({ slug: f.slug, name: f.name || f.slug, mode: f.mode, previous: f.previous || undefined })
      setF({ slug: '', name: '', mode: 'public', previous: '' })
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
              <Link to={`/repos/${r.slug}`}>{r.name}</Link> <span className={`badge ${r.mode}`}>{r.mode}</span>
            </span>
          ))}
        </div>
      ))}
      {repos.length === 0 && <p className="muted">No repos yet.</p>}

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
          <label>Promotes from
            <select value={f.previous} onChange={(e) => setF({ ...f, previous: e.target.value })}>
              <option value="">(none — first stage)</option>
              {tails.map((r) => <option key={r.slug} value={r.slug}>{r.name}</option>)}
            </select>
          </label>
        </div>
        <button className="primary">Create</button>
      </form>
    </>
  )
}
