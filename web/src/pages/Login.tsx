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
    <div className="landing">
    <section className="pitch">
      <h1>Your own package server</h1>
      <p className="lead">dnfrepo hosts signed dnf/yum and apt repositories and a container registry from one small Go service, built for a homelab or small team.</p>
      <ul>
        <li><strong>Signed rpm and deb repos.</strong> Upload packages, follow GitHub releases, or mirror an upstream repo in full or as a pull-through cache.</li>
        <li><strong>dev → test → prod.</strong> Packages reach later stages only by promotion, one at a time or a whole sync batch.</li>
        <li><strong>Container registry.</strong> Push with tokens, control who pulls each image, and mirror upstream images when they change.</li>
        <li><strong>Access per repo.</strong> Public, IP allowlist, or per-user tokens, plus retention limits and an activity log.</li>
      </ul>
      <p className="muted">Hosts just add the <code>.repo</code> file or apt source line and install as usual.</p>
    </section>
    <div className="card signin">
      <h2 style={{ marginTop: 0 }}>Sign in</h2>
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
    </div>
  )
}
