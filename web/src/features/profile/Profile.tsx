import { Bot, CheckCircle2, ChevronDown, Clock, FileJson, Link2, Lock, ShieldAlert, ShieldCheck, User as UserIcon } from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { BrowserRouter, Route, Routes } from 'react-router'
import { Login } from '@/app/Login'
import { Lockup, Mark } from '@/ui/Brand'
import { AccessBadge } from '@/features/access/Access'
import { accessHint } from '@/features/access/options'
import { TestMessage } from '@/features/agents/Connect'
import { actIn, api, type Profile as ProfileData } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { Copy } from '@/ui/Copy'
import { reachOf, snippets, type Snippet } from './snippets'

/**
 * An address's public page, at its own URL (/a/<handle>). It works signed out; signed
 * in, it lets the visitor try the address or ask for access. Links to the rest of the
 * app reload the page, so the app starts there as usual.
 */
export function ProfilePage({ handle }: { handle: string }) {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/a/:handle" element={<Profile handle={handle} />} />
        <Route path="*" element={<Reload />} />
      </Routes>
    </BrowserRouter>
  )
}

function Reload() {
  useEffect(() => location.reload(), [])
  return null
}

function Profile({ handle }: { handle: string }) {
  const { ready, user, load } = useSession()
  const [data, setData] = useState<ProfileData | null>()
  const [loggingIn, setLoggingIn] = useState(false)
  const [example, setExample] = useState<string>()
  const reload = useCallback(() => void api.profile(handle).then(setData, () => setData(null)), [handle])
  useEffect(() => {
    void load()
  }, [load])
  // Again once signed in: the visitor's standing comes with the page.
  useEffect(() => {
    if (ready) reload()
  }, [ready, user?.id, reload])

  if (loggingIn && !user) return <Login />
  if (data === null) return <NotFound handle={handle} />
  if (!data) return <div className="chat-wall min-h-full" />
  const tryText = example ?? data.card.skills.flatMap((s) => s.examples ?? [])[0] ?? (data.kind === 'human' ? 'Can you approve this?' : 'Hello!')
  return (
    <div className="chat-wall min-h-full">
      <header className="mx-auto flex max-w-3xl items-center justify-between px-4 pt-6 sm:px-6">
        <a href="/" className="flex items-center gap-2">
          <Lockup size={19} />
        </a>
        {user ? (
          <a href="/" className="flex items-center gap-2 text-xs font-semibold text-muted hover:text-ink">
            Open Lagrange <Avatar handle={user.handle} kind="human" name={user.name} size={26} />
          </a>
        ) : (
          <Button onClick={() => setLoggingIn(true)}>Log in</Button>
        )}
      </header>
      <main className="mx-auto flex max-w-3xl flex-col gap-5 px-4 py-6 sm:px-6">
        <Hero p={data} />
        <Reach p={data} signedIn={!!user} onLogin={() => setLoggingIn(true)} onChanged={reload} tryText={tryText} />
        {data.card.skills.length > 0 && <Skills p={data} onExample={setExample} />}
        <Code p={data} text={tryText} />
        <Check p={data} />
      </main>
    </div>
  )
}

function NotFound({ handle }: { handle: string }) {
  return (
    <div className="chat-wall flex min-h-full items-center justify-center px-4">
      <div className="card max-w-sm space-y-3 p-7 text-center">
        <div className="flex justify-center">
          <Mark size={44} motion="idle" />
        </div>
        <h1 className="font-bold">No address @{handle}</h1>
        <p className="text-sm text-muted">Nobody on this server has that handle. It may have been deleted, or the link has a typo.</p>
        <a href="/" className="text-sm font-semibold text-accent">
          Go to Lagrange
        </a>
      </div>
    </div>
  )
}

function Hero({ p }: { p: ProfileData }) {
  const person = p.kind === 'human'
  return (
    <section className="card p-6">
      <div className="flex items-start gap-4">
        <Avatar handle={p.handle} kind={p.kind} name={p.name} size={64} />
        <div className="min-w-0 flex-1">
          <h1 className="text-xl leading-tight font-extrabold">{p.name}</h1>
          <p className="font-mono text-sm text-muted">@{p.handle}</p>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            <span className="flex items-center gap-1 rounded-full bg-accent-soft px-2 py-0.5 text-[10px] font-bold text-accent">
              {person ? <UserIcon size={10} /> : <Bot size={10} />} {person ? 'Person' : 'Agent'}
            </span>
            {!person && (
              <span className={cn('flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-bold', p.online ? 'bg-good-soft text-good' : 'bg-surface-2 text-muted')}>
                <span className={cn('h-1.5 w-1.5 rounded-full', p.online ? 'bg-good' : 'bg-muted')} />
                {p.online ? 'online' : 'offline: tasks wait for it'}
              </span>
            )}
            <AccessBadge access={p.access} />
            {p.workspace && <span className="rounded-full bg-surface-2 px-2 py-0.5 text-[10px] font-bold text-muted">by {p.workspace}</span>}
          </div>
        </div>
      </div>
      {p.description && <p className="mt-4 text-sm leading-relaxed">{p.description}</p>}
      <div className="mt-4 flex items-center gap-2 rounded-lg bg-surface-2 px-3 py-2">
        <Link2 size={14} className="shrink-0 text-muted" />
        <code className="min-w-0 flex-1 truncate font-mono text-xs">{p.url}</code>
        <Copy text={p.url} label="Copy" />
      </div>
      <p className="mt-2 text-[11px] text-muted">
        This page is also an A2A endpoint: any A2A client given this URL gets {person ? 'their' : 'its'} agent card.
      </p>
    </section>
  )
}

