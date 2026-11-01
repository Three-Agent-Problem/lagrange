import { Globe } from 'lucide-react'
import { useId } from 'react'
import { cn } from '@/lib/cn'
import { OUTSIDE } from '@/store/model'

// Stable hue per handle, so a contact keeps its color everywhere.
export function hue(handle: string): number {
  let h = 0
  for (const c of handle) h = (h * 31 + c.charCodeAt(0)) % 360
  return h
}

export function Avatar({ handle, kind, name, size = 36, className }: { handle: string; kind?: 'agent' | 'human'; name?: string; size?: number; className?: string }) {
  const style = { width: size, height: size }
  if (handle === OUTSIDE) {
    return (
      <span className={cn('flex shrink-0 items-center justify-center rounded-full bg-surface-2 text-muted ring-1 ring-line', className)} style={style}>
        <Globe size={size * 0.5} />
      </span>
    )
  }
  const h = hue(handle)
  if (kind === 'human') {
    const initials = (name || handle)
      .split(/\s+/)
      .map((w) => w[0])
      .join('')
      .slice(0, 2)
      .toUpperCase()
    return (
      <span
        className={cn('flex shrink-0 items-center justify-center rounded-full font-bold text-white', className)}
        style={{ ...style, fontSize: size * 0.38, background: `oklch(0.62 0.14 ${h})` }}
      >
        {initials}
      </span>
    )
  }
  return <Orbitar handle={handle} size={size} className={className} />
}

/* ---------- Orbitars: every agent address hashes to its own small orbital system ---------- */

function fnv(s: string) {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619)
  return h >>> 0
}

