import { describe, expect, it } from 'vitest'
import { cliRequest, defaultKeyRole, keyRoles } from './keys'

const challenge = 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM'

describe('keyRoles', () => {
  it('caps keys at your role, and never gives owner', () => {
    expect(keyRoles('owner')).toEqual(['viewer', 'member', 'admin'])
    expect(keyRoles('member')).toEqual(['viewer', 'member'])
    expect(keyRoles('viewer')).toEqual(['viewer'])
    expect(keyRoles(undefined)).toEqual([])
  })
  it('defaults to member when it can', () => {
    expect(defaultKeyRole('owner')).toBe('member')
    expect(defaultKeyRole('viewer')).toBe('viewer')
  })
})

describe('cliRequest', () => {
  it('reads the browser flow', () => {
    expect(cliRequest(`?challenge=${challenge}&port=51234&name=lagrange+CLI+on+mac`)).toEqual({ challenge, port: 51234, name: 'lagrange CLI on mac' })
  })
  it('reads the paste flow', () => {
    expect(cliRequest(`?challenge=${challenge}`)).toEqual({ challenge, port: 0, name: 'lagrange CLI' })
  })
  it('refuses broken links', () => {
    expect(cliRequest('?challenge=short')).toBeUndefined()
    expect(cliRequest(`?challenge=${challenge}&port=80`)).toBeUndefined()
    expect(cliRequest(`?challenge=${challenge}&port=abc`)).toBeUndefined()
  })
})
