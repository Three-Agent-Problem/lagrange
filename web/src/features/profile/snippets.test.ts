import { describe, expect, it } from 'vitest'
import { reachOf, snippets, type Standing } from './snippets'

const url = 'https://lg.example.com/a/quotes'
const ws = (slug: string, status: string, role = 'member'): Standing => ({ slug, name: slug, role, status })

describe('snippets', () => {
  it('carry a token only for addresses that need one', () => {
    const open = snippets(url, 'Hello', false)
    expect(open.map((s) => s.id)).toEqual(['cli', 'curl', 'python', 'go'])
    expect(open.some((s) => s.code.includes('LAGRANGE_TOKEN'))).toBe(false)
    const closed = snippets(url, 'Hello', true)
    expect(closed.every((s) => s.code.includes('LAGRANGE_TOKEN'))).toBe(true)
    expect(closed[0].code).toBe(`a2a -a ${url} --auth "Bearer $LAGRANGE_TOKEN" send Hello`)
  })
  it('quote the message for the shell', () => {
    const [cli, curl] = snippets(url, "Quote 200 hoodies, it's urgent", false)
    expect(cli.code).toBe(`a2a -a ${url} send 'Quote 200 hoodies, it'\\''s urgent'`)
    expect(curl.code).toContain(`"text":"Quote 200 hoodies, it'\\''s urgent"`)
    expect(curl.code).toContain(`${url}/rpc`)
  })
})

describe('reachOf', () => {
  it('sends through a workspace the address is in or connected to', () => {
    expect(reachOf('quotes', 'ada', 'connections', [ws('a', ''), ws('b', 'accepted')], 'a')).toEqual({ kind: 'send', via: 'b' })
    expect(reachOf('quotes', 'ada', 'workspace', [ws('a', 'in')], 'a')).toEqual({ kind: 'send', via: 'a' })
    expect(reachOf('ada', 'ada', 'workspace', [], '')).toEqual({ kind: 'self' })
  })
  it('sends to public addresses from the current workspace', () => {
    expect(reachOf('quotes', 'ada', 'public', [ws('a', ''), ws('b', '')], 'b')).toEqual({ kind: 'send', via: 'b' })
  })
  it('asks from workspaces where the visitor may, unless pending or blocked', () => {
    expect(reachOf('quotes', 'ada', 'connections', [ws('a', 'pending')], 'a').kind).toBe('pending')
    expect(reachOf('quotes', 'ada', 'connections', [ws('a', ''), ws('v', '', 'viewer'), ws('x', 'blocked')], 'a')).toEqual({ kind: 'ask', from: [ws('a', '')] })
    expect(reachOf('quotes', 'ada', 'connections', [ws('x', 'blocked')], 'x').kind).toBe('blocked')
    expect(reachOf('quotes', 'ada', 'connections', [ws('v', '', 'viewer')], 'v').kind).toBe('closed')
    expect(reachOf('quotes', 'ada', 'workspace', [ws('a', '')], 'a').kind).toBe('closed')
  })
})
