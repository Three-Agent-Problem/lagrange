// The story of a run: its activity entries and HTTP exchanges turned into numbered steps
// a person can follow ("@acme-buyer asked @contoso for a quote… it quoted 4560 EUR").
// Pure, so the runs page, its replay and the tests all read the same steps.
import type { Entry, Exchange, RunDetail } from '@/lib/api'
import { isTerminal, normState, OUTSIDE, type State } from '@/store/model'

export type StepKind = 'ask' | 'message' | 'progress' | 'artifact' | 'wait' | 'answer' | 'queued' | 'push' | 'done' | 'failed'

/** A payload rendered for people: plain text, or the fields of a JSON object. */
export type Content = { text?: string; fields?: [string, string][] }

export type Step = {
  /** 1-based, as shown. */
  n: number
  at: number
  kind: StepKind
  /** The lane the step starts on, and where its arrow goes (none: it happens inside `from`). */
  from: string
  to?: string
  taskId?: string
  state?: State
  title: string
  body?: Content
  /** A plain message's answer, which comes straight back. */
  reply?: Content
  /** Later chunks folded into this step (artifact updates). */
  updates: Content[]
  /** Webhooks the platform delivered for this step. */
  pushes: { to: string; at: number; exchange: Exchange }[]
  entries: Entry[]
  exchanges: Exchange[]
}

export type Story = {
  steps: Step[]
  /** Participants in order of first appearance: the diagram's lanes. */
  lanes: string[]
  /** Exchanges no step explains (kept for the full traffic list). */
  loose: Exchange[]
}

const ms = (iso: string) => Date.parse(iso)

/** How a participant is called in a step: "Ada" for a person, "@acme-buyer" for an agent. */
export type Namer = (handle: string) => string

export const defaultNamer: Namer = (h) => (!h || h === OUTSIDE ? 'An outside client' : `@${h}`)

let who: Namer = defaultNamer

/** Renders a payload: a JSON object as fields, anything else as text. */
export function content(s: string | undefined): Content | undefined {
  const t = (s ?? '').trim()
  if (!t) return undefined
  const lines = t.split('\n')
  const fields: [string, string][] = []
  const text: string[] = []
  for (const line of lines) {
    const obj = parseObject(line)
    if (obj) {
      for (const [k, v] of Object.entries(obj)) fields.push([k, typeof v === 'string' ? v : JSON.stringify(v)])
    } else if (line.trim()) {
      text.push(line.trim())
    }
  }
  return { text: text.length ? text.join('\n') : undefined, fields: fields.length ? fields : undefined }
}

function parseObject(s: string): Record<string, unknown> | undefined {
  const t = s.trim()
  if (!t.startsWith('{') || t.endsWith('…')) return undefined
  try {
    const v = JSON.parse(t)
    return v && typeof v === 'object' && !Array.isArray(v) ? v : undefined
  } catch {
    return undefined
  }
}

const str = (v: unknown) => (typeof v === 'string' ? v : undefined)

export function story(run: Pick<RunDetail, 'entries' | 'exchanges'> & { tasks?: RunDetail['tasks'] }, name: Namer = defaultNamer): Story {
  who = (h) => (!h || h === OUTSIDE ? defaultNamer(h) : name(h))
  try {
    return tell(run)
  } finally {
    who = defaultNamer
  }
}

