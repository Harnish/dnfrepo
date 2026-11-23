import { FormEvent, useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { byLastPulled, nextMirrorFor } from '../mirrorState'
import MirrorPanel from './MirrorPanel'
import BatchTable from './BatchTable'
import { Pager, usePaged } from './Pager'
import { api, ApiError, AuditEvent, Batch, fmtSize, Mode, Pkg, Repo, RepoUser, Source, sourcesListLine } from '../api'

export default function RepoDetail() {
  const { slug = '' } = useParams()
  const nav = useNavigate()
  const [params, setParams] = useSearchParams()
  const [repos, setRepos] = useState<Repo[]>([])
  const [pkgs, setPkgs] = useState<Pkg[]>([])
  const [batches, setBatches] = useState<Batch[]>([])
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
  const [cacheFor, setCacheFor] = useState<string | null>(null) // slug whose mirror is in cache mode
  const [keep, setKeep] = useState(0)
  const [rules, setRules] = useState<{ name: string; keep: number }[]>([])

  const mirrorParam = params.get('mirror') === '1'
  const repo = repos.find((r) => r.slug === slug)
  const next = repos.find((r) => r.previous === slug)
  const deb = repo?.kind === 'deb'
  const first = repo ? !repo.previous : false
  const isMirror = mirrorFor === slug && first
  const cache = isMirror && cacheFor === slug
  const shown = cache ? byLastPulled(pkgs) : pkgs
  const pkgPg = usePaged(shown)
  const srcPg = usePaged(sources)
  const auditPg = usePaged(audit)

  const showBatches = !isMirror && batches.length > 0
  const tabs: [string, string][] = [
    ['packages', `Packages (${pkgs.length})`],
    ...(first && !isMirror ? [['sources', `Sources (${sources.length})`] as [string, string]] : []),
    ...(isMirror ? [['mirror', 'Mirror'] as [string, string]] : []),
    ...(showBatches ? [['batches', `Batches (${batches.length})`] as [string, string]] : []),
    ['access', 'Access'], ['retention', 'Retention'], ['activity', 'Activity'], ['pipeline', 'Pipeline'],
  ]
  const tab = tabs.some(([k]) => k === params.get('tab')) ? params.get('tab')! : 'packages'
  const setTab = (k: string) => setParams((p) => { const n = new URLSearchParams(p); n.set('tab', k); return n }, { replace: true })

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
    const [p, a, b] = await Promise.all([api.packages(slug), api.audit(slug), api.batches(slug)])
    setPkgs(p); setAudit(a); setBatches(b); setSelected(new Set())
    if (r.previous) setMirrorFor(null)
    if (!r.previous) {
      setSources(await api.sources(slug))
      // 404 = not a mirror; a freshly created "mirror" repo (?mirror=1) shows the empty form
      const mc = await api.mirror(slug).then((c) => c, (x) => { if (x instanceof ApiError && x.status === 404) return null; throw x })
      setCacheFor(mc?.mode === 'cache' ? slug : null)
      setMirrorFor((was) => nextMirrorFor(was, slug, true, mc !== null, mirrorParam))
    }
    if (r.mode === 'users') {
      setUsers(await api.repoUsers())
      setGranted((await api.repoGrants(slug)).user_ids)
    }
  }, [slug, mirrorParam])
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
        <a href={`/r/${slug}/`}>Browse</a> · {!deb && <><a href={`/r/${slug}/${slug}.repo`}>{slug}.repo</a> · </>}<a href={`/r/${slug}/feed.xml`}>RSS</a>
        {repo.previous && <> · promoted from <Link to={`/repos/${repo.previous}`}>{repo.previous}</Link></>}
        {next && <> · promotes to <Link to={`/repos/${next.slug}`}>{next.name}</Link></>}
      </p>
      {deb ? (
        <div className="muted">
          Install:
          <pre>{`curl -fsS ${location.origin}/gpg.key | sudo tee /etc/apt/keyrings/dnfrepo.asc\necho "${sourcesListLine(repo, location.origin)}" | sudo tee /etc/apt/sources.list.d/${slug}.list\nsudo apt update`}</pre>
        </div>
      ) : (
        <p className="muted">Install: <code>sudo dnf config-manager addrepo --from-repofile={location.origin}/r/{slug}/{slug}.repo</code></p>
      )}
      {err && <p className="error">{err}</p>}
      {msg && <p className="ok">{msg}</p>}

      <nav className="tabs" role="tablist">
        {tabs.map(([k, label]) => (
          <button key={k} type="button" role="tab" aria-selected={tab === k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{label}</button>
        ))}
      </nav>

      {tab === 'packages' && <>
      <table>
        <thead><tr><th /><th>Name</th><th>Version</th><th>Arch</th>{deb && <th>Component</th>}<th>Size</th>{cache && <th>Last pulled</th>}<th>File</th><th /></tr></thead>
        <tbody>
          {pkgPg.items.map((p) => (
            <tr key={p.filename}>
              <td><input type="checkbox" checked={selected.has(p.filename)} onChange={() => toggle(p.filename)} aria-label={`select ${p.filename}`} /></td>
              <td>{p.name}</td><td className="nw">{deb ? p.version : `${p.version}-${p.release}`}</td><td className="nw">{p.arch}</td>{deb && <td className="nw">{p.component}</td>}<td className="nw">{fmtSize(p.size)}</td>{cache && <td className="nw">{new Date(p.last_pulled).toLocaleString()}</td>}
              <td><a href={deb ? `/r/${slug}/pool/${p.component}/${p.filename}` : `/r/${slug}/${p.filename}`}>{p.filename}</a></td>
              <td><button onClick={() => confirm(`Delete ${p.filename}?`) && run(async () => { await api.deletePackage(slug, p.filename); await load() })}>Delete</button></td>
            </tr>
          ))}
          {pkgs.length === 0 && <tr><td colSpan={(deb ? 8 : 7) + (cache ? 1 : 0)} className="muted">{cache ? 'Nothing cached yet: packages are stored when a host first downloads them.' : 'No packages.'}</td></tr>}
        </tbody>
      </table>
      <Pager {...pkgPg} />
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
          <label className="upload">{deb ? 'Upload deb' : 'Upload rpm'}
            <input type="file" accept={deb ? '.deb' : '.rpm'} onChange={(e) => { const f = e.target.files?.[0]; if (f) run(async () => { await api.upload(slug, f); await load() }, `Uploaded ${f.name}`); e.target.value = '' }} />
          </label>
        )}
        {first && isMirror && <span className="muted">{cache ? 'Packages are cached on first download.' : 'Packages arrive by sync.'}</span>}
        {!first && <span className="muted">Packages arrive here only by promotion.</span>}
      </div>
      </>}

      {tab === 'access' && <form className="card" onSubmit={saveAccess}>
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
      </form>}

      {tab === 'retention' && <form className="card" onSubmit={(ev) => {
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
      </form>}

      {tab === 'sources' && (
        <>
          <table>
            <thead><tr><th>Kind</th><th>Spec</th><th>Last run</th><th>Status</th><th /></tr></thead>
            <tbody>
              {srcPg.items.map((s) => (
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
          <Pager {...srcPg} />
          <form className="row" onSubmit={(ev) => { ev.preventDefault(); run(async () => { await api.addSource(slug, src.kind, src.spec); setSrc({ ...src, spec: '' }); await load() }) }}>
            <select value={src.kind} onChange={(e) => setSrc({ ...src, kind: e.target.value })}>
              <option value="github">GitHub release</option>{!deb && <option value="plex">Plex Media Server</option>}
            </select>
            {src.kind === 'github' && <input value={src.spec} onChange={(e) => setSrc({ ...src, spec: e.target.value })} placeholder="owner/name or github.com URL" required />}
            <button>Add source</button>
          </form>
          {pkgs.length === 0 && sources.length === 0 && (
            <p><button onClick={() => { setMirrorFor(slug); setTab('mirror') }}>Make this a mirror</button></p>
          )}
        </>
      )}

      {tab === 'mirror' && <MirrorPanel slug={slug} deb={deb} next={next?.name} onChanged={load} />}
      {/* batches promoted into a later stage (or left on a former mirror) stay promotable */}
      {tab === 'batches' && (
        <BatchTable batches={batches} next={next?.name} onPromote={(b) => next && confirm(`Promote every package from batch ${b.id} to ${next.name}?`) && run(async () => {
          const r = await api.promoteBatch(slug, b.id)
          await load()
          setMsg(`Promoted ${r.promoted.length} to ${r.to}` + (r.skipped.length ? `, ${r.skipped.length} already there` : ''))
        })} />
      )}
      {tab === 'pipeline' && <>
      <div className="row">
        {repo.previous && <button onClick={() => run(async () => { await api.setPrevious(slug, null); await load() })}>Unlink from {repo.previous}</button>}
        <button onClick={() => confirm(`Delete repo ${slug} and its files?`) && run(async () => { await api.deleteRepo(slug); nav('/') })}>Delete repo</button>
      </div>
      <p className="muted">A repo in the middle of a chain can't be deleted until it's unlinked.</p>
      </>}

      {tab === 'activity' && <>
      <ul className="audit">
        {auditPg.items.map((a) => <li key={a.id}><span className="muted">{a.at}</span> {a.action} <code>{a.detail}</code> <span className="muted">by {a.actor}</span></li>)}
        {audit.length === 0 && <li className="muted">Nothing yet.</li>}
      </ul>
      <Pager {...auditPg} />
      {audit.length >= 100 && <p className="muted">Shows the newest 100 events.</p>}
      </>}
    </>
  )
}
