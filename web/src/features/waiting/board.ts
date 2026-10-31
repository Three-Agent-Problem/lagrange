// The waiting board's arithmetic: group waits by who blocks them, and turn the policy's
// seconds into "2 hours". Pure, so it is tested without a browser.
import type { Wait } from '@/lib/api'

export type Group = { who: string; person: boolean; waits: Wait[]; oldest: number }

/** Waits grouped by who they wait on, the longest-blocked person first; waits oldest first. */
export function groupWaits(waits: Wait[]): Group[] {
  const by = new Map<string, Group>()
  for (const w of waits) {
    const since = Date.parse(w.since)
    const g = by.get(w.who) ?? { who: w.who, person: w.person, waits: [], oldest: since }
    g.waits.push(w)
    g.oldest = Math.min(g.oldest, since)
    by.set(w.who, g)
  }
  for (const g of by.values()) g.waits.sort((a, b) => Date.parse(a.since) - Date.parse(b.since))
  return [...by.values()].sort((a, b) => a.oldest - b.oldest)
}

/** How late a wait is: fine, past its first reminder, or past escalation or cancel. */
export function urgency(w: Wait, now: number): 'ok' | 'late' | 'overdue' {
  const past = (t?: string) => !!t && Date.parse(t) <= now
  if (w.escalatedAt || past(w.escalateAt) || past(w.cancelAt)) return 'overdue'
  if (w.reminders > 0 || past(w.remindAt)) return 'late'
  return 'ok'
}

export const UNITS = [
  { id: 'm', label: 'minutes', s: 60 },
  { id: 'h', label: 'hours', s: 3600 },
  { id: 'd', label: 'days', s: 86400 },
] as const
export type Unit = (typeof UNITS)[number]['id']

/** Seconds as the largest whole unit: 7200 → 2 hours. */
export function toUnit(s: number): { n: number; unit: Unit } {
  for (const u of [...UNITS].reverse()) if (s % u.s === 0) return { n: s / u.s, unit: u.id }
  return { n: Math.max(1, Math.round(s / 60)), unit: 'm' }
}

export const fromUnit = (n: number, unit: Unit) => n * UNITS.find((u) => u.id === unit)!.s

/** A delay in words: "30 minutes", "1 hour", "2 days". */
export function words(s: number): string {
  const { n, unit } = toUnit(s)
  const label = UNITS.find((u) => u.id === unit)!.label
  return `${n} ${n === 1 ? label.slice(0, -1) : label}`
}

/** Time left until t, in words ("in 12m"), or "due" once it is past (the server acts within a tick). */
export function until(t: string, now: number): string {
  const ms = Date.parse(t) - now
  if (ms <= 0) return 'due'
  if (ms < 3_600_000) return `in ${Math.max(1, Math.round(ms / 60_000))}m`
  if (ms < 86_400_000) return `in ${Math.round(ms / 3_600_000)}h`
  return `in ${Math.round(ms / 86_400_000)}d`
}

/** How long something has waited: "45s", "12m", "3h", "2d". */
export function waited(since: string, now: number): string {
  const ms = Math.max(0, now - Date.parse(since))
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h`
  return `${Math.floor(ms / 86_400_000)}d`
}
