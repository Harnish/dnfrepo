import { describe, expect, it } from 'vitest'
import { archLabel, pullCommand, sortTags, type RegistryTag } from './registry'

const tag = (name: string, created: string | null): RegistryTag =>
  ({ name, digest: 'sha256:' + name, size: 1, created, arches: [], layers: [], pull: '' })

describe('pullCommand', () => {
  it('builds docker pull for nested names', () => {
    expect(pullCommand('reg.example.org', 'a/b/c', 'v1')).toBe('docker pull reg.example.org/a/b/c:v1')
  })
  it('keeps a registry port', () => {
    expect(pullCommand('localhost:8080', 'app', 'latest')).toBe('docker pull localhost:8080/app:latest')
  })
})

describe('sortTags', () => {
  it('puts newest first and tags without a created time last', () => {
    const out = sortTags([tag('old', '2023-01-01T00:00:00Z'), tag('none', null), tag('new', '2024-06-01T00:00:00Z')])
    expect(out.map((t) => t.name)).toEqual(['new', 'old', 'none'])
  })
  it('breaks ties by name and does not mutate its input', () => {
    const input = [tag('b', null), tag('a', null)]
    expect(sortTags(input).map((t) => t.name)).toEqual(['a', 'b'])
    expect(input.map((t) => t.name)).toEqual(['b', 'a'])
  })
})

describe('archLabel', () => {
  it('joins platforms', () => expect(archLabel(['linux/amd64', 'linux/arm64'])).toBe('linux/amd64, linux/arm64'))
  it('shows a dash when unknown', () => expect(archLabel([])).toBe('—'))
})
