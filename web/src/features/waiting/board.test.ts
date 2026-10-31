import { describe, expect, it } from 'vitest'
import type { Wait } from '@/lib/api'
import { fromUnit, groupWaits, toUnit, until, urgency, waited, words } from '@/features/waiting/board'

const now = Date.parse('2026-10-31T12:00:00Z')
const min = (n: number) => new Date(now - n * 60_000).toISOString()
const wait = (who: string, since: string, more: Partial<Wait> = {}): Wait =>
  ({ task: { id: `${who}-${since}` }, runId: 'r', who, asker: 'buyer', person: true, since, reminders: 0, ...more }) as Wait

describe('groupWaits', () => {
  it('puts whoever has blocked longest first, oldest wait first', () => {
    const g = groupWaits([wait('bob', min(5)), wait('ada', min(30)), wait('bob', min(90)), wait('ada', min(10))])
    expect(g.map((x) => x.who)).toEqual(['bob', 'ada'])
    expect(g[0].waits.map((w) => w.since)).toEqual([min(90), min(5)])
    expect(g[1].oldest).toBe(Date.parse(min(30)))
  })
})

describe('urgency', () => {
  it('is late after a reminder and overdue once escalated', () => {
    expect(urgency(wait('ada', min(1), { remindAt: min(-5) }), now)).toBe('ok')
    expect(urgency(wait('ada', min(10), { remindAt: min(1) }), now)).toBe('late')
    expect(urgency(wait('ada', min(10), { reminders: 1 }), now)).toBe('late')
    expect(urgency(wait('ada', min(10), { escalatedAt: min(1) }), now)).toBe('overdue')
  })
})

describe('units', () => {
  it('round-trips the largest whole unit', () => {
    expect(toUnit(7200)).toEqual({ n: 2, unit: 'h' })
    expect(toUnit(90 * 60)).toEqual({ n: 90, unit: 'm' })
    expect(toUnit(172800)).toEqual({ n: 2, unit: 'd' })
    expect(fromUnit(3, 'h')).toBe(10800)
    expect(words(3600)).toBe('1 hour')
    expect(words(1800)).toBe('30 minutes')
  })
  it('says how long and how soon', () => {
    expect(waited(min(125), now)).toBe('2h')
    expect(until(min(-12), now)).toBe('in 12m')
    expect(until(min(1), now)).toBe('due')
  })
})
