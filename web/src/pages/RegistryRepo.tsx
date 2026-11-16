import { Fragment, useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, fmtSize, RepoUser } from '../api'
import { archLabel, pullCommand, sortTags, type RegistryRepoDetail } from '../registry'

export default function RegistryRepo() {
  const name = useSearchParams()[0].get('name') ?? ''
  const nav = useNavigate()
  const [d, setD] = useState<RegistryRepoDetail | null>(null)
  const [users, setUsers] = useState<RepoUser[]>([])
  const [ips, setIPs] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const host = location.host
  const load = useCallback(() => {
    api.registryRepo(name).then((x) => { setD(x); setIPs(x.ips.join('\n')) }).catch((x) => setErr(x.message))
    api.repoUsers().then(setUsers).catch(() => {})
  }, [name])
  useEffect(() => { load() }, [load])
  if (!d) return <p className="muted pad">{err || 'Loading…'}</p>

  async function save(p: { listed?: boolean; mode?: string; ips?: string[]; user_ids?: number[] }) {
    setErr('')
    try {
      await api.setRegistryRepo(name, { listed: p.listed ?? d!.listed, mode: p.mode ?? d!.mode, ips: p.ips ?? d!.ips, user_ids: p.user_ids ?? d!.user_ids })
      load()
    } catch (x) { setErr((x as Error).message) }
  }
  async function delTag(tag: string) {
    if (!confirm(`Delete tag ${tag}? The layers stay until garbage collection.`)) return
    try { await api.deleteRegistryTag(name, tag); load() } catch (x) { setErr((x as Error).message) }
  }
  async function forget() {
    if (!confirm(`Remove the settings for ${name}? It goes back to public and unlisted.`)) return
    try { await api.deleteRegistryRepo(name); nav('/registry') } catch (x) { setErr((x as Error).message) }
  }

  return (
    <>
      <p><Link to="/registry">← Registry</Link></p>
      <h1>{name}</h1>
      {err && <p className="error">{err}</p>}
      <div className="card">
        <label className="inline"><input type="checkbox" checked={d.listed} onChange={(e) => save({ listed: e.target.checked })} /> Listed on the public page</label>
        <label>Pull access
          <select value={d.mode} onChange={(e) => save({ mode: e.target.value })}>
            <option value="public">public</option><option value="ips">ips</option><option value="users">users</option>
          </select>
        </label>
        {d.mode === 'ips' && (
          <label>Allowed IPs / CIDRs (one per line)
            <textarea rows={3} value={ips} onChange={(e) => setIPs(e.target.value)} />
            <button onClick={() => save({ ips: ips.split('\n').map((s) => s.trim()).filter(Boolean) })}>Save IPs</button>
          </label>
        )}
        {d.mode === 'users' && (
          <fieldset><legend>Users who may pull</legend>
            {users.filter((u) => !u.revoked).map((u) => (
              <label key={u.id} className="inline">
                <input type="checkbox" checked={d.user_ids.includes(u.id)}
                  onChange={(e) => save({ user_ids: e.target.checked ? [...d.user_ids, u.id] : d.user_ids.filter((i) => i !== u.id) })} /> {u.name}
              </label>
            ))}
            {users.length === 0 && <span className="muted">No repo users yet (create them under Repo users).</span>}
          </fieldset>
        )}
        <button onClick={forget}>Reset settings</button>
      </div>
      <h2>Tags</h2>
      <table>
        <thead><tr><th>Tag</th><th>Digest</th><th>Size</th><th>Created</th><th>Platforms</th><th /></tr></thead>
        <tbody>
          {sortTags(d.tags).map((t) => (
            <Fragment key={t.name}>
              <tr>
                <td>{t.name}</td>
                <td><code title={t.digest}>{t.digest.slice(7, 19)}</code></td>
                <td>{fmtSize(t.size)}</td>
                <td>{t.created ? new Date(t.created).toLocaleString() : '—'}</td>
                <td>{archLabel(t.arches)}</td>
                <td>
                  <button onClick={() => navigator.clipboard?.writeText(pullCommand(host, name, t.name))}>Copy pull</button>{' '}
                  <button onClick={() => setOpen(open === t.name ? null : t.name)}>{t.layers.length} layers</button>{' '}
                  <button onClick={() => delTag(t.name)}>Delete</button>
                </td>
              </tr>
              {open === t.name && (
                <tr><td colSpan={6}>
                  <pre>{pullCommand(host, name, t.name)}</pre>
                  {t.layers.map((l) => <div key={l.digest}><code>{l.digest.slice(7, 19)}</code> {fmtSize(l.size)}</div>)}
                </td></tr>
              )}
            </Fragment>
          ))}
          {d.tags.length === 0 && <tr><td colSpan={6} className="muted">No tags.</td></tr>}
        </tbody>
      </table>
    </>
  )
}
