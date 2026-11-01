import { describe, expect, it } from 'vitest'
import { orbitarOf } from './Avatar'

describe('orbitarOf', () => {
  it('draws the same system for the same handle', () => {
    const a = orbitarOf('acme-buyer')
    const b = orbitarOf('acme-buyer')
    expect([a.kind, a.tilt, a.hue]).toEqual([b.kind, b.tilt, b.hue])
    expect(Array.from({ length: 5 }, a.rnd)).toEqual(Array.from({ length: 5 }, b.rnd))
  })

  it('varies the design across handles, not only the color', () => {
    const kinds = new Set(Array.from({ length: 60 }, (_, i) => orbitarOf(`agent-${i}`).kind))
    expect(kinds.size).toBeGreaterThanOrEqual(6)
  })
})