function tell(run: Pick<RunDetail, 'entries' | 'exchanges'> & { tasks?: RunDetail['tasks'] }): Story {
  const entries = [...run.entries].filter((e) => e.kind !== 'presence' && e.kind !== 'address').sort((a, b) => a.seq - b.seq)
  const steps: Step[] = []
  const seen = new Set<string>()
  const artifactStep = new Map<string, Step>()
  const queuedStep = new Map<string, Step>()
  const lastOf = new Map<string, Step>()
  const human = new Set((run.tasks ?? []).filter((t) => t.awaiting && t.awaiting === t.recipient).map((t) => t.recipient))

  const push = (s: Omit<Step, 'n' | 'updates' | 'pushes' | 'exchanges'> & Partial<Pick<Step, 'updates'>>) => {
    const step: Step = { updates: [], pushes: [], exchanges: [], ...s, n: steps.length + 1 }
    steps.push(step)
    if (step.taskId) lastOf.set(step.taskId, step)
    return step
  }

  for (const e of entries) {
    const at = ms(e.at)
    const sender = e.sender || OUTSIDE
    const recipient = e.recipient ?? ''
    const tid = e.taskId
    switch (e.kind) {
      case 'message': {
        push({
          at, kind: 'message', from: sender, to: recipient, title: `${who(sender)} asked ${who(recipient)}`,
          body: content(str(e.data?.request) ?? e.summary), reply: content(str(e.data?.replyContent) ?? str(e.data?.reply)),
          entries: [e],
        })
        continue
      }
      case 'input': {
        const from = str(e.data?.from) || sender
        const step = push({ at, kind: 'answer', from, to: recipient, taskId: tid, title: `${who(from)} answered ${who(recipient)}`, body: content(str(e.data?.content) ?? e.summary), entries: [e] })
        if (from === recipient) step.to = undefined
        continue
      }
      case 'queued': {
        const s = push({ at, kind: 'queued', from: recipient, taskId: tid, title: `Waiting in ${who(recipient)}'s mailbox`, body: content(e.summary), entries: [e] })
        if (tid) queuedStep.set(tid, s)
        continue
      }
      case 'delivered': {
        const q = tid ? queuedStep.get(tid) : undefined
        if (q) {
          q.title = `Waited in ${who(recipient)}'s mailbox for ${seconds(at - q.at)}, then delivered`
          q.entries.push(e)
        }
        continue
      }
      case 'artifact': {
        const id = str(e.data?.artifactId) ?? ''
        const prev = artifactStep.get(id)
        const body = content(str(e.data?.content))
        if (prev && e.data?.append) {
          if (body) prev.updates.push(body)
          prev.entries.push(e)
          continue
        }
        const s = push({ at, kind: 'artifact', from: recipient, taskId: tid, title: `${who(recipient)} produced ${e.summary ? `“${e.summary}”` : 'a result'}`, body, entries: [e] })
        artifactStep.set(id, s)
        continue
      }
      case 'task':
        break
      default:
        continue
    }

    // A task state change.
    const state = normState(e.state)
    const first = !!tid && !seen.has(tid)
    if (tid) seen.add(tid)
    const text = content(e.summary)
    if (first) {
      push({ at, kind: 'ask', from: sender, to: recipient, taskId: tid, state, title: `${who(sender)} asked ${who(recipient)}`, body: text, entries: [e] })
      continue
    }
    if (state === 'input_required' || state === 'auth_required') {
      const awaiting = str(e.data?.awaiting) || sender
      const toPerson = awaiting === recipient || human.has(recipient)
      push({
        at, kind: 'wait', from: recipient, to: toPerson ? undefined : awaiting, taskId: tid, state,
        title: toPerson ? `Waiting for ${who(recipient)} to answer` : state === 'auth_required' ? `${who(recipient)} needs ${who(awaiting)} to sign in` : `${who(recipient)} asks ${who(awaiting)}`,
        body: toPerson ? undefined : text, entries: [e],
      })
      continue
    }
    if (isTerminal(state)) {
      const ok = state === 'completed'
      const verb = { completed: 'finished', failed: 'failed', canceled: 'was canceled', rejected: 'was rejected' }[state as 'completed']
      push({ at, kind: ok ? 'done' : 'failed', from: recipient, to: sender === recipient ? undefined : sender, taskId: tid, state, title: `${who(recipient)} ${verb}`, body: text, entries: [e] })
      continue
    }
    if (text) {
      push({ at, kind: 'progress', from: recipient, taskId: tid, state, title: who(recipient), body: text, entries: [e] })
    } else {
      // A state change with nothing to say belongs to the step before it.
      const last = (tid && lastOf.get(tid)) || steps[steps.length - 1]
      last?.entries.push(e)
    }
  }

  const loose = attach(steps, [...run.exchanges].sort((a, b) => ms(a.at) - ms(b.at) || a.id - b.id))
  const lanes: string[] = []
  for (const s of steps) {
    for (const h of [s.from, s.to, ...s.pushes.map((p) => p.to)]) if (h && !lanes.includes(h)) lanes.push(h)
  }
  return { steps, lanes, loose }
}

