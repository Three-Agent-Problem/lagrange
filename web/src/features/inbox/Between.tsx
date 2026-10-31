import { ArrowLeftRight, Eye, GitFork, Search } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useLocation, useParams } from 'react-router'
import { api, textOf, type Conversation, type Entry, type TaskView } from '@/lib/api'
import { cn } from '@/lib/cn'
import { humanData, summarize } from '@/lib/data'
import { useLive } from '@/store/live'
import { normState, OUTSIDE } from '@/store/model'
import { useSession } from '@/store/session'
import { Avatar } from '@/ui/Avatar'
import { Bubble } from '@/ui/Bubble'
import { DataView } from '@/ui/DataView'
import { StateBadge } from '@/ui/StateBadge'
import { ago, clock } from '@/ui/time'
import { useNames } from '@/ui/useNames'
import { others, timeline } from '@/features/inbox/pairs'

/** Mine | Between agents, at the top of the inbox list. */
export function InboxTabs() {
  const between = useLocation().pathname.startsWith('/inbox/between')
  const tab = (on: boolean) => cn('flex-1 rounded-md px-2 py-1 text-center', on ? 'bg-surface text-ink shadow-card' : 'text-muted hover:text-ink')
  return (
    <div className="flex rounded-lg bg-surface-2 p-0.5 text-[11px] font-bold">
      <Link to="/inbox" className={tab(!between)}>
        Mine
      </Link>
      <Link to="/inbox/between" className={tab(between)}>
        Between agents
      </Link>
    </div>
  )
}

/** Refetch shortly after new activity arrives. */
function useOnActivity(fn: () => void) {
  const seq = useLive((s) => s.model.seq)
  const first = useRef(true)
  useEffect(() => {
    if (first.current) {
      first.current = false
      fn()
      return
    }
    const t = setTimeout(fn, 400)
    return () => clearTimeout(t)
  }, [seq, fn])
}

/** What the other addresses say to each other, read only. */
export function Between() {
  const { a, b } = useParams()
  const me = useSession((s) => s.user)!.handle
  const { name, kind } = useNames()
  const [all, setAll] = useState<Conversation[]>()
  const [q, setQ] = useState('')
  const load = useCallback(() => {
    api.conversations().then((r) => setAll(r.conversations), () => setAll((x) => x ?? []))
  }, [])
  useOnActivity(load)
  const list = useMemo(() => others(all ?? [], me, q), [all, me, q])
  const active = a && b ? { a, b } : list[0]

  return (
    <div className="flex h-full min-h-0">
      <aside className="flex w-[320px] shrink-0 flex-col border-r border-line bg-surface">
        <div className="flex flex-col gap-2.5 border-b border-line px-4 py-3">
          <h1 className="text-base font-bold">Inbox</h1>
          <InboxTabs />
          <label className="flex items-center gap-2">
            <Search size={14} className="text-muted" />
            <input className="input" placeholder="Filter by agent…" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
        </div>
        <ul className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
          {all && list.length === 0 && <li className="p-4 text-sm text-muted">{q ? 'No conversation matches.' : 'Your agents haven’t talked to anyone yet. When one agent asks another for something, it shows up here.'}</li>}
          {list.map((c) => {
            const on = active?.a === c.a && active?.b === c.b
            return (
              <li key={`${c.a}|${c.b}`}>
                <Link to={`/inbox/between/${c.a}/${c.b}`} className={cn('flex gap-3 px-4 py-3 hover:bg-surface-2', on && 'bg-accent-soft/60')}>
                  <span className="relative h-[42px] w-[42px] shrink-0">
                    <Avatar handle={c.a} kind={kind(c.a)} name={name(c.a)} size={30} className="absolute top-0 left-0" />
                    <Avatar handle={c.b} kind={kind(c.b)} name={name(c.b)} size={30} className="absolute right-0 bottom-0 ring-2 ring-surface" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex items-baseline gap-2">
                      <span className="truncate text-sm font-bold">
                        {name(c.a)} <span className="font-normal text-muted">&</span> {name(c.b)}
                      </span>
                      <span className="ml-auto shrink-0 font-mono text-[10px] text-muted">{ago(c.lastAt)}</span>
                    </span>
                    <span className="flex items-center gap-2">
                      <span className="truncate text-xs text-muted">
                        {c.last && (
                          <>
                            <b className="font-semibold text-ink/70">{name(c.lastFrom)}:</b> {summarize(c.last)}
                          </>
                        )}
                      </span>
                      {c.waiting > 0 && <span className="ml-auto flex h-5 min-w-5 shrink-0 items-center justify-center rounded-full bg-warn px-1.5 text-[10px] font-bold text-white">{c.waiting}</span>}
                    </span>
                  </span>
                </Link>
              </li>
            )
          })}
        </ul>
      </aside>
      {active ? (
        <PairThread key={`${active.a}|${active.b}`} a={active.a} b={active.b} />
      ) : (
        <div className="chat-wall flex flex-1 items-center justify-center text-sm text-muted">{all ? 'Pick a conversation.' : ''}</div>
      )}
    </div>
  )
}

