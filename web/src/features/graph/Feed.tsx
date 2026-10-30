import { ArrowRight, Hourglass, Package, PlugZap, Send, UserPlus } from 'lucide-react'
import { Link } from 'react-router'
import type { Entry } from '@/lib/api'
import { useView } from '@/store/live'
import { OUTSIDE } from '@/store/model'
import { addressMap, useSession } from '@/store/session'
import { Avatar } from '@/ui/Avatar'
import { StateBadge } from '@/ui/StateBadge'
import { clock } from '@/ui/time'

/** The activity log, newest first: everything the graph animates, in words. */
export function Feed() {
  const model = useView()
  const byHandle = addressMap(useSession((s) => s.addresses))
  const entries = [...model.feed].reverse()
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="border-b border-line px-4 py-3">
        <h2 className="text-sm font-bold">Activity</h2>
        <p className="text-xs text-muted">Every task, hand-off and answer on this server.</p>
      </div>
      <ol className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
        {entries.length === 0 && <li className="p-4 text-sm text-muted">Nothing yet. Send a message from the Inbox.</li>}
        {entries.map((e) => (
          <Row key={e.seq} e={e} kind={(h) => byHandle[h]?.kind} />
        ))}
      </ol>
    </div>
  )
}

function Row({ e, kind }: { e: Entry; kind: (h: string) => 'agent' | 'human' | undefined }) {
  const sender = e.sender || OUTSIDE
  const body =
    e.kind === 'presence' ? (
      <span className="flex items-center gap-1 text-muted">
        <PlugZap size={12} /> {workers(Number(e.data?.sessions ?? 0))}
      </span>
    ) : e.kind === 'address' ? (
      <span className="flex items-center gap-1 text-muted">
        <UserPlus size={12} /> address changed
      </span>
    ) : e.kind === 'queued' ? (
      <span className="flex items-center gap-1 text-muted">
        <Hourglass size={12} /> {e.summary}
      </span>
    ) : e.kind === 'delivered' ? (
      <span className="flex items-center gap-1 text-good">
        <Send size={12} /> {e.summary}
      </span>
    ) : e.kind === 'artifact' ? (
      <span className="flex items-center gap-1 text-muted">
        <Package size={12} /> artifact {e.summary && <b className="text-ink">{e.summary}</b>}
      </span>
    ) : e.kind === 'message' ? (
      <span className="truncate text-muted">
        {e.summary && <>asked: {e.summary} · </>}
        {e.data?.reply ? `replied: ${String(e.data.reply)}` : 'replied'}
      </span>
    ) : e.kind === 'input' ? (
      <span className="truncate text-muted">answered: {e.summary}</span>
    ) : (
      <span className="flex min-w-0 items-center gap-1.5">
        {e.state && <StateBadge state={e.state} />}
        <span className="truncate text-muted">{e.summary}</span>
      </span>
    )
  const inner = (
    <div className="flex gap-2.5 px-4 py-2.5 hover:bg-surface-2">
      <Avatar handle={e.kind === 'presence' || e.kind === 'address' ? (e.recipient ?? '') : sender} kind={kind(e.kind === 'presence' ? (e.recipient ?? '') : sender)} size={26} />
      <div className="min-w-0 flex-1 text-xs">
        <div className="flex items-center gap-1 font-semibold">
          {e.kind === 'presence' || e.kind === 'address' ? (
            <span>@{e.recipient}</span>
          ) : (
            <>
              <span className="truncate">{sender === OUTSIDE ? 'outside' : `@${sender}`}</span>
              <ArrowRight size={11} className="shrink-0 text-muted" />
              <span className="truncate">@{e.recipient}</span>
            </>
          )}
          <span className="ml-auto shrink-0 font-mono text-[10px] text-muted">{clock(e.at)}</span>
        </div>
        <div className="mt-0.5">{body}</div>
      </div>
    </div>
  )
  const run = runLink(e)
  return <li className="border-b border-line/60">{run ? <Link to={run}>{inner}</Link> : inner}</li>
}

function workers(n: number): string {
  if (n <= 0) return 'went offline'
  return n === 1 ? '1 worker online' : `${n} workers online`
}

/** The run an entry belongs to, at its task: a plain message is a run of its own unless it
 * was sent from a task. */
function runLink(e: Entry): string | undefined {
  if (e.taskId) return `/runs/${e.taskId}?task=${e.taskId}`
  if (e.kind === 'message') return e.parentTaskId ? `/runs/${e.parentTaskId}` : `/runs/msg-${e.seq}`
  return undefined
}
