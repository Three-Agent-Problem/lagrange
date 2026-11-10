// The live model of the platform, built by folding activity entries. The live graph
// (snapshot + SSE) and the replay scrubber use the same reducer, so a replay looks
// exactly like it did live.
import type { Entry, Graph, Stats, TaskView } from '@/lib/api'

/** Lower-case A2A state without the TASK_STATE_ prefix: "working", "input_required", … */
export type State = 'submitted' | 'working' | 'input_required' | 'auth_required' | 'completed' | 'failed' | 'canceled' | 'rejected' | 'unknown'

export const OUTSIDE = '~outside'

export function normState(s: string | undefined): State {
  const v = (s ?? '').replace(/^TASK_STATE_/, '').toLowerCase()
  switch (v) {
    case 'submitted':
    case 'working':
    case 'input_required':
    case 'auth_required':
    case 'completed':
    case 'failed':
    case 'canceled':
    case 'rejected':
      return v
    default:
      return 'unknown'
  }
}

export const isTerminal = (s: State) => s === 'completed' || s === 'failed' || s === 'canceled' || s === 'rejected'
export const isWaiting = (s: State) => s === 'input_required' || s === 'auth_required'
export const isActive = (s: State) => s === 'submitted' || s === 'working'

export type TaskNode = {
  id: string
  contextId?: string
  sender: string
  recipient: string
  parent?: string
  state: State
  /** What was asked: the first message. */
  summary: string
  /** The latest status text: a question, a progress note, an answer. */
  last: string
  awaiting?: string
  queued: boolean
  artifacts: number
  createdAt: number
  updatedAt: number
  /** When each state began, in order, for time-in-state bars. */
  states: { state: State; at: number }[]
}

export type Model = {
  seq: number
  /** Time of the newest entry folded in (wall clock live, replay clock in replays). */
  now: number
  tasks: Record<string, TaskNode>
  presence: Record<string, Stats>
  /** Bumped when addresses are created or removed, so views refetch them. */
  addressVersion: number
  /** Newest last; capped. */
  feed: Entry[]
  /** Direct message answers (no task): sender→recipient pair to time, for a brief flash. */
  replies: Record<string, number>
}

export const FEED_MAX = 120

export function emptyModel(): Model {
  return { seq: 0, now: 0, tasks: {}, presence: {}, addressVersion: 0, feed: [], replies: {} }
}

const ms = (iso: string | undefined) => (iso ? Date.parse(iso) : 0)

/** The model the live graph starts from: the server's snapshot. */
export function fromSnapshot(g: Graph): Model {
  const m = emptyModel()
  m.seq = g.seq
  m.now = Date.now()
  for (const a of g.addresses) {
    if (a.kind === 'agent') m.presence[a.handle] = a.stats
  }
  for (const t of g.tasks) m.tasks[t.id] = fromView(t)
  return m
}

export function fromView(t: TaskView): TaskNode {
  const state = normState(t.state)
  return {
    id: t.id,
    contextId: t.contextId,
    sender: t.sender || OUTSIDE,
    recipient: t.recipient,
    parent: t.parentTaskId || undefined,
    state,
    summary: t.summary,
    last: '',
    awaiting: t.awaiting,
    queued: false,
    artifacts: t.task?.artifacts?.length ?? 0,
    createdAt: ms(t.createdAt),
    updatedAt: ms(t.updatedAt),
    states: [{ state, at: ms(t.updatedAt) }],
  }
}

/** Folds one entry into the model. It returns a new model and never mutates its input. */
export function reduce(m: Model, e: Entry): Model {
  if (e.seq <= m.seq) return m
  // Usage reports are bookkeeping, not traffic: they stay off the graph and the feed.
  if (e.kind === 'usage') return { ...m, seq: e.seq }
  const at = ms(e.at)
  const next: Model = { ...m, seq: e.seq, now: Math.max(m.now, at), feed: [...m.feed, e].slice(-FEED_MAX) }
  switch (e.kind) {
    case 'presence': {
      if (!e.recipient) return next
      const d = e.data ?? {}
      next.presence = {
        ...m.presence,
        [e.recipient]: { sessions: num(d.sessions), capacity: num(d.capacity), inFlight: num(d.inFlight), waiting: num(d.waiting) },
      }
      return next
    }
    case 'address':
      next.addressVersion = m.addressVersion + 1
      return next
    case 'message':
      // An agent answered with a message and no task: a quick round trip.
      next.replies = { ...m.replies, [`${e.sender || OUTSIDE}>${e.recipient}`]: at }
      return next
  }
  if (!e.taskId) return next
  const prev = m.tasks[e.taskId]
  const t: TaskNode = prev
    ? { ...prev }
    : {
        id: e.taskId,
        contextId: e.contextId,
        sender: e.sender || OUTSIDE,
        recipient: e.recipient ?? '',
        parent: e.parentTaskId || undefined,
        state: 'submitted',
        summary: e.summary ?? '',
        last: '',
        queued: false,
        artifacts: 0,
        createdAt: at,
        updatedAt: at,
        states: [],
      }
  t.updatedAt = at
  if (e.parentTaskId) t.parent = e.parentTaskId
  switch (e.kind) {
    case 'task': {
      const state = normState(e.state)
      if (state !== 'unknown' && state !== t.state) {
        t.state = state
        t.states = [...t.states, { state, at }]
      } else if (t.states.length === 0) {
        t.states = [{ state: t.state, at }]
      }
      if (prev && e.summary) t.last = e.summary
      t.awaiting = typeof e.data?.awaiting === 'string' ? e.data.awaiting : isWaiting(t.state) ? t.awaiting : undefined
      if (!isActive(t.state)) t.queued = false
      break
    }
    case 'queued':
      t.queued = true
      break
    case 'delivered':
      t.queued = false
      break
    case 'artifact':
      t.artifacts = t.artifacts + 1
      break
  }
  next.tasks = { ...m.tasks, [t.id]: t }
  return next
}

