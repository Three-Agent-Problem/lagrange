import { Bell, ExternalLink, KeyRound, Link2, Mail, Send, ShieldCheck } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { api, type NotificationSettingsView, type TelegramStatus } from '@/lib/api'
import { Connections } from '@/features/access/Access'
import { ApiKeys } from '@/features/keys/ApiKeys'
import { Webhooks } from '@/features/webhooks/Webhooks'
import { Members } from '@/features/workspaces/Workspaces'
import { useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'

export function Settings() {
  const user = useSession((s) => s.user)!
  return (
    <div className="scrollbar-thin h-full overflow-y-auto">
      <div className="mx-auto flex max-w-2xl flex-col gap-5 px-4 py-8 sm:px-6">
        <div>
          <h1 className="text-xl font-extrabold">Settings</h1>
          <p className="mt-1 text-sm text-muted">Your account, your workspace, and how Lagrange reaches you when a task waits on you.</p>
        </div>
        <div className="card flex items-center gap-3 p-5">
          <Avatar handle={user.handle} kind="human" name={user.name} size={44} />
          <div className="min-w-0">
            <p className="font-bold">{user.name}</p>
            <p className="truncate font-mono text-xs text-muted">
              @{user.handle} · {user.email}
            </p>
          </div>
        </div>
        <Members />
        <Connections />
        <Notifications />
        <Telegram />
        <ApiKeys />
        <Webhooks />
        <Password />
        <LinkedAccounts />
      </div>
    </div>
  )
}

function Notifications() {
  const [view, setView] = useState<NotificationSettingsView>()
  const [email, setEmail] = useState(true)
  const [slack, setSlack] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<{ ok: boolean; text: string }>()

  useEffect(() => {
    void api.notificationSettings().then((v) => {
      setView(v)
      setEmail(v.settings.email)
      setSlack(v.settings.slackWebhook)
    })
  }, [])

  const run = async (what: () => Promise<unknown>, ok: string) => {
    setBusy(true)
    setStatus(undefined)
    try {
      await what()
      setStatus({ ok: true, text: ok })
    } catch (e) {
      setStatus({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  const save = (e: FormEvent) => {
    e.preventDefault()
    void run(async () => setView(await api.setNotificationSettings({ email, slackWebhook: slack.trim() })), 'Saved.')
  }
  const dirty = !!view && (view.settings.email !== email || view.settings.slackWebhook !== slack.trim())

  if (!view) return <div className="card h-56 animate-pulse" />
  return (
    <form onSubmit={save} className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <Bell size={17} className="text-accent" />
        <h2 className="font-bold">Notifications</h2>
      </div>
      <p className="text-xs text-muted">
        When an agent asks you something, you get the question and a link that answers it in one click, without logging in. The link works once.
      </p>

      <label className="flex items-start gap-3">
        <input type="checkbox" className="mt-0.5 h-4 w-4 accent-[var(--accent)]" checked={email && view.emailAvailable} disabled={!view.emailAvailable} onChange={(e) => setEmail(e.target.checked)} />
        <span className="text-sm">
          <span className="flex items-center gap-1.5 font-semibold">
            <Mail size={14} /> Email me at {view.email}
          </span>
          {!view.emailAvailable && (
            <span className="mt-0.5 block text-xs text-muted">
              This server can’t send email yet. An admin sets <code className="font-mono">LAGRANGE_SMTP_URL</code> to turn it on.
            </span>
          )}
        </span>
      </label>

      <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
        Slack incoming webhook
        <input className="input font-mono" placeholder="https://hooks.slack.com/services/…" value={slack} onChange={(e) => setSlack(e.target.value)} />
        <span className="font-normal">
          In Slack: Apps → Incoming Webhooks → Add to a channel (or your DMs), then paste the URL here.
        </span>
      </label>

      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={busy || !dirty}>
          Save
        </Button>
        <Button tone="good" disabled={busy || dirty} onClick={() => void run(api.testNotification, 'Test sent. Check your email or Slack.')} icon={<Send size={13} />}>
          Send a test
        </Button>
        {status && <span className={status.ok ? 'text-xs font-semibold text-good' : 'text-xs font-semibold text-bad'}>{status.text}</span>}
      </div>
    </form>
  )
}

/** Your own Telegram bot: questions arrive there with buttons to answer them. */
function Telegram() {
  const [st, setSt] = useState<TelegramStatus>()
  const [token, setToken] = useState('')
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<{ ok: boolean; text: string }>()

  useEffect(() => {
    void api.telegram().then(setSt)
  }, [])
  // While a pairing link is out, watch for the chat to pair.
  useEffect(() => {
    if (!st?.connected || st.paired || !link) return
    const t = setInterval(() => void api.telegram().then((s) => s.paired && (setSt(s), setLink(''))), 2000)
    return () => clearInterval(t)
  }, [st, link])

  const run = async (what: () => Promise<TelegramStatus | void>, ok?: string) => {
    setBusy(true)
    setStatus(undefined)
    try {
      const next = await what()
      if (next) {
        setSt(next)
        if (next.pairLink) setLink(next.pairLink)
      }
      if (ok) setStatus({ ok: true, text: ok })
    } catch (e) {
      setStatus({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  const connect = (e: FormEvent) => {
    e.preventDefault()
    void run(async () => {
      const next = await api.connectTelegram(token.trim())
      setToken('')
      return next
    })
  }

  if (!st) return <div className="card h-40 animate-pulse" />
  return (
    <div className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <Send size={17} className="text-accent" />
        <h2 className="font-bold">Telegram</h2>
        {st.paired && <span className="rounded-full bg-good-soft px-2 py-0.5 text-[11px] font-bold text-good">paired</span>}
      </div>
      <p className="flex items-start gap-1.5 text-xs text-muted">
        <ShieldCheck size={14} className="mt-px shrink-0 text-good" />
        Your own bot sends what waits on you, and you answer with its buttons: approve, reject, or pick an option. It can’t start, stop or message anything, and typed text goes nowhere.
      </p>

      {!st.connected && (
        <form onSubmit={connect} className="flex flex-col gap-3">
          <ol className="list-decimal space-y-1 pl-5 text-sm">
            <li>
              Open{' '}
              <a className="font-semibold text-accent hover:underline" href="https://t.me/BotFather" target="_blank" rel="noreferrer">
                @BotFather
              </a>{' '}
              in Telegram and send <code className="font-mono">/newbot</code>.
            </li>
            <li>Pick a name, then a username that ends in “bot”.</li>
            <li>Paste the token it sends you here. Keep it secret: it works like a password for the bot.</li>
          </ol>
          <div className="flex flex-wrap gap-2">
            <input className="input min-w-0 flex-1 font-mono" type="password" autoComplete="off" placeholder="123456789:AA…" value={token} onChange={(e) => setToken(e.target.value)} />
            <Button type="submit" disabled={busy || !token.trim()}>
              Connect
            </Button>
          </div>
        </form>
      )}

      {st.connected && !st.paired && (
        <div className="flex flex-col gap-2 text-sm">
          <p>
            Connected to <span className="font-semibold">@{st.botUsername}</span>. Now open it and tap <b>Start</b>: that pairs it with your chat.
          </p>
          <div className="flex flex-wrap items-center gap-2">
            {link ? (
              <a href={link} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1.5 rounded-lg bg-accent px-3 py-1.5 text-xs font-bold text-accent-ink shadow-card">
                <ExternalLink size={13} /> Open @{st.botUsername}
              </a>
            ) : (
              <Button disabled={busy} onClick={() => void run(api.pairTelegram)}>
                Make a pairing link
              </Button>
            )}
            {link && <span className="text-xs text-muted">The link works once, for 10 minutes. Waiting for you to tap Start…</span>}
          </div>
        </div>
      )}

      {st.paired && (
        <div className="flex flex-col gap-3 text-sm">
          <p>
            <span className="font-semibold">@{st.botUsername}</span> talks to <span className="font-semibold">@{st.chatUsername || 'you'}</span>
            {st.mutedUntil && <span className="ml-2 text-xs text-muted">muted until {new Date(st.mutedUntil).toLocaleTimeString()}</span>}
          </p>
          <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
            Send me
            <select className="input" value={st.level} disabled={busy} onChange={(e) => void run(() => api.setTelegramLevel(e.target.value as 'questions' | 'milestones'), 'Saved.')}>
              <option value="milestones">Questions, plus runs’ news and when they finish</option>
              <option value="questions">Only questions for me</option>
            </select>
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <Button tone="good" disabled={busy} icon={<Send size={13} />} onClick={() => void run(api.testTelegram, 'Test sent. Check Telegram.')}>
              Send a test
            </Button>
            <Button disabled={busy} onClick={() => void run(api.pairTelegram)}>
              Pair another chat
            </Button>
            <Button tone="bad" disabled={busy} onClick={() => void run(async () => (await api.disconnectTelegram(), { connected: false, paired: false }), 'Disconnected.')}>
              Disconnect
            </Button>
          </div>
          {link && (
            <a href={link} target="_blank" rel="noreferrer" className="text-xs font-semibold text-accent hover:underline">
              Open @{st.botUsername} in the other chat and tap Start →
            </a>
          )}
        </div>
      )}
      {status && <span className={status.ok ? 'text-xs font-semibold text-good' : 'text-xs font-semibold text-bad'}>{status.text}</span>}
    </div>
  )
}

function Password() {
  const { user, load } = useSession()
  const has = !!user?.hasPassword
  const [current, setCurrent] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<{ ok: boolean; text: string }>()
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setStatus(undefined)
    try {
      await api.changePassword(current, password)
      setCurrent('')
      setPassword('')
      setStatus({ ok: true, text: 'Saved. Your other sessions were logged out.' })
      await load()
    } catch (err) {
      setStatus({ ok: false, text: (err as Error).message })
    } finally {
      setBusy(false)
    }
  }
  return (
    <form onSubmit={save} className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <KeyRound size={17} className="text-accent" />
        <h2 className="font-bold">{has ? 'Password' : 'Set a password'}</h2>
      </div>
      {!has && <p className="text-xs text-muted">You log in with a link or another account. A password lets you log in with your email too.</p>}
      <div className="grid gap-3 sm:grid-cols-2">
        {has && (
          <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
            Current password
            <input className="input" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
          </label>
        )}
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          New password
          <input className="input" type="password" autoComplete="new-password" minLength={8} required value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" disabled={busy || password.length < 8}>
          {has ? 'Change password' : 'Set password'}
        </Button>
        {status && <span className={status.ok ? 'text-xs font-semibold text-good' : 'text-xs font-semibold text-bad'}>{status.text}</span>}
      </div>
    </form>
  )
}

/** OAuth accounts that log you in. Connecting one goes through the provider and back here (?linked=). */
function LinkedAccounts() {
  const { auth, identities } = useSession()
  const linked = new URLSearchParams(location.search).get('linked')
  if (!auth?.providers.length) return null
  return (
    <div className="card flex flex-col gap-3 p-5">
      <div className="flex items-center gap-2">
        <Link2 size={17} className="text-accent" />
        <h2 className="font-bold">Log in with</h2>
      </div>
      {auth.providers.map((p) => {
        const id = identities.find((i) => i.provider === p.name)
        return (
          <div key={p.name} className="flex items-center justify-between gap-3 text-sm">
            <span>
              <span className="font-semibold">{p.label}</span>
              {id && <span className="ml-2 text-xs text-muted">{id.email || 'connected'}</span>}
              {linked === p.name && <span className="ml-2 text-xs font-semibold text-good">Connected.</span>}
            </span>
            {!id && (
              <a href={`/api/auth/${p.name}/start?next=/settings`} className="rounded-lg border border-line px-2.5 py-1 text-xs font-semibold hover:bg-surface-2">
                Connect
              </a>
            )}
          </div>
        )
      })}
    </div>
  )
}
