import { CheckCircle2, Link2Off, MailCheck } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { api, type AuthMethods, type Invite, type User } from '@/lib/api'
import { useSession } from '@/store/session'
import { HANDLE_PATTERN, suggestHandle } from './handle'
import { AuthShell, Field, Problem, Providers, Submit } from './parts'

type Purpose = 'verify' | 'login' | 'reset' | 'invite'

/** Opening the app after a link leaves the link's path behind. */
const enter = () => location.replace('/')

/**
 * The pages emailed links open (/auth/<purpose>/<token>), outside the login gate: the
 * token is the credential. Links that log in are claimed with a click, not on load, so
 * mail scanners that open links don't use them up.
 */
export function AuthLink({ purpose, token }: { purpose: Purpose; token: string }) {
  switch (purpose) {
    case 'verify':
      return <Verify token={token} />
    case 'login':
      return <LoginLink token={token} />
    case 'reset':
      return <Reset token={token} />
    case 'invite':
      return <AcceptInvite token={token} />
  }
}

function Gone({ text }: { text: string }) {
  return (
    <AuthShell title="This link no longer works">
      <div className="flex items-start gap-3 text-sm">
        <Link2Off size={20} className="mt-0.5 shrink-0 text-muted" />
        <p>{text}</p>
      </div>
      <a href="/" className="block text-xs font-semibold text-accent">
        Go to log in
      </a>
    </AuthShell>
  )
}

function Verify({ token }: { token: string }) {
  const [state, setState] = useState<'busy' | 'done' | string>('busy')
  useEffect(() => {
    api.verify(token).then(
      () => setState('done'),
      (e: Error) => setState(e.message),
    )
  }, [token])
  if (state === 'busy') return <AuthShell title="Confirming your email…">{null}</AuthShell>
  if (state !== 'done') return <Gone text={state} />
  return (
    <AuthShell title="Email confirmed">
      <div className="flex items-start gap-3 text-sm">
        <CheckCircle2 size={20} className="mt-0.5 shrink-0 text-good" />
        <p>You’re all set. Agents and people can now send you work.</p>
      </div>
      <button onClick={enter} className="w-full rounded-lg bg-accent py-2 text-sm font-bold text-accent-ink shadow-card">
        Open Lagrange
      </button>
    </AuthShell>
  )
}

function LoginLink({ token }: { token: string }) {
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const claim = async () => {
    setBusy(true)
    try {
      await api.claimLoginLink(token)
      enter()
    } catch (e) {
      setError((e as Error).message)
      setBusy(false)
    }
  }
  if (error) return <Gone text={error} />
  return (
    <AuthShell title="Log in to Lagrange" subtitle="This link works once.">
      <button onClick={() => void claim()} disabled={busy} className="w-full rounded-lg bg-accent py-2 text-sm font-bold text-accent-ink shadow-card disabled:opacity-60">
        Log in
      </button>
    </AuthShell>
  )
}

function Reset({ token }: { token: string }) {
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [error, setError] = useState('')
  const [gone, setGone] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (password !== again) return setError('The two passwords differ.')
    setBusy(true)
    setError('')
    try {
      await api.confirmReset(token, password)
      enter()
    } catch (err) {
      const msg = (err as Error).message
      if (/doesn't work/.test(msg)) setGone(msg)
      else setError(msg)
      setBusy(false)
    }
  }
  if (gone) return <Gone text={gone} />
  return (
    <AuthShell title="Choose a new password" subtitle="You’ll be logged out everywhere else.">
      <form onSubmit={submit} className="space-y-4">
        <Field label="New password" type="password" autoComplete="new-password" minLength={8} required autoFocus value={password} onChange={(e) => setPassword(e.target.value)} hint="At least 8 characters." />
        <Field label="Again" type="password" autoComplete="new-password" required value={again} onChange={(e) => setAgain(e.target.value)} />
        <Problem text={error} />
        <Submit busy={busy}>Save and log in</Submit>
      </form>
    </AuthShell>
  )
}

