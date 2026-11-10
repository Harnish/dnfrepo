import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { nextMirrorFor } from '../mirrorState'
import MirrorPanel from './MirrorPanel'
import { api, ApiError, AuditEvent, fmtSize, Mode, Pkg, Repo, RepoUser, Source } from '../api'

export default function RepoDetail() {
  const { slug = '' } = useParams()
  const nav = useNavigate()
  const [params] = useSearchParams()
  const [repos, setRepos] = useState<Repo[]>([])
  const [pkgs, setPkgs] = useState<Pkg[]>([])
  const [sources, setSources] = useState<Source[]>([])
  const [audit, setAudit] = useState<AuditEvent[]>([])
  const [users, setUsers] = useState<RepoUser[]>([])
  const [granted, setGranted] = useState<number[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [ips, setIps] = useState('')
  const [mode, setMode] = useState<Mode>('public')
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')
  const [src, setSrc] = useState({ kind: 'github', spec: '' })
  const [mirrorFor, setMirrorFor] = useState<string | null>(null)
  const [keep, setKeep] = useState(0)
  const [rules, setRules] = useState<{ name: string; keep: number }[]>([])

  const repo = repos.find((r) => r.slug === slug)
  const next = repos.find((r) => r.previous === slug)
  const first = repo ? !repo.previous : false
  const isMirror = mirrorFor === slug && first

  const run = async (f: () => Promise<unknown>, ok = '') => {
    setErr(''); setMsg('')
    try { await f(); if (ok) setMsg(ok) } catch (x) { setErr((x as Error).message) }
  }

  const load = useCallback(async () => {
    const rs = await api.repos()
    setRepos(rs)
    const r = rs.find((x) => x.slug === slug)
    if (!r) return
    setMode(r.mode)
    setIps(r.ips.join('\n'))
    setKeep(r.keep)
    setRules(Object.entries(r.keep_overrides ?? {}).map(([name, k]) => ({ name, keep: k })))
    const [p, a] = await Promise.all([api.packages(slug), api.audit(slug)])
    setPkgs(p); setAudit(a); setSelected(new Set())
    if (r.previous) setMirrorFor(null)
    if (!r.previous) {
      setSources(await api.sources(slug))
      // 404 = not a mirror; a freshly created "mirror" repo (?mirror=1) shows the empty form
      const m = await api.mirror(slug).then(() => true, (x) => { if (x instanceof ApiError && x.status === 404) return false; throw x })
      setMirrorFor((was) => nextMirrorFor(was, slug, true, m, params.get('mirror') === '1'))
    }
    if (r.mode === 'users') {
      setUsers(await api.repoUsers())
      setGranted((await api.repoGrants(slug)).user_ids)
    }
  }, [slug, params])
  useEffect(() => { load().catch((x) => setErr(x.message)) }, [load])

  if (!repo) return <p className="muted">{err || 'Loading…'}</p>

  const toggle = (f: string) => setSelected((s) => { const n = new Set(s); n.has(f) ? n.delete(f) : n.add(f); return n })

  async function saveAccess(ev: FormEvent) {
    ev.preventDefault()
    run(async () => {
      await api.updateRepo(slug, { mode })
      if (mode === 'ips') await api.setIPs(slug, ips.split(/[\s,]+/).filter(Boolean))
      if (mode === 'users') await api.setRepoGrants(slug, granted)
      await load()
    }, 'Access saved')
  }

  return (
    <>
      <h1>{repo.name} <span className={`badge ${repo.mode}`}>{repo.mode}</span></h1>
      <p>
        <a href={`/r/${slug}/`}>Browse</a> · <a href={`/r/${slug}/${slug}.repo`}>{slug}.repo</a> · <a href={`/r/${slug}/feed.xml`}>RSS</a>
        {repo.previous && <> · promoted from <Link to={`/repos/${repo.previous}`}>{repo.previous}</Link></>}
        {next && <> · promotes to <Link to={`/repos/${next.slug}`}>{next.name}</Link></>}
      </p>
      <p className="muted">Install: <code>sudo dnf config-manager addrepo --from-repofile={location.origin}/r/{slug}/{slug}.repo</code></p>
      {err && <p className="error">{err}</p>}
      {msg && <p className="ok">{msg}</p>}

      <h2>Packages</h2>
      <table>
        <thead><tr><th /><th>Name</th><th>Version</th><th>Arch</th><th>Size</th><th>File</th><th /></tr></thead>
        <tbody>
          {pkgs.map((p) => (
            <tr key={p.filename}>
              <td><input type="checkbox" checked={selected.has(p.filename)} onChange={() => toggle(p.filename)} aria-label={`select ${p.filename}`} /></td>
              <td>{p.name}</td><td>{p.version}-{p.release}</td><td>{p.arch}</td><td>{fmtSize(p.size)}</td>
              <td><a href={`/r/${slug}/${p.filename}`}>{p.filename}</a></td>
              <td><button onClick={() => confirm(`Delete ${p.filename}?`) && run(async () => { await api.deletePackage(slug, p.filename); await load() })}>Delete</button></td>
            </tr>
          ))}
          {pkgs.length === 0 && <tr><td colSpan={7} className="muted">No packages.</td></tr>}
        </tbody>
      </table>
      <div className="row">
        {next && (
          <button className="primary" disabled={selected.size === 0}
            onClick={() => run(async () => {
              const r = await api.promote(slug, [...selected])
              setMsg(`Promoted ${r.promoted.length} to ${r.to}` + (r.skipped.length ? `, ${r.skipped.length} already there` : ''))
              await load()
            })}>
            Promote {selected.size || ''} selected → {next.name}
          </button>
        )}
        {first && !isMirror && (
          <label className="upload">Upload rpm
            <input type="file" accept=".rpm" onChange={(e) => { const f = e.target.files?.[0]; if (f) run(async () => { await api.upload(slug, f); await load() }, `Uploaded ${f.name}`); e.target.value = '' }} />
          </label>
        )}
        {first && isMirror && <span className="muted">Packages arrive by sync.</span>}
        {!first && <span className="muted">Packages arrive here only by promotion.</span>}
      </div>

      <h2>Access</h2>
      <form className="card" onSubmit={saveAccess}>
        <label>Who can pull
          <select value={mode} onChange={(e) => setMode(e.target.value as Mode)}>
            <option value="public">anyone (public)</option><option value="ips">only these IPs</option><option value="users">only these users</option>
          </select>
        </label>
        {mode === 'ips' && <label>IPs / CIDR ranges (one per line)<textarea rows={4} value={ips} onChange={(e) => setIps(e.target.value)} placeholder="10.0.0.0/8&#10;203.0.113.7" /></label>}
        {mode === 'users' && (
          <fieldset>
            <legend>Allowed users</legend>
            {users.filter((u) => !u.revoked).map((u) => (
              <label key={u.id} className="inline"><input type="checkbox" checked={granted.includes(u.id)}
                onChange={() => setGranted((g) => g.includes(u.id) ? g.filter((x) => x !== u.id) : [...g, u.id])} /> {u.name}</label>
            ))}
            {users.length === 0 && <span className="muted">No repo users yet — create some on the Repo users page.</span>}
          </fieldset>
        )}
        <button className="primary">Save access</button>
      </form>

      <h2>Retention</h2>
      <form className="card" onSubmit={(ev) => {
        ev.preventDefault()
        run(async () => {
          const overrides = Object.fromEntries(rules.filter((r) => r.name.trim()).map((r) => [r.name.trim(), r.keep]))
          const res = await api.setKeep(slug, keep, overrides)
          await load()
          setMsg(res.purged.length ? `Saved. Purged ${res.purged.length} old version(s): ${res.purged.join(', ')}` : 'Saved. Nothing to purge.')
        })
      }}>
        <p className="muted">Keep only the newest N versions of each package in this repo; older ones are deleted. 0 keeps everything. Applies now and after every upload or promotion here.</p>
        <label>Default versions to keep
          <input type="number" min={0} value={keep} onChange={(e) => setKeep(Math.max(0, Number(e.target.value) || 0))} />
        </label>
        {rules.map((r, i) => (
          <div className="row" key={i}>
            <label>Package name
              <input value={r.name} onChange={(e) => setRules(rules.map((x, j) => j === i ? { ...x, name: e.target.value } : x))} placeholder="plexmediaserver" />
            </label>
            <label>Keep
              <input type="number" min={0} value={r.keep} onChange={(e) => setRules(rules.map((x, j) => j === i ? { ...x, keep: Math.max(0, Number(e.target.value) || 0) } : x))} />
            </label>
            <button type="button" onClick={() => setRules(rules.filter((_, j) => j !== i))}>Remove</button>
          </div>
        ))}
        <div className="row">
          <button type="button" onClick={() => setRules([...rules, { name: '', keep: 1 }])}>Add per-package limit</button>
          <button className="primary">Save &amp; apply</button>
        </div>
      </form>

      {first && !isMirror && (
        <>
          <h2>Sources</h2>
          <table>
            <thead><tr><th>Kind</th><th>Spec</th><th>Last run</th><th>Status</th><th /></tr></thead>
            <tbody>
              {sources.map((s) => (
                <tr key={s.id}>
                  <td>{s.kind}</td><td>{s.spec || '—'}</td><td>{s.last_run ?? 'never'}</td>
                  <td>{s.last_status}{s.last_error && <span className="error"> {s.last_error}</span>}</td>
                  <td>
                    <button onClick={() => run(async () => { await api.runSource(s.id) }, 'Check started — refresh in a moment')}>Check now</button>{' '}
                    <button onClick={() => run(async () => { await api.deleteSource(s.id); await load() })}>Remove</button>
                  </td>
                </tr>
              ))}
              {sources.length === 0 && <tr><td colSpan={5} className="muted">No sources.</td></tr>}
            </tbody>
          </table>
          <form className="row" onSubmit={(ev) => { ev.preventDefault(); run(async () => { await api.addSource(slug, src.kind, src.spec); setSrc({ ...src, spec: '' }); await load() }) }}>
            <select value={src.kind} onChange={(e) => setSrc({ ...src, kind: e.target.value })}>
              <option value="github">GitHub release</option><option value="plex">Plex Media Server</option>
            </select>
            {src.kind === 'github' && <input value={src.spec} onChange={(e) => setSrc({ ...src, spec: e.target.value })} placeholder="owner/name or github.com URL" required />}
            <button>Add source</button>
          </form>
        </>
      )}

      {isMirror && <MirrorPanel slug={slug} next={next?.name} onChanged={load} />}
      {first && !isMirror && pkgs.length === 0 && sources.length === 0 && (
        <p><button onClick={() => setMirrorFor(slug)}>Make this a mirror</button></p>
      )}

      <h2>Pipeline</h2>
      <div className="row">
        {repo.previous && <button onClick={() => run(async () => { await api.setPrevious(slug, null); await load() })}>Unlink from {repo.previous}</button>}
        <button onClick={() => confirm(`Delete repo ${slug} and its files?`) && run(async () => { await api.deleteRepo(slug); nav('/') })}>Delete repo</button>
      </div>
      <p className="muted">A repo in the middle of a chain can't be deleted until it's unlinked.</p>

      <h2>Activity</h2>
      <ul className="audit">
        {audit.map((a) => <li key={a.id}><span className="muted">{a.at}</span> {a.action} <code>{a.detail}</code> <span className="muted">by {a.actor}</span></li>)}
        {audit.length === 0 && <li className="muted">Nothing yet.</li>}
      </ul>
    </>
  )
}
