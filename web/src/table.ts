export type Dir = 'asc' | 'desc'
type Val = string | number | null | undefined

// ponytail: numeric-aware string compare, not rpm/dpkg version rules (epoch, ~); fine for a UI sort.
const coll = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** Copy of xs sorted by get(row, key); null key keeps the order. Empty values sort last. */
export function sortRows<T>(xs: T[], key: string | null, dir: Dir, get: (r: T, k: string) => Val): T[] {
  if (!key) return xs
  const s = dir === 'asc' ? 1 : -1
  return [...xs].sort((a, b) => {
    const x = get(a, key), y = get(b, key)
    const ex = x == null || x === '', ey = y == null || y === ''
    if (ex || ey) return ex === ey ? 0 : ex ? 1 : -1
    return s * (typeof x === 'number' && typeof y === 'number' ? x - y : coll.compare(String(x), String(y)))
  })
}

/** Rows where every whitespace-separated word of q is a case-insensitive substring of some field. */
export function filterRows<T>(xs: T[], q: string, text: (r: T) => Val[]): T[] {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean)
  if (!words.length) return xs
  return xs.filter((r) => {
    const hay = text(r).map((v) => String(v ?? '').toLowerCase()).join('\n')
    return words.every((w) => hay.includes(w))
  })
}
