import { FormEvent, useState } from 'react'
import { api } from '../api'

export default function Admins() {
  const [email, setEmail] = useState('')
  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')
  async function submit(ev: FormEvent) {
    ev.preventDefault()
    setMsg(''); setErr('')
    try { await api.invite(email); setMsg(`Invitation sent to ${email}`); setEmail('') } catch (x) { setErr((x as Error).message) }
  }
  return (
    <form className="card narrow" onSubmit={submit}>
      <h1>Invite an admin</h1>
      <p className="muted">They get an email with a link to verify and set a password.</p>
      <label>Email<input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required /></label>
      {msg && <p className="ok">{msg}</p>}
      {err && <p className="error">{err}</p>}
      <button className="primary">Send invitation</button>
    </form>
  )
}
