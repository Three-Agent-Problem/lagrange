import { Bell, KeyRound, Link2, Mail, Send } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { api, type NotificationSettingsView } from '@/lib/api'
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
