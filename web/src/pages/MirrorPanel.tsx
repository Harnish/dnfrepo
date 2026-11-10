import { FormEvent, useCallback, useEffect, useState } from 'react'
import { api, ApiError, Batch, Mirror, MirrorInput, MirrorSchedule, RepoSection } from '../api'
import { dayName, describeSchedule } from '../schedule'

const browserTZ = () => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC' } catch { return 'UTC' } }
const zones = (): string[] => { try { return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? [] } catch { return [] } }
const lines = (s: string) => s.split(/[\s,]+/).filter(Boolean)
const defaultSchedule = (): MirrorSchedule => ({ frequency: 'manual', time_of_day: '02:00', timezone: browserTZ(), weekdays: [], day_of_month: 1, paused: false })

/** Upstream, credentials, signature, filters and schedule form. `next` is the next stage's name, if any. */
export default function MirrorPanel({ slug, next, onChanged }: { slug: string; next?: string; onChanged: () => void }) {
  const [m, setM] = useState<Mirror | null>(null) // null = not configured yet
  const [batches, setBatches] = useState<Batch[]>([])
  const [ready, setReady] = useState(false)
  const [err, setErr] = useState('')
  const [msg, setMsg] = useState('')

  // form state
  const [src, setSrc] = useState<'url' | 'file'>('url')
  const [url, setUrl] = useState('')
  const [repoFile, setRepoFile] = useState('')
  const [sections, setSections] = useState<RepoSection[]>([])
  const [sectionIDs, setSectionIDs] = useState<string[]>([]) // from a 400 when sections were not parsed here
  const [section, setSection] = useState('')
  const [vars, setVars] = useState<Record<string, string>>({})
  const [varNames, setVarNames] = useState<string[]>([])
  const [user, setUser] = useState('')
  const [token, setToken] = useState('')
  const [clearCreds, setClearCreds] = useState(false)
  const [gpg, setGpg] = useState('')
  const [reqSig, setReqSig] = useState(false)
  const [arches, setArches] = useState('')
  const [include, setInclude] = useState('')
  const [exclude, setExclude] = useState('')
  const [keepLatest, setKeepLatest] = useState(0)
  const [sched, setSched] = useState<MirrorSchedule>(defaultSchedule())

  const status = useCallback(async () => {
    try { setM(await api.mirror(slug)) } catch (x) { if (!(x instanceof ApiError && x.status === 404)) throw x }
    setBatches(await api.batches(slug))
  }, [slug])

  const fill = (c: Mirror) => {
    setUrl(c.upstream_url); setUser(c.username); setGpg(c.gpg_key); setReqSig(c.require_signature)
    setArches(c.arches.join(' ')); setInclude(c.include.join(' ')); setExclude(c.exclude.join(' '))
    setKeepLatest(c.keep_latest); setSched({ ...defaultSchedule(), ...c.schedule, weekdays: c.schedule.weekdays ?? [] })
  }

  useEffect(() => {
    let live = true
    api.mirror(slug).then((c) => { if (live) { setM(c); fill(c) } }, (x) => { if (!(x instanceof ApiError && x.status === 404)) setErr(x.message) })
      .then(() => api.batches(slug)).then((b) => live && setBatches(b)).catch(() => {}).finally(() => live && setReady(true))
    return () => { live = false }
  }, [slug])

  // Poll only while a sync is running; the interval is cleared on unmount or when it stops.
  const running = m?.state === 'running'
  useEffect(() => {
    if (!running) return
    const t = setInterval(() => { status().catch(() => {}); onChanged() }, 5000)
    return () => clearInterval(t)
  }, [running, status, onChanged])

  // variables to prompt for: those of the parsed section plus any the server reported missing
  const shownVars = [...new Set([...(sections.find((s) => s.id === section)?.variables ?? []), ...varNames])]

  async function parse() {
    setErr('')
    try {
      const r = await api.parseRepo(repoFile)
      setSections(r.sections); setSection(r.sections[0]?.id ?? '')
      setVarNames([])
    } catch (x) { setErr((x as Error).message) }
  }

  async function save(ev: FormEvent) {
    ev.preventDefault()
    setErr(''); setMsg('')
    const body: MirrorInput = {
      gpg_key: gpg, require_signature: reqSig, arches: lines(arches), include: lines(include), exclude: lines(exclude),
      keep_latest: keepLatest, schedule: sched,
    }
    if (src === 'url') body.upstream_url = url
    else {
      body.repo_file = repoFile
      if (section) body.section = section
      const used = shownVars.filter((n) => vars[n] !== undefined && vars[n] !== '')
      if (used.length) body.vars = Object.fromEntries(used.map((n) => [n, vars[n]]))
    }
    if (user) body.username = user
    if (token) body.token = token // write-only: blank keeps the stored one
    if (clearCreds) body.clear_credentials = true
    try {
      const c = await api.putMirror(slug, body)
      setM(c); setToken(''); setClearCreds(false); setSectionIDs([]); setVarNames([]); setSections([])
      fill(c); setSrc('url'); setMsg('Mirror saved'); onChanged(); await status()
    } catch (x) {
      if (x instanceof ApiError && x.status === 400) {
        const mv = x.data.missing_vars as string[] | undefined
        const ids = x.data.sections as string[] | undefined
        if (mv?.length) setVarNames((v) => [...new Set([...v, ...mv])])
        if (ids?.length) { setSectionIDs(ids); setSection((s) => s || ids[0]) }
      }
      setErr((x as Error).message)
    }
  }

  async function sync() {
    setErr(''); setMsg('')
    try { await api.syncNow(slug); setMsg('Sync started') } catch (x) {
      if (x instanceof ApiError && x.status === 409) setMsg('A sync is already running')
      else setErr((x as Error).message)
    }
    await status().catch(() => {})
  }

  async function promote(b: Batch) {
    if (!next || !confirm(`Promote every package from batch ${b.id} to ${next}?`)) return
    setErr(''); setMsg('')
    try {
      const r = await api.promoteBatch(slug, b.id)
      setMsg(`Promoted ${r.promoted.length} to ${r.to}` + (r.skipped.length ? `, ${r.skipped.length} already there` : ''))
      onChanged()
    } catch (x) { setErr((x as Error).message) }
  }

  async function remove() {
    if (!confirm('Stop mirroring? The repo becomes a normal first stage and keeps its packages.')) return
    setErr('')
    try { await api.deleteMirror(slug); setM(null); onChanged() } catch (x) { setErr((x as Error).message) }
  }

  if (!ready) return <p className="muted">Loading mirror…</p>
  const sectionOpts = sections.length ? sections.map((s) => ({ id: s.id, name: s.name })) : sectionIDs.map((id) => ({ id, name: id }))
  const setS = (p: Partial<MirrorSchedule>) => setSched({ ...sched, ...p })

  return (
    <>
      <h2>Mirror</h2>
      {err && <p className="error" role="alert">{err}</p>}
      {msg && <p className="ok">{msg}</p>}
      {m && (
        <div className="card">
          <p>
            Status: <strong>{m.state === 'running' ? 'syncing…' : m.last_status || 'never synced'}</strong>
            {m.last_error && <span className="error"> {m.last_error}</span>}
          </p>
          <p className="muted">Last run: {m.last_run ?? 'never'} · Next run: {m.next_run ?? 'none'} · {describeSchedule(m.schedule)}</p>
          <div className="row">
            <button className="primary" onClick={sync} disabled={running}>Sync now</button>
            <button onClick={remove} disabled={running}>Stop mirroring</button>
          </div>
        </div>
      )}

      <form className="card" onSubmit={save}>
        <fieldset>
          <legend>Upstream</legend>
          <div className="row">
            <label className="inline"><input type="radio" name="src" checked={src === 'url'} onChange={() => setSrc('url')} /> Repo URL</label>
            <label className="inline"><input type="radio" name="src" checked={src === 'file'} onChange={() => setSrc('file')} /> Paste a .repo file</label>
          </div>
          {src === 'url' ? (
            <label>Upstream URL<input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://pkgs.example.org/el9/x86_64/" required /></label>
          ) : (
            <>
              <label>.repo file<textarea rows={6} value={repoFile} onChange={(e) => setRepoFile(e.target.value)} required /></label>
              <button type="button" onClick={parse} disabled={!repoFile.trim()}>Read .repo file</button>
            </>
          )}
          {src === 'file' && sectionOpts.length > 1 && (
            <label>Section
              <select value={section} onChange={(e) => setSection(e.target.value)}>
                {sectionOpts.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
            </label>
          )}
          {src === 'file' && shownVars.map((n) => (
            <label key={n}>Value for <code>${n}</code>
              <input value={vars[n] ?? ''} onChange={(e) => setVars({ ...vars, [n]: e.target.value })} required />
            </label>
          ))}
        </fieldset>

        <fieldset>
          <legend>Credentials {m?.has_credentials && <span className="badge">stored</span>}</legend>
          <div className="row">
            <label>Username<input value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" /></label>
            <label>Token<input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="new-password"
              placeholder={m?.has_credentials ? 'leave blank to keep the stored token' : ''} /></label>
          </div>
          {m?.has_credentials && <label className="inline"><input type="checkbox" checked={clearCreds} onChange={(e) => setClearCreds(e.target.checked)} /> Clear credentials</label>}
        </fieldset>

        <fieldset>
          <legend>Signature</legend>
          <label>GPG public key (armored)<textarea rows={4} value={gpg} onChange={(e) => setGpg(e.target.value)} /></label>
          <label className="inline"><input type="checkbox" checked={reqSig} onChange={(e) => setReqSig(e.target.checked)} /> Require a valid signature</label>
        </fieldset>

        <fieldset>
          <legend>Filters</legend>
          <div className="row">
            <label>Arches<input value={arches} onChange={(e) => setArches(e.target.value)} placeholder="x86_64 noarch" /></label>
            <label>Include (name globs)<input value={include} onChange={(e) => setInclude(e.target.value)} /></label>
            <label>Exclude (name globs)<input value={exclude} onChange={(e) => setExclude(e.target.value)} /></label>
            <label>Keep latest (0 = all)<input type="number" min={0} value={keepLatest} onChange={(e) => setKeepLatest(Math.max(0, Number(e.target.value) || 0))} /></label>
          </div>
        </fieldset>

        <fieldset>
          <legend>Schedule</legend>
          <div className="row">
            <label>Frequency
              <select value={sched.frequency} onChange={(e) => setS({ frequency: e.target.value as MirrorSchedule['frequency'] })}>
                <option value="manual">Manual only</option><option value="daily">Daily</option><option value="weekly">Weekly</option><option value="monthly">Monthly</option>
              </select>
            </label>
            {sched.frequency !== 'manual' && <>
              <label>Time<input type="time" value={sched.time_of_day} onChange={(e) => setS({ time_of_day: e.target.value })} required /></label>
              <label>Time zone<input list="tz-list" value={sched.timezone} onChange={(e) => setS({ timezone: e.target.value })} required /></label>
              <datalist id="tz-list">{zones().map((z) => <option key={z} value={z} />)}</datalist>
            </>}
            {sched.frequency === 'monthly' && <label>Day of month<input type="number" min={1} max={31} value={sched.day_of_month} onChange={(e) => setS({ day_of_month: Number(e.target.value) || 1 })} /></label>}
          </div>
          {sched.frequency === 'weekly' && (
            <div className="row">
              {[0, 1, 2, 3, 4, 5, 6].map((d) => (
                <label key={d} className="inline"><input type="checkbox" checked={sched.weekdays.includes(d)}
                  onChange={() => setS({ weekdays: sched.weekdays.includes(d) ? sched.weekdays.filter((x) => x !== d) : [...sched.weekdays, d] })} /> {dayName(d)}</label>
              ))}
            </div>
          )}
          <label className="inline"><input type="checkbox" checked={sched.paused} onChange={(e) => setS({ paused: e.target.checked })} /> Pause scheduled syncs</label>
        </fieldset>

        <button className="primary">Save mirror</button>
      </form>

      <h3>Batches</h3>
      <table>
        <thead><tr><th>ID</th><th>Started</th><th>Status</th><th>Added</th><th>Error</th><th /></tr></thead>
        <tbody>
          {batches.map((b) => (
            <tr key={b.id}>
              <td>{b.id}</td><td>{b.started_at}</td><td>{b.status}</td><td>{b.added}</td><td className="error">{b.error}</td>
              <td><button disabled={!next} title={next ? '' : 'This repo has no next stage'} onClick={() => promote(b)}>Promote batch → {next ?? 'next stage'}</button></td>
            </tr>
          ))}
          {batches.length === 0 && <tr><td colSpan={6} className="muted">No syncs yet.</td></tr>}
        </tbody>
      </table>
    </>
  )
}
