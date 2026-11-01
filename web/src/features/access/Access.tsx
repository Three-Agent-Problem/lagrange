import { Ban, Check, Link2, Lock, Globe, Users, X } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { api, type Access, type Connection } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useCan, useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { ago } from '@/ui/time'
import { accessHint, ACCESS } from './options'

const ICONS: Record<Access, typeof Globe> = { public: Globe, workspace: Lock, connections: Link2 }

/** The access of an address as a small badge. */
export function AccessBadge({ access }: { access: Access }) {
  const Icon = ICONS[access]
  return (
    <span className="flex items-center gap-1 rounded-full bg-surface-2 px-2 py-0.5 text-[10px] font-bold text-muted" title={accessHint(access)}>
      <Icon size={10} /> {ACCESS.find((a) => a.access === access)?.label}
    </span>
  )
}

/** "Who can reach this": picks an address's access and saves it at once. */
export function AccessPicker({ handle, value, onSaved, person }: { handle: string; value: Access; onSaved?: () => void; person?: boolean }) {
  const [access, setAccess] = useState(value)
  const [error, setError] = useState('')
  useEffect(() => setAccess(value), [value])
  const pick = async (next: Access) => {
    const was = access
    setAccess(next)
    setError('')
    try {
      await api.setAccess(handle, next)
      onSaved?.()
    } catch (e) {
      setAccess(was)
      setError((e as Error).message)
    }
  }
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap gap-1" role="radiogroup" aria-label={`Who can reach @${handle}`}>
        {ACCESS.map((a) => {
          const Icon = ICONS[a.access]
          return (
            <button
              key={a.access}
              role="radio"
              aria-checked={access === a.access}
              className={cn('flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs font-semibold', access === a.access ? 'border-accent bg-accent-soft text-accent' : 'border-line text-muted hover:bg-surface-2 hover:text-ink')}
              onClick={() => void pick(a.access)}
            >
              <Icon size={12} /> {a.label}
            </button>
          )
        })}
      </div>
      <p className="text-[11px] text-muted">{accessHint(access, person)}</p>
      {error && <p className="text-[11px] font-semibold text-bad">{error}</p>}
    </div>
  )
}

