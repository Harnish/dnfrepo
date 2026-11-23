import { describe, it, expect } from 'vitest'
import { byLastPulled, nextMirrorFor } from './mirrorState'

describe('nextMirrorFor', () => {
  it('slug change resets', () => expect(nextMirrorFor('el9', 'el9-test', true, false, false)).toBeNull())
  it('keeps a manual choice on the same slug', () => expect(nextMirrorFor('el9', 'el9', true, false, false)).toBe('el9'))
  it('probe or param marks the slug', () => {
    expect(nextMirrorFor(null, 'a', true, true, false)).toBe('a')
    expect(nextMirrorFor(null, 'a', true, false, true)).toBe('a')
  })
  it('non-first stage is never a mirror', () => expect(nextMirrorFor('a', 'a', false, true, true)).toBeNull())
})

describe('byLastPulled', () => {
  it('orders most recently pulled first without mutating', () => {
    const ps = [{ filename: 'a', last_pulled: '2026-01-01T00:00:00Z' }, { filename: 'b', last_pulled: '2026-03-01T00:00:00Z' }, { filename: 'c', last_pulled: '2026-02-01T00:00:00Z' }]
    expect(byLastPulled(ps).map((p) => p.filename)).toEqual(['b', 'c', 'a'])
    expect(ps[0].filename).toBe('a')
  })
})
