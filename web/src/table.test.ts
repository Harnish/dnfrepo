import { describe, it, expect } from 'vitest'
import { filterRows, sortRows } from './table'

type R = { name: string; size: number; ver: string; when: string | null }
const rows: R[] = [
  { name: 'beta', size: 20, ver: '1.10.0', when: '2026-11-02T00:00:00Z' },
  { name: 'Alpha', size: 3, ver: '1.9.0', when: null },
  { name: 'gamma', size: 100, ver: '1.2.0', when: '2026-11-01T00:00:00Z' },
]
const get = (r: R, k: string) => (r as unknown as Record<string, string | number | null>)[k]

describe('sortRows', () => {
  it('no key keeps the given order', () => expect(sortRows(rows, null, 'asc', get)).toEqual(rows))
  it('strings sort case-insensitively', () => expect(sortRows(rows, 'name', 'asc', get).map((r) => r.name)).toEqual(['Alpha', 'beta', 'gamma']))
  it('numbers sort numerically, desc reverses', () => expect(sortRows(rows, 'size', 'desc', get).map((r) => r.size)).toEqual([100, 20, 3]))
  it('versions compare digit runs as numbers', () => expect(sortRows(rows, 'ver', 'asc', get).map((r) => r.ver)).toEqual(['1.2.0', '1.9.0', '1.10.0']))
  it('nulls go last either direction', () => {
    expect(sortRows(rows, 'when', 'asc', get).map((r) => r.when)).toEqual(['2026-11-01T00:00:00Z', '2026-11-02T00:00:00Z', null])
    expect(sortRows(rows, 'when', 'desc', get)[2].when).toBeNull()
  })
  it('does not mutate', () => { sortRows(rows, 'name', 'asc', get); expect(rows[0].name).toBe('beta') })
})

describe('filterRows', () => {
  const text = (r: R) => [r.name, r.ver]
  it('blank query keeps everything', () => expect(filterRows(rows, '  ', text)).toEqual(rows))
  it('case-insensitive substring on any field', () => {
    expect(filterRows(rows, 'ALP', text).map((r) => r.name)).toEqual(['Alpha'])
    expect(filterRows(rows, '1.10', text).map((r) => r.name)).toEqual(['beta'])
  })
  it('every word must match', () => expect(filterRows(rows, 'gam 1.2', text).map((r) => r.name)).toEqual(['gamma']))
})
