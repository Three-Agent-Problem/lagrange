// Where a run's time went: working, waiting on someone, or queued for an agent that was
// offline or busy. Pure, from the run's activity entries, so it is tested without a browser.
import type { Entry } from '@/lib/api'
import { isActive, isWaiting, normState, OUTSIDE, type State } from '@/store/model'

export type Kind = 'working' | 'waiting' | 'queued'

/** A stretch of the run spent one way; who is set for waiting. */
export type Segment = { kind: Kind; who?: string; from: number; to: number }

export type Split = {
  start: number
  end: number
  segments: Segment[]
  working: number
  queued: number
  /** Time waiting, per address, longest first. */
  waiting: { who: string; ms: number }[]
}

type Status = { state: State; awaiting?: string; sender: string; queued: boolean }

/**
 * Splits a run's time. At any moment, a task waiting on someone wins (the run can't move
 * until they answer), then a task queued for its agent, then any task still active.
 * Stretches where nothing is active (the run ended) don't count. now is the end of a
 * run still going.
 */
export function timeSplit(entries: Entry[], now: number): Split {
  const sorted = entries.filter((e) => e.taskId && (e.kind === 'task' || e.kind === 'queued' || e.kind === 'delivered')).sort((a, b) => a.seq - b.seq)
  const tasks = new Map<string, Status>()
  const segments: Segment[] = []
  const start = sorted.length ? Date.parse(sorted[0].at) : now

  const classify = (): Omit<Segment, 'from' | 'to'> | undefined => {
    for (const t of tasks.values()) if (isWaiting(t.state)) return { kind: 'waiting', who: t.awaiting || t.sender }
    for (const t of tasks.values()) if (t.queued && isActive(t.state)) return { kind: 'queued' }
    for (const t of tasks.values()) if (isActive(t.state)) return { kind: 'working' }
    return undefined
  }
  const add = (from: number, to: number) => {
    const c = classify()
    if (!c || to <= from) return
    const last = segments.at(-1)
    if (last && last.kind === c.kind && last.who === c.who && last.to === from) last.to = to
    else segments.push({ ...c, from, to })
  }

  let at = start
  for (const e of sorted) {
    const t = Date.parse(e.at)
    add(at, t)
    at = Math.max(at, t)
    const s = tasks.get(e.taskId!) ?? { state: 'submitted' as State, sender: e.sender || OUTSIDE, queued: false }
    if (e.kind === 'task') {
      const state = normState(e.state)
      if (state !== 'unknown') s.state = state
      s.awaiting = typeof e.data?.awaiting === 'string' ? e.data.awaiting : isWaiting(s.state) ? s.awaiting : undefined
      if (!isActive(s.state)) s.queued = false
    } else s.queued = e.kind === 'queued'
    tasks.set(e.taskId!, s)
  }
  if (classify()) add(at, Math.max(at, now))
  const end = segments.at(-1)?.to ?? at

  const sum = (k: Kind) => segments.filter((s) => s.kind === k).reduce((n, s) => n + s.to - s.from, 0)
  const waits = new Map<string, number>()
  for (const s of segments) if (s.kind === 'waiting') waits.set(s.who!, (waits.get(s.who!) ?? 0) + s.to - s.from)
  return {
    start,
    end,
    segments,
    working: sum('working'),
    queued: sum('queued'),
    waiting: [...waits].map(([who, ms]) => ({ who, ms })).sort((a, b) => b.ms - a.ms),
  }
}
