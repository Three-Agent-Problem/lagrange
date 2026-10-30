import { Bot, Globe } from 'lucide-react'
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
  return (
    <span
      className={cn('flex shrink-0 items-center justify-center rounded-xl', className)}
      style={{ ...style, background: `oklch(0.93 0.05 ${h})`, color: `oklch(0.45 0.15 ${h})` }}
    >
      <Bot size={size * 0.55} strokeWidth={2.2} />
    </span>
  )
}

export const displayName = (handle: string) => (handle === OUTSIDE ? 'Outside clients' : `@${handle}`)
