import { describe, expect, it } from 'vitest'
import { deliveryLine } from './events'

describe('deliveryLine', () => {
  it('says how a delivery went', () => {
    expect(deliveryLine({ status: 'delivered', attempts: 1, lastStatus: 204 })).toBe('204 · 1 try')
    expect(deliveryLine({ status: 'pending', attempts: 0 })).toBe('queued')
    expect(deliveryLine({ status: 'pending', attempts: 2, lastStatus: 500, lastError: 'the endpoint answered 500' })).toBe('retrying · 2 tries · the endpoint answered 500')
    expect(deliveryLine({ status: 'failed', attempts: 8, lastStatus: 404 })).toBe('gave up after 8 tries · HTTP 404')
  })
})
