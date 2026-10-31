import { describe, expect, it } from 'vitest'
import type { Conversation, Entry, TaskView } from '@/lib/api'
import { others, partsOf, timeline } from '@/features/inbox/pairs'

describe('timeline', () => {
  it('interleaves tasks and message exchanges by time', () => {
    const tasks = [{ id: 't1', createdAt: '2026-10-31T10:00:02Z' }] as TaskView[]
    const messages = [
      { seq: 4, at: '2026-10-31T10:00:01Z', kind: 'message', sender: 'buyer', recipient: 'contoso', summary: 'price?', data: { request: 'price?', replyContent: 'Quote\n{"total":4560}' } },
    ] as Entry[]
    const items = timeline(tasks, messages)
    expect(items.map((i) => i.kind)).toEqual(['message', 'task'])
    const m = items[0]
    if (m.kind !== 'message') throw new Error('want a message')
    expect(m.ask.parts).toEqual([{ text: 'price?' }])
    expect(m.reply?.parts).toEqual([{ text: 'Quote' }, { data: { total: 4560 } }])
  })
})

describe('partsOf', () => {
  it('keeps text that only looks like JSON', () => {
    expect(partsOf('{not json}')).toEqual([{ text: '{not json}' }])
  })
})

describe('others', () => {
  it('leaves out my own conversations and filters by handle', () => {
    const cs = [{ a: 'ada', b: 'buyer' }, { a: 'buyer', b: 'contoso' }, { a: 'buyer', b: 'northwind' }] as Conversation[]
    expect(others(cs, 'ada', '').map((c) => c.b)).toEqual(['contoso', 'northwind'])
    expect(others(cs, 'ada', '@north').map((c) => c.b)).toEqual(['northwind'])
  })
})