/** Gives every exchange to the step that explains it; returns the rest. */
function attach(steps: Step[], exchanges: Exchange[]): Exchange[] {
  const loose: Exchange[] = []
  const byCall = new Map<string, Step>()
  for (const s of steps) {
    const id = s.kind === 'message' ? str(s.entries[0]?.data?.callId) : undefined
    if (id) byCall.set(id, s)
  }
  for (const x of exchanges) {
    const at = ms(x.at)
    let step: Step | undefined
    if (x.taskId && byCall.has(x.taskId)) {
      step = byCall.get(x.taskId)
    } else if (x.taskId) {
      // The newest step of the task that had happened by then (with a little slack: a
      // call is recorded when it starts, its events when they arrive).
      // Calls belong to the step that made them (asking, answering); pushes to the update
      // they deliver.
      const ofTask = steps.filter((s) => s.taskId === x.taskId && s.at <= at + 50)
      const calls = ofTask.filter((s) => s.kind === 'ask' || s.kind === 'answer')
      step = (x.direction === 'push' ? ofTask.at(-1) : calls.at(-1) ?? ofTask.at(-1)) ?? steps.find((s) => s.taskId === x.taskId)
    } else {
      // A plain message: the exchange between the same parties, closest in time.
      const candidates = steps.filter((s) => s.kind === 'message' && s.from === (x.sender || OUTSIDE) && s.to === x.recipient)
      step = candidates.sort((a, b) => Math.abs(a.at - at) - Math.abs(b.at - at))[0]
    }
    if (!step) {
      loose.push(x)
      continue
    }
    if (x.direction === 'push') {
      step.pushes.push({ to: x.recipient ?? '', at, exchange: x })
    }
    step.exchanges.push(x)
  }
  return loose
}

function seconds(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

/** A one-line description of an exchange: "POST /a/northwind/rpc · SendMessage · 200 · 12ms". */
export function exchangeLine(x: Exchange): string {
  const method = rpcMethod(x.request)
  return [`${x.method} ${x.url}`, method, x.error ? 'error' : String(x.status || '—'), `${x.durationMs}ms`].filter(Boolean).join(' · ')
}

/** The JSON-RPC method of a request body, or the REST operation from its path. */
export function rpcMethod(body: string): string | undefined {
  try {
    const v = JSON.parse(body)
    if (v && typeof v.method === 'string') return v.method
  } catch {
    /* not JSON-RPC */
  }
  return undefined
}

/** Pretty-prints a body: JSON indented, SSE streams one event per block, anything else as is. */
export function pretty(body: string): string {
  const t = body.trim()
  if (!t) return ''
  try {
    return JSON.stringify(JSON.parse(t), null, 2)
  } catch {
    /* not one JSON document */
  }
  if (/^(data|event|id):/m.test(t)) {
    return t
      .split(/\n\n+/)
      .map((block) =>
        block
          .split('\n')
          .map((line) => {
            const d = line.match(/^data:\s?(.*)$/)
            if (!d) return line
            try {
              return 'data: ' + JSON.stringify(JSON.parse(d[1]), null, 2)
            } catch {
              return line
            }
          })
          .join('\n'),
      )
      .join('\n\n')
  }
  return t
}

/** Who a direction is from and to, in words. */
export function exchangeParties(x: Exchange): string {
  switch (x.direction) {
    case 'in':
      return `${defaultNamer(x.sender ?? '')} → Lagrange (to ${defaultNamer(x.recipient ?? '')})`
    case 'out':
      return `Lagrange → ${defaultNamer(x.recipient ?? '')}'s agent (for ${defaultNamer(x.sender ?? '')})`
    case 'push':
      return `Lagrange → ${defaultNamer(x.recipient ?? '')}'s webhook${x.sender ? ` (update from ${defaultNamer(x.sender)})` : ''}`
  }
}