function AcceptInvite({ token }: { token: string }) {
  const [invite, setInvite] = useState<Invite>()
  const [auth, setAuth] = useState<AuthMethods>()
  const [gone, setGone] = useState('')
  const [f, setF] = useState({ name: '', handle: '', password: '' })
  const [handleEdited, setHandleEdited] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.invite(token).then(
      (inv) => {
        setInvite(inv)
        setF((f) => ({ ...f, handle: suggestHandle(inv.email) }))
      },
      (e: Error) => setGone(e.message),
    )
    void api.authMethods().then(setAuth)
  }, [token])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.acceptInvite(token, f)
      enter()
    } catch (err) {
      setError((err as Error).message)
      setBusy(false)
    }
  }
  if (gone) return <Gone text={gone} />
  if (!invite) return <AuthShell title="Opening your invitation…">{null}</AuthShell>
  if (invite.existing) return <JoinExisting token={token} invite={invite} auth={auth} />
  return (
    <AuthShell
      title={invite.invitedBy ? `${invite.invitedBy} invited you` : 'You’re invited'}
      subtitle={
        <>
          Join <span className="font-semibold text-ink">{invite.workspace}</span> as <span className="font-semibold text-ink">{invite.email}</span>. You get an address
          that agents and people can send work to.
        </>
      }
    >
      <Providers auth={auth} query={`?invite=${encodeURIComponent(token)}`} />
      <form onSubmit={submit} className="space-y-4">
        <Field
          label="Name"
          autoComplete="name"
          required
          autoFocus
          value={f.name}
          onChange={(e) => {
            const name = e.target.value
            setF((f) => ({ ...f, name, ...(handleEdited ? {} : { handle: suggestHandle(name) || f.handle }) }))
          }}
        />
        <Field
          label="Handle"
          required
          pattern={HANDLE_PATTERN.source}
          value={f.handle}
          onChange={(e) => (setHandleEdited(true), setF({ ...f, handle: e.target.value }))}
          hint={<>Your address: <span className="font-mono">@{f.handle || '…'}</span></>}
        />
        <Field label="Password" type="password" autoComplete="new-password" minLength={8} required value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} hint="At least 8 characters." />
        <Problem text={error} />
        <Submit busy={busy}>Join</Submit>
      </form>
    </AuthShell>
  )
}

/** Remembers the workspace an invitation joined, so the app opens in it. */
async function openJoined(name: string) {
  try {
    const { workspaces } = await api.workspaces()
    const ws = workspaces.filter((w) => w.name === name).at(-1)
    if (ws) localStorage.setItem('lagrange-workspace', ws.slug)
  } catch {
    /* the oldest workspace it is */
  }
  enter()
}

/** An invitation for an email that has an account: log in as it (if needed), then join. */
function JoinExisting({ token, invite, auth }: { token: string; invite: Invite; auth?: AuthMethods }) {
  const [me, setMe] = useState<User | null>()
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.me().then(
      (m) => setMe(m.user),
      () => setMe(null),
    )
  }, [])
  const join = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError('')
    try {
      if (!me) await api.login(invite.email, password)
      await api.acceptInvite(token)
      await openJoined(invite.workspace)
    } catch (err) {
      setError((err as Error).message)
      setBusy(false)
    }
  }
  const title = `${invite.invitedBy || 'Someone'} invited you to ${invite.workspace}`
  const subtitle = (
    <>
      As <span className="font-semibold text-ink">{invite.role}</span>, with your account <span className="font-semibold text-ink">{invite.email}</span>.
    </>
  )
  if (me === undefined) return <AuthShell title="Opening your invitation…">{null}</AuthShell>
  if (me && me.email !== invite.email)
    return (
      <AuthShell title={title} subtitle={subtitle}>
        <p className="text-sm">
          You’re logged in as <span className="font-mono">@{me.handle}</span> ({me.email}). Log out, then open this link again to join as {invite.email}.
        </p>
        <button className="text-xs font-semibold text-accent" onClick={() => void api.logout().then(() => location.reload())}>
          Log out
        </button>
      </AuthShell>
    )
  if (me)
    return (
      <AuthShell title={title} subtitle={subtitle}>
        <Problem text={error} />
        <button onClick={() => void join()} disabled={busy} className="w-full rounded-lg bg-accent py-2 text-sm font-bold text-accent-ink shadow-card disabled:opacity-60">
          Join {invite.workspace}
        </button>
      </AuthShell>
    )
  return (
    <AuthShell title={title} subtitle={subtitle}>
      <Providers auth={auth} query={`?invite=${encodeURIComponent(token)}`} />
      <form onSubmit={join} className="space-y-4">
        <Field label="Email" type="email" value={invite.email} disabled />
        <Field label="Password" type="password" autoComplete="current-password" required autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
        <Problem text={error} />
        <Submit busy={busy}>Log in and join</Submit>
      </form>
    </AuthShell>
  )
}

/** Shown to someone logged in who hasn't confirmed their email yet. */
export function VerifyEmail() {
  const { user, auth, logout } = useSession()
  const [status, setStatus] = useState<{ ok: boolean; text: string }>()
  const resend = async () => {
    try {
      await api.resendVerify()
      setStatus({ ok: true, text: 'Sent. Check your inbox (and spam).' })
    } catch (e) {
      setStatus({ ok: false, text: (e as Error).message })
    }
  }
  return (
    <AuthShell title="Confirm your email">
      <div className="flex items-start gap-3 text-sm">
        <MailCheck size={20} className="mt-0.5 shrink-0 text-accent" />
        <p>
          We sent a link to <span className="font-semibold">{user?.email}</span>. Open it to start using Lagrange as{' '}
          <span className="font-mono">@{user?.handle}</span>.
        </p>
      </div>
      {status && <p className={status.ok ? 'text-xs font-semibold text-good' : 'text-xs font-semibold text-bad'}>{status.text}</p>}
      <div className="flex justify-between text-xs font-semibold">
        {auth?.email ? (
          <button className="text-accent" onClick={() => void resend()}>
            Send it again
          </button>
        ) : (
          <span />
        )}
        <button className="text-muted hover:text-ink" onClick={() => void logout()}>
          Log out
        </button>
      </div>
    </AuthShell>
  )
}
