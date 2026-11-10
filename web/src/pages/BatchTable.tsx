import { Batch } from '../api'

const fmt = (t: string | null) => (t ? new Date(t).toLocaleString() : '')

/** Sync batches with a promote button. `next` is the next stage's name, if any. */
export default function BatchTable({ batches, next, onPromote }: { batches: Batch[]; next?: string; onPromote: (b: Batch) => void }) {
  return (
    <>
      <h3>Batches</h3>
      <table>
        <thead><tr><th>ID</th><th>Started</th><th>Status</th><th>Added</th><th>Error</th><th /></tr></thead>
        <tbody>
          {batches.map((b) => (
            <tr key={b.id}>
              <td>{b.id}</td><td>{fmt(b.started_at)}</td><td>{b.status}</td><td>{b.added}</td><td className="error">{b.error}</td>
              <td><button disabled={!next} title={next ? '' : 'This repo has no next stage'} onClick={() => onPromote(b)}>Promote batch → {next ?? 'next stage'}</button></td>
            </tr>
          ))}
          {batches.length === 0 && <tr><td colSpan={6} className="muted">No syncs yet.</td></tr>}
        </tbody>
      </table>
    </>
  )
}
