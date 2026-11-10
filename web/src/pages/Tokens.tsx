import { FormEvent, useCallback, useEffect, useState } from 'react'
import { api, ApiToken } from '../api'

export default function Tokens() {
  const [tokens, setTokens] = useState<ApiToken[]>([])
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null)
  const load = useCallback(() => { api.tokens().then(setTokens).catch((x) => setErr(x.message)) }, [])
  useEffect(() => { load() }, [load])

  async function create(ev: FormEvent) {
    ev.preventDefault()
    setErr('')
    try { const t = await api.createToken(name); setCreated({ name: t.name, token: t.token }); setName(''); load() } catch (x) { setErr((x as Error).message) }
  }

  return (
    <>
      <h1>API tokens</h1>
      <p className="muted">
        For scripts and CI that upload, delete or promote packages. A token has full admin API access; give each script its own so you can revoke it alone.
        See <a href="/help#automate">Help → Automate from CI</a>.
      </p>
      {err && <p className="error">{err}</p>}
      {created && (
        <div className="card notice">
          <strong>Token “{created.name}” — shown once, copy it now:</strong>
          <pre>{created.token}</pre>
          <div className="row"><button onClick={() => setCreated(null)}>Dismiss</button></div>
        </div>
      )}
      <table>
        <thead><tr><th>Name</th><th>Created by</th><th>Created</th><th /></tr></thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id}>
              <td>{t.name}</td><td>{t.created_by}</td><td>{new Date(t.created_at).toLocaleString()}</td>
              <td><button onClick={() => confirm(`Revoke ${t.name}? Scripts using it stop working.`) && api.deleteToken(t.id).then(load)}>Revoke</button></td>
            </tr>
          ))}
          {tokens.length === 0 && <tr><td colSpan={4} className="muted">No tokens.</td></tr>}
        </tbody>
      </table>
      <form className="card" onSubmit={create}>
        <h2>New token</h2>
        <div className="row"><label>Name (e.g. the script that uses it)<input value={name} onChange={(e) => setName(e.target.value)} maxLength={64} required /></label></div>
        <button className="primary">Create token</button>
      </form>
    </>
  )
}
