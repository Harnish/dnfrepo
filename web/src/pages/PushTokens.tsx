import { FormEvent, useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { PushTokenRow } from '../registry'

export default function PushTokens() {
  const [rows, setRows] = useState<PushTokenRow[]>([])
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null)
  const load = useCallback(() => { api.pushTokens().then(setRows).catch((x) => setErr(x.message)) }, [])
  useEffect(() => { load() }, [load])
  async function create(ev: FormEvent) {
    ev.preventDefault()
    setErr('')
    try { const t = await api.createPushToken(name); setCreated({ name: t.name, token: t.token }); setName(''); load() } catch (x) { setErr((x as Error).message) }
  }
  return (
    <>
      <h1>Push tokens</h1>
      <p className="muted">Credentials that may push (and pull) any image. Use the token name as the username: <code>docker login {location.host} -u NAME</code>.</p>
      {err && <p className="error">{err}</p>}
      {created && (
        <div className="card notice">
          <strong>Token for {created.name} — shown once, copy it now:</strong>
          <pre>{created.token}</pre>
          <button onClick={() => setCreated(null)}>Dismiss</button>
        </div>
      )}
      <table>
        <thead><tr><th>Name</th><th>Created by</th><th>Status</th><th /></tr></thead>
        <tbody>
          {rows.map((t) => (
            <tr key={t.id}>
              <td>{t.name}</td><td>{t.created_by}</td>
              <td>{t.revoked ? <span className="badge ips">revoked</span> : 'active'}</td>
              <td>{!t.revoked && <button onClick={async () => { await api.revokePushToken(t.id); load() }}>Revoke</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <form className="card" onSubmit={create}>
        <h2>New push token</h2>
        <label>Name (no colons)<input value={name} onChange={(e) => setName(e.target.value)} required /></label>
        <button className="primary">Create &amp; generate token</button>
      </form>
    </>
  )
}
