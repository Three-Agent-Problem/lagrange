import { describe, expect, it } from 'vitest'
import { formData, formFields } from '@/lib/schemaForm'

const approval = {
  type: 'object',
  properties: {
    approved: { type: 'boolean', title: 'Approve' },
    note: { type: 'string' },
    quantity: { type: 'integer' },
    size: { type: 'string', enum: ['S', 'M', 'L'] },
  },
  required: ['approved'],
}

describe('formFields', () => {
  it('turns a lone boolean into a decision placed last', () => {
    const fields = formFields(approval)
    expect(fields.map((f) => [f.name, f.kind])).toEqual([
      ['note', 'text'],
      ['quantity', 'integer'],
      ['size', 'select'],
      ['approved', 'decision'],
    ])
    expect(fields[3].required).toBe(true)
    expect(fields[2].options).toEqual(['S', 'M', 'L'])
  })

  it('shows several booleans as checkboxes', () => {
    const fields = formFields({ properties: { a: { type: 'boolean' }, b: { type: ['boolean', 'null'] } } })
    expect(fields.map((f) => f.kind)).toEqual(['checkbox', 'checkbox'])
  })
})

describe('formData', () => {
  it('converts values to schema types', () => {
    expect(formData(approval, { approved: true, quantity: '200', size: 'M', note: '' })).toEqual({ data: { approved: true, quantity: 200, size: 'M' } })
  })

  it('rejects bad numbers', () => {
    expect(formData(approval, { approved: true, quantity: '2.5' })).toEqual({ error: 'quantity must be a whole number' })
  })
})
