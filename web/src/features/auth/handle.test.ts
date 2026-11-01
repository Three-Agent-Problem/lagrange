import { describe, expect, it } from 'vitest'
import { authLinkOf, HANDLE_PATTERN, suggestHandle } from './handle'

describe('suggestHandle', () => {
  it('makes a valid handle from a name or email', () => {
    expect(suggestHandle('Grace Hopper')).toBe('grace-hopper')
    expect(suggestHandle('Ada.Lovelace@example.com')).toBe('ada-lovelace')
    expect(suggestHandle('  Zoë  Ångström ')).toBe('zoe-angstrom')
    expect(suggestHandle('x')).toBe('')
    expect(suggestHandle('a'.repeat(50) + ' b')).toHaveLength(40)
    for (const h of ['grace-hopper', 'ada-lovelace', 'zoe-angstrom']) expect(HANDLE_PATTERN.test(h)).toBe(true)
  })
})

describe('authLinkOf', () => {
  it('reads emailed link paths', () => {
    expect(authLinkOf('/auth/invite/lga_ab12')).toEqual({ purpose: 'invite', token: 'lga_ab12' })
    expect(authLinkOf('/auth/reset/lga_x/')).toEqual({ purpose: 'reset', token: 'lga_x' })
    expect(authLinkOf('/auth/other/lga_x')).toBeUndefined()
    expect(authLinkOf('/runs')).toBeUndefined()
  })
})
