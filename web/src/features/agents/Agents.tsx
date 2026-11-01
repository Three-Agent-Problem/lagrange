import { KeyRound, Plus, Trash2 } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { AccessPicker } from '@/features/access/Access'
import { accessHint, ACCESS } from '@/features/access/options'
import { Connect } from '@/features/agents/Connect'
import { api, type Access, type Address, type Token } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useView } from '@/store/live'
import { useCan, useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { CodeLine } from '@/ui/Copy'
import { ago } from '@/ui/time'

export function Agents() {
  const { addresses, user, publicUrl, refreshAddresses } = useSession()
  const model = useView()
  const [params, setParams] = useSearchParams()
  const [creating, setCreating] = useState(params.has('new'))
  const [secret, setSecret] = useState<{ handle: string; token: string }>()
  const admin = useCan('admin')
  const member = useCan('member')
  // Admins manage every agent of the workspace; members the ones they created.
  const mine = addresses.filter((a) => a.kind === 'agent' && (admin || a.ownerId === user?.id))

  useEffect(() => {
    void refreshAddresses()
  }, [model.addressVersion, refreshAddresses])

  return (
    <div className="scrollbar-thin h-full overflow-y-auto">
      <div className="mx-auto max-w-4xl space-y-6 p-8">
        <header className="flex items-end justify-between gap-4">
          <div>
            <h1 className="text-xl font-extrabold">Agents</h1>
            <p className="mt-1 text-sm text-muted">Every agent gets an address. Run it anywhere; the tunnel connects it here, no deploy or open port needed.</p>
          </div>
          {member && (
            <Button icon={<Plus size={14} />} onClick={() => (setCreating(true), setSecret(undefined))}>
              New agent
            </Button>
          )}
        </header>

        {creating && (
          <Create
            onDone={(r) => {
              setCreating(false)
              setParams({}, { replace: true })
              setSecret(r)
              void refreshAddresses()
            }}
            onCancel={() => (setCreating(false), setParams({}, { replace: true }))}
          />
        )}
        {secret && <Connect key={secret.token} {...secret} onClose={() => setSecret(undefined)} />}

        <div className="grid gap-4">
          {mine.length === 0 && !creating && !secret && member && <FirstAgent onStart={() => setCreating(true)} />}
          {!member && <p className="text-sm text-muted">You are a viewer in this workspace: its agents are in the Directory, and members and admins add new ones.</p>}
          {mine.map((a) => (
            <AgentCard key={a.handle} a={a} live={model.presence[a.handle]} publicUrl={publicUrl} onSecret={setSecret} onChanged={() => void refreshAddresses()} />
          ))}
        </div>

        <section className="card space-y-3 p-5 text-sm">
          <h2 className="font-bold">Connecting agents</h2>
          <p className="text-muted">Try an address before writing any code: a built-in test agent that repeats what it is sent.</p>
          <CodeLine text={`lagrange tunnel --server ${publicUrl} --token <token> --echo`} />
          <p className="text-muted">Any A2A agent, in any language, behind a tunnel:</p>
          <CodeLine text={`lagrange tunnel --server ${publicUrl} --token <token> --to http://localhost:9001`} />
          <p className="text-muted">A coding agent that speaks ACP (Claude Code, Gemini CLI, opencode). Its "may I run this?" prompts land in your inbox:</p>
          <CodeLine text={`lagrange tunnel --server ${publicUrl} --token <token> --acp claude-agent-acp --dir ~/code/project`} />
          <p className="text-muted">
            Run the same command on several machines to add workers: tasks spread across them. An agent with a public URL can skip the tunnel: set its endpoint when you create it. Others reach every address at{' '}
            <code className="font-mono text-ink">{publicUrl}/a/&lt;handle&gt;</code>, as far as its access allows: outside workspaces ask to connect in Settings → Connections, then call with their own agent’s token.
          </p>
        </section>
      </div>
    </div>
  )
}

/** The empty state: what connecting the first agent takes. */
function FirstAgent({ onStart }: { onStart: () => void }) {
  const steps = ['Pick a handle: your agent’s address', 'Run one command next to it (or try the built-in echo agent)', 'Send it a test message']
  return (
    <div className="card p-6">
      <h2 className="text-base font-bold">Connect your first agent in two minutes</h2>
      <ol className="mt-3 space-y-1.5 text-sm text-muted">
        {steps.map((s, i) => (
          <li key={s} className="flex items-center gap-2">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-accent-soft text-[11px] font-bold text-accent">{i + 1}</span>
            {s}
          </li>
        ))}
      </ol>
      <Button className="mt-4" icon={<Plus size={14} />} onClick={onStart}>
        New agent
      </Button>
    </div>
  )
}

function Create({ onDone, onCancel }: { onDone: (r: { handle: string; token: string }) => void; onCancel: () => void }) {
  const [form, setForm] = useState({ handle: '', displayName: '', description: '', endpoint: '', access: 'connections' as Access })
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    try {
      const r = await api.createAddress(form)
      onDone({ handle: r.address.handle, token: r.token })
    } catch (err) {
      setError((err as Error).message)
    }
  }
  const field = (k: Exclude<keyof typeof form, 'access'>, label: string, placeholder: string) => (
    <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
      {label}
      <input className="input" placeholder={placeholder} value={form[k]} onChange={(e) => setForm({ ...form, [k]: e.target.value })} />
    </label>
  )
  return (
    <form onSubmit={submit} className="card grid gap-3 p-5 sm:grid-cols-2">
      {field('handle', 'Handle', 'my-agent')}
      {field('displayName', 'Name', 'Acme Buyer')}
      <div className="sm:col-span-2">{field('description', 'Description', 'Buys merch from approved suppliers')}</div>
      <div className="sm:col-span-2">{field('endpoint', 'Public endpoint (optional, skips the tunnel)', 'https://agent.example.com')}</div>
      <label className="flex flex-col gap-1 text-xs font-semibold text-muted sm:col-span-2">
        Who can send it tasks
        <select className="input" value={form.access} onChange={(e) => setForm({ ...form, access: e.target.value as Access })}>
          {ACCESS.map((a) => (
            <option key={a.access} value={a.access}>
              {a.label}
            </option>
          ))}
        </select>
        <span className="font-normal">{accessHint(form.access)}</span>
      </label>
      {error && <p className="text-xs font-semibold text-bad sm:col-span-2">{error}</p>}
      <div className="flex gap-2 sm:col-span-2">
        <Button type="submit">Create address</Button>
        <button type="button" className="text-xs font-semibold text-muted" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}

function AgentCard({ a, live, publicUrl, onSecret, onChanged }: { a: Address; live?: Address['stats']; publicUrl: string; onSecret: (s: { handle: string; token: string }) => void; onChanged: () => void }) {
  const [tokens, setTokens] = useState<Token[]>()
  const stats = live ?? a.stats
  const online = !!a.endpoint || stats.sessions > 0
  return (
    <article className="card p-5">
      <div className="flex items-start gap-3">
        <Avatar handle={a.handle} kind="agent" size={44} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h2 className="truncate font-bold">{a.displayName || a.card.name || a.handle}</h2>
            <span className={cn('flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-bold', online ? 'bg-good-soft text-good' : 'bg-surface-2 text-muted')}>
              <span className={cn('h-1.5 w-1.5 rounded-full', online ? 'bg-good' : 'bg-muted')} />
              {a.endpoint ? 'public endpoint' : online ? `${stats.sessions} worker${stats.sessions === 1 ? '' : 's'}` : 'offline'}
            </span>
          </div>
          <p className="font-mono text-xs text-muted">
            {publicUrl}/a/{a.handle}
          </p>
          <p className="mt-1 text-sm text-muted">{a.card.description}</p>
        </div>
        <div className="flex gap-1">
          <button
            className="rounded-lg p-2 text-muted hover:bg-surface-2 hover:text-ink"
            title="New token"
            onClick={() => void api.mintToken(a.handle, 'web').then((r) => onSecret({ handle: a.handle, token: r.token }))}
          >
            <KeyRound size={15} />
          </button>
          <button
            className="rounded-lg p-2 text-muted hover:bg-bad-soft hover:text-bad"
            title="Delete address"
            onClick={() => {
              if (confirm(`Delete @${a.handle}? Its tokens stop working.`)) void api.deleteAddress(a.handle).then(onChanged)
            }}
          >
            <Trash2 size={15} />
          </button>
        </div>
      </div>

      {!a.endpoint && (
        <div className="mt-4 grid gap-3 sm:grid-cols-3">
          <Meter label="Running" value={stats.inFlight} max={stats.capacity} />
          <div className="rounded-xl bg-surface-2 p-3">
            <div className="text-[11px] font-bold text-muted uppercase">Queued</div>
            <div className={cn('font-mono text-lg font-bold', stats.waiting ? 'text-warn' : '')}>{stats.waiting}</div>
          </div>
          <div className="rounded-xl bg-surface-2 p-3">
            <div className="text-[11px] font-bold text-muted uppercase">Capacity</div>
            <div className="font-mono text-lg font-bold">{stats.capacity}</div>
          </div>
        </div>
      )}
      {a.sessions.length > 0 && (
        <ul className="mt-3 space-y-1 text-xs">
          {a.sessions.map((s) => (
            <li key={s.id} className="flex items-center gap-2 font-mono text-muted">
              <span className="h-1.5 w-1.5 rounded-full bg-good" /> {s.id} · {s.agent || 'tunnel'} · capacity {s.capacity}
              {s.expose && ' · exposes HTTP'} · connected {ago(s.connected)}
            </li>
          ))}
        </ul>
      )}
      <div className="mt-4">
        <p className="mb-1.5 text-[11px] font-bold text-muted uppercase">Who can send it tasks</p>
        <AccessPicker handle={a.handle} value={a.access} onSaved={onChanged} />
      </div>
      {a.card.skills.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1">
          {a.card.skills.map((s) => (
            <span key={s.id} className="rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-semibold text-accent" title={s.description}>
              {s.name}
            </span>
          ))}
        </div>
      )}
      <div className="mt-3 text-xs">
        {tokens ? (
          <ul className="space-y-0.5 text-muted">
            {tokens.map((t) => (
              <li key={t.prefix + t.createdAt} className="font-mono">
                {t.prefix}… · {t.label || 'token'} · {t.lastUsed ? `used ${ago(t.lastUsed)}` : 'never used'}
              </li>
            ))}
          </ul>
        ) : (
          <button className="font-semibold text-muted hover:text-ink" onClick={() => void api.tokens(a.handle).then((r) => setTokens(r.tokens))}>
            Show tokens
          </button>
        )}
      </div>
    </article>
  )
}

function Meter({ label, value, max }: { label: string; value: number; max: number }) {
  return (
    <div className="rounded-xl bg-surface-2 p-3">
      <div className="text-[11px] font-bold text-muted uppercase">{label}</div>
      <div className="font-mono text-lg font-bold">
        {value}
        <span className="text-sm text-muted">/{max}</span>
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-surface ring-1 ring-line">
        <div className="h-full rounded-full bg-accent transition-all" style={{ width: `${max ? Math.min(100, (value / max) * 100) : 0}%` }} />
      </div>
    </div>
  )
}
