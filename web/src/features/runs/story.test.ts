import { describe, expect, it } from 'vitest'
import type { RunDetail } from '@/lib/api'
import { content, pretty, story } from '@/features/runs/story'
import fixture from './hoodies.fixture.json'

// A real run recorded from the procurement demo: Ada orders hoodies from @acme-buyer,
// which asks @contoso and @northwind for quotes, asks Ada to approve, orders from
// Northwind and gets its fulfilment updates by webhook.
const run = fixture as unknown as RunDetail

describe('story', () => {
  const s = story(run)
  const line = (i: number) => {
    const st = s.steps[i]
    return `${st.kind} ${st.from}${st.to ? `>${st.to}` : ''}`
  }

  it('tells the order step by step, in order', () => {
    expect(s.steps.map((_, i) => line(i))).toEqual([
      'ask ada>acme-buyer',
      'progress acme-buyer',
      'message acme-buyer>northwind',
      'message acme-buyer>contoso',
      'artifact acme-buyer',
      'wait acme-buyer>ada',
      'answer ada>acme-buyer',
      'progress acme-buyer',
      'ask acme-buyer>northwind',
      'progress northwind',
      'artifact northwind',
      'progress acme-buyer',
      'artifact northwind',
      'done northwind>acme-buyer',
      'artifact acme-buyer',
      'done acme-buyer>ada',
    ])
    expect(s.steps.map((st) => st.n)).toEqual(s.steps.map((_, i) => i + 1))
  })

  it('names the lanes in order of appearance', () => {
    expect(s.lanes).toEqual(['ada', 'acme-buyer', 'northwind', 'contoso'])
  })

  it('shows what was asked and answered, not just that something happened', () => {
    // The quotes are asked in parallel; this recording asked Northwind first.
    const quote = s.steps.find((st) => st.kind === 'message' && st.to === 'contoso')!
    expect(quote.title).toBe('@acme-buyer asked @contoso')
    expect(quote.body?.fields).toContainEqual(['request', 'quote'])
    expect(quote.reply?.fields).toContainEqual(['supplier', 'Contoso Prints'])
    expect(s.steps[4].body?.text).toMatch(/^Quotes for 200 hoodies/)
    expect(s.steps[5].title).toBe('@acme-buyer asks @ada')
    expect(s.steps[5].body?.text).toMatch(/^Approve 200 hoodies from Northwind/)
    expect(s.steps[6].title).toBe('@ada answered @acme-buyer')
    expect(s.steps[6].body?.fields).toContainEqual(['approved', 'true'])
  })

  it('folds artifact chunks into one step', () => {
    const fulfilment = s.steps[10]
    expect(fulfilment.title).toBe('@northwind produced “fulfilment”')
    expect(fulfilment.updates.map((u) => u.text)).toEqual(['Printed and quality-checked.', 'Packed and handed to the carrier.'])
  })

  const quote = () => s.steps.find((st) => st.kind === 'message' && st.to === 'contoso')!

  it('gives every exchange to a step, with the webhooks on the supplier’s steps', () => {
    expect(s.loose).toEqual([])
    expect(s.steps.reduce((n, st) => n + st.exchanges.length, 0)).toBe(run.exchanges.length)
    expect(quote().exchanges.map((x) => x.direction).sort()).toEqual(['in', 'out'])
    const pushes = s.steps.flatMap((st) => st.pushes.map((p) => `${st.from}>${p.to}`))
    expect(pushes.length).toBe(run.exchanges.filter((x) => x.direction === 'push').length)
    expect(new Set(pushes)).toEqual(new Set(['northwind>acme-buyer']))
  })
})

describe('content', () => {
  it('turns JSON objects into fields and keeps text', () => {
    expect(content('{"a":1,"b":"x"}')).toEqual({ fields: [['a', '1'], ['b', 'x']] })
    expect(content('Shipped.\n{"tracking":"T1"}')).toEqual({ text: 'Shipped.', fields: [['tracking', 'T1']] })
    expect(content('{"cut":"of…')).toEqual({ text: '{"cut":"of…' })
    expect(content('')).toBeUndefined()
  })
})

describe('pretty', () => {
  it('indents JSON and each event of a stream', () => {
    expect(pretty('{"a":1}')).toBe('{\n  "a": 1\n}')
    expect(pretty('event: message\ndata: {"a":1}\n\ndata: {"b":2}')).toBe('event: message\ndata: {\n  "a": 1\n}\n\ndata: {\n  "b": 2\n}')
    expect(pretty('plain')).toBe('plain')
  })
})
