import type { Tokens } from '@/lib/api'

/** A count, short: 950, 1.5k, 28k, 1.2M (as the Go side writes it). */
export function count(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, '')}k`
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`
  return `${(n / 1e6).toFixed(1).replace(/\.0$/, '')}M`
}

/** "12 in · 340 out · 28k cache read · 1.5k cache write", leaving out zeros. */
export function tokensText(t?: Tokens): string {
  if (!t) return ''
  const parts: [number | undefined, string][] = [
    [t.input, 'in'],
    [t.output, 'out'],
    [t.cacheRead, 'cache read'],
    [t.cacheWrite, 'cache write'],
  ]
  return parts
    .filter(([n]) => n)
    .map(([n, label]) => `${count(n!)} ${label}`)
    .join(' · ')
}