/** What the visitor can do: try it, ask for access, or log in first. */
function Reach({ p, signedIn, onLogin, onChanged, tryText }: { p: ProfileData; signedIn: boolean; onLogin: () => void; onChanged: () => void; tryText: string }) {
  const { workspace } = useSession()
  const verb = p.kind === 'human' ? `Ask ${p.name.split(' ')[0]}` : 'Try it'
  if (!signedIn || !p.viewer) {
    return (
      <section className="card flex flex-wrap items-center gap-3 p-5">
        <div className="min-w-0 flex-1">
          <h2 className="font-bold">{p.access === 'public' ? verb : 'Get access'}</h2>
          <p className="text-sm text-muted">
            {p.access === 'public'
              ? 'Anyone can send it a task: call it from code below, or log in to send a message from here.'
              : p.access === 'connections'
                ? 'It takes tasks from its workspace and the workspaces it accepted. Log in to ask for access for yours.'
                : 'It takes tasks from its own workspace only.'}
          </p>
        </div>
        {p.access !== 'workspace' && <Button onClick={onLogin}>Log in</Button>}
      </section>
    )
  }
  const reach = reachOf(p.handle, p.viewer.handle, p.access, p.viewer.workspaces, workspace?.slug ?? '')
  return (
    <section className="card flex flex-col gap-3 p-5">
      {reach.kind === 'self' && (
        <>
          <h2 className="font-bold">This is you</h2>
          <p className="text-sm text-muted">This is your public page. {accessHint(p.access, true)} Change it in Settings → Connections.</p>
        </>
      )}
      {reach.kind === 'send' && (
        <>
          <h2 className="font-bold">{verb}</h2>
          <p className="text-sm text-muted">Send a message as you{reach.via && p.viewer.workspaces.length > 1 ? `, from ${p.viewer.workspaces.find((w) => w.slug === reach.via)?.name}` : ''}. The reply shows here, and the conversation is in your inbox.</p>
          <SendAs via={reach.via} handle={p.handle} text={tryText} />
        </>
      )}
      {reach.kind === 'pending' && (
        <p className="flex items-center gap-2 text-sm">
          <Clock size={15} className="text-warn" /> {reach.via.name} asked to connect. @{p.handle}’s side decides; you can send it tasks once they accept.
        </p>
      )}
      {reach.kind === 'ask' && <AskAccess p={p} from={reach.from} onSent={onChanged} />}
      {reach.kind === 'blocked' && (
        <p className="flex items-center gap-2 text-sm text-muted">
          <ShieldAlert size={15} /> @{p.handle} doesn’t take requests from your workspaces.
        </p>
      )}
      {reach.kind === 'closed' && (
        <p className="flex items-center gap-2 text-sm text-muted">
          <Lock size={15} />
          {p.access === 'workspace' ? `@${p.handle} takes tasks from its own workspace only.` : 'Asking to connect takes the member role in one of your workspaces.'}
        </p>
      )}
    </section>
  )
}

function SendAs({ via, handle, text }: { via: string; handle: string; text: string }) {
  useEffect(() => {
    if (via) actIn(via)
  }, [via])
  return <TestMessage key={text} handle={handle} disabled={false} onAnswered={() => {}} initial={text} />
}

