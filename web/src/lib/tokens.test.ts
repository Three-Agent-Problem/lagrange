import { describe, expect, it } from 'vitest'
import { count, tokensText } from '@/lib/tokens'

describe('tokens', () => {
  it('writes counts short', () => {
    expect([0, 999, 1000, 1540, 9999, 28400, 1_200_000].map(count)).toEqual(['0', '999', '1k', '1.5k', '10k', '28k', '1.2M'])
  })
  it('leaves out zero counters', () => {
    expect(tokensText({ output: 340, cacheRead: 28400 })).toBe('340 out · 28k cache read')
    expect(tokensText(undefined)).toBe('')
  })
})
