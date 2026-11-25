import { FormEvent, useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import { defaultLocalName, type ImageMirror } from '../registry'

const blank = { upstream: '', local: '', localTouched: false, keep: 3, username: '', token: '' }

/** Registry image mirrors: follow an upstream tag, copy it when it changes, keep the last N. */
export default function ImageMirrors({ onChanged }: { onChanged: () => void }) {
  const [rows, setRows] = useState<ImageMirror[]>([])
  const [f, setF] = useState(blank)
  const [editing, setEditing] = useState<ImageMirror | null>(null)
  const [avail, setAvail] = useState<string[] | null>(null) // null = not loaded
  const [sel, setSel] = useState<string[]>([])
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => { api.imageMirrors().then(setRows).catch((x) => setErr(x.message)) }, [])
  useEffect(() => { load() }, [load])
  const later = () => setTimeout(() => { load(); onChanged() }, 2000) // runs are detached

  const run = async (fn: () => Promise<unknown>, ok = '') => {
    setErr(''); setMsg('')
    try { await fn(); if (ok) setMsg(ok) } catch (x) { setErr((x as Error).message) }
  }

  function reset() { setF(blank); setEditing(null); setAvail(null); setSel([]) }

  function edit(m: ImageMirror) {
    setEditing(m)
    setF({ upstream: `${m.upstream}:${m.tag}`, local: m.local_name, localTouched: true, keep: m.keep, username: m.username, token: '' })
    setSel(m.platforms); setAvail(null)
  }

  async function loadPlatforms() {
    setBusy(true)
    await run(async () => {
      const info = editing && !f.token
        ? await api.inspectUpstream({ id: editing.id })
        : await api.inspectUpstream({ upstream: f.upstream, username: f.username || undefined, token: f.token || undefined })
      setAvail(info.platforms)
      setSel((s) => s.filter((p) => info.platforms.includes(p)))
    })
    setBusy(false)
  }

  function save(ev: FormEvent) {
    ev.preventDefault()
    const creds = f.token ? { username: f.username, token: f.token } : {}
    run(async () => {
      if (editing) await api.updateImageMirror(editing.id, { platforms: sel, keep: f.keep, ...creds })
      else await api.addImageMirror({ upstream: f.upstream, local_name: f.local, platforms: sel, keep: f.keep, ...creds })
      reset(); load(); later()
    }, editing ? 'Saved.' : 'Mirror added; first copy started.')
  }

  const toggle = (p: string) => setSel((s) => s.includes(p) ? s.filter((x) => x !== p) : [...s, p])

  return (
    <div className="card">
      <h2>Mirrors</h2>
      <p className="muted">Follow an upstream image tag: checked when added, every 24 h, or with <em>Check now</em>. A change is copied in and tagged <code>&lt;tag&gt;-YYYYMMDD</code>; the newest N copies are kept. Run garbage collection to free removed ones.</p>
      {err && <p className="error">{err}</p>}
      {msg && <p className="ok">{msg}</p>}
      <table>
        <thead><tr className="nw"><th>Upstream</th><th>Local image</th><th>Platforms</th><th>Keep</th><th>Last run</th><th>Status</th><th /></tr></thead>
        <tbody>
          {rows.map((m) => (
            <tr key={m.id}>
              <td>{m.upstream}:{m.tag}{m.has_credentials && <span className="badge"> credentials</span>}</td>
              <td>{m.local_name}:{m.tag}</td>
              <td className="ww">{m.platforms.length ? m.platforms.join(', ') : 'all'}</td>
              <td className="nw">{m.keep}</td>
              <td className="nw">{m.last_run ? new Date(m.last_run).toLocaleString() : 'never'}</td>
              <td className="ww">{m.last_status || '—'}{m.last_error && <span className="error"> {m.last_error}</span>}{m.upstream_digest && <div className="muted"><code>{m.upstream_digest.slice(7, 19)}</code></div>}</td>
              <td className="nw">
                <button onClick={() => run(async () => { await api.runImageMirror(m.id); later() }, 'Check started.')}>Check now</button>{' '}
                <button onClick={() => edit(m)}>Edit</button>{' '}
                <button onClick={() => confirm(`Stop mirroring ${m.upstream}:${m.tag}? The local image and its tags stay.`) && run(async () => { await api.deleteImageMirror(m.id); if (editing?.id === m.id) reset(); load() })}>Remove</button>
              </td>
            </tr>
          ))}
          {rows.length === 0 && <tr><td colSpan={7} className="muted">No mirrors.</td></tr>}
        </tbody>
      </table>

      <form onSubmit={save}>
        <h3>{editing ? `Edit ${editing.upstream}:${editing.tag}` : 'Add mirror'}</h3>
        <div className="row">
          <label>Upstream image
            <input value={f.upstream} disabled={!!editing} required placeholder="docker.io/plexinc/pms-docker:latest"
              onChange={(e) => setF({ ...f, upstream: e.target.value, local: f.localTouched ? f.local : defaultLocalName(e.target.value) })} />
          </label>
          <label>Local name
            <input value={f.local} disabled={!!editing} required onChange={(e) => setF({ ...f, local: e.target.value, localTouched: true })} />
          </label>
          <label>Versions to keep
            <input type="number" min={1} value={f.keep} onChange={(e) => setF({ ...f, keep: Math.max(1, Number(e.target.value) || 1) })} />
          </label>
        </div>
        <div className="row">
          <label>Upstream username (optional)<input value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} autoComplete="off" /></label>
          <label>Upstream token<input type="password" value={f.token} onChange={(e) => setF({ ...f, token: e.target.value })} autoComplete="new-password"
            placeholder={editing?.has_credentials ? 'stored; type to replace' : ''} /></label>
        </div>
        <fieldset>
          <legend>CPU types</legend>
          <div className="row">
            <button type="button" disabled={busy || !f.upstream} onClick={loadPlatforms}>{busy ? 'Loading…' : 'Load platforms'}</button>
            <span className="muted">{sel.length ? `${sel.length} selected` : 'none selected = copy all'}</span>
          </div>
          {avail !== null && avail.length === 0 && <p className="muted">Single-platform image: copied as is.</p>}
          {(avail ?? sel).map((p) => (
            <label key={p} className="inline"><input type="checkbox" checked={sel.includes(p)} onChange={() => toggle(p)} /> {p}</label>
          ))}
        </fieldset>
        <div className="row">
          <button className="primary">{editing ? 'Save' : 'Add mirror'}</button>
          {editing && <button type="button" onClick={reset}>Cancel</button>}
        </div>
      </form>
    </div>
  )
}
