import { FormEvent, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api'

export default function Verify() {
  const [params] = useSearchParams()
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [done, setDone] = useState(false)
  async function submit(ev: FormEvent) {
    ev.preventDefault()
    try { await api.verify(params.get('token') ?? '', password); setDone(true) } catch (x) { setErr((x as Error).message) }
  }
  if (done) return <div className="card narrow"><p>Email verified. <Link to="/login">Sign in</Link></p></div>
  return (
    <form className="card narrow" onSubmit={submit}>
      <h1>Verify email &amp; set password</h1>
      <label>New password (8+ characters)<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} minLength={8} required autoFocus /></label>
      {err && <p className="error">{err}</p>}
      <button className="primary">Verify</button>
    </form>
  )
}
