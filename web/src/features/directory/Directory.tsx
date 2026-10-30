import { ExternalLink, MessageSquare, Search, UserPlus } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { api } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useView } from '@/store/live'
import { useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { Copy } from '@/ui/Copy'

/** Everyone on this server, people and agents, with their cards and skills. */
export function Directory() {
  const { addresses, user, refreshAddresses } = useSession()
  const model = useView()
  const [q, setQ] = useState('')
  const [kind, setKind] = useState<'all' | 'agent' | 'human'>('all')
  const [inviting, setInviting] = useState(false)
  useEffect(() => {
    void refreshAddresses()
  }, [model.addressVersion, refreshAddresses])

  const hits = addresses
    .filter((a) => kind === 'all' || a.kind === kind)
    .filter((a) => {
      const hay = `${a.handle} ${a.displayName} ${a.card.name} ${a.card.description} ${a.card.skills.map((s) => `${s.id} ${s.name} ${(s.tags ?? []).join(' ')}`).join(' ')}`
      return hay.toLowerCase().includes(q.toLowerCase())
    })

  return (
    <div className="scrollbar-thin h-full overflow-y-auto">
      <div className="mx-auto max-w-5xl space-y-5 p-8">
        <header className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <h1 className="text-xl font-extrabold">Directory</h1>
            <p className="mt-1 text-sm text-muted">People and agents are both addresses with an A2A agent card. Anyone can delegate to either.</p>
          </div>
          {user?.admin && (
            <Button icon={<UserPlus size={14} />} onClick={() => setInviting(!inviting)}>
              Add a person
            </Button>
          )}
        </header>
        {inviting && <Invite onDone={() => (setInviting(false), void refreshAddresses())} />}
        <div className="flex flex-wrap items-center gap-2">
          <label className="flex min-w-[260px] flex-1 items-center gap-2">
            <Search size={15} className="text-muted" />
            <input className="input" placeholder="Search by name, skill or tag…" value={q} onChange={(e) => setQ(e.target.value)} />
          </label>
          <div className="flex rounded-lg bg-surface-2 p-0.5">
            {(['all', 'agent', 'human'] as const).map((k) => (
              <button key={k} onClick={() => setKind(k)} className={cn('rounded-md px-3 py-1 text-xs font-bold', k === kind ? 'bg-surface text-accent shadow-card' : 'text-muted')}>
                {k === 'all' ? 'Everyone' : k === 'agent' ? 'Agents' : 'People'}
              </button>
            ))}
          </div>
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {hits.map((a) => {
            const online = a.kind === 'human' || !!a.endpoint || (model.presence[a.handle]?.sessions ?? a.stats.sessions) > 0
            return (
              <article key={a.handle} className="card flex flex-col p-4">
                <div className="flex items-center gap-3">
                  <Avatar handle={a.handle} kind={a.kind} name={a.displayName} size={40} />
                  <div className="min-w-0 flex-1">
                    <h2 className="truncate text-sm font-bold">{a.displayName || a.card.name || a.handle}</h2>
                    <p className="font-mono text-[11px] text-muted">@{a.handle}</p>
                  </div>
                  <span className={cn('h-2.5 w-2.5 rounded-full', online ? 'bg-good' : 'bg-line')} title={online ? 'online' : 'offline'} />
                </div>
                <p className="mt-2 line-clamp-3 flex-1 text-xs text-muted">{a.card.description}</p>
                <div className="mt-2 flex flex-wrap gap-1">
                  {a.card.skills.slice(0, 4).map((s) => (
                    <span key={s.id} className="rounded-full bg-accent-soft px-2 py-0.5 text-[10.5px] font-semibold text-accent">
                      {s.name}
                    </span>
                  ))}
                </div>
                <div className="mt-3 flex items-center gap-1 border-t border-line pt-3 text-xs">
                  {a.handle !== user?.handle && (
                    <Link to={`/inbox/${a.handle}`} className="inline-flex items-center gap-1 rounded-lg bg-accent px-2.5 py-1 font-bold text-accent-ink">
                      <MessageSquare size={12} /> Message
                    </Link>
                  )}
                  <a href={a.cardUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 rounded-lg px-2 py-1 font-semibold text-muted hover:bg-surface-2">
                    <ExternalLink size={12} /> Card
                  </a>
                  <Copy text={a.cardUrl} label="URL" />
                </div>
              </article>
            )
          })}
        </div>
      </div>
    </div>
  )
}

function Invite({ onDone }: { onDone: () => void }) {
  const [f, setF] = useState({ email: '', name: '', handle: '', password: '' })
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await api.invite(f)
      onDone()
    } catch (err) {
      setError((err as Error).message)
    }
  }
  return (
    <form onSubmit={submit} className="card grid gap-3 p-5 sm:grid-cols-4">
      {(['name', 'handle', 'email', 'password'] as const).map((k) => (
        <label key={k} className="flex flex-col gap-1 text-xs font-semibold text-muted capitalize">
          {k}
          <input className="input" type={k === 'password' ? 'password' : k === 'email' ? 'email' : 'text'} value={f[k]} onChange={(e) => setF({ ...f, [k]: e.target.value })} />
        </label>
      ))}
      {error && <p className="text-xs font-semibold text-bad sm:col-span-4">{error}</p>}
      <div className="sm:col-span-4">
        <Button type="submit">Create account</Button>
      </div>
    </form>
  )
}
