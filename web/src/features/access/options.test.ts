import { describe, expect, it } from 'vitest'
import { accessHint, ACCESS } from './options'

describe('access', () => {
  it('offers the three settings, narrowest first', () => {
    expect(ACCESS.map((a) => a.access)).toEqual(['workspace', 'connections', 'public'])
  })
  it('explains each for agents and people', () => {
    expect(accessHint('public')).toContain('without a token')
    expect(accessHint('workspace', true)).toContain('your workspaces')
    expect(accessHint('connections')).toContain('Its workspace')
  })
})
