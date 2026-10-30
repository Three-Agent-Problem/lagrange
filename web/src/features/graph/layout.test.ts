import { expect, it } from 'vitest'
import { layout } from '@/features/graph/layout'

it('puts delegates to the right of their callers', () => {
  const box = (id: string) => ({ id, width: 200, height: 60 })
  const pos = layout([box('ada'), box('buyer'), box('s1'), box('s2')], [
    { source: 'ada', target: 'buyer' },
    { source: 'buyer', target: 's1' },
    { source: 'buyer', target: 's2' },
  ])
  expect(pos.ada.x).toBeLessThan(pos.buyer.x)
  expect(pos.buyer.x).toBeLessThan(pos.s1.x)
  expect(pos.s1.x).toBe(pos.s2.x)
  expect(pos.s1.y).not.toBe(pos.s2.y)
})
