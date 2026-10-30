import { useEffect, useState } from 'react'
import { Link, Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { api, setUnauthorizedHandler } from './api'
import Login from './pages/Login'
import Verify from './pages/Verify'
import Repos from './pages/Repos'
import RepoDetail from './pages/RepoDetail'
import Users from './pages/Users'
import Admins from './pages/Admins'

export default function App() {
  const nav = useNavigate()
  const loc = useLocation()
  const [email, setEmail] = useState<string | null>(null)
  const [checked, setChecked] = useState(false)
  const open = loc.pathname === '/login' || loc.pathname === '/verify'

  useEffect(() => {
    setUnauthorizedHandler(() => { setEmail(null); if (!open) nav('/login') })
  }, [nav, open])

  useEffect(() => {
    api.me().then((m) => setEmail(m.email)).catch(() => {}).finally(() => setChecked(true))
  }, [])

  if (!checked) return <p className="muted pad">Loading…</p>
  if (!email && !open) return <Navigate to="/login" replace />

  return (
    <>
      {email && (
        <header>
          <strong>dnfrepo</strong>
          <nav>
            <Link to="/">Repos</Link>
            <Link to="/users">Repo users</Link>
            <Link to="/admins">Admins</Link>
          </nav>
          <span className="spacer" />
          <span className="muted">{email}</span>
          <button onClick={async () => { await api.logout(); setEmail(null); nav('/login') }}>Log out</button>
        </header>
      )}
      <main>
        <Routes>
          <Route path="/login" element={<Login onLogin={(e) => { setEmail(e); nav('/') }} />} />
          <Route path="/verify" element={<Verify />} />
          <Route path="/" element={<Repos />} />
          <Route path="/repos/:slug" element={<RepoDetail />} />
          <Route path="/users" element={<Users />} />
          <Route path="/admins" element={<Admins />} />
        </Routes>
      </main>
    </>
  )
}
