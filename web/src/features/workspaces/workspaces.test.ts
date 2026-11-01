import { describe, expect, it } from 'vitest'
import type { Workspace } from '@/lib/api'
import { atLeast, initials, pickWorkspace } from './roles'

const ws = (slug: string): Workspace => ({ id: slug.length, slug, name: slug, role: 'member' })

describe('workspaces', () => {
  it('opens the workspace picked last, if still a member, else the oldest', () => {
    const all = [ws('default'), ws('lab')]
    expect(pickWorkspace(all, 'lab')?.slug).toBe('lab')
    expect(pickWorkspace(all, 'gone')?.slug).toBe('default')
    expect(pickWorkspace(all, '')?.slug).toBe('default')
    expect(pickWorkspace([], 'lab')).toBeUndefined()
  })
  it('ranks roles', () => {
    expect(atLeast('owner', 'admin')).toBe(true)
    expect(atLeast('member', 'member')).toBe(true)
    expect(atLeast('viewer', 'member')).toBe(false)
    expect(atLeast(undefined, 'viewer')).toBe(false)
  })
  it('makes two-letter badges', () => {
    expect(initials('Acme procurement')).toBe('AP')
    expect(initials('default')).toBe('DE')
    expect(initials('vic-s-lab')).toBe('VS')
  })
})
