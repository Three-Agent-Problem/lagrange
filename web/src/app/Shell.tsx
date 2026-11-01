import { Bot, Hourglass, Inbox, ListTree, LogOut, Moon, Network, RotateCcw, Sun, Users } from 'lucide-react'
import { useEffect, useState } from 'react'
import { NavLink, Outlet } from 'react-router'
import { api } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useLive } from '@/store/live'
import { waitingFor } from '@/store/model'
import { WorkspaceSwitcher } from '@/features/workspaces/Workspaces'
import { useSession } from '@/store/session'
import { Avatar } from '@/ui/Avatar'

export function Logo({ size = 34 }: { size?: number }) {
  return (
    <span className="flex shrink-0 items-center justify-center rounded-xl bg-accent font-extrabold text-accent-ink shadow-card" style={{ width: size, height: size, fontSize: size * 0.42 }}>
      3A
    </span>
  )
}

/** The theme the person picked, or else the system's. */
export function savedTheme(): 'light' | 'dark' {
  try {
    const saved = localStorage.getItem('lagrange-theme')
    if (saved === 'light' || saved === 'dark') return saved
  } catch {
    /* storage unavailable */
  }
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function useTheme() {
  const [theme, setTheme] = useState(savedTheme)
  useEffect(() => {
    document.documentElement.dataset.theme = theme
    try {
      localStorage.setItem('lagrange-theme', theme)
    } catch {
      /* storage unavailable */
    }
  }, [theme])
  return [theme, () => setTheme(theme === 'dark' ? 'light' : 'dark')] as const
}

export function Shell() {
  const { user, logout, dev } = useSession()
  const start = useLive((s) => s.start)
  const model = useLive((s) => s.model)
  const [theme, toggle] = useTheme()
  useEffect(() => start(), [start])
  const waiting = user ? waitingFor(model, user.handle).length : 0

  const item = (to: string, icon: React.ReactNode, label: string, badge?: number) => (
    <NavLink
      to={to}
      end={to === '/'}
      className={({ isActive }) => cn('relative flex flex-col items-center gap-1 rounded-xl px-2 py-2 text-[10.5px] font-bold', isActive ? 'bg-accent-soft text-accent' : 'text-muted hover:bg-surface-2 hover:text-ink')}
    >
      {icon}
      {label}
      {!!badge && <span className="absolute top-1 right-2 flex h-4 min-w-4 items-center justify-center rounded-full bg-warn px-1 text-[9px] text-white">{badge}</span>}
    </NavLink>
  )

  return (
    <div className="flex h-full">
      <nav className="flex w-[76px] shrink-0 flex-col items-center gap-1.5 border-r border-line bg-surface py-3">
        <Logo />
        <div className="mt-2.5">
          <WorkspaceSwitcher />
        </div>
        <div className="mt-2 flex w-full flex-col gap-1 px-2">
          {item('/', <Network size={19} />, 'Live')}
          {item('/inbox', <Inbox size={19} />, 'Inbox', waiting)}
          {item('/runs', <ListTree size={19} />, 'Runs')}
          {item('/waiting', <Hourglass size={19} />, 'Waiting')}
          {item('/agents', <Bot size={19} />, 'Agents')}
          {item('/directory', <Users size={19} />, 'Directory')}
        </div>
        <div className="mt-auto flex flex-col items-center gap-2">
          {dev && user?.admin && <DevReset />}
          <button className="rounded-lg p-2 text-muted hover:bg-surface-2 hover:text-ink" onClick={toggle} title="Theme">
            {theme === 'dark' ? <Sun size={17} /> : <Moon size={17} />}
          </button>
          {user && (
            <NavLink to="/settings" title={`${user.name} (@${user.handle}): settings`} className={({ isActive }) => cn('rounded-full', isActive && 'ring-2 ring-accent ring-offset-2 ring-offset-surface')}>
              <Avatar handle={user.handle} kind="human" name={user.name} size={32} />
            </NavLink>
          )}
          <button className="rounded-lg p-2 text-muted hover:bg-surface-2 hover:text-bad" onClick={() => void logout()} title="Log out">
            <LogOut size={16} />
          </button>
        </div>
      </nav>
      <main className="min-w-0 flex-1">
        <Outlet />
      </main>
    </div>
  )
}

/** Dev servers only: reset to the demo seed, or empty the server to try onboarding. */
function DevReset() {
  const devSeed = useSession((s) => s.devSeed)
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const reset = async (seed: boolean) => {
    setBusy(true)
    setError('')
    try {
      await api.devReset(seed)
      location.assign('/')
    } catch (e) {
      setBusy(false)
      setError((e as Error).message)
    }
  }
  const choice = (label: string, hint: string, seed: boolean, danger?: boolean) => (
    <button className={cn('block w-full rounded-lg px-3 py-2 text-left hover:bg-surface-2 disabled:opacity-50', danger && 'text-bad')} disabled={busy} onClick={() => void reset(seed)}>
      <span className="block text-[13px] font-bold">{label}</span>
      <span className="block text-[11px] font-normal text-muted">{hint}</span>
    </button>
  )
  return (
    <div className="relative">
      <button
        className={cn('flex flex-col items-center gap-0.5 rounded-lg px-1.5 py-1 text-[9.5px] font-bold', open ? 'bg-bad-soft text-bad' : 'text-bad/80 hover:bg-bad-soft hover:text-bad')}
        onClick={() => setOpen(!open)}
        title="Dev only: reset the server"
      >
        <RotateCcw size={15} className={busy ? 'animate-spin' : ''} />
        Reset
      </button>
      {open && (
        <>
          <button className="fixed inset-0 z-40 cursor-default" aria-label="Close" onClick={() => setOpen(false)} />
          <div className="card absolute bottom-0 left-full z-50 ml-3 w-72 p-1.5 shadow-lg">
            <p className="px-3 pt-1.5 pb-1 text-[10px] font-bold tracking-wide text-muted">DEV SERVER · PEOPLE AND LOGINS STAY</p>
            {devSeed
              ? choice('Reset to demo seed', 'Wipe, reconnect the demo agents, replay the scripted orders', true)
              : <p className="px-3 py-2 text-[11px] text-muted">No seed command: start the server with LAGRANGE_DEV_SEED to get “Reset to demo seed”.</p>}
            {choice('Empty', 'Delete every agent, run and message, to try onboarding', false, true)}
            {error && <p className="px-3 py-1.5 text-[11px] font-semibold text-bad">{error}</p>}
          </div>
        </>
      )}
    </div>
  )
}
