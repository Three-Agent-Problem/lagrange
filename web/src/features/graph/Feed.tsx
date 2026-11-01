import { ArrowRight, ChevronDown, Hourglass, Package, PlugZap, Send, UserPlus } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type Entry, type Run } from '@/lib/api'
import { cn } from '@/lib/cn'
import { summarize } from '@/lib/data'
import { useLive, useView } from '@/store/live'
import { OUTSIDE } from '@/store/model'
import { addressMap, useSession } from '@/store/session'
import { Avatar } from '@/ui/Avatar'
import { StateBadge } from '@/ui/StateBadge'
import { ago, clock, duration } from '@/ui/time'
import { useNames } from '@/ui/useNames'
import { digest, digestLine, headline, outcomeOf, runOf, type Outcome } from '@/features/graph/digest'
import { RunPeek } from '@/features/graph/RunPeek'

type Tab = 'summary' | 'events'

function savedTab(): Tab {
  try {
    return localStorage.getItem('lagrange-feed') === 'events' ? 'events' : 'summary'
  } catch {
    return 'summary'
  }
}

/** The side panel of the live graph: what happened, summed up per run or event by event. */
export function Feed() {
  const [tab, setTab] = useState(savedTab)
  const pick = (t: Tab) => {
    setTab(t)
    try {
      localStorage.setItem('lagrange-feed', t)
    } catch {
      /* storage unavailable */
    }
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex items-center gap-2 border-b border-line px-4 py-2.5">
        <h2 className="text-sm font-bold">Activity</h2>
        <div className="ml-auto flex rounded-lg bg-surface-2 p-0.5 text-[11px] font-bold">
          {(['summary', 'events'] as const).map((t) => (
            <button key={t} className={cn('rounded-md px-2 py-1', tab === t ? 'bg-surface text-ink shadow-card' : 'text-muted hover:text-ink')} onClick={() => pick(t)}>
              {t === 'summary' ? 'Summary' : 'All events'}
            </button>
          ))}
        </div>
      </div>
      {tab === 'summary' ? <Summary /> : <Events />}
    </div>
  )
}

const HOUR = 3_600_000

const TONE: Record<Outcome, string> = {
  done: 'bg-good',
  failed: 'bg-bad',
  waiting: 'bg-warn',
  running: 'bg-work animate-pulse',
}

/** One card per run, newest first, each saying how it ended; a digest of the last hour on top. */
function Summary() {
  const [runs, setRuns] = useState<Run[]>()
  const [open, setOpen] = useState<string>()
  const seq = useLive((s) => s.model.seq)
  const { name, kind } = useNames()
  const first = useRef(true)

  const load = useCallback(() => {
    api.runs({ limit: '40' }).then((r) => setRuns(r.runs), () => setRuns((x) => x ?? []))
  }, [])
  useEffect(() => {
    if (first.current) {
      first.current = false
      load()
      return
    }
    const t = setTimeout(load, 500)
    return () => clearTimeout(t)
  }, [seq, load])

  const now = Date.now()
  const d = useMemo(() => digest(runs ?? [], Date.now() - HOUR), [runs])

  if (!runs) return <div className="m-4 h-24 animate-pulse rounded-xl bg-surface-2" />
  return (
    <ol className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
      <li className="border-b border-line bg-surface-2/50 px-4 py-3">
        <p className="text-[10.5px] font-bold tracking-wide text-muted">LAST HOUR</p>
        <p className="mt-0.5 text-sm font-semibold">{digestLine(d, name)}</p>
      </li>
      {runs.length === 0 && <li className="p-4 text-sm text-muted">Nothing yet. Send a message from the Inbox.</li>}
      {runs.map((r) => {
        const h = headline(r, name, now)
        const o = outcomeOf(r)
        const isOpen = open === r.id
        const took = Date.parse(r.updatedAt) - Date.parse(r.startedAt)
        const others = r.participants.filter((p) => p !== r.starter && p !== r.recipient)
        return (
          <li key={r.id} className={cn('border-b border-line/60', isOpen && 'bg-surface-2/40')}>
            <button className="flex w-full gap-2.5 px-4 py-3 text-left hover:bg-surface-2" onClick={() => setOpen(isOpen ? undefined : r.id)} aria-expanded={isOpen}>
              <span className="relative shrink-0">
                <Avatar handle={r.starter || OUTSIDE} kind={kind(r.starter)} name={name(r.starter)} size={28} />
                <span className={cn('absolute -right-0.5 -bottom-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-surface', TONE[o])} />
              </span>
              <span className="min-w-0 flex-1 text-xs">
                <span className="flex items-baseline gap-2">
                  <span className="truncate font-semibold text-muted">{h.who}</span>
                  <span className="ml-auto shrink-0 font-mono text-[10px] text-muted">{ago(r.updatedAt)}</span>
                </span>
                <span className="mt-0.5 block truncate text-[13px] font-bold">{r.title || 'Untitled'}</span>
                <span className={cn('mt-0.5 line-clamp-2 block', o === 'failed' ? 'text-bad' : o === 'waiting' ? 'text-warn' : 'text-ink/80')}>{h.result}</span>
                <span className="mt-1 flex items-center gap-1.5 text-[10.5px] text-muted">
                  {others.length > 0 && <>via {others.map(name).join(', ')} · </>}
                  {r.entries} {r.entries === 1 ? 'event' : 'events'}{took > 0 && ` · ${duration(took)}`}
                  <ChevronDown size={12} className={cn('ml-auto transition', isOpen && 'rotate-180')} />
                </span>
              </span>
            </button>
            {isOpen && <RunPeek runId={r.id} name={name} />}
          </li>
        )
      })}
    </ol>
  )
}

