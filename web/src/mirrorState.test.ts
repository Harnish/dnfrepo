import { describe, it, expect } from 'vitest'
import { nextMirrorFor } from './mirrorState'

describe('nextMirrorFor', () => {
  it('slug change resets', () => expect(nextMirrorFor('el9', 'el9-test', true, false, false)).toBeNull())
  it('keeps a manual choice on the same slug', () => expect(nextMirrorFor('el9', 'el9', true, false, false)).toBe('el9'))
  it('probe or param marks the slug', () => {
    expect(nextMirrorFor(null, 'a', true, true, false)).toBe('a')
    expect(nextMirrorFor(null, 'a', true, false, true)).toBe('a')
  })
  it('non-first stage is never a mirror', () => expect(nextMirrorFor('a', 'a', false, true, true)).toBeNull())
})
