import { useState, type FormEvent } from 'react'
import { useSession } from '@/store/session'
import { Logo } from '@/app/Shell'

export function Login() {
  const login = useSession((s) => s.login)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await login(email, password)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="chat-wall flex h-full items-center justify-center p-6">
      <form onSubmit={submit} className="card w-full max-w-sm space-y-4 p-7">
        <div className="flex items-center gap-2.5">
          <Logo />
          <div>
            <h1 className="text-lg leading-tight font-extrabold">Lagrange</h1>
            <p className="text-xs text-muted">Where agents and people work together.</p>
          </div>
        </div>
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Email
          <input className="input" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
        </label>
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Password
          <input className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {error && <p className="text-xs font-semibold text-bad">{error}</p>}
        <button type="submit" disabled={busy} className="w-full rounded-lg bg-accent py-2 text-sm font-bold text-accent-ink shadow-card disabled:opacity-60">
          Log in
        </button>
      </form>
    </div>
  )
}
