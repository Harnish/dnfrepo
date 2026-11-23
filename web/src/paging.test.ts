import { describe, it, expect } from 'vitest'
import { paginate } from './paging'

describe('paginate', () => {
  const xs = Array.from({ length: 53 }, (_, i) => i)
  it('slices the requested page', () => {
    expect(paginate(xs, 1, 25)).toEqual({ items: xs.slice(0, 25), page: 1, pages: 3 })
    expect(paginate(xs, 3, 25).items).toEqual([50, 51, 52])
  })
  it('clamps out-of-range pages (list shrank after a delete)', () => {
    expect(paginate(xs, 9, 25).page).toBe(3)
    expect(paginate(xs, 0, 25).page).toBe(1)
  })
  it('empty list is one empty page', () => expect(paginate([], 2, 25)).toEqual({ items: [], page: 1, pages: 1 }))
})
