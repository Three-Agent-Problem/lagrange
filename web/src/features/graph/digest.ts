// The live feed in words: runs summed up ("Ada ordered 200 hoodies → approved, shipped"),
// a digest of the last hour, and which run an event belongs to. Pure, so it is tested
// without a browser.
import type { Entry, Run } from '@/lib/api'
import { normState, type TaskNode } from '@/store/model'

export type Outcome = 'done' | 'failed' | 'waiting' | 'running'

export function outcomeOf(r: Run): Outcome {
  if (r.state === 'message') return 'done'
  const s = normState(r.state)
  if (s === 'completed') return 'done'
  if (s === 'failed' || s === 'rejected' || s === 'canceled') return 'failed'
  if (r.waiting.length || s === 'input_required' || s === 'auth_required') return 'waiting'
  return 'running'
}

export type Digest = { done: number; failed: number; waiting: number; running: number; waitingOn: { who: string; n: number }[] }

/** What happened to the runs touched since `since`, and who the waiting ones wait on. */
export function digest(runs: Run[], since: number): Digest {
  const d: Digest = { done: 0, failed: 0, waiting: 0, running: 0, waitingOn: [] }
  const on = new Map<string, number>()
  for (const r of runs) {
    const o = outcomeOf(r)
    // Waiting and running runs count however old they are: they are still open.
    if ((o === 'done' || o === 'failed') && Date.parse(r.updatedAt) < since) continue
    d[o]++
    if (o === 'waiting') for (const w of r.waiting) on.set(w, (on.get(w) ?? 0) + 1)
  }
  d.waitingOn = [...on].map(([who, n]) => ({ who, n })).sort((a, b) => b.n - a.n)
  return d
}

/** The digest as one sentence: "3 done, 1 failed · 2 waiting (Ada 2) · 1 in progress". */
export function digestLine(d: Digest, name: (h: string) => string): string {
  const parts: string[] = []
  const ended = [d.done && `${d.done} done`, d.failed && `${d.failed} failed`].filter(Boolean)
  if (ended.length) parts.push(ended.join(', '))
  if (d.waiting) parts.push(`${d.waiting} waiting (${d.waitingOn.map((w) => `${name(w.who)}${w.n > 1 ? ` ${w.n}` : ''}`).join(', ')})`)
  if (d.running) parts.push(`${d.running} in progress`)
  return parts.length ? parts.join(' · ') : 'Quiet: nothing happened.'
}

/** A run in one line: who asked whom, and what came of it. */
export function headline(r: Run, name: (h: string) => string, now: number): { who: string; result: string } {
  const who = `${name(r.starter)} → ${name(r.recipient)}`
  const o = outcomeOf(r)
  const out = clean(r.outcome)
  const s = normState(r.state)
  switch (o) {
    case 'done':
      return { who, result: out || 'Done.' }
    case 'failed':
      return { who, result: `${s === 'rejected' ? 'Rejected' : s === 'canceled' ? 'Canceled' : 'Failed'}${out ? `: ${out}` : '.'}` }
    case 'waiting': {
      const on = r.waiting.map(name).join(' and ') || 'someone'
      const since = r.waitingSince ? ` for ${short(now - Date.parse(r.waitingSince))}` : ''
      return { who, result: `Waiting on ${on}${since}.` }
    }
    default:
      return { who, result: r.tasks > 1 ? `In progress: ${r.tasks - 1} hand-off${r.tasks > 2 ? 's' : ''} so far.` : 'In progress.' }
  }
}

/** The run an event belongs to: its task's root, walking up parents the live model knows. */
export function runOf(e: Entry, tasks: Record<string, TaskNode>): string | undefined {
  let id: string | undefined = e.taskId || e.parentTaskId
  if (!id) return e.kind === 'message' ? `msg-${e.seq}` : undefined
  for (let i = 0; i < 10; i++) {
    const parent: string | undefined = tasks[id]?.parent
    if (!parent) break
    id = parent
  }
  return id
}

/** Outcome text without JSON noise: an object becomes "key: value, …". */
function clean(s: string | undefined): string {
  const t = (s ?? '').trim()
  if (!t.startsWith('{')) return t
  try {
    const o = JSON.parse(t) as Record<string, unknown>
    return Object.entries(o)
      .filter(([, v]) => v !== null && typeof v !== 'object')
      .map(([k, v]) => `${k}: ${v}`)
      .join(', ')
  } catch {
    return t
  }
}

function short(ms: number): string {
  if (ms < 60_000) return `${Math.max(1, Math.round(ms / 1000))}s`
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h`
  return `${Math.floor(ms / 86_400_000)}d`
}