function AskAccess({ p, from, onSent }: { p: ProfileData; from: { slug: string; name: string }[]; onSent: () => void }) {
  const [slug, setSlug] = useState(from[0].slug)
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    actIn(slug)
    try {
      await api.requestConnection(p.handle, note)
      onSent()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <form onSubmit={submit} className="flex flex-col gap-3">
      <div>
        <h2 className="font-bold">Ask for access</h2>
        <p className="text-sm text-muted">
          @{p.handle} takes tasks from {p.workspace ?? 'its workspace'} and the workspaces it accepted. Once its side accepts, your workspace’s agents (with their own token) and people can send it tasks.
        </p>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        {from.length > 1 && (
          <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
            For
            <select className="input" value={slug} onChange={(e) => setSlug(e.target.value)}>
              {from.map((w) => (
                <option key={w.slug} value={w.slug}>
                  {w.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="flex min-w-[220px] flex-1 flex-col gap-1 text-xs font-semibold text-muted">
          Note for them (optional)
          <input className="input" placeholder="What you'd like to use it for" value={note} onChange={(e) => setNote(e.target.value)} />
        </label>
        <Button type="submit" disabled={busy} icon={<Link2 size={13} />}>
          Ask for access
        </Button>
      </div>
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}
    </form>
  )
}

function Skills({ p, onExample }: { p: ProfileData; onExample: (text: string) => void }) {
  return (
    <section className="card space-y-3 p-5">
      <h2 className="font-bold">What {p.kind === 'human' ? 'you can ask' : 'it does'}</h2>
      <ul className="space-y-3">
        {p.card.skills.map((s) => (
          <li key={s.id} className="rounded-xl bg-surface-2 p-3">
            <p className="text-sm font-bold">{s.name}</p>
            {s.description && <p className="mt-0.5 text-xs text-muted">{s.description}</p>}
            {(s.examples?.length ?? 0) > 0 && (
              <div className="mt-2 flex flex-wrap gap-1.5">
                {s.examples!.map((ex) => (
                  <button key={ex} className="rounded-lg border border-line bg-surface px-2 py-1 text-left text-xs hover:border-accent hover:text-accent" title="Use this example" onClick={() => onExample(ex)}>
                    “{ex}”
                  </button>
                ))}
              </div>
            )}
            {(s.tags?.length ?? 0) > 0 && (
              <div className="mt-2 flex flex-wrap gap-1">
                {s.tags!.map((t) => (
                  <span key={t} className="rounded-full bg-accent-soft px-2 py-0.5 text-[10px] font-semibold text-accent">
                    {t}
                  </span>
                ))}
              </div>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

function Code({ p, text }: { p: ProfileData; text: string }) {
  const [tab, setTab] = useState<Snippet['id']>('cli')
  const all = snippets(p.url, text, p.access !== 'public')
  const shown = all.find((s) => s.id === tab)!
  return (
    <section className="card space-y-3 p-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-bold">Call it from code</h2>
        <div className="flex gap-1" role="tablist">
          {all.map((s) => (
            <button key={s.id} role="tab" aria-selected={tab === s.id} className={cn('rounded-lg px-2.5 py-1 text-xs font-semibold', tab === s.id ? 'bg-accent-soft text-accent' : 'text-muted hover:bg-surface-2 hover:text-ink')} onClick={() => setTab(s.id)}>
              {s.label}
            </button>
          ))}
        </div>
      </div>
      <div className="relative rounded-lg bg-ink/95 dark:bg-black/60">
        <pre className="scrollbar-thin overflow-x-auto p-3 pr-12 font-mono text-[12px] leading-relaxed text-white">{shown.code}</pre>
        <Copy text={shown.code} className="absolute top-2 right-2 text-white/70 hover:bg-white/10 hover:text-white" />
      </div>
      <p className="text-xs text-muted">
        {p.access === 'public'
          ? 'No token needed. Calls with an address token are recorded as coming from that agent.'
          : p.access === 'connections'
            ? `Set LAGRANGE_TOKEN to an address token of one of your agents (Agents → key icon). Its workspace must be ${p.workspace ?? 'this address’s'} or one that @${p.handle} accepted.`
            : `Set LAGRANGE_TOKEN to an address token of an agent in ${p.workspace ?? 'its workspace'}.`}{' '}
        The card is at{' '}
        <a href={p.cardUrl} className="font-mono text-accent">
          /.well-known/agent-card.json
        </a>
        .
      </p>
    </section>
  )
}

function Check({ p }: { p: ProfileData }) {
  const [open, setOpen] = useState(false)
  const findings = p.check.findings ?? []
  const good = p.check.grade === 'conformant'
  return (
    <section className="card p-5">
      <button className="flex w-full items-center gap-2 text-left" onClick={() => setOpen(!open)} disabled={findings.length === 0}>
        {good ? <ShieldCheck size={17} className="text-good" /> : <ShieldAlert size={17} className={p.check.grade === 'near-conformant' ? 'text-warn' : 'text-bad'} />}
        <h2 className="font-bold">Card check: {p.check.grade}</h2>
        <span className="text-xs text-muted">{findings.length === 0 ? 'A2A 1.0, no findings' : `${findings.length} finding${findings.length === 1 ? '' : 's'}`}</span>
        {findings.length > 0 && <ChevronDown size={15} className={cn('ml-auto text-muted transition-transform', open && 'rotate-180')} />}
      </button>
      {open && (
        <ul className="mt-3 space-y-1.5 text-xs">
          {findings.map((f) => (
            <li key={f.path + f.message} className="flex gap-2">
              <span className={cn('shrink-0 rounded px-1.5 py-0.5 text-[10px] font-bold uppercase', f.severity === 'error' ? 'bg-bad-soft text-bad' : 'bg-warn-soft text-warn')}>{f.severity}</span>
              <span>
                <code className="font-mono">{f.path || 'card'}</code>: {f.message}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-2 flex items-center gap-1 text-[11px] text-muted">
        {good ? <CheckCircle2 size={11} /> : <FileJson size={11} />} Graded by the Three Agent Problem card linter, on the card as served at this address.
      </p>
    </section>
  )
}
