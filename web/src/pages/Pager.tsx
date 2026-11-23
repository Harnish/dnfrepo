import { useState } from 'react'
import { paginate, PAGE_SIZE } from '../paging'

/** Client-side paging state for a list; render <Pager {...pg} /> under it. */
export function usePaged<T>(xs: T[]) {
  const [page, setPage] = useState(1)
  const r = paginate(xs, page, PAGE_SIZE)
  return { ...r, total: xs.length, setPage }
}

export function Pager({ page, pages, total, setPage }: { page: number; pages: number; total: number; setPage: (p: number) => void }) {
  if (pages <= 1) return null
  return (
    <div className="row pager">
      <button type="button" disabled={page <= 1} onClick={() => setPage(page - 1)}>← Prev</button>
      <span className="muted">Page {page} of {pages} · {total} total</span>
      <button type="button" disabled={page >= pages} onClick={() => setPage(page + 1)}>Next →</button>
    </div>
  )
}