/** A small seeded PRNG (mulberry32), so the same handle always draws the same system. */
function seeded(seed: number) {
  let a = seed
  return () => {
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

type Orbit = { rx: number; ry: number; tilt: number; dash?: boolean }
const pt = (o: Orbit, deg: number): [number, number] => {
  const t = (deg * Math.PI) / 180
  const k = (o.tilt * Math.PI) / 180
  const x = o.rx * Math.cos(t)
  const y = o.ry * Math.sin(t)
  return [16 + x * Math.cos(k) - y * Math.sin(k), 16 + x * Math.sin(k) + y * Math.cos(k)]
}

const KINDS = ['single', 'crossed', 'nested', 'binary', 'ringed', 'dashed', 'swarm'] as const
export type OrbitarKind = (typeof KINDS)[number]

/** The design parameters for a handle. Exported for tests and the brand page. */
export function orbitarOf(handle: string) {
  const rnd = seeded(fnv(handle))
  const r = (a: number, b: number) => a + rnd() * (b - a)
  const kind = KINDS[Math.floor(rnd() * KINDS.length)]
  const tilt = r(-70, 70)
  return { kind, tilt, r, rnd, hue: hue(handle) }
}

export function Orbitar({ handle, size = 36, className }: { handle: string; size?: number; className?: string }) {
  const clip = useId()
  const { kind, tilt, r, rnd, hue: h } = orbitarOf(handle)
  const ink = `color-mix(in oklab, oklch(0.48 0.15 ${h}) 78%, var(--ink))`
  const tint = `oklch(0.64 0.17 ${h})`
  const tile = `color-mix(in oklab, oklch(0.72 0.12 ${h}) 22%, var(--surface))`
  const sw = size < 28 ? 1.7 : 1.3
  const ring = (o: Orbit, i: number) => (
    <ellipse key={`o${i}`} cx="16" cy="16" rx={o.rx} ry={o.ry} transform={`rotate(${o.tilt} 16 16)`} stroke={ink} strokeWidth={sw} strokeDasharray={o.dash ? '1.6 2.2' : undefined} opacity={0.85} />
  )
  const body = (x: number, y: number, rad: number, style: 'ink' | 'tint' | 'hollow', i: number) => (
    <circle key={`b${i}`} cx={x} cy={y} r={rad} fill={style === 'hollow' ? tile : style === 'tint' ? tint : ink} stroke={style === 'hollow' ? ink : tile} strokeWidth={style === 'hollow' ? 1.1 : 1.3} />
  )
  const parts: React.ReactNode[] = []

  if (kind === 'single' || kind === 'dashed') {
    const o = { rx: r(10.5, 13), ry: r(3.5, 8), tilt, dash: kind === 'dashed' }
    parts.push(ring(o, 0))
    parts.push(kind === 'dashed' ? body(16, 16, r(3, 4.2), 'hollow', 0) : body(16, 16, r(2.6, 4.2), 'ink', 0))
    const n = 1 + Math.floor(rnd() * 3)
    const a0 = r(0, 360)
    for (let i = 0; i < n; i++) {
      const [x, y] = pt(o, a0 + i * r(50, 130))
      parts.push(body(x, y, r(1.6, 2.8), i === 0 ? 'tint' : rnd() > 0.5 ? 'hollow' : 'ink', i + 1))
    }
  } else if (kind === 'crossed') {
    const a = { rx: r(11, 13), ry: r(3.5, 5.5), tilt }
    const b = { rx: r(10, 12.5), ry: r(3.5, 5.5), tilt: tilt + r(55, 95) }
    parts.push(ring(a, 0), ring(b, 1), body(16, 16, r(2.4, 3.4), 'ink', 0))
    const [x1, y1] = pt(a, r(0, 360))
    const [x2, y2] = pt(b, r(0, 360))
    parts.push(body(x1, y1, r(1.8, 2.6), 'tint', 1), body(x2, y2, r(1.4, 2.2), 'ink', 2))
  } else if (kind === 'nested') {
    const n = 2 + Math.floor(rnd() * 2)
    const flat = r(0.3, 0.6)
    const orbits = Array.from({ length: n }, (_, i) => {
      const rx = 13 - i * (n === 3 ? 3.4 : 4.6)
      return { rx, ry: rx * flat, tilt }
    })
    orbits.forEach((o, i) => parts.push(ring(o, i)))
    parts.push(body(16, 16, r(1.8, 2.6), 'ink', 0))
    orbits.forEach((o, i) => {
      const [x, y] = pt(o, r(0, 360))
      parts.push(body(x, y, i === 0 ? 2.4 : r(1.4, 2), i === 0 ? 'tint' : 'ink', i + 1))
    })
  } else if (kind === 'binary') {
    const o = { rx: r(8, 11.5), ry: r(3, 6), tilt }
    const a = r(0, 360)
    const [x1, y1] = pt(o, a)
    const [x2, y2] = pt(o, a + 180)
    const big = r(3, 4.2)
    parts.push(ring(o, 0), body(16, 16, 0.9, 'ink', 0), body(x1, y1, big, 'ink', 1), body(x2, y2, big * r(0.55, 0.85), 'tint', 2))
  } else if (kind === 'ringed') {
    const o = { rx: r(11, 13.5), ry: r(2.2, 3.6), tilt: tilt * 0.5 }
    const planet = r(5, 6.5)
    // back half of the ring, the planet, then the front half over it
    parts.push(
      <g key="back" clipPath={`url(#${clip}b)`}>{ring(o, 0)}</g>,
      body(16, 16, planet, 'tint', 0),
      <g key="front" clipPath={`url(#${clip}f)`}>{ring(o, 1)}</g>,
    )
    if (rnd() > 0.35) {
      const [x, y] = pt({ rx: 13.2, ry: 13.2, tilt: 0 }, r(200, 340))
      parts.push(body(x, y, r(1.4, 2), 'ink', 1))
    }
  } else {
    // swarm: a sun with a scatter of small bodies on one orbit, like a belt
    const o = { rx: r(11, 13), ry: r(4, 7.5), tilt }
    parts.push(ring(o, 0), body(16, 16, r(3, 4), 'ink', 0))
    const a0 = r(0, 360)
    const n = 4 + Math.floor(rnd() * 3)
    for (let i = 0; i < n; i++) {
      const [x, y] = pt(o, a0 + i * r(18, 34))
      parts.push(body(x, y, i === 0 ? 2.2 : r(0.9, 1.4), i === 0 ? 'tint' : 'ink', i + 1))
    }
  }

  const ringTilt = kind === 'ringed' ? tilt * 0.5 : 0
  return (
    <span className={cn('flex shrink-0 items-center justify-center overflow-hidden rounded-xl', className)} style={{ width: size, height: size, background: tile }}>
      <svg width={size} height={size} viewBox="-2.5 -2.5 37 37" fill="none" aria-hidden>
        {kind === 'ringed' && (
          <defs>
            <clipPath id={`${clip}b`}>
              <rect x="-20" y="-20" width="72" height="36" transform={`rotate(${ringTilt} 16 16)`} />
            </clipPath>
            <clipPath id={`${clip}f`}>
              <rect x="-20" y="16" width="72" height="36" transform={`rotate(${ringTilt} 16 16)`} />
            </clipPath>
          </defs>
        )}
        {parts}
      </svg>
    </span>
  )
}

export const displayName = (handle: string) => (handle === OUTSIDE ? 'Outside clients' : `@${handle}`)