/** Everything a and b said to each other: a on the left, b on the right. */
function PairThread({ a, b }: { a: string; b: string }) {
  const { name, kind } = useNames()
  const [data, setData] = useState<{ tasks: TaskView[]; messages: Entry[] }>()
  const [error, setError] = useState('')
  const end = useRef<HTMLDivElement>(null)
  const load = useCallback(() => {
    api.conversation(a, b).then(setData, (e: Error) => setError(e.message))
  }, [a, b])
  useOnActivity(load)
  const items = useMemo(() => (data ? timeline(data.tasks, data.messages) : []), [data])
  const last = items.at(-1)

  // Block body: Chrome's scrollIntoView returns a promise, which React would call as a cleanup.
  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' })
  }, [items.length, last?.at])

  return (
    <section className="flex min-w-0 flex-1 flex-col">
      <header className="flex items-center gap-3 border-b border-line bg-surface px-5 py-2.5">
        <Avatar handle={a} kind={kind(a)} name={name(a)} size={34} />
        <ArrowLeftRight size={14} className="text-muted" />
        <Avatar handle={b} kind={kind(b)} name={name(b)} size={34} />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-bold">
            {name(a)} and {name(b)}
          </h2>
          <p className="flex items-center gap-1 truncate text-xs text-muted">
            <Eye size={12} /> You’re reading along. {data && `${data.tasks.length} tasks, ${data.messages.length} messages.`}
          </p>
        </div>
      </header>
      <div className="chat-wall scrollbar-thin min-h-0 flex-1 overflow-y-auto px-6 py-4">
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          {error && <p className="text-sm text-bad">{error}</p>}
          {data && items.length === 0 && <p className="mx-auto mt-10 rounded-full bg-surface px-3 py-1 text-xs text-muted shadow-card">They haven’t said anything yet.</p>}
          {items.map((it) =>
            it.kind === 'task' ? (
              <PairTask key={it.task.id} t={it.task} b={b} kind={kind} name={name} />
            ) : (
              <div key={it.ask.messageId} className="flex flex-col gap-1.5">
                <Stamp at={it.at} from={name(it.sender)} to={name(it.recipient)} />
                <Bubble m={it.ask} sender={it.sender} recipient={it.recipient} kind={kind} mine={it.sender === b} />
                {it.reply && <Bubble m={it.reply} sender={it.sender} recipient={it.recipient} kind={kind} mine={it.recipient === b} />}
              </div>
            ),
          )}
          <div ref={end} />
        </div>
      </div>
    </section>
  )
}

/** When, and who started it: "10:53 · @acme-buyer → @contoso". */
function Stamp({ at, from, to }: { at: number; from?: string; to?: string }) {
  return (
    <div className="flex justify-center">
      <span className="rounded-full bg-surface/90 px-2.5 py-0.5 text-[10px] text-muted shadow-card">
        <span className="font-mono">{clock(at)}</span>
        {from && to && (
          <>
            {' · '}
            <b className="font-semibold text-ink/70">{from}</b> → <b className="font-semibold text-ink/70">{to}</b>
          </>
        )}
      </span>
    </div>
  )
}

/** One task between the two, read only: its messages, results and state. */
function PairTask({ t, b, kind, name }: { t: TaskView; b: string; kind: (h: string) => 'agent' | 'human' | undefined; name: (h: string) => string }) {
  const history = [...(t.task?.history ?? [])]
  const status = t.task?.status.message
  if (status && !history.some((m) => m.messageId === status.messageId) && !status.metadata?.['https://threeagents.dev/ext/awaiting/v1']) history.push(status)
  const senderRight = (t.sender || OUTSIDE) === b
  return (
    <div className="flex flex-col gap-1.5">
      <Stamp at={Date.parse(t.createdAt)} from={name(t.sender || OUTSIDE)} to={name(t.recipient)} />
      {history.map((m) => (
        <Bubble key={m.messageId} m={m} sender={t.sender} recipient={t.recipient} kind={kind} mine={(m.role === 'ROLE_USER') === senderRight} />
      ))}
      {(t.task?.artifacts ?? []).map((art) => (
        <div key={art.artifactId} className={cn('max-w-[85%] rounded-2xl border border-line bg-surface px-3 py-2 text-xs shadow-card', senderRight ? 'self-start' : 'self-end')}>
          <b className="text-accent">{art.name || 'Result'}</b>
          {textOf(art.parts) && <p className="mt-0.5 whitespace-pre-wrap">{textOf(art.parts)}</p>}
          {humanData(art.parts).map((d, i) => (
            <DataView key={i} data={d} className="mt-1" />
          ))}
        </div>
      ))}
      <div className={cn('flex items-center gap-2', senderRight ? 'justify-end' : 'justify-start')}>
        <StateBadge state={normState(t.state)} />
        {t.awaiting && <span className="text-[11px] font-semibold text-warn">waiting on {name(t.awaiting)}</span>}
        <Link to={`/runs/${t.id}?task=${t.id}`} className="flex items-center gap-1 text-[11px] font-semibold text-muted hover:text-accent">
          <GitFork size={11} /> step by step
        </Link>
      </div>
    </div>
  )
}
