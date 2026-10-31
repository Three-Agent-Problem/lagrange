import { GitFork, MessageSquarePlus, Search, Send } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api, textOf, type Address, type TaskView } from '@/lib/api'
import { cn } from '@/lib/cn'
import { humanData, summarize } from '@/lib/data'
import { useLive } from '@/store/live'
import { isWaiting, normState, OUTSIDE } from '@/store/model'
import { addressMap, useSession } from '@/store/session'
import { Bubble } from '@/ui/Bubble'
import { DataView } from '@/ui/DataView'
import { AnswerForm } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { StateBadge } from '@/ui/StateBadge'
import { ago, clock } from '@/ui/time'

type Conversation = { handle: string; tasks: TaskView[]; latest: TaskView; waiting: number }

/** Does this task wait for me: addressed to me as a person, or my task whose agent asked back? */
export function waitsFor(t: TaskView, me: string): boolean {
  return isWaiting(normState(t.state)) && (t.awaiting === me || (!t.awaiting && t.sender === me))
}

function conversations(tasks: TaskView[], me: string): Conversation[] {
  const by = new Map<string, TaskView[]>()
  for (const t of tasks) {
    const other = t.sender === me ? t.recipient : t.sender || OUTSIDE
    by.set(other, [...(by.get(other) ?? []), t])
  }
  return [...by.entries()]
    .map(([handle, ts]) => {
      ts.sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))
      const latest = ts.reduce((a, b) => (Date.parse(b.updatedAt) > Date.parse(a.updatedAt) ? b : a))
      return { handle, tasks: ts, latest, waiting: ts.filter((t) => waitsFor(t, me)).length }
    })
    .sort((a, b) => Date.parse(b.latest.updatedAt) - Date.parse(a.latest.updatedAt))
}

export function Inbox() {
  const { handle: selected } = useParams()
  const navigate = useNavigate()
  const { user, addresses } = useSession()
  const me = user!.handle
  const byHandle = useMemo(() => addressMap(addresses), [addresses])
  const kind = useCallback((h: string) => byHandle[h]?.kind, [byHandle])
  const seq = useLive((s) => s.model.seq)
  const [tasks, setTasks] = useState<TaskView[]>()
  const [picking, setPicking] = useState(false)

  const reload = useCallback(() => {
    void api.tasks(true).then((r) => setTasks(r.tasks))
  }, [])
  useEffect(() => {
    const t = setTimeout(reload, tasks ? 200 : 0)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seq, reload])

  const convs = useMemo(() => conversations(tasks ?? [], me), [tasks, me])
  const active = selected ?? convs[0]?.handle
  const conv = convs.find((c) => c.handle === active)

  return (
    <div className="flex h-full min-h-0">
      <aside className="flex w-[320px] shrink-0 flex-col border-r border-line bg-surface">
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h1 className="text-base font-bold">Inbox</h1>
          <button className="rounded-lg p-1.5 text-accent hover:bg-accent-soft" onClick={() => setPicking(true)} title="New conversation">
            <MessageSquarePlus size={18} />
          </button>
        </div>
        {picking && <Picker addresses={addresses.filter((a) => a.handle !== me)} onPick={(h) => (setPicking(false), navigate(`/inbox/${h}`))} onClose={() => setPicking(false)} />}
        <ul className="scrollbar-thin min-h-0 flex-1 overflow-y-auto">
          {tasks && convs.length === 0 && <li className="p-4 text-sm text-muted">No conversations yet. Start one with the + button.</li>}
          {convs.map((c) => {
            const last = c.latest
            const preview = textOf(last.task?.status.message?.parts) || summarize(last.summary)
            return (
              <li key={c.handle}>
                <Link to={`/inbox/${c.handle}`} className={cn('flex gap-3 px-4 py-3 hover:bg-surface-2', c.handle === active && 'bg-accent-soft/60')}>
                  <Avatar handle={c.handle} kind={kind(c.handle)} name={byHandle[c.handle]?.displayName} size={42} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline gap-2">
                      <span className="truncate text-sm font-bold">{c.handle === OUTSIDE ? 'Outside clients' : byHandle[c.handle]?.displayName || `@${c.handle}`}</span>
                      <span className="ml-auto shrink-0 font-mono text-[10px] text-muted">{ago(last.updatedAt)}</span>
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="truncate text-xs text-muted">{preview}</span>
                      {c.waiting > 0 && <span className="ml-auto flex h-5 min-w-5 shrink-0 items-center justify-center rounded-full bg-warn px-1.5 text-[10px] font-bold text-white">{c.waiting}</span>}
                    </div>
                  </div>
                </Link>
              </li>
            )
          })}
        </ul>
      </aside>
      {active ? (
        <Thread key={active} handle={active} conv={conv} me={me} address={byHandle[active]} kind={kind} onChange={reload} />
      ) : (
        <div className="chat-wall flex flex-1 items-center justify-center text-sm text-muted">Pick a conversation, or start one.</div>
      )}
    </div>
  )
}

