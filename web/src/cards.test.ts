import { describe, it, expect } from 'vitest'
import { cellLabels } from './cards'

describe('cellLabels', () => {
  it('maps cells to header text, sort arrows stripped', () =>
    expect(cellLabels(['Name ▲', 'Size', ''], [1, 1, 1])).toEqual(['Name', 'Size', null]))
  it('spanning cells get no label and shift later columns', () =>
    expect(cellLabels(['A', 'B', 'C'], [2, 1])).toEqual([null, 'C']))
  it('cells past the headers get no label', () =>
    expect(cellLabels(['A'], [1, 1])).toEqual(['A', null]))
})
