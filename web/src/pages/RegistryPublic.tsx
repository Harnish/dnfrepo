import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api, fmtSize } from '../api'
import { archLabel, pullCommand, sortTags, type PublicRepoRow, type RegistryTag } from '../registry'

/** Read-only browser for images an admin marked Listed. No sign-in, no controls. */
export default function RegistryPublic() {
  const name = useSearchParams()[0].get('name')
  const [repos, setRepos] = useState<PublicRepoRow[] | null>(null)
  const [tags, setTags] = useState<RegistryTag[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    setErr('')
    if (name) { setTags(null); api.publicRegistryRepo(name).then((r) => setTags(r.tags)).catch((x) => setErr(x.message)) }
    else api.publicRegistryRepos().then(setRepos).catch((x) => setErr(x.message))
  }, [name])

  return (
    <>
      <h1>Container images</h1>
      {err && <p className="error">{err}</p>}
      {!name && repos && (
        repos.length === 0 ? <p className="muted">No public images.</p> : (
          <table>
            <thead><tr><th>Image</th><th>Tags</th></tr></thead>
            <tbody>{repos.map((r) => (
              <tr key={r.name}><td><Link to={`/browse/registry?name=${encodeURIComponent(r.name)}`}>{r.name}</Link></td><td>{r.tags}</td></tr>
            ))}</tbody>
          </table>
        )
      )}
      {name && (
        <>
          <p><Link to="/browse/registry">← All images</Link></p>
          <h2>{name}</h2>
          {tags && (
            <table>
              <thead><tr><th>Tag</th><th>Size</th><th>Created</th><th>Platforms</th><th>Pull</th></tr></thead>
              <tbody>{sortTags(tags).map((t) => (
                <tr key={t.name}>
                  <td>{t.name}</td><td>{fmtSize(t.size)}</td>
                  <td>{t.created ? new Date(t.created).toLocaleDateString() : '—'}</td>
                  <td>{archLabel(t.arches)}</td>
                  <td><code>{pullCommand(t.pull)}</code> <button onClick={() => navigator.clipboard?.writeText(pullCommand(t.pull))}>Copy</button></td>
                </tr>
              ))}</tbody>
            </table>
          )}
        </>
      )}
    </>
  )
}
