import { FormEvent, useState } from 'react'
import { api } from '../api'

export default function Login({ onLogin }: { onLogin: (email: string) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  async function submit(ev: FormEvent) {
    ev.preventDefault()
    try { onLogin((await api.login(email, password)).email) } catch (x) { setErr((x as Error).message) }
  }
  return (
    <form className="card narrow" onSubmit={submit}>
      <h1>Sign in</h1>
      <label>Email<input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus /></label>
      <label>Password<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
      {err && <p className="error">{err}</p>}
      <button className="primary">Sign in</button>
    </form>
  )
}
