import { Check, Plus, UserPlus, Users } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { AuthShell, Field, Problem, Submit } from '@/features/auth/parts'
import { api, type Invite, type Member, type Role } from '@/lib/api'
import { cn } from '@/lib/cn'
import { useCan, useSession } from '@/store/session'
import { Button } from '@/ui/AnswerForm'
import { Avatar } from '@/ui/Avatar'
import { CodeLine } from '@/ui/Copy'
import { atLeast, initials } from './roles'

export const ROLES: { role: Role; hint: string }[] = [
  { role: 'owner', hint: 'Manages members and roles' },
  { role: 'admin', hint: 'Manages agents, invitations and waiting rules' },
  { role: 'member', hint: 'Sends, answers and adds agents' },
  { role: 'viewer', hint: 'Looks, doesn’t act' },
]

/** The workspace badge in the sidebar: switch workspaces, or make a new one. */
export function WorkspaceSwitcher() {
  const { workspace, workspaces, switchWorkspace } = useSession()
  const [open, setOpen] = useState(false)
  if (!workspace) return null
  return (
    <div className="relative">
      <button
        className={cn('flex h-9 w-12 items-center justify-center rounded-lg border text-[12px] font-extrabold', open ? 'border-accent bg-accent-soft text-accent' : 'border-line text-ink hover:bg-surface-2')}
        onClick={() => setOpen(!open)}
        title={`Workspace: ${workspace.name} (you are ${workspace.role})`}
      >
        {initials(workspace.name)}
      </button>
      {open && (
        <>
          <button className="fixed inset-0 z-40 cursor-default" aria-label="Close" onClick={() => setOpen(false)} />
          <div className="card absolute top-0 left-full z-50 ml-3 w-72 p-1.5 shadow-lg">
            <p className="px-3 pt-1.5 pb-1 text-[10px] font-bold tracking-wide text-muted">WORKSPACES</p>
            {workspaces.map((w) => (
              <button key={w.slug} className="flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-left hover:bg-surface-2" onClick={() => w.slug !== workspace.slug && switchWorkspace(w.slug)}>
                <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-surface-2 text-[10.5px] font-extrabold">{initials(w.name)}</span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] font-bold">{w.name}</span>
                  <span className="block text-[11px] text-muted">
                    {w.role} · <span className="font-mono">{w.slug}</span>
                  </span>
                </span>
                {w.slug === workspace.slug && <Check size={15} className="text-accent" />}
              </button>
            ))}
            <NewWorkspace />
          </div>
        </>
      )}
    </div>
  )
}

function NewWorkspace() {
  const switchWorkspace = useSession((s) => s.switchWorkspace)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const create = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      const { workspace } = await api.createWorkspace(name)
      switchWorkspace(workspace.slug)
    } catch (err) {
      setError((err as Error).message)
    }
  }
  if (!open)
    return (
      <button className="flex w-full items-center gap-2 rounded-lg border-t border-line px-3 py-2 text-left text-[12.5px] font-semibold text-muted hover:bg-surface-2 hover:text-ink" onClick={() => setOpen(true)}>
        <Plus size={14} /> New workspace
      </button>
    )
  return (
    <form onSubmit={create} className="space-y-2 border-t border-line px-3 pt-2 pb-1.5">
      <input className="input" placeholder="Acme procurement" required autoFocus value={name} onChange={(e) => setName(e.target.value)} />
      {error && <p className="text-[11px] font-semibold text-bad">{error}</p>}
      <Button type="submit" className="w-full">
        Create and switch
      </Button>
    </form>
  )
}

/** Someone who isn't in any workspace (they left, or were removed): make one. */
export function NoWorkspace() {
  const { user, logout, switchWorkspace } = useSession()
  const [name, setName] = useState(user ? `${user.name}’s workspace` : '')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const create = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      const { workspace } = await api.createWorkspace(name)
      switchWorkspace(workspace.slug)
    } catch (err) {
      setError((err as Error).message)
      setBusy(false)
    }
  }
  return (
    <AuthShell title="Create a workspace" subtitle="A workspace holds your team’s agents and runs. You aren’t in one yet; an invitation link puts you into someone else’s.">
      <form onSubmit={create} className="space-y-4">
        <Field label="Name" required autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        <Problem text={error} />
        <Submit busy={busy}>Create workspace</Submit>
      </form>
      <button className="text-xs font-semibold text-muted hover:text-ink" onClick={() => void logout()}>
        Log out
      </button>
    </AuthShell>
  )
}

