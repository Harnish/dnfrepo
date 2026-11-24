import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, fmtSize } from '../api'
import type { GCStatus, RegistryRepoRow } from '../registry'
import ImageMirrors from './ImageMirrors'
import { FilterBox, Pager, SortTh, useTable } from './Pager'

export default function Registry() {
  const [repos, setRepos] = useState<RegistryRepoRow[]>([])
  const [gc, setGC] = useState<GCStatus | null>(null)
  const [err, setErr] = useState('')
  const [params, setParams] = useSearchParams()
  const imgT = useTable(repos, (r, k) => r[k as keyof RegistryRepoRow] as string | number, (r) => [r.name, r.mode])
  const tabs: [string, string][] = [['images', `Images (${repos.length})`], ['mirrors', 'Mirrors'], ['gc', 'Garbage collection']]
  const tab = tabs.some(([k]) => k === params.get('tab')) ? params.get('tab')! : 'images'
  const setTab = (k: string) => setParams((p) => { const n = new URLSearchParams(p); n.set('tab', k); return n }, { replace: true })
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
      <nav className="tabs" role="tablist">
        {tabs.map(([k, label]) => (
          <button key={k} type="button" role="tab" aria-selected={tab === k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{label}</button>
        ))}
      </nav>
      {tab === 'images' && <>
      {repos.length > 0 && <FilterBox {...imgT.filter} />}
      <table>
        <thead><tr><SortTh k="name" label="Image" t={imgT} /><SortTh k="tags" label="Tags" t={imgT} /><th>Listed</th><SortTh k="mode" label="Pull access" t={imgT} /></tr></thead>
        <tbody>
          {imgT.pg.items.map((r) => (
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
          {repos.length > 0 && imgT.pg.total === 0 && <tr><td colSpan={4} className="muted">No matches.</td></tr>}
          {repos.length === 0 && <tr><td colSpan={4} className="muted">No images yet. Push one with a push token, or add a mirror.</td></tr>}
        </tbody>
      </table>
      <Pager {...imgT.pg} />
      </>}
      {tab === 'mirrors' && <ImageMirrors onChanged={load} />}
      {tab === 'gc' && <div className="card">
        <h2>Garbage collection</h2>
        <p className="muted">Removes untagged manifests and unreferenced layers. Pushes are refused while it runs.</p>
        <div className="row">
          <button onClick={runGC} disabled={gc?.state === 'running'}>{gc?.state === 'running' ? 'Running…' : 'Run garbage collection'}</button>
          {gc?.state === 'done' && <span className="ok">Done: removed {gc.removed} files, freed {fmtSize(gc.bytes_freed)}.</span>}
          {gc?.state === 'aborted' && <span className="error">Aborted: {gc.error}</span>}
        </div>
      </div>}
    </>
  )
}
