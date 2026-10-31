// Why a run failed, in words, with what to do next. The relay marks the failures it
// causes (could not deliver) with FAILURE metadata; anything else is the agent's or a
// person's own answer. Pure, so it is tested without a browser.
import { textOf, type Exchange, type Message, type RunDetail, type TaskView } from '@/lib/api'
import { normState } from '@/store/model'

/** relay.FailureKey */
export const FAILURE = 'https://threeagents.dev/ext/delivery-failure/v1'

type Failure = { kind: 'agent-down' | 'unreachable' | 'timeout' | 'task-lost' | 'agent-error'; address: string; detail: string }

export type Diagnosis = {
  title: string
  cause: string
  hint?: string
  /** The address that failed, if the failure is an agent's. */
  address?: string
  /** The HTTP status the agent answered with, when that explains it. */
  status?: number
  /** "delivery": Lagrange could not get the request through; "answer": an agent or person said no. */
  source: 'delivery' | 'answer'
}

const statusMessage = (t: TaskView): Message | undefined => t.task?.status.message
const failureOf = (t: TaskView) => statusMessage(t)?.metadata?.[FAILURE] as Failure | undefined

/** The last HTTP error the address answered with, if any. */
function httpError(exchanges: Exchange[], address: string): Exchange | undefined {
  return exchanges.filter((x) => x.recipient === address && x.direction !== 'push' && x.status >= 400).at(-1)
}

/** Why a finished run didn't succeed; undefined for runs that succeeded or are still going. */
export function diagnose(d: RunDetail, name: (h: string) => string): Diagnosis | undefined {
  const state = normState(d.run.state)
  if (state !== 'failed' && state !== 'rejected' && state !== 'canceled') return undefined
  const root = d.tasks.find((t) => t.id === d.run.id) ?? d.tasks[0]
  const said = (t: TaskView | undefined) => (t ? textOf(statusMessage(t)?.parts) : '')

  // A delivery failure anywhere in the run explains it best: the deepest one is the cause.
  const delivered = d.tasks.filter((t) => normState(t.state) === 'failed' && failureOf(t))
  const failing = delivered.at(-1)
  if (failing) {
    const f = failureOf(failing)!
    const who = name(f.address)
    switch (f.kind) {
      case 'agent-down':
        return {
          source: 'delivery',
          address: f.address,
          title: `${who} isn't running`,
          cause: `Its tunnel is connected, but ${f.detail}`,
          hint: 'Start the agent, or restart the tunnel with --to pointing at the port it listens on. Then send again.',
        }
      case 'unreachable':
        return {
          source: 'delivery',
          address: f.address,
          title: `Lagrange couldn't reach ${who}`,
          cause: f.detail,
          hint: 'Check that its endpoint is right and reachable from the Lagrange server, or connect it with a tunnel instead (no open port needed).',
        }
      case 'timeout':
        return {
          source: 'delivery',
          address: f.address,
          title: `${who} didn't answer in time`,
          cause: f.detail,
          hint: "Look in the agent's logs for a call that hangs. For long jobs, return a task right away and update it as you go.",
        }
      case 'task-lost':
        return {
          source: 'delivery',
          address: f.address,
          title: `${who} lost the task`,
          cause: f.detail,
          hint: 'The agent no longer knows this task, most likely because it restarted and keeps tasks in memory. Give it a persistent task store, then send the request again.',
        }
      default: {
        const x = httpError(d.exchanges, f.address)
        const s = x?.status
        const base = { source: 'delivery' as const, address: f.address, status: s, cause: f.detail }
        if (s === 401 || s === 403)
          return { ...base, title: `${who} refused the request (HTTP ${s})`, hint: "The agent wants credentials Lagrange didn't send: check its auth settings and what its card asks for." }
        if (s === 404 || s === 405)
          return { ...base, title: `${who} has no A2A endpoint at that URL (HTTP ${s})`, hint: `Lagrange called ${x!.url}, the URL in the agent's card. Check the card's URL and the path your server serves A2A on.` }
        if (s && s >= 500) return { ...base, title: `${who} hit an error (HTTP ${s})`, hint: "The error is inside the agent: check its logs. Its response is under All traffic." }
        return { ...base, title: `${who}'s answer didn't work`, hint: 'Compare its response under All traffic with the A2A spec (JSON-RPC 2.0, lf.a2a.v1 shapes).' }
      }
    }
  }

  if (state === 'rejected') {
    return { source: 'answer', title: 'Rejected', cause: said(root) || 'The request was turned down.', hint: 'Change the request and send it again.' }
  }
  if (state === 'canceled') {
    return { source: 'answer', title: 'Canceled', cause: said(root) || 'The run was canceled before it finished.' }
  }
  // The agent failed the task itself: its own words, from the root (it speaks to the sender).
  const failed = normState(root?.state) === 'failed' ? root : d.tasks.filter((t) => normState(t.state) === 'failed').at(-1)
  const who = failed ? name(failed.recipient) : name(d.run.recipient)
  return {
    source: 'answer',
    address: failed?.recipient,
    title: `${who} gave up`,
    cause: said(failed) || 'It gave no reason.',
    hint: "That's the agent's own answer, not a connection problem: change the request and send it again.",
  }
}

/** The request that started the run: the root task's first message from the sender. */
export function originalRequest(d: RunDetail): { text: string; data?: Record<string, unknown> } | undefined {
  const root = d.tasks.find((t) => t.id === d.run.id)
  const first = root?.task?.history?.find((m) => m.role === 'ROLE_USER')
  if (first) {
    const data = first.parts.find((p) => p.data && typeof p.data === 'object' && !Array.isArray(p.data))?.data as Record<string, unknown> | undefined
    return { text: textOf(first.parts), data }
  }
  return d.run.title ? { text: d.run.title } : undefined
}
