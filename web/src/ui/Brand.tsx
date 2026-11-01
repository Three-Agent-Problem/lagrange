import { useAnimationFrame, useReducedMotion } from 'motion/react'
import { useRef, useState } from 'react'
import { cn } from '@/lib/cn'

/*
 * The Lagrange mark: a tilted orbit, the sun, a planet, and the planet's two Lagrange
 * points, L4 (lit) 60° ahead and L5 (hollow) 60° behind. The orbit is a circle seen at
 * an angle, so angles on the ellipse are true projected positions.
 *
 * Animated, the planet and both points travel together, because that is what Lagrange
 * points do: they keep their place relative to the planet as it goes round.
 */
const RX = 13.6
const RY = 6.6
const TILT = (-28 * Math.PI) / 180
const PLANET = -122 // resting angle, degrees
const L4 = PLANET + 60
const L5 = PLANET - 60

function at(deg: number): [x: number, y: number, depth: number] {
  const t = (deg * Math.PI) / 180
  const x = RX * Math.cos(t)
  const y = RY * Math.sin(t)
  // depth: +1 nearest the viewer (bottom of the orbit), -1 behind the sun
  return [16 + x * Math.cos(TILT) - y * Math.sin(TILT), 16 + x * Math.sin(TILT) + y * Math.cos(TILT), Math.sin(t)]
}

export type MarkMotion = 'orbit' | 'idle' | 'settle'

/** Degrees the whole system has turned at `ms`; null once it has come to rest. */
function phaseAt(motion: MarkMotion, ms: number): number {
  if (motion === 'orbit') return (ms / 2600) * 360
  if (motion === 'idle') return (ms / 18000) * 360
  const p = Math.min(ms / 1500, 1)
  return -360 * (1 - p) ** 3 // one turn, easing into its resting place
}

function Drawing({ size, phase, hot, className }: { size: number; phase: number; hot?: boolean; className?: string }) {
  const small = size < 26
  const bodies = [
    { key: 'planet', a: PLANET + phase, r: small ? 2.6 : 2.1 },
    { key: 'l4', a: L4 + phase, r: small ? 3.4 : 2.8 },
    ...(small ? [] : [{ key: 'l5', a: L5 + phase, r: 1.9 }]),
  ].map((b) => {
    const [x, y, depth] = at(b.a)
    return { ...b, x, y, depth, r: b.r * (1 + 0.14 * depth) }
  })
  const draw = (b: (typeof bodies)[number]) =>
    b.key === 'l4' ? (
      <g key={b.key}>
        {hot && (
          <circle cx={b.x} cy={b.y} r={b.r} fill="none" stroke="var(--accent)" strokeWidth="1.2">
            <animate attributeName="r" values={`${b.r};${b.r + 5}`} dur="1.8s" repeatCount="indefinite" />
            <animate attributeName="opacity" values="0.8;0" dur="1.8s" repeatCount="indefinite" />
          </circle>
        )}
        <circle cx={b.x} cy={b.y} r={b.r} fill="var(--accent)" stroke="var(--brand-bg, var(--bg))" strokeWidth="1.6" />
      </g>
    ) : b.key === 'planet' ? (
      <circle key={b.key} cx={b.x} cy={b.y} r={b.r} fill="currentColor" stroke="var(--brand-bg, var(--bg))" strokeWidth="1.4" />
    ) : (
      <circle key={b.key} cx={b.x} cy={b.y} r={b.r} fill="var(--brand-bg, var(--bg))" stroke="currentColor" strokeWidth="1.2" />
    )
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" fill="none" aria-hidden className={cn('shrink-0 overflow-visible text-ink', className)}>
      <ellipse cx="16" cy="16" rx={RX} ry={RY} transform="rotate(-28 16 16)" stroke="currentColor" strokeWidth={small ? 2.2 : 1.4} />
      {bodies.filter((b) => b.depth < 0).map(draw)}
      <circle cx="16" cy="16" r={small ? 3.8 : 3.2} fill="currentColor" />
      {bodies.filter((b) => b.depth >= 0).map(draw)}
    </svg>
  )
}

function Moving({ size, motion, hot, className }: { size: number; motion: MarkMotion; hot?: boolean; className?: string }) {
  const [phase, setPhase] = useState(motion === 'settle' ? -360 : 0)
  const done = useRef(false)
  useAnimationFrame((ms) => {
    if (done.current) return
    const p = phaseAt(motion, ms)
    if (motion === 'settle' && p === 0) done.current = true
    setPhase(p)
  })
  return <Drawing size={size} phase={phase} hot={hot} className={className} />
}

/**
 * The mark. `motion`: "orbit" while something loads, "idle" for a slow drift on quiet
 * pages, "settle" to swing once into place. `hot` pulses L4: something needs you.
 * Respects reduced motion.
 */
export function Mark({ size = 32, motion, hot, className }: { size?: number; motion?: MarkMotion; hot?: boolean; className?: string }) {
  const still = useReducedMotion()
  if (!motion || still) return <Drawing size={size} phase={0} hot={hot && !still} className={className} />
  return <Moving size={size} motion={motion} hot={hot} className={className} />
}

/** The mark that swings once round its orbit each time the pointer comes over it. */
export function HoverMark({ size = 32, hot, className }: { size?: number; hot?: boolean; className?: string }) {
  const [turn, setTurn] = useState(0)
  return (
    <span onMouseEnter={() => setTurn((n) => n + 1)} className="inline-flex">
      {turn ? <Mark key={turn} size={size} motion="settle" hot={hot} className={className} /> : <Mark size={size} hot={hot} className={className} />}
    </span>
  )
}

export function Wordmark({ className }: { className?: string }) {
  return <span className={cn('font-semibold tracking-[-0.04em] text-ink', className)}>lagrange</span>
}

/** Mark + wordmark. `size` is the wordmark's font size in px. */
export function Lockup({ size = 20, motion, className }: { size?: number; motion?: MarkMotion; className?: string }) {
  return (
    <span className={cn('inline-flex items-center', className)} style={{ gap: size * 0.36, fontSize: size, lineHeight: 1 }}>
      <Mark size={Math.round(size * 1.2)} motion={motion} />
      <Wordmark />
    </span>
  )
}

/** The loading state: the system orbiting, with a line saying what it waits for. */
export function Orbiting({ label, size = 28, className }: { label?: string; size?: number; className?: string }) {
  return (
    <div role="status" className={cn('flex flex-col items-center justify-center gap-3 text-sm text-muted', className)}>
      <Mark size={size} motion="orbit" />
      {label && <span>{label}</span>}
    </div>
  )
}
