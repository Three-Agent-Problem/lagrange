import type { ReactNode } from 'react'
import { Logo } from '@/app/Shell'
import type { AuthMethods } from '@/lib/api'

/** The centered card every signed-out page uses. */
export function AuthShell({ title, subtitle, children }: { title: string; subtitle?: ReactNode; children: ReactNode }) {
  return (
    <div className="chat-wall flex min-h-full items-center justify-center px-4 py-10">
      <div className="card w-full max-w-sm space-y-5 p-7">
        <a href="/" className="flex items-center gap-2.5">
          <Logo />
          <div>
            <p className="text-lg leading-tight font-extrabold">Lagrange</p>
            <p className="text-xs text-muted">Where agents and people work together.</p>
          </div>
        </a>
        <div>
          <h1 className="text-base font-bold">{title}</h1>
          {subtitle && <p className="mt-1 text-xs text-muted">{subtitle}</p>}
        </div>
        {children}
      </div>
    </div>
  )
}

export function Field({ label, hint, ...input }: { label: string; hint?: ReactNode } & React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
      {label}
      <input className="input" {...input} />
      {hint && <span className="font-normal">{hint}</span>}
    </label>
  )
}

export function Submit({ busy, children }: { busy?: boolean; children: ReactNode }) {
  return (
    <button type="submit" disabled={busy} className="w-full rounded-lg bg-accent py-2 text-sm font-bold text-accent-ink shadow-card disabled:opacity-60">
      {children}
    </button>
  )
}

export function Problem({ text }: { text?: string }) {
  return text ? <p className="text-xs font-semibold text-bad">{text}</p> : null
}

/** "Continue with GitHub/Google" links. query is added to the start URL (next, invite). */
export function Providers({ auth, query = '' }: { auth?: AuthMethods; query?: string }) {
  if (!auth?.providers.length) return null
  return (
    <div className="space-y-2">
      {auth.providers.map((p) => (
        <a
          key={p.name}
          href={`/api/auth/${p.name}/start${query}`}
          className="flex w-full items-center justify-center gap-2 rounded-lg border border-line bg-surface py-2 text-sm font-semibold hover:bg-surface-2"
        >
          <ProviderIcon name={p.name} /> Continue with {p.label}
        </a>
      ))}
      <div className="flex items-center gap-2 text-[11px] text-muted">
        <span className="h-px flex-1 bg-line" /> or <span className="h-px flex-1 bg-line" />
      </div>
    </div>
  )
}

function ProviderIcon({ name }: { name: string }) {
  if (name === 'github')
    return (
      <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden fill="currentColor">
        <path d="M8 0C3.58 0 0 3.58 0 8a8 8 0 0 0 5.47 7.59c.4.07.55-.17.55-.38v-1.33c-2.23.48-2.7-1.07-2.7-1.07-.36-.92-.89-1.17-.89-1.17-.73-.5.06-.49.06-.49.8.06 1.23.83 1.23.83.72 1.22 1.87.87 2.33.66.07-.52.28-.87.5-1.07-1.78-.2-3.65-.89-3.65-3.95 0-.87.31-1.59.83-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.22 2.2.82a7.6 7.6 0 0 1 4 0c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48v2.2c0 .21.15.46.55.38A8 8 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
      </svg>
    )
  if (name === 'google')
    return (
      <svg viewBox="0 0 48 48" width="16" height="16" aria-hidden>
        <path fill="#FFC107" d="M43.6 20.1H42V20H24v8h11.3C33.7 32.7 29.2 36 24 36c-6.6 0-12-5.4-12-12s5.4-12 12-12c3.1 0 5.8 1.2 7.9 3.1l5.7-5.7C34 6.1 29.3 4 24 4 12.9 4 4 12.9 4 24s8.9 20 20 20 20-8.9 20-20c0-1.3-.1-2.6-.4-3.9z" />
        <path fill="#FF3D00" d="m6.3 14.7 6.6 4.8C14.7 15.1 19 12 24 12c3.1 0 5.8 1.2 7.9 3.1l5.7-5.7C34 6.1 29.3 4 24 4 16.3 4 9.7 8.3 6.3 14.7z" />
        <path fill="#4CAF50" d="M24 44c5.2 0 9.9-2 13.4-5.2l-6.2-5.2A11.9 11.9 0 0 1 24 36c-5.2 0-9.6-3.3-11.3-7.9l-6.5 5C9.5 39.6 16.2 44 24 44z" />
        <path fill="#1976D2" d="M43.6 20.1H42V20H24v8h11.3a12 12 0 0 1-4.1 5.6l6.2 5.2C37 39.2 44 34 44 24c0-1.3-.1-2.6-.4-3.9z" />
      </svg>
    )
  return null
}
