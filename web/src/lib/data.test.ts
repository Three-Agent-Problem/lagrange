import { describe, expect, it } from 'vitest'
import fixture from '@/features/runs/hoodies.fixture.json'
import { dataFields, decisionOf, formatValue, humanData, humanize, summarize } from '@/lib/data'

const quote = {
  agentUrl: 'http://localhost:8090/a/northwind',
  currency: 'EUR',
  item: 'hoodie',
  leadDays: 7,
  quantity: 200,
  quoteId: 'Q-38972e33',
  shipDate: '2026-11-07',
  supplier: 'Northwind Merch',
  total: 5416,
  unitPrice: 27.08,
  validUntil: '2026-11-07',
}

describe('humanize', () => {
  it('turns keys into labels', () => {
    expect(humanize('shipDate')).toBe('Ship date')
    expect(humanize('unit_price')).toBe('Unit price')
    expect(humanize('quoteId')).toBe('Quote ID')
    expect(humanize('agentUrl')).toBe('Agent URL')
  })
})

describe('dataFields', () => {
  it('puts what people need first, money with its currency, machine fields aside', () => {
    const f = dataFields(quote)!
    expect(f.main.map((x) => x.key)).toEqual(['item', 'quantity', 'supplier', 'total', 'unitPrice', 'shipDate', 'leadDays', 'validUntil'])
    expect(f.main.find((x) => x.key === 'total')).toEqual({ key: 'total', label: 'Total', value: '5,416 EUR', strong: true })
    expect(f.main.find((x) => x.key === 'unitPrice')?.value).toBe('27.08 EUR')
    expect(f.more.map((x) => x.key)).toEqual(['agentUrl', 'currency', 'quoteId'])
  })
  it('ignores non-objects', () => {
    expect(dataFields([1, 2])).toBeUndefined()
    expect(dataFields('x')).toBeUndefined()
  })
  it('formats values', () => {
    expect(formatValue('ok', true)).toBe('Yes')
    expect(formatValue('tags', ['a', 'b'])).toBe('a, b')
    expect(formatValue('ship', { by: 'air', days: 2 })).toBe('By: air · Days: 2')
    expect(formatValue('x', null)).toBe('—')
  })
})

describe('decisionOf', () => {
  it('reads a lone yes/no answer', () => {
    expect(decisionOf({ approved: true })).toEqual({ yes: true, label: 'Approved' })
    expect(decisionOf({ approved: false })).toEqual({ yes: false, label: 'Not approved' })
    expect(decisionOf({ approved: true, note: 'x' })).toBeUndefined()
  })
})

describe('summarize', () => {
  it('keeps text and shortens JSON', () => {
    expect(summarize('Looking for suppliers.')).toBe('Looking for suppliers.')
    expect(summarize('{"approved":true}')).toBe('✓ Approved')
    expect(summarize(JSON.stringify(quote))).toBe('Item hoodie · Quantity 200 · Supplier Northwind Merch')
    expect(summarize('{"cut":"of…')).toBe('{"cut":"of…')
    // The activity log cuts long payloads; the complete pairs still read.
    expect(summarize('{"agentUrl":"http://x/a/n","currency":"EUR","item":"cap","quantity":300,"supplier":"Northwind Merch","total":5416,"suppl…')).toBe(
      'Item cap · Quantity 300 · Supplier Northwind Merch',
    )
  })
})

describe('humanData', () => {
  it('skips schema and challenge parts', () => {
    const parts = [
      { text: 'Approve?' },
      { data: quote },
      { data: { type: 'object' }, mediaType: 'application/schema+json', metadata: { 'https://github.com/a2aproject/experimental-ext-structured-input/tree/main/v1': { type: 'input_schema' } } },
      { data: { type: 'object' }, metadata: { 'https://github.com/a2aproject/experimental-ext-structured-input/tree/main/v1': { type: 'input_schema' } } },
      { data: { url: 'x' }, metadata: { 'https://threeagents.dev/ext/credential-challenge/v1': true } },
    ]
    expect(humanData(parts)).toEqual([quote])
    // A structured answer is what the person said, so it stays.
    const answer = { data: { approved: true }, metadata: { 'https://github.com/a2aproject/experimental-ext-structured-input/tree/main/v1': { type: 'structured_input' } } }
    expect(humanData([answer])).toEqual([{ approved: true }])
  })
  it('keeps nested objects for their own fields', () => {
    const f = dataFields({ poNumber: 'PO-1', confirmation: { orderId: 'O-1', total: 5 } })!
    expect(f.main.find((x) => x.key === 'confirmation')?.nested).toEqual({ orderId: 'O-1', total: 5 })
  })
  it('renders every recorded reply of a real run without JSON', () => {
    const replies = (fixture as { entries: { data?: { replyContent?: string; content?: string } }[] }).entries.flatMap((e) => [e.data?.replyContent, e.data?.content]).filter(Boolean) as string[]
    expect(replies.length).toBeGreaterThan(0)
    for (const r of replies) for (const line of r.split('\n')) expect(summarize(line)).not.toMatch(/^\{"/)
  })
})
