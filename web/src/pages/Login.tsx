import { useEffect, useState } from 'react'
import { api } from '../api'

export default function Login() {
  const [local, setLocal] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => { api.methods().then(m => setLocal(m.local)).catch(() => {}) }, [])
  const submit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    try {
      await api.localLogin(String(f.get('email')), String(f.get('password')))
      window.location.assign('/')
    } catch (x) { setErr((x as Error).message) }
  }
  return (
    <div className="card narrow">
      <h1>Sign in</h1>
      {local ? (
        <form onSubmit={submit}>
          <input name="email" type="email" placeholder="Email" autoComplete="username" required />
          <input name="password" type="password" placeholder="Password" autoComplete="current-password" required />
          {err && <p role="alert">{err}</p>}
          <button className="primary" type="submit">Sign in</button>
        </form>
      ) : (
        <>
          <p className="muted">Admins sign in with Authentik.</p>
          <a className="primary button" href="/api/auth/oidc/login">Sign in with Authentik</a>
        </>
      )}
    </div>
  )
}
