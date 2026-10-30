import { FormEvent, useCallback, useEffect, useState } from 'react'
import { api, download, repoFileWithToken, Repo, RepoUser } from '../api'

export default function Users() {
  const [users, setUsers] = useState<RepoUser[]>([])
  const [repos, setRepos] = useState<Repo[]>([])
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null)
  const [dlSlug, setDlSlug] = useState('')
  const load = useCallback(() => {
    api.repoUsers().then(setUsers).catch((x) => setErr(x.message))
    api.repos().then(setRepos).catch(() => {})
  }, [])
  useEffect(() => { load() }, [load])
  const userRepos = repos.filter((r) => r.mode === 'users')

  async function create(ev: FormEvent) {
    ev.preventDefault()
    setErr('')
    try { const u = await api.createRepoUser(name); setCreated({ name: u.name, token: u.token }); setName(''); load() } catch (x) { setErr((x as Error).message) }
  }
  async function getRepoFile() {
    if (!created || !dlSlug) return
    try { download(`${dlSlug}.repo`, await repoFileWithToken(dlSlug, created.name, created.token)) } catch (x) { setErr((x as Error).message) }
  }

  return (
    <>
      <h1>Repo users</h1>
      <p className="muted">People who pull from repos in <em>users</em> mode. Grant them on each repo's page.</p>
      {err && <p className="error">{err}</p>}
      {created && (
        <div className="card notice">
          <strong>Token for {created.name} — shown once, copy it now:</strong>
          <pre>{created.token}</pre>
          <p className="muted">Once granted on a repo, you can download a ready-to-use .repo file with these credentials filled in:</p>
          <div className="row">
            <select value={dlSlug} onChange={(e) => setDlSlug(e.target.value)}>
              <option value="">choose repo…</option>
              {userRepos.map((r) => <option key={r.slug} value={r.slug}>{r.name}</option>)}
            </select>
            <button onClick={getRepoFile} disabled={!dlSlug}>Download .repo</button>
            <button onClick={() => setCreated(null)}>Dismiss</button>
          </div>
        </div>
      )}
      <table>
        <thead><tr><th>Name</th><th>Status</th><th /></tr></thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id}>
              <td>{u.name}</td>
              <td>{u.revoked ? <span className="badge ips">revoked</span> : 'active'}</td>
              <td>{!u.revoked && <button onClick={async () => { await api.revokeRepoUser(u.id); load() }}>Revoke</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <form className="card" onSubmit={create}>
        <h2>New user</h2>
        <div className="row">
          <label>Username (no colons)<input value={name} onChange={(e) => setName(e.target.value)} required /></label>
        </div>
        <button className="primary">Create &amp; generate token</button>
      </form>
    </>
  )
}
