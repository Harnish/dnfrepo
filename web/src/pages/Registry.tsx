import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, fmtSize } from '../api'
import type { GCStatus, RegistryRepoRow } from '../registry'
import ImageMirrors from './ImageMirrors'

export default function Registry() {
  const [repos, setRepos] = useState<RegistryRepoRow[]>([])
  const [gc, setGC] = useState<GCStatus | null>(null)
  const [err, setErr] = useState('')
  const load = useCallback(() => {
    api.registryRepos().then(setRepos).catch((x) => setErr(x.message))
    api.gcStatus().then(setGC).catch(() => {})
  }, [])
  useEffect(() => { load() }, [load])
  useEffect(() => {
    if (gc?.state !== 'running') return
    const t = setInterval(() => api.gcStatus().then((s) => { setGC(s); if (s.state !== 'running') load() }).catch(() => {}), 1500)
    return () => clearInterval(t)
  }, [gc?.state, load])

  async function save(r: RegistryRepoRow, listed: boolean, mode: string) {
    setErr('')
    try {
      const d = await api.registryRepo(r.name).catch(() => null)
      await api.setRegistryRepo(r.name, { listed, mode, ips: d?.ips ?? [], user_ids: d?.user_ids ?? [] })
      load()
    } catch (x) { setErr((x as Error).message) }
  }
  async function runGC() {
    if (!confirm('Run garbage collection? Pushes are refused (pulls keep working) until it finishes.')) return
    setErr('')
    try { await api.startGC(); setGC({ state: 'running', removed: 0, bytes_freed: 0, duration_ms: 0, error: '' }) } catch (x) { setErr((x as Error).message) }
  }

  return (
    <>
      <h1>Container registry</h1>
      <p className="muted">Images are public to pull and hidden from the public page until you tick <em>Listed</em>. Pushing always needs a <Link to="/push-tokens">push token</Link>.</p>
      {err && <p className="error">{err}</p>}
      <table>
        <thead><tr><th>Image</th><th>Tags</th><th>Listed</th><th>Pull access</th></tr></thead>
        <tbody>
          {repos.map((r) => (
            <tr key={r.name}>
              <td><Link to={`/registry/repo?name=${encodeURIComponent(r.name)}`}>{r.name}</Link>{r.empty && <span className="badge"> no images</span>}</td>
              <td>{r.tags}</td>
              <td><input type="checkbox" checked={r.listed} onChange={(e) => save(r, e.target.checked, r.mode)} aria-label={`list ${r.name}`} /></td>
              <td>
                <select value={r.mode} onChange={(e) => save(r, r.listed, e.target.value)}>
                  <option value="public">public</option><option value="ips">ips</option><option value="users">users</option>
                </select>
              </td>
            </tr>
          ))}
          {repos.length === 0 && <tr><td colSpan={4} className="muted">No images yet. Push one with a push token.</td></tr>}
        </tbody>
      </table>
      <ImageMirrors onChanged={load} />
      <div className="card">
        <h2>Garbage collection</h2>
        <p className="muted">Removes untagged manifests and unreferenced layers. Pushes are refused while it runs.</p>
        <div className="row">
          <button onClick={runGC} disabled={gc?.state === 'running'}>{gc?.state === 'running' ? 'Running…' : 'Run garbage collection'}</button>
          {gc?.state === 'done' && <span className="ok">Done: removed {gc.removed} files, freed {fmtSize(gc.bytes_freed)}.</span>}
          {gc?.state === 'aborted' && <span className="error">Aborted: {gc.error}</span>}
        </div>
      </div>
    </>
  )
}