const num = (v: unknown) => (typeof v === 'number' ? v : 0)

export function fold(entries: Entry[], from: Model = emptyModel()): Model {
  return entries.reduce(reduce, from)
}

/** A sender→recipient pair with the tasks between them. */
export type Flow = {
  id: string
  source: string
  target: string
  active: number
  waiting: number
  queued: number
  done: number
  failed: number
  total: number
  /** The newest task on this flow. */
  latest: TaskNode
  /** Tasks that started or finished in the last moments, for comet and flash effects. */
  startedAt: number
  finishedAt: number
  finishedState?: State
  /** A skill-like label: the newest summary, shortened. */
  label: string
}

export function flows(m: Model): Flow[] {
  const byPair = new Map<string, Flow>()
  for (const t of Object.values(m.tasks)) {
    if (!t.recipient) continue
    const id = `${t.sender}>${t.recipient}`
    let f = byPair.get(id)
    if (!f) {
      f = { id, source: t.sender, target: t.recipient, active: 0, waiting: 0, queued: 0, done: 0, failed: 0, total: 0, latest: t, startedAt: 0, finishedAt: 0, label: '' }
      byPair.set(id, f)
    }
    f.total++
    if (isActive(t.state)) f.active++
    if (isWaiting(t.state)) f.waiting++
    if (t.queued) f.queued++
    if (t.state === 'completed') f.done++
    if (t.state === 'failed' || t.state === 'rejected') f.failed++
    if (t.updatedAt >= f.latest.updatedAt) f.latest = t
    f.startedAt = Math.max(f.startedAt, t.createdAt)
    if (isTerminal(t.state) && t.updatedAt >= f.finishedAt) {
      f.finishedAt = t.updatedAt
      f.finishedState = t.state
    }
  }
  for (const f of byPair.values()) f.label = shorten(f.latest.summary, 28)
  for (const [pair, at] of Object.entries(m.replies)) {
    if (byPair.has(pair)) continue
    const [source, target] = pair.split('>')
    const latest: TaskNode = { id: pair, sender: source, recipient: target, state: 'completed', summary: '', last: '', queued: false, artifacts: 0, createdAt: at, updatedAt: at, states: [] }
    byPair.set(pair, { id: pair, source, target, active: 0, waiting: 0, queued: 0, done: 1, failed: 0, total: 1, latest, startedAt: at, finishedAt: at, finishedState: 'completed', label: 'message' })
  }
  return [...byPair.values()]
}

/** What an address is doing right now, for its node. */
export type Activity = {
  /** Tasks it is working on. */
  working: number
  /** Tasks it sent that wait for someone. */
  blocked: number
  /** Tasks addressed to it that wait for an answer (for people: their inbox). */
  inbox: TaskNode[]
  /** Its own questions back to the sender. */
  asking: TaskNode[]
  queued: number
  failed: number
}

export function activityOf(m: Model, handle: string): Activity {
  const a: Activity = { working: 0, blocked: 0, inbox: [], asking: [], queued: 0, failed: 0 }
  for (const t of Object.values(m.tasks)) {
    if (t.recipient === handle) {
      if (isActive(t.state)) {
        if (t.queued) a.queued++
        else a.working++
      }
      if (isWaiting(t.state)) {
        if (t.awaiting === handle) a.inbox.push(t)
        else a.asking.push(t)
      }
      if (t.state === 'failed' && m.now - t.updatedAt < 60_000) a.failed++
    }
    if (t.sender === handle && isWaiting(t.state)) a.blocked++
  }
  a.inbox.sort((x, y) => y.updatedAt - x.updatedAt)
  a.asking.sort((x, y) => y.updatedAt - x.updatedAt)
  return a
}

/** Tasks that wait for handle to act: addressed to them as a person, or their own tasks
 * whose agent asked a question. */
export function waitingFor(m: Model, handle: string): TaskNode[] {
  return Object.values(m.tasks)
    .filter((t) => isWaiting(t.state) && (t.awaiting === handle || (t.sender === handle && !t.awaiting)))
    .sort((x, y) => y.updatedAt - x.updatedAt)
}

/** The task and everything delegated from it, root first. */
export function tree(m: Model, rootId: string): TaskNode[] {
  const out: TaskNode[] = []
  const children = new Map<string, TaskNode[]>()
  for (const t of Object.values(m.tasks)) {
    if (!t.parent) continue
    children.set(t.parent, [...(children.get(t.parent) ?? []), t])
  }
  const walk = (id: string, depth: number) => {
    const t = m.tasks[id]
    if (!t || depth > 8 || out.includes(t)) return
    out.push(t)
    for (const c of (children.get(id) ?? []).sort((x, y) => x.createdAt - y.createdAt)) walk(c.id, depth + 1)
  }
  walk(rootId, 0)
  return out
}

/** Time spent in each state, until now for the current one. */
export function timeInStates(t: TaskNode, now: number): { state: State; ms: number }[] {
  return t.states.map((s, i) => ({ state: s.state, ms: Math.max(0, (t.states[i + 1]?.at ?? (isTerminal(t.state) ? t.updatedAt : now)) - s.at) }))
}

export function shorten(s: string, n: number): string {
  const one = s.replace(/\s+/g, ' ').trim()
  return one.length > n ? `${one.slice(0, n - 1)}…` : one
}
