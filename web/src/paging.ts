export const PAGE_SIZE = 25

/** One page of xs (1-based), with page clamped to the range that exists. */
export function paginate<T>(xs: T[], page: number, size = PAGE_SIZE): { items: T[]; page: number; pages: number } {
  const pages = Math.max(1, Math.ceil(xs.length / size))
  const p = Math.min(Math.max(1, page), pages)
  return { items: xs.slice((p - 1) * size, p * size), page: p, pages }
}