function Picker({ addresses, onPick, onClose }: { addresses: Address[]; onPick: (h: string) => void; onClose: () => void }) {
  const [q, setQ] = useState('')
  const hits = addresses.filter((a) => `${a.handle} ${a.displayName} ${a.card.name} ${a.card.description}`.toLowerCase().includes(q.toLowerCase()))
  return (
    <div className="border-b border-line p-3">
      <label className="flex items-center gap-2">
        <Search size={14} className="text-muted" />
        <input autoFocus className="input" placeholder="Message an agent or a person…" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && onClose()} />
      </label>
      <ul className="scrollbar-thin mt-2 max-h-64 overflow-y-auto">
        {hits.map((a) => (
          <li key={a.handle}>
            <button className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left hover:bg-surface-2" onClick={() => onPick(a.handle)}>
              <Avatar handle={a.handle} kind={a.kind} name={a.displayName} size={28} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-semibold">{a.displayName || a.card.name || a.handle}</span>
                <span className="block truncate font-mono text-[10px] text-muted">@{a.handle}</span>
              </span>
              {a.kind === 'agent' && <span className={cn('h-2 w-2 rounded-full', a.online ? 'bg-good' : 'bg-line')} />}
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

function Thread({ handle, conv, me, address, kind, onChange }: { handle: string; conv?: Conversation; me: string; address?: Address; kind: (h: string) => 'agent' | 'human' | undefined; onChange: () => void }) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const end = useRef<HTMLDivElement>(null)
  const tasks = conv?.tasks ?? []
  // The agent's open question to me, which the composer answers.
  const question = [...tasks].reverse().find((t) => t.sender === me && waitsFor(t, me) && !t.input)
  const lastUpdate = conv?.latest.updatedAt

  // Block body: Chrome's scrollIntoView returns a promise, which React would call as a cleanup.
  useEffect(() => {
    end.current?.scrollIntoView({ block: 'end' })
  }, [tasks.length, lastUpdate])

  const send = async (e: FormEvent) => {
    e.preventDefault()
    if (!text.trim() || handle === OUTSIDE) return
    setBusy(true)
    setError('')
    try {
      if (question) await api.answer(question.id, { text })
      else await api.send({ to: handle, text })
      setText('')
      onChange()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="flex min-w-0 flex-1 flex-col">
      <header className="flex items-center gap-3 border-b border-line bg-surface px-5 py-2.5">
        <Avatar handle={handle} kind={address?.kind} name={address?.displayName} size={38} />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-bold">{handle === OUTSIDE ? 'Outside clients' : address?.displayName || address?.card.name || `@${handle}`}</h2>
          <p className="truncate text-xs text-muted">
            {address?.kind === 'human' ? 'person' : address?.online ? 'online' : address ? 'offline: messages wait in the mailbox' : ''}
            {address?.card.description && ` · ${address.card.description}`}
          </p>
        </div>
      </header>
      <div className="chat-wall scrollbar-thin min-h-0 flex-1 overflow-y-auto px-6 py-4">
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          {tasks.length === 0 && <p className="mx-auto mt-10 rounded-full bg-surface px-3 py-1 text-xs text-muted shadow-card">Say hello. Every message starts a task.</p>}
          {tasks.map((t) => (
            <TaskThread key={t.id} t={t} me={me} kind={kind} onChange={onChange} />
          ))}
          <div ref={end} />
        </div>
      </div>
      {handle !== OUTSIDE && (
        <form onSubmit={send} className="border-t border-line bg-surface px-5 py-3">
          {question && <p className="mb-1.5 text-xs font-semibold text-warn">Replying to @{handle}: “{textOf(question.task?.status.message?.parts)}”</p>}
          <div className="mx-auto flex max-w-3xl items-center gap-2">
            <input className="input rounded-full! px-4! py-2!" placeholder={question ? 'Your answer…' : `Message @${handle}…`} value={text} onChange={(e) => setText(e.target.value)} />
            <button type="submit" disabled={busy || !text.trim()} className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-accent text-accent-ink shadow-card disabled:opacity-50" aria-label="Send">
              <Send size={15} />
            </button>
          </div>
          {error && <p className="mt-1 text-xs text-bad">{error}</p>}
        </form>
      )}
    </section>
  )
}

/** One task in a thread: its messages as bubbles, then its live state and artifacts. */
function TaskThread({ t, me, kind, onChange }: { t: TaskView; me: string; kind: (h: string) => 'agent' | 'human' | undefined; onChange: () => void }) {
  const state = normState(t.state)
  const history = [...(t.task?.history ?? [])]
  const status = t.task?.status.message
  if (status && !history.some((m) => m.messageId === status.messageId) && !status.metadata?.['https://threeagents.dev/ext/awaiting/v1']) history.push(status)
  const mine = t.sender === me
  const forMe = waitsFor(t, me)
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-center gap-2">
        <span className="rounded-full bg-surface/90 px-2.5 py-0.5 font-mono text-[10px] text-muted shadow-card">{clock(t.createdAt)}</span>
      </div>
      {history.map((m) => (
        <Bubble key={m.messageId} m={m} sender={t.sender} recipient={t.recipient} kind={kind} mine={(m.role === 'ROLE_USER') === mine} />
      ))}
      {(t.task?.artifacts ?? []).map((a) => (
        <div key={a.artifactId} className={cn('max-w-[85%] rounded-2xl border border-line bg-surface px-3 py-2 text-xs shadow-card', !mine && 'self-end')}>
          <b className="text-accent">{a.name || 'Result'}</b>
          {textOf(a.parts) && <p className="mt-0.5 whitespace-pre-wrap">{textOf(a.parts)}</p>}
          {humanData(a.parts).map((d, i) => (
            <DataView key={i} data={d} className="mt-1" />
          ))}
        </div>
      ))}
      <div className={cn('flex items-center gap-2', mine ? 'justify-end' : 'justify-start')}>
        <StateBadge state={state} />
        <Link to={`/runs/${t.id}?task=${t.id}`} className="flex items-center gap-1 text-[11px] font-semibold text-muted hover:text-accent">
          <GitFork size={11} /> what happened, step by step
        </Link>
      </div>
      {forMe && (t.awaiting === me || t.input) && (
        <div className={cn('w-[min(420px,90%)] rounded-2xl bg-warn-soft p-3 shadow-card', mine ? 'self-end' : 'self-start')}>
          <p className="mb-2 text-xs font-bold text-warn">{t.awaiting === me ? `@${t.sender || 'someone'} needs your answer` : 'Answer the question'}</p>
          <AnswerForm taskId={t.id} mode={t.awaiting === me ? 'person' : 'sender'} schema={t.input?.schema} onDone={onChange} />
        </div>
      )}
    </div>
  )
}
