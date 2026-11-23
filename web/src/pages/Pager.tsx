import { useState } from 'react'
import { paginate, PAGE_SIZE } from '../paging'
import { Dir, filterRows, sortRows } from '../table'

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

type Val = string | number | null | undefined
type Sort = { key: string | null; dir: Dir }

/** Filter + sort + page a list. get(row, key) gives a column's sort value; text(row) the filterable fields. */
export function useTable<T>(xs: T[], get: (r: T, k: string) => Val, text: (r: T) => Val[]) {
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<Sort>({ key: null, dir: 'asc' })
  const matched = filterRows(xs, q, text)
  const pg = usePaged(sortRows(matched, sort.key, sort.dir, get))
  return {
    pg, sort,
    filter: { q, setQ: (v: string) => { setQ(v); pg.setPage(1) }, shown: matched.length, total: xs.length },
    // click: asc, again: desc, again: back to the list's own order
    toggle: (k: string) => {
      setSort((s) => s.key !== k ? { key: k, dir: 'asc' } : s.dir === 'asc' ? { key: k, dir: 'desc' } : { key: null, dir: 'asc' })
      pg.setPage(1)
    },
  }
}

export function SortTh({ k, label, t }: { k: string; label: string; t: { sort: Sort; toggle: (k: string) => void } }) {
  const on = t.sort.key === k
  return (
    <th aria-sort={on ? (t.sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <button type="button" className="th-sort" onClick={() => t.toggle(k)}>{label}{on ? (t.sort.dir === 'asc' ? ' ▲' : ' ▼') : ''}</button>
    </th>
  )
}

export function FilterBox({ q, setQ, shown, total }: { q: string; setQ: (v: string) => void; shown: number; total: number }) {
  return (
    <div className="row filter">
      <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter…" aria-label="Filter" />
      {q.trim() && <span className="muted">{shown} of {total}</span>}
    </div>
  )
}
