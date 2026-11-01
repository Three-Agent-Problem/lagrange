import { MailCheck } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { AuthShell, Field, Problem, Providers, Submit } from '@/features/auth/parts'
import { HANDLE_PATTERN, suggestHandle } from '@/features/auth/handle'
import { api, type AuthMethods } from '@/lib/api'
import { useSession } from '@/store/session'

type Mode = 'login' | 'signup' | 'link' | 'forgot'

/** An OAuth callback that went wrong comes back as /?authError=… */
const authError = new URLSearchParams(location.search).get('authError') ?? ''

export function Login() {
  const { login, load } = useSession()
  const [auth, setAuth] = useState<AuthMethods>()
  const [mode, setMode] = useState<Mode>('login')
  const [f, setF] = useState({ email: '', password: '', name: '', handle: '' })
  const [handleEdited, setHandleEdited] = useState(false)
  const [error, setError] = useState(authError)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState('')

  useEffect(() => {
    void api.authMethods().then(setAuth)
    if (authError) history.replaceState(null, '', '/')
  }, [])

  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => {
    const v = e.target.value
    setF((f) => ({ ...f, [k]: v, ...(k === 'name' && !handleEdited ? { handle: suggestHandle(v) } : {}) }))
    if (k === 'handle') setHandleEdited(true)
  }
  const go = (m: Mode) => {
    setMode(m)
    setError('')
    setSent('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      if (mode === 'login') await login(f.email, f.password)
      else if (mode === 'signup') {
        await api.signup(f)
        await load()
      } else if (mode === 'link') {
        await api.loginLink(f.email)
        setSent(`If ${f.email} has an account here, a login link is on its way. It works once, for 15 minutes.`)
      } else {
        await api.resetPassword(f.email)
        setSent(`If ${f.email} has an account here, a link to choose a new password is on its way. It works for an hour.`)
      }
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const titles: Record<Mode, [string, string?]> = {
    login: ['Log in'],
    signup: ['Create your account', 'You get an address (@handle) that agents and people can send work to.'],
    link: ['Log in with an email link', 'No password needed: we email you a link that logs you in.'],
    forgot: ['Reset your password', 'We email you a link to choose a new one.'],
  }
  const [title, subtitle] = titles[mode]
  const open = auth?.signup === 'open'

  if (sent)
    return (
      <AuthShell title="Check your email">
        <div className="flex items-start gap-3 text-sm">
          <MailCheck size={20} className="mt-0.5 shrink-0 text-good" />
          <p>{sent}</p>
        </div>
        <button className="text-xs font-semibold text-accent" onClick={() => go('login')}>
          Back to log in
        </button>
      </AuthShell>
    )

  return (
    <AuthShell title={title} subtitle={subtitle}>
      {(mode === 'login' || mode === 'signup') && <Providers auth={auth} />}
      <form onSubmit={submit} className="space-y-4">
        {mode === 'signup' && (
          <>
            <Field label="Name" value={f.name} onChange={set('name')} autoComplete="name" autoFocus required />
            <Field
              label="Handle"
              value={f.handle}
              onChange={set('handle')}
              pattern={HANDLE_PATTERN.source}
              required
              hint={f.handle ? <>Your address: <span className="font-mono">@{f.handle}</span></> : 'Lowercase letters, digits and dashes.'}
            />
          </>
        )}
        <Field label="Email" type="email" autoComplete="username" value={f.email} onChange={set('email')} autoFocus={mode !== 'signup'} required />
        {(mode === 'login' || mode === 'signup') && (
          <Field
            label="Password"
            type="password"
            autoComplete={mode === 'signup' ? 'new-password' : 'current-password'}
            value={f.password}
            onChange={set('password')}
            minLength={mode === 'signup' ? 8 : undefined}
            required
            hint={mode === 'signup' ? 'At least 8 characters.' : undefined}
          />
        )}
        <Problem text={error} />
        <Submit busy={busy}>{mode === 'login' ? 'Log in' : mode === 'signup' ? 'Create account' : 'Send the link'}</Submit>
      </form>
      <div className="flex flex-wrap justify-between gap-2 text-xs font-semibold">
        {mode === 'login' ? (
          <>
            {auth?.email && (
              <>
                <button className="text-accent" onClick={() => go('link')}>
                  Email me a login link
                </button>
                <button className="text-muted hover:text-ink" onClick={() => go('forgot')}>
                  Forgot password?
                </button>
              </>
            )}
          </>
        ) : (
          <button className="text-accent" onClick={() => go('login')}>
            Back to log in
          </button>
        )}
      </div>
      {mode === 'login' && (
        <p className="border-t border-line pt-4 text-xs text-muted">
          {open ? (
            <>
              New here?{' '}
              <button className="font-semibold text-accent" onClick={() => go('signup')}>
                Create an account
              </button>
            </>
          ) : (
            'New here? This server is invite-only: ask someone on it for an invitation.'
          )}
        </p>
      )}
    </AuthShell>
  )
}