/** Settings: who can reach you, requests to reach the workspace's addresses, and the workspace's own requests. */
export function Connections() {
  const { user, workspace, addresses } = useSession()
  const member = useCan('member')
  const [list, setList] = useState<{ incoming: Connection[]; outgoing: Connection[] }>()
  const [status, setStatus] = useState<{ ok: boolean; text: string }>()
  const reload = () => void api.connections().then(setList)
  useEffect(reload, [])
  // Arriving from a request's email (/settings#connections): bring the list into view.
  useEffect(() => {
    if (list && location.hash === '#connections') document.getElementById('connections')?.scrollIntoView({ block: 'start' })
  }, [list])
  const me = addresses.find((a) => a.handle === user?.handle)

  const act = async (what: () => Promise<unknown>, ok?: string) => {
    setStatus(undefined)
    try {
      await what()
      if (ok) setStatus({ ok: true, text: ok })
      reload()
    } catch (e) {
      setStatus({ ok: false, text: (e as Error).message })
    }
  }

  if (!workspace || !user) return null
  const pending = list?.incoming.filter((c) => c.status === 'pending') ?? []
  const decided = list?.incoming.filter((c) => c.status !== 'pending') ?? []
  return (
    <div id="connections" className="card flex scroll-mt-6 flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <Users size={17} className="text-accent" />
        <h2 className="font-bold">Connections</h2>
        {pending.length > 0 && <span className="rounded-full bg-warn-soft px-2 py-0.5 text-[11px] font-bold text-warn">{pending.length} waiting</span>}
      </div>
      <p className="text-sm text-muted">
        Agents and people outside {workspace.name} reach an address that takes connections once its side accepts their workspace. Each connection has its own rate limit.
      </p>

      {me && (
        <section className="space-y-2">
          <p className="text-xs font-bold tracking-wide text-muted">WHO CAN REACH YOU (@{me.handle})</p>
          <AccessPicker handle={me.handle} value={me.access} person />
        </section>
      )}

      <section className="space-y-2">
        <p className="text-xs font-bold tracking-wide text-muted">ASKING TO REACH THIS WORKSPACE</p>
        {!list ? (
          <div className="h-16 animate-pulse rounded-lg bg-surface-2" />
        ) : list.incoming.length === 0 ? (
          <p className="text-sm text-muted">No requests yet.</p>
        ) : (
          <ul className="divide-y divide-line">
            {[...pending, ...decided].map((c) => (
              <Incoming key={c.id} c={c} act={act} />
            ))}
          </ul>
        )}
      </section>

      <section className="space-y-2">
        <p className="text-xs font-bold tracking-wide text-muted">{workspace.name.toUpperCase()} ASKED TO REACH</p>
        {member && <RequestForm onSent={(c) => (reload(), setStatus({ ok: true, text: c.status === 'accepted' ? `Already connected to @${c.to}.` : `Asked @${c.to}. Their side decides.` }))} />}
        {list && list.outgoing.length > 0 && (
          <ul className="divide-y divide-line">
            {list.outgoing.map((c) => (
              <li key={c.id} className="flex items-center gap-3 py-2.5">
                <Avatar handle={c.to} kind="agent" size={28} />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-semibold">@{c.to}</p>
                  <p className="truncate text-[11px] text-muted">
                    asked {ago(c.createdAt)}
                    {c.requestedBy && ` by @${c.requestedBy}`}
                    {c.status === 'accepted' && ` · ${c.ratePerMin} tasks/min`}
                  </p>
                </div>
                <StatusPill status={c.status} />
                {member && c.status !== 'blocked' && (
                  <button className="rounded-lg px-2 py-1 text-xs font-semibold text-muted hover:bg-bad-soft hover:text-bad" onClick={() => void act(() => api.endConnection(c.id))}>
                    {c.status === 'accepted' ? 'Disconnect' : 'Withdraw'}
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
      {status && <p className={status.ok ? 'text-xs font-semibold text-good' : 'text-xs font-semibold text-bad'}>{status.text}</p>}
    </div>
  )
}

function Incoming({ c, act }: { c: Connection; act: (what: () => Promise<unknown>, ok?: string) => Promise<void> }) {
  const [rate, setRate] = useState(c.ratePerMin)
  return (
    <li className="flex flex-wrap items-center gap-3 py-2.5">
      <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-surface-2 text-[10.5px] font-extrabold">{c.fromName.slice(0, 2).toUpperCase()}</span>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm">
          <span className="font-semibold">{c.fromName}</span> <span className="text-muted">→</span> <span className="font-mono text-xs">@{c.to}</span>
        </p>
        <p className="truncate text-[11px] text-muted">
          {c.requestedBy && `@${c.requestedBy} · `}
          {ago(c.createdAt)}
          {c.note && ` · “${c.note}”`}
        </p>
      </div>
      {c.status === 'pending' ? (
        <div className="flex items-center gap-1">
          <Button icon={<Check size={13} />} onClick={() => void act(() => api.decideConnection(c.id, 'accepted'), `${c.fromName} can reach @${c.to}.`)}>
            Accept
          </Button>
          <button className="rounded-lg px-2 py-1 text-xs font-semibold text-muted hover:bg-surface-2 hover:text-ink" onClick={() => void act(() => api.decideConnection(c.id, 'declined'))}>
            <X size={13} className="inline" /> Decline
          </button>
          <button className="rounded-lg px-2 py-1 text-xs font-semibold text-muted hover:bg-bad-soft hover:text-bad" title="Refuse, and don't let them ask again" onClick={() => void act(() => api.decideConnection(c.id, 'blocked'))}>
            <Ban size={13} className="inline" /> Block
          </button>
        </div>
      ) : (
        <div className="flex items-center gap-2">
          <StatusPill status={c.status} />
          {c.status === 'accepted' && (
            <label className="flex items-center gap-1 text-[11px] text-muted">
              <input
                className="input w-16 py-0.5 text-xs"
                type="number"
                min={1}
                value={rate}
                onChange={(e) => setRate(Number(e.target.value))}
                onBlur={() => rate > 0 && rate !== c.ratePerMin && void act(() => api.decideConnection(c.id, 'accepted', rate), 'Rate saved.')}
                aria-label={`Tasks a minute from ${c.fromName}`}
              />
              /min
            </label>
          )}
          {c.status === 'accepted' && (
            <button className="rounded-lg px-2 py-1 text-xs font-semibold text-muted hover:bg-bad-soft hover:text-bad" onClick={() => void act(() => api.decideConnection(c.id, 'blocked'))}>
              Block
            </button>
          )}
          {c.status === 'blocked' && (
            <button className="rounded-lg px-2 py-1 text-xs font-semibold text-muted hover:bg-surface-2 hover:text-ink" title="They can ask again" onClick={() => void act(() => api.endConnection(c.id))}>
              Unblock
            </button>
          )}
        </div>
      )}
    </li>
  )
}

function RequestForm({ onSent }: { onSent: (c: Connection) => void }) {
  const [to, setTo] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      const { connection } = await api.requestConnection(to, note)
      setTo('')
      setNote('')
      onSent(connection)
    } catch (err) {
      setError((err as Error).message)
    }
  }
  return (
    <form onSubmit={submit} className="space-y-1.5">
      <div className="flex flex-wrap items-end gap-2">
        <label className="flex w-44 flex-col gap-1 text-xs font-semibold text-muted">
          Address
          <input className="input" required placeholder="@northwind" value={to} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label className="flex min-w-[200px] flex-1 flex-col gap-1 text-xs font-semibold text-muted">
          Note for them (optional)
          <input className="input" placeholder="We'd like to order merch from you" value={note} onChange={(e) => setNote(e.target.value)} />
        </label>
        <Button type="submit" icon={<Link2 size={13} />}>
          Ask to connect
        </Button>
      </div>
      {error && <p className="text-[11px] font-semibold text-bad">{error}</p>}
    </form>
  )
}

function StatusPill({ status }: { status: Connection['status'] }) {
  const style = { pending: 'bg-warn-soft text-warn', accepted: 'bg-good-soft text-good', declined: 'bg-surface-2 text-muted', blocked: 'bg-bad-soft text-bad' }[status]
  return <span className={cn('rounded-full px-2 py-0.5 text-[11px] font-bold', style)}>{status}</span>
}
