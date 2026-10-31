export default function Login() {
  return (
    <div className="card narrow">
      <h1>Sign in</h1>
      <p className="muted">Admins sign in with Authentik.</p>
      <a className="primary button" href="/api/auth/oidc/login">Sign in with Authentik</a>
    </div>
  )
}
