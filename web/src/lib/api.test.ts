import { describe, expect, it } from 'vitest'
import { textOf } from './api'

describe('textOf', () => {
  it('joins streamed chunks without adding spaces', () => {
    expect(textOf([{ text: 'I fixed `add' }, { text: '.py` by chang' }, { text: 'ing it.' }])).toBe('I fixed `add.py` by changing it.')
  })
  it('skips data parts', () => {
    expect(textOf([{ text: 'Approve?' }, { data: { type: 'object' } }])).toBe('Approve?')
  })
})
