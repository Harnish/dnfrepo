import { describe, expect, it } from 'vitest'
import { archLabel, defaultLocalName, isOpenPath, pullCommand, sortTags, type RegistryTag } from './registry'

const tag = (name: string, created: string | null): RegistryTag =>
  ({ name, digest: 'sha256:' + name, size: 1, created, arches: [], layers: [], pull: '' })

describe('pullCommand', () => {
  it('formats the server-provided reference, nested names and ports included', () => {
    expect(pullCommand('reg.example.org/a/b/c:v1')).toBe('docker pull reg.example.org/a/b/c:v1')
    expect(pullCommand('localhost:8080/app:latest')).toBe('docker pull localhost:8080/app:latest')
  })
})

describe('isOpenPath', () => {
  it('lets the login and public browse pages through without a session', () => {
    expect(isOpenPath('/login')).toBe(true)
    expect(isOpenPath('/browse/registry')).toBe(true)
  })
  it('keeps admin pages behind sign-in', () => {
    for (const p of ['/', '/registry', '/registry/repo', '/push-tokens', '/browse', '/browser/x']) expect(isOpenPath(p)).toBe(false)
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

describe('defaultLocalName', () => {
  it('drops the registry host and tag; bare names get library/', () => {
    expect(defaultLocalName('docker.io/plexinc/pms-docker:latest')).toBe('plexinc/pms-docker')
    expect(defaultLocalName('plexinc/pms-docker')).toBe('plexinc/pms-docker')
    expect(defaultLocalName('alpine')).toBe('library/alpine')
    expect(defaultLocalName('alpine:3.20')).toBe('library/alpine')
    expect(defaultLocalName('ghcr.io/a/b:1')).toBe('a/b')
    expect(defaultLocalName('localhost:5000/x')).toBe('x')
    expect(defaultLocalName('  ')).toBe('')
  })
})