/** Every event, newest first; a click opens it in place with the run it belongs to. */
function Events() {
  const model = useView()
  const byHandle = addressMap(useSession((s) => s.addresses))
  const { name } = useNames()
  const [open, setOpen] = useState<number>()
  const entries = [...model.feed].reverse()
  return (
    <ol className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
      {entries.length === 0 && <li className="p-4 text-sm text-muted">Nothing yet. Send a message from the Inbox.</li>}
      {entries.map((e) => {
        const isOpen = open === e.seq
        const run = runOf(e, model.tasks)
        return (
          <li key={e.seq} className={cn('border-b border-line/60', isOpen && 'bg-surface-2/40')}>
            <button className="w-full text-left" onClick={() => setOpen(isOpen ? undefined : e.seq)} aria-expanded={isOpen}>
              <Row e={e} kind={(h) => byHandle[h]?.kind} />
            </button>
            {isOpen && (
              <>
                <Detail e={e} />
                {run && (
                  <>
                    <p className="px-4 pt-1 text-[10.5px] font-bold tracking-wide text-muted">THE RUN IT BELONGS TO</p>
                    <RunPeek runId={run} name={name} focus={e.seq} />
                  </>
                )}
              </>
            )}
          </li>
        )
      })}
    </ol>
  )
}

/** What one event carried, in full. */
function Detail({ e }: { e: Entry }) {
  const request = typeof e.data?.request === 'string' ? e.data.request : undefined
  const reply = typeof e.data?.replyContent === 'string' ? e.data.replyContent : typeof e.data?.reply === 'string' ? e.data.reply : undefined
  const rows: [string, string | undefined][] = [
    [e.kind === 'message' ? 'Asked' : e.kind === 'input' ? 'Answer' : 'Said', request ?? e.summary],
    ['Reply', reply],
    ['Waiting on', typeof e.data?.awaiting === 'string' ? `@${e.data.awaiting}` : undefined],
  ]
  return (
    <div className="mx-4 mb-2 rounded-lg border border-line bg-surface px-3 py-2 text-xs">
      <dl className="flex flex-col gap-1.5">
        {rows
          .filter(([, v]) => v)
          .map(([k, v]) => (
            <div key={k}>
              <dt className="text-[10px] font-bold text-muted">{k}</dt>
              <dd className="line-clamp-6 whitespace-pre-wrap break-words">{v}</dd>
            </div>
          ))}
      </dl>
      <p className="mt-1.5 font-mono text-[10px] text-muted">
        {new Date(e.at).toLocaleString()} · {e.kind}
        {e.state && ` · ${e.state.replace('TASK_STATE_', '').toLowerCase()}`}
      </p>
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
        {e.summary && <>asked: {summarize(e.summary)} · </>}
        {e.data?.reply ? `replied: ${summarize(String(e.data.reply))}` : 'replied'}
      </span>
    ) : e.kind === 'input' ? (
      <span className="truncate text-muted">answered: {summarize(e.summary)}</span>
    ) : (
      <span className="flex min-w-0 items-center gap-1.5">
        {e.state && <StateBadge state={e.state} />}
        <span className="truncate text-muted">{summarize(e.summary)}</span>
      </span>
    )
  return (
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
}

function workers(n: number): string {
  if (n <= 0) return 'went offline'
  return n === 1 ? '1 worker online' : `${n} workers online`
}