/** Invites someone into the current workspace with a role: they open a one-time link and join. */
export function InviteForm() {
  const owner = useCan('owner')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState<Invite>()
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      setSent(await api.createInvite(email, role))
      setEmail('')
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="space-y-3">
      <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
        <label className="flex min-w-[220px] flex-1 flex-col gap-1 text-xs font-semibold text-muted">
          Email
          <input className="input" type="email" required placeholder="grace@example.com" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
        </label>
        <label className="flex flex-col gap-1 text-xs font-semibold text-muted">
          Role
          <select className="input" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {ROLES.filter((r) => owner || r.role !== 'owner').map((r) => (
              <option key={r.role} value={r.role}>
                {r.role}
              </option>
            ))}
          </select>
        </label>
        <Button type="submit" disabled={busy}>
          Send invitation
        </Button>
      </form>
      <p className="text-[11px] text-muted">{ROLES.find((r) => r.role === role)?.hint}.</p>
      {error && <p className="text-xs font-semibold text-bad">{error}</p>}
      {sent && (
        <div className="space-y-1.5 text-xs">
          <p className="font-semibold text-good">
            {sent.emailed ? `Invitation emailed to ${sent.email}.` : `Send ${sent.email} this link (this server can’t send email).`}{' '}
            {sent.existing ? 'They already have an account: they log in and join.' : ''} It works once, for 7 days.
          </p>
          {sent.link && <CodeLine text={sent.link} />}
        </div>
      )}
    </div>
  )
}

/** Settings: the workspace's name, its members and their roles. */
export function Members() {
  const { user, workspace, switchWorkspace, load } = useSession()
  const owner = useCan('owner')
  const admin = useCan('admin')
  const [members, setMembers] = useState<Member[]>()
  const [name, setName] = useState(workspace?.name ?? '')
  const [inviting, setInviting] = useState(false)
  const [status, setStatus] = useState<{ ok: boolean; text: string }>()
  const reload = () => void api.members().then((m) => setMembers(m.members))
  useEffect(reload, [])

  const act = async (what: () => Promise<unknown>, ok?: string) => {
    setStatus(undefined)
    try {
      await what()
      if (ok) setStatus({ ok: true, text: ok })
      reload()
    } catch (e) {
      setStatus({ ok: false, text: (e as Error).message })
    }
  }
  const leave = (m: Member) =>
    void act(async () => {
      await api.removeMember(m.id)
      // Leaving the workspace you're in: go to another one (or make one).
      if (m.id === user?.id) switchWorkspace('')
    })

  if (!workspace) return null
  return (
    <div className="card flex flex-col gap-4 p-5">
      <div className="flex items-center gap-2">
        <Users size={17} className="text-accent" />
        <h2 className="font-bold">Workspace</h2>
        <span className="ml-auto font-mono text-xs text-muted">{workspace.slug}</span>
      </div>
      {admin ? (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void act(async () => {
              await api.renameWorkspace(name)
              await load()
            }, 'Renamed.')
          }}
        >
          <label className="flex min-w-[220px] flex-1 flex-col gap-1 text-xs font-semibold text-muted">
            Name
            <input className="input" required value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <Button type="submit" disabled={name.trim() === workspace.name}>
            Rename
          </Button>
        </form>
      ) : (
        <p className="text-sm font-semibold">{workspace.name}</p>
      )}

      <div className="flex items-center justify-between">
        <p className="text-xs font-bold tracking-wide text-muted">MEMBERS</p>
        {admin && (
          <Button icon={<UserPlus size={13} />} onClick={() => setInviting(!inviting)}>
            Invite
          </Button>
        )}
      </div>
      {inviting && <InviteForm />}
      {!members ? (
        <div className="h-24 animate-pulse rounded-lg bg-surface-2" />
      ) : (
        <ul className="divide-y divide-line">
          {members.map((m) => (
            <li key={m.id} className="flex items-center gap-3 py-2.5">
              <Avatar handle={m.handle} kind="human" name={m.name} size={32} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-semibold">
                  {m.name} {m.id === user?.id && <span className="text-xs font-normal text-muted">(you)</span>}
                </p>
                <p className="truncate font-mono text-[11px] text-muted">
                  @{m.handle} · {m.email}
                </p>
              </div>
              {owner ? (
                <select className="input w-auto py-1 text-xs" value={m.role} onChange={(e) => void act(() => api.setRole(m.id, e.target.value as Role))} aria-label={`Role of @${m.handle}`}>
                  {ROLES.map((r) => (
                    <option key={r.role} value={r.role}>
                      {r.role}
                    </option>
                  ))}
                </select>
              ) : (
                <span className={cn('rounded-full px-2 py-0.5 text-[11px] font-bold', atLeast(m.role, 'admin') ? 'bg-accent-soft text-accent' : 'bg-surface-2 text-muted')}>{m.role}</span>
              )}
              {(owner || m.id === user?.id) && (
                <button className="rounded-lg px-2 py-1 text-xs font-semibold text-muted hover:bg-bad-soft hover:text-bad" onClick={() => leave(m)}>
                  {m.id === user?.id ? 'Leave' : 'Remove'}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {status && <p className={status.ok ? 'text-xs font-semibold text-good' : 'text-xs font-semibold text-bad'}>{status.text}</p>}
    </div>
  )
}
