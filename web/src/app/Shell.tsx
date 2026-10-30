import { Bot, Inbox, ListTree, LogOut, Moon, Network, Sun, Users } from 'lucide-react'
import { useEffect, useState } from 'react'
import { NavLink, Outlet } from 'react-router'
import { cn } from '@/lib/cn'
import { useLive } from '@/store/live'
import { waitingFor } from '@/store/model'
import { useSession } from '@/store/session'
import { Avatar } from '@/ui/Avatar'

export function Logo({ size = 34 }: { size?: number }) {
  return (
    <span className="flex shrink-0 items-center justify-center rounded-xl bg-accent font-extrabold text-accent-ink shadow-card" style={{ width: size, height: size, fontSize: size * 0.42 }}>
      3A
    </span>
  )
}

function useTheme() {
  const [theme, setTheme] = useState<'light' | 'dark'>(() => {
    try {
      const saved = localStorage.getItem('lagrange-theme')
      if (saved === 'light' || saved === 'dark') return saved
    } catch {
      /* storage unavailable */
    }
    return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  })
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
  const { user, logout } = useSession()
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
        <div className="mt-3 flex w-full flex-col gap-1 px-2">
          {item('/', <Network size={19} />, 'Live')}
          {item('/inbox', <Inbox size={19} />, 'Inbox', waiting)}
          {item('/runs', <ListTree size={19} />, 'Runs')}
          {item('/agents', <Bot size={19} />, 'Agents')}
          {item('/directory', <Users size={19} />, 'Directory')}
        </div>
        <div className="mt-auto flex flex-col items-center gap-2">
          <button className="rounded-lg p-2 text-muted hover:bg-surface-2 hover:text-ink" onClick={toggle} title="Theme">
            {theme === 'dark' ? <Sun size={17} /> : <Moon size={17} />}
          </button>
          {user && (
            <span title={`${user.name} (@${user.handle})`}>
              <Avatar handle={user.handle} kind="human" name={user.name} size={32} />
            </span>
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
